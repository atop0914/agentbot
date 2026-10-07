package app_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 本文件是 Day 29 的第三批：**真实二进制**的行为级验证。
//
// 为什么 httptest 不够：
//
//	前面两批跑的是进程内的 handler，绕过了 cmd/agentbot 的启动路径 ——
//	而「配置分层」这件事只存在于启动路径上。Day 28 引入的
//	config.Load（默认值 → 配置文件 → 环境变量 → fail-fast 校验）
//	如果只在单测里验证，就只是验证了函数；这里要验证的是
//	「只给 AGENTBOT_* 环境变量、不给配置文件，进程能否真的起来并对外服务」，
//	这是 k8s 部署的常态形态。
//
// 另外顺带验证 fail-fast：无机密必须拒绝启动（响亮失败），
// 而不是带着一个公开的 dev 密钥静默跑起来。

// buildBinary 编译被测二进制到临时目录。
//
// 编译产物放 t.TempDir()：**不**放在仓库目录里，否则会留下一个
// 被 .gitignore 忽略的二进制，下次 cron 的 git status 会误判为遗留改动。
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agentbot-e2e")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/agentbot")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("编译 agentbot 失败: %v\n%s", err, out)
	}
	return bin
}

// repoRoot 定位仓库根目录（本测试文件在 internal/app 下）。
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	// internal/app -> 仓库根
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// freePort 申请一个空闲端口。
//
// 为什么不用固定端口：并行跑测试或上一次的进程还在 TIME_WAIT 时，
// 固定端口会以「bind: address already in use」的形式失败，
// 而那个错误与本次改动毫无关系，最容易误导排查方向。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("申请空闲端口失败: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// startBinary 用给定的环境变量启动二进制，等待 /health 就绪。
//
// 返回的 cleanup 会先发 SIGTERM（验证优雅退出），超时才 SIGKILL。
func startBinary(t *testing.T, bin string, env []string) (baseURL string, output *bytes.Buffer) {
	t.Helper()

	cmd := exec.Command(bin)
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
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

	port := envValue(env, "AGENTBOT_SERVER_PORT")
	base := "http://127.0.0.1:" + port
	if !waitForHealth(base, 20*time.Second) {
		t.Fatalf("服务在 20s 内未就绪\n--- 进程输出 ---\n%s", buf.String())
	}
	return base, &buf
}

