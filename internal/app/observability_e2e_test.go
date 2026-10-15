package app_test

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 Day 3-7 第四批：**真实二进制**上的双指标端点一致性验证。
//
// 为什么必须用真实二进制：端口绑定只存在于 cmd/agentbot 的启动路径上，
// httptest 根本够不着「:8080 与 :9090 是否答同一份内容」这个问题 ——
// 而这恰恰是本次修掉的那个 bug 的所在。
//
// 被修掉的 bug（非常有代表性）：cmd/agentbot 在独立端口上起了一个
// **stub** /metrics，只输出 "# AgentBot metrics (stub)" 与 build_info。
// 而部署文档、Dockerfile（EXPOSE 9090）与 docker-compose 都把 9090
// 当成抓取目标。后果：运维照着文档配 Prometheus，程序**不报任何错**，
// 仪表盘却永远是空的（唯一看得见的指标是版本号）。
// 两个端点各答一份、其中一个在撒谎 —— 这正是本项目反复踩到的
// 「同一件事被两个组件各存一份 = 报表撒谎」。

// TestBinaryBothMetricsEndpointsServeSameRealData 是本批的核心断言。
func TestBinaryBothMetricsEndpointsServeSameRealData(t *testing.T) {
	bin := buildBinary(t)

	mainPort := freePort(t)
	metricsPort := freePort(t)

	env := observabilityEnv(t, mainPort, metricsPort, "")

	_, logBuf := startBinary(t, bin, env)

	mainURL := "http://127.0.0.1:" + itoa(mainPort)
	metricsURL := "http://127.0.0.1:" + itoa(metricsPort)

	// 产生一些真实流量，确保两边都有非空数据可比。
	for i := 0; i < 2; i++ {
		resp, err := http.Get(mainURL + "/health")
		if err != nil {
			t.Fatalf("GET /health 失败: %v", err)
		}
		resp.Body.Close()
	}
	// 未授权请求：验证 401 也被计入（本批修掉的第二个 bug 的回归）
	resp, err := http.Get(mainURL + "/api/v1/agents")
	if err != nil {
		t.Fatalf("GET /api/v1/agents 失败: %v", err)
	}
	resp.Body.Close()

	mainBody := getBody(t, mainURL+"/metrics")
	dedicatedBody := getBody(t, metricsURL+"/metrics")

	// ① 独立端口不得再出现 stub 标记
	if strings.Contains(dedicatedBody, "stub") {
		t.Errorf("独立指标端口仍在输出 stub —— 文档/镜像都把它当作抓取目标，\n"+
			"运维会得到一份永远为空的仪表盘且看不到任何错误\n%s", dedicatedBody)
	}

	// ② 两个端点必须都含有真实业务指标
	required := []string{
		"process_uptime_seconds ",
		"agentbot_storage_backend_memory ",
		"agentbot_http_requests_total{",
	}
	for _, series := range required {
		if !strings.Contains(mainBody, series) {
			t.Errorf("主端口 /metrics 缺少 %q", series)
		}
		if !strings.Contains(dedicatedBody, series) {
			t.Errorf("独立端口 /metrics 缺少 %q", series)
		}
	}

	// ③ 关键：两边对**同一个事实**必须给出**同一个数值**。
	// 只断言「都含指标名」是不够的 —— 两份独立的注册表也能同时含有一堆
	// 同名指标，而数值不同（那正是「两份各答一份」的本质）。
	for _, series := range []string{
		`agentbot_http_requests_total{method="GET",route="/health",status="200"} `,
		`agentbot_http_requests_total{method="GET",route="/api/v1/agents",status="401"} `,
		"agentbot_storage_backend_memory ",
		"agentbot_storage_backend_postgres ",
	} {
		mainV := extractValue(t, mainBody, series)
		dedicatedV := extractValue(t, dedicatedBody, series)
		if mainV != dedicatedV {
			t.Errorf("两个端点对 %q 给出不同数值：主端口=%v 独立端口=%v —— 抓取目标不同会导致仪表盘撒谎",
				series, mainV, dedicatedV)
		}
	}

	// ④ 反向断言：401 必须**真的**被计入（这正是本批修掉的「错误率恒为 0」）
	if got := extractValue(t, mainBody, `agentbot_http_requests_total{method="GET",route="/api/v1/agents",status="401"} `); got < 1 {
		t.Errorf("未授权请求未被计入指标（错误率会恒为 0）: %v", got)
	}

	// ⑤ 日志里不得出现 per-request 访问日志（INFO 级别下应只有启动/关闭行）
	if n := strings.Count(logBuf.String(), `"msg":"request"`); n != 0 {
		t.Errorf("INFO 级别下不应打访问日志，实际 %d 行 —— 真实流量下启动信息会被淹没", n)
	}
}

