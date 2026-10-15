package app_test

import (
	"bytes"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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

	// 用自己的同步捕获启动（见 startBinarySynced 的说明）：
	// startBinary 返回的 bytes.Buffer 由 exec 内部 goroutine 持续写入，
	// 测试侧同时读它 = data race（-race 下必红，实测踩过）。
	logBuf := startBinarySynced(t, bin, env, mainPort)

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

	// ⑤ 日志里不得出现 per-request 访问日志（INFO 级别下应只有启动/关闭行）。
	//
	// ⚠️ 必须用**自己持有的同步缓冲**，不能读 exec.Cmd 写的那只 bytes.Buffer：
	// 子进程的输出由 exec 内部的 goroutine 持续写入，测试侧同时读它是
	// data race（-race 下必红，实测踩过）。
	// startBinary 返回的 buffer 只适合在进程**退出后**读。
	logs := logBuf.String()
	if n := strings.Count(logs, `"msg":"request"`); n != 0 {
		t.Errorf("INFO 级别下不应打访问日志，实际 %d 行 —— 真实流量下启动信息会被淹没\n%s", n, logs)
	}
	// 反向断言：启动日志**必须**在 —— 否则上面的断言在「子进程根本没起来」
	// 时也会通过（那正是最容易被放过的假绿）。
	if !strings.Contains(logs, "http server listening") {
		t.Errorf("启动日志缺失，说明该断言可能什么都没测到\n%s", logs)
	}
	// 顺带验证指标端点路径被写进了启动日志（配置项可改，必须能确认实际值）
	if !strings.Contains(logs, "metrics endpoint registered") {
		t.Errorf("启动日志应写出指标端点的实际路径\n%s", logs)
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
		// 沙箱根目录的变量名是 AGENTBOT_FILESYSTEM_ROOT（config 里
		// FileSystem.Root，见 internal/config/loader.go）。
		//
		// ⚠️ 这里第一版写成了 AGENTBOT_FILE_SYSTEM_ROOT（多一个下划线）。
		// 那个键在 loader 里**不存在**，于是这行环境变量被静默忽略，
		// FileSystem.Root 退回默认值 /var/lib/agentbot/fs；CI runner 与
		// 开发机都以普通用户跑测试，os.MkdirAll 直接 permission denied，
		// 进程以「filesystem sandbox root is unavailable」退出，
		// waitForHealth 等满 20s 后报「服务在 20s 内未就绪」。
		//
		// 症状之所以误导：报错指向「服务起不来」，而真正的原因是
		// 一行拼错的环境变量名 —— 配置类 bug 的典型形态，
		// 不报「未知配置项」，只让取到默认值的那一处继续按旧值回答。
		"AGENTBOT_FILESYSTEM_ROOT=" + filepath.Join(t.TempDir(), "fs"),
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

// syncBuffer 是一个并发安全的输出缓冲。
//
// 为什么需要它：exec.Cmd 会起一个 goroutine 把子进程的 stdout/stderr
// 拷进我们给的 io.Writer。若直接给 *bytes.Buffer，测试侧读它时
// 就与那个 goroutine 形成 data race —— `go test -race` 必然报错。
// 实测：第一条断言在单跑时是绿的，只有整机 -race 全量跑才复现。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// String 在锁内取快照，可安全地与子进程的输出并发调用。
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// startBinarySynced 启动被测二进制并返回**并发安全**的输出缓冲。
//
// 与共享的 startBinary 的区别仅在输出捕获方式；清理与就绪等待保持一致
// （SIGTERM 优先，超时 SIGKILL），这样行为不会因用了哪个启动器而不同。
func startBinarySynced(t *testing.T, bin string, env []string, mainPort int) *syncBuffer {
	t.Helper()

	cmd := exec.Command(bin)
	cmd.Env = env
	out := &syncBuffer{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动二进制失败: %v", err)
	}

	done := make(chan struct{})
	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
		<-done
	})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	if !waitForHealth("http://127.0.0.1:"+itoa(mainPort), 20*time.Second) {
		t.Fatalf("服务在 20s 内未就绪\n--- 进程输出 ---\n%s", out.String())
	}
	return out
}