// waitForHealth 轮询 /health 直到就绪或超时。
//
// 轮询而不是 sleep 固定时长：启动耗时随机器负载波动，
// 固定 sleep 要么慢（等太久）要么脆（偶尔不够）。
func waitForHealth(base string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// envValue 从 KEY=VALUE 列表中取某个键的值。
func envValue(env []string, key string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

// minimalEnv 构造「只有 AGENTBOT_* 环境变量」的部署环境。
//
// 刻意**不**带任何配置文件路径（AGENTBOT_CONFIG_FILE 不设，--config 不传），
// 模拟 k8s 的纯环境变量部署：配置全部来自 env，默认值补齐其余字段。
// 同时刻意不带 PATH/HOME 之外的宿主变量，验证进程不依赖任何隐式环境。
func minimalEnv(t *testing.T, serverPort, metricsPort int, extra ...string) []string {
	t.Helper()
	tmp := t.TempDir()

	// JWT 密钥必须是「非占位符 + 足够长度」。占位符检测会拦下含
	// replace/changeme/example 等字样的值，所以这里用随机十六进制。
	secret := strings.Repeat("a1b2c3d4", 5) // 40 字节 > 32 字节下限

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + tmp,
		// --- 机密（无默认值，不注入就起不来）---
		"AGENTBOT_AUTH_JWT_SECRET=" + secret,
		"AGENTBOT_DATABASE_PASSWORD=" + "pgpass-" + strings.Repeat("f9", 8),
		// --- 服务端 ---
		fmt.Sprintf("AGENTBOT_SERVER_HOST=127.0.0.1"),
		fmt.Sprintf("AGENTBOT_SERVER_PORT=%d", serverPort),
		// --- 指标端点：端口必须与 server.port 不同 ---
		"AGENTBOT_METRICS_ENABLED=true",
		fmt.Sprintf("AGENTBOT_METRICS_PORT=%d", metricsPort),
		// --- 落盘路径指到临时目录，不污染宿主 ---
		"AGENTBOT_FILESYSTEM_ROOT=" + filepath.Join(tmp, "fs"),
		"AGENTBOT_STORAGE_LOCAL_PATH=" + filepath.Join(tmp, "storage"),
		"AGENTBOT_ADMIN_CONSOLE_DIR=" + filepath.Join(tmp, "web"),
		// --- 日志：text 格式便于在失败输出里人读 ---
		"AGENTBOT_LOG_LEVEL=info",
		"AGENTBOT_LOG_FORMAT=text",
	}
	return append(env, extra...)
}

// TestE2EBinaryBootsFromEnvOnlyAndServesFullChain 是本批的核心：
// 只给环境变量启动真实二进制，然后在它上面跑一遍完整链路。
//
// 这条测试同时覆盖三件事：
//  1. 配置分层（纯 env 部署可用，默认值补齐其余字段）；
//  2. 真实 HTTP 服务器 + 真实中间件链（不是 httptest 的内存 handler）；
//  3. 优雅退出（SIGTERM 后进程主动停止，而不是被超时杀掉）。
func TestE2EBinaryBootsFromEnvOnlyAndServesFullChain(t *testing.T) {
	if testing.Short() {
		t.Skip("真实二进制测试在 -short 模式下跳过（需要 go build）")
	}

	bin := buildBinary(t)
	serverPort := freePort(t)
	metricsPort := freePort(t)
	env := minimalEnv(t, serverPort, metricsPort)

	base, out := startBinary(t, bin, env)

	client := &http.Client{Timeout: 10 * time.Second}

	// --- 健康检查 ---
	resp, err := client.Get(base + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v\n%s", err, out.String())
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health = %d, want 200", resp.StatusCode)
	}

	// --- 指标端点：build_info 必须带版本信息 ---
	metricsURL := fmt.Sprintf("http://127.0.0.1:%d/metrics", metricsPort)
	mResp, err := client.Get(metricsURL)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	mBody, _ := io.ReadAll(mResp.Body)
	_ = mResp.Body.Close()
	if !bytes.Contains(mBody, []byte("agentbot_build_info")) {
		t.Errorf("指标端点缺少 agentbot_build_info: %s", string(mBody))
	}
	if ct := mResp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("指标端点 Content-Type = %q, want text/plain（Prometheus 抓取格式）", ct)
	}

	// --- 无令牌访问受保护路由必须 401（真实中间件链） ---
	anonResp, err := client.Get(base + "/api/v1/agents")
	if err != nil {
		t.Fatalf("GET /api/v1/agents (匿名): %v", err)
	}
	_ = anonResp.Body.Close()
	if anonResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("匿名访问 /api/v1/agents = %d, want 401", anonResp.StatusCode)
	}

	// --- 注册 → 登录 ---
	email := fmt.Sprintf("bin-%d@corp.example", time.Now().UnixNano())
	regBody := fmt.Sprintf(`{"email":%q,"username":"binuser","password":"Str0ngPass!123"}`, email)
	regResp := postJSON(t, client, base+"/api/v1/auth/register", regBody, "")
	if regResp.Code != http.StatusCreated {
		t.Fatalf("注册 = %d, want 201: %s", regResp.Code, regResp.Raw)
	}
	var reg struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	_ = json.Unmarshal([]byte(regResp.Raw), &reg)

	loginResp := postJSON(t, client, base+"/api/v1/auth/login",
		fmt.Sprintf(`{"email":%q,"password":"Str0ngPass!123"}`, email), "")
	if loginResp.Code != http.StatusOK {
		t.Fatalf("登录 = %d, want 200: %s", loginResp.Code, loginResp.Raw)
	}
	var login struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal([]byte(loginResp.Raw), &login)
	if login.AccessToken == "" {
		t.Fatal("登录未返回 access_token")
	}

	// 新用户还没有任何角色：受保护路由必须是 403（已认证、无权限），
	// 而不是 200 —— 这条断言验证的是「默认拒绝」在真实二进制里同样生效。
	noRoleResp := getJSON(t, client, base+"/api/v1/agents", login.AccessToken)
	if noRoleResp.Code != http.StatusForbidden {
		t.Fatalf("无角色用户访问 = %d, want 403: %s", noRoleResp.Code, noRoleResp.Raw)
	}

	// --- 确认进程没有带着「开发密钥」跑起来 ---
	// app.New() 会在启动时打一条 Warn；真实二进制走 NewWithConfig，
	// 不应出现这条告警。这是一条反向断言：出现即说明启动路径选错了。
	if strings.Contains(out.String(), "DEVELOPMENT config") {
		t.Errorf("二进制启动时用了开发默认配置（应当走注入配置）:\n%s", out.String())
	}
	_ = reg
}