// TestBinaryMetricsPathIsConfigurable 验证 metrics.path 改配置后
// 两个端点都跟着改 —— 若有一个写死 /metrics，改配置就会出现
// 「路由挂在新路径、告警还在抓老路径」的静默失联。
func TestBinaryMetricsPathIsConfigurable(t *testing.T) {
	bin := buildBinary(t)

	mainPort := freePort(t)
	metricsPort := freePort(t)
	const customPath = "/internal-metrics"

	env := observabilityEnv(t, mainPort, metricsPort, customPath)

	_, _ = startBinary(t, bin, env)

	mainURL := "http://127.0.0.1:" + itoa(mainPort)
	metricsURL := "http://127.0.0.1:" + itoa(metricsPort)

	// 自定义路径可用
	if body := getBody(t, mainURL+customPath); !strings.Contains(body, "process_uptime_seconds ") {
		t.Errorf("主端口 %s 未提供指标\n%s", customPath, body)
	}
	if body := getBody(t, metricsURL+customPath); !strings.Contains(body, "process_uptime_seconds ") {
		t.Errorf("独立端口 %s 未提供指标\n%s", customPath, body)
	}

	// 老路径不再可用（这是**预期**的：路径是配置项，不是兼容契约）
	if code := probeStatus(t, mainURL+"/metrics"); code == http.StatusOK {
		// 这里其实会落到 authz 默认拒绝（403）而不是 404，两者都不算 200 即可。
		t.Errorf("改配置后 /metrics 仍返回 200 —— 路径配置没有真正生效")
	}
}

// --- 辅助 ---

// observabilityEnv 构造被测进程的环境变量。
//
// 刻意**不**用 os.Environ()：startBinary 里是 cmd.Env = env（整体替换），
// 且继承宿主环境会让「本机恰好设了某个 AGENTBOT_*」变成隐式输入 ——
// 那类失败只在别人机器上复现。
func observabilityEnv(t *testing.T, mainPort, metricsPort int, metricsPath string) []string {
	t.Helper()
	env := []string{
		"AGENTBOT_AUTH_JWT_SECRET=agentbot-e2e-observability-secret-0000000000",
		"AGENTBOT_DATABASE_HOST=",
		"AGENTBOT_DATABASE_PASSWORD=",
		"AGENTBOT_SERVER_PORT=" + itoa(mainPort),
		"AGENTBOT_METRICS_PORT=" + itoa(metricsPort),
		"AGENTBOT_METRICS_ENABLED=true",
		"AGENTBOT_LOG_LEVEL=info",
		"AGENTBOT_FILE_SYSTEM_ROOT=" + filepath.Join(t.TempDir(), "fs"),
		"AGENTBOT_STORAGE_LOCAL_PATH=" + filepath.Join(t.TempDir(), "storage"),
		"AGENTBOT_ADMIN_CONSOLE_DIR=" + filepath.Join(t.TempDir(), "web"),
	}
	if metricsPath != "" {
		env = append(env, "AGENTBOT_METRICS_PATH="+metricsPath)
	}
	return env
}

// getBody 取回 200 响应正文；非 200 直接失败（避免把错误页当指标解析）。
func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s 失败: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s 状态码 = %d\n%s", url, resp.StatusCode, body)
	}
	return string(body)
}

// probeStatus 只取状态码（用于断言某路径**不再**返回 200）。
func probeStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s 失败: %v", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