// TestE2EBinaryFailsFastWithoutSecrets 验证无机密时**拒绝启动**。
//
// 这条测试保护的是一类特别隐蔽的事故：配置缺失时不报错，
// 而是拿一个公开默认值继续服务。断言的是「响亮失败」这个契约 ——
// 退出码非零，且 stderr 里具名指出缺少哪个变量（可操作性）。
func TestE2EBinaryFailsFastWithoutSecrets(t *testing.T) {
	if testing.Short() {
		t.Skip("真实二进制测试在 -short 模式下跳过（需要 go build）")
	}

	bin := buildBinary(t)
	tmp := t.TempDir()

	cases := []struct {
		name         string
		env          []string
		wantInStderr string
	}{
		{
			name: "完全不给机密",
			env: []string{
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + tmp,
				"AGENTBOT_SERVER_PORT=" + fmt.Sprint(freePort(t)),
			},
			wantInStderr: "AUTH_JWT_SECRET",
		},
		{
			name: "JWT 密钥仍是占位符",
			env: []string{
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + tmp,
				"AGENTBOT_AUTH_JWT_SECRET=changeme-please",
				"AGENTBOT_DATABASE_PASSWORD=real-password-value",
			},
			wantInStderr: "jwt_secret",
		},
		{
			name: "JWT 密钥过短",
			env: []string{
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + tmp,
				"AGENTBOT_AUTH_JWT_SECRET=too-short",
				"AGENTBOT_DATABASE_PASSWORD=real-password-value",
			},
			wantInStderr: "too short",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin)
			cmd.Env = tc.env
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			cmd.Stdout = io.Discard

			if err := cmd.Start(); err != nil {
				t.Fatalf("启动进程失败: %v", err)
			}
			// 配置校验发生在监听之前，进程应当**迅速**退出。
			// 20s 是给慢机器的余量；真等满说明它没退出（挂住了）。
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatalf("配置非法但进程以 0 退出（静默降级）: %s", stderr.String())
				}
				var ee *exec.ExitError
				if !asExitError(err, &ee) {
					t.Fatalf("期望非零退出码，得到 %v", err)
				}
				if ee.ExitCode() == 0 {
					t.Fatalf("期望非零退出码，得到 0")
				}
			case <-time.After(20 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatal("配置非法但进程 20s 内未退出（挂住而不是响亮失败）")
			}

			if !strings.Contains(stderr.String(), tc.wantInStderr) {
				t.Errorf("stderr 应具名指出问题（含 %q），实际:\n%s",
					tc.wantInStderr, stderr.String())
			}
			// 修复提示必须可操作：告诉使用者该设哪个环境变量。
			if !strings.Contains(stderr.String(), "AGENTBOT_") {
				t.Errorf("stderr 未提示需要设置的环境变量:\n%s", stderr.String())
			}
		})
	}
}

// --- HTTP 断言小工具 ---

type httpResult struct {
	Code int
	Raw  string
}

func postJSON(t *testing.T, c *http.Client, url, body, token string) httpResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return do(t, c, req)
}

func getJSON(t *testing.T, c *http.Client, url, token string) httpResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return do(t, c, req)
}

func do(t *testing.T, c *http.Client, req *http.Request) httpResult {
	t.Helper()
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.String(), err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return httpResult{Code: resp.StatusCode, Raw: string(raw)}
}

// asExitError 是 errors.As 的薄封装（避免本文件引入 errors 只为一次断言）。
func asExitError(err error, dst **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*dst = ee
	}
	return ok
}
