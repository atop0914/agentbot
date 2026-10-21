package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/sandbox"
)

// fakeServerBin 在 t.TempDir() 里编译 testdata/fake_server，返回可执行文件路径。
//
// 为什么现编译而不是预编译入库：
//   - 入库一个二进制意味着它会在某个时刻与源码不同步（改了行为没重新编译，
//     测试仍然绿），那是最难查的一类「测试撒谎」。
//   - 编译发生在 t.TempDir()，不污染仓库目录（否则会留下被 .gitignore
//     忽略的产物，下次 cron 的 git status 误判为遗留改动）。
//
// 编译一次、整个包复用：编译本身约 0.3s，每个测试各编一次会明显拖慢。
var (
	fakeBinOnce sync.Once
	fakeBinPath string
	fakeBinErr  error
)

func fakeServerBin(t *testing.T) string {
	t.Helper()
	fakeBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fake-mcp-*")
		if err != nil {
			fakeBinErr = err
			return
		}
		bin := filepath.Join(dir, "fake_server")
		cmd := exec.Command("go", "build", "-o", bin, "./testdata/fake_server")
		out, err := cmd.CombinedOutput()
		if err != nil {
			fakeBinErr = errors.New("build fake server: " + err.Error() + "\n" + string(out))
			return
		}
		fakeBinPath = bin
	})
	if fakeBinErr != nil {
		t.Fatalf("%v", fakeBinErr)
	}
	return fakeBinPath
}

// newFakeSandbox 返回一个只做 rlimit 的沙箱。
//
// 刻意禁用 cgroup：测试机上 cgroup v2 可能不可写，而「能不能创建 cgroup」
// 不是本包任何一条断言要验证的东西（那是 sandbox 包自己的测试）。
// 把它拖进来只会让 mcp 的测试在某些机器上以无关原因失败。
func newFakeSandbox() *sandbox.Manager {
	cfg := sandbox.DefaultConfig()
	cfg.DisableCgroup = true
	return sandbox.New(cfg)
}

// fakeServerConfig 构造一个指向假 server 的配置。
func fakeServerConfig(t *testing.T, mode string, timeout time.Duration) ServerConfig {
	t.Helper()
	return ServerConfig{
		Name:           "fake",
		Command:        fakeServerBin(t),
		AgentID:        "mcp-test-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Env:            []string{"FAKE_MCP_MODE=" + mode},
		RequestTimeout: timeout,
		StartTimeout:   5 * time.Second,
	}
}

// connectFake 连接一个假 server，并在测试结束时关闭。
func connectFake(t *testing.T, mode string, timeout time.Duration) *Client {
	t.Helper()
	c, err := Connect(context.Background(), newFakeSandbox(), fakeServerConfig(t, mode, timeout))
	if err != nil {
		t.Fatalf("Connect(mode=%s): %v", mode, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestConnect_HandshakeAndServerInfo 覆盖正常握手。
//
// 断言的是**副作用证据**：server 真的被启动过（ServerInfo 来自它的响应，
// 不是我们编的常量），我们真的发出过 initialized 通知（否则 server
// 不会进入就绪状态 —— 假 server 不做校验，但 Client 侧必须发出）。
func TestConnect_HandshakeAndServerInfo(t *testing.T) {
	c := connectFake(t, "", 5*time.Second)

	if got := c.ServerInfo().Name; got != "fake-mcp" {
		t.Errorf("server name = %q, want fake-mcp (proves we talked to the real subprocess)", got)
	}
	if got := c.ProtocolVersion(); got != "2024-11-05" {
		t.Errorf("protocol version = %q, want 2024-11-05", got)
	}
	if !c.ToolsSupported() {
		t.Error("fake server declares tools capability; ToolsSupported() must be true")
	}
}

// TestConnect_SkipsNonJSONStdout 覆盖「server 往 stdout 打日志」这条现实形态。
//
// 处置是**跳过**而不是判死：一个只是多打了一行 banner 的 server 不该
// 完全不可用。但跳过要有上限（maxNonJSONLines），见下一条测试。
func TestConnect_SkipsNonJSONStdout(t *testing.T) {
	c := connectFake(t, "noisy", 5*time.Second)

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools.Tools))
	}

	tr, ok := c.tr.(*stdioTransport)
	if !ok {
		t.Fatalf("expected stdioTransport, got %T", c.tr)
	}
	// 至少握手 + list_tools 各有一条 banner 被跳过
	if tr.SkippedLines() < 2 {
		t.Errorf("SkippedLines() = %d, want >= 2 (banner lines must be counted, not silently dropped)", tr.SkippedLines())
	}
}

// TestListTools_RejectsServerWithoutToolsCapability 覆盖 no_tools。
//
// 这条是「反向断言」：不声明 tools 能力的 server 必须能被识别出来，
// 否则调用方会把「本来就不提供工具」误读成「server 坏了」。
func TestListTools_RejectsServerWithoutToolsCapability(t *testing.T) {
	c := connectFake(t, "no_tools", 5*time.Second)

	if c.ToolsSupported() {
		t.Fatal("server did not declare tools capability; ToolsSupported() must be false")
	}
}

// TestCallTool_Success 断言工具真的被执行了 —— 返回的文本里带着入参。
func TestCallTool_Success(t *testing.T) {
	c := connectFake(t, "", 5*time.Second)

	res, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hello"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("expected at least one content block")
	}
	if got := res.Content[0].Text; got != "echo: hello" {
		t.Errorf("content text = %q, want %q", got, "echo: hello")
	}
	if res.IsError {
		t.Error("IsError must be false on success")
	}
}

// TestErrorClassification 是本包最重要的一条测试。
//
// 第 4 轮 4-1 的硬性要求：协议错误 / 传输错误 / 工具级错误必须**可区分**。
// 四类故障各断言一次，且断言的是**具体类型**而非「有错误」——
// 后者会让「统一报 500」的实现通过。
func TestErrorClassification(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		tool    string
		timeout time.Duration
		// wantTool / wantRPC / wantProto / wantTransport 四选一
		wantTool      bool
		wantRPC       bool
		wantProto     bool
		wantTransport bool
	}{
		{name: "tool reports its own failure", mode: "", tool: "fail", timeout: 5 * time.Second, wantTool: true},
		{name: "rpc level error", mode: "rpc_error", tool: "echo", timeout: 5 * time.Second, wantRPC: true},
		{name: "unknown tool is an rpc error", mode: "", tool: "nope", timeout: 5 * time.Second, wantRPC: true},
		// 非 JSON 行被跳过（那是「server 打日志」的合法形态），随后进程退出 →
		// 读到 EOF。这条归为传输错误是对事实的准确描述：server 死了。
		{name: "non-JSON output followed by exit is a transport error", mode: "malformed", tool: "echo", timeout: 5 * time.Second, wantTransport: true},
		{name: "JSON that is not a response is a protocol error", mode: "badjson", tool: "echo", timeout: 5 * time.Second, wantProto: true},
		{name: "crashed server is a transport error", mode: "crash", tool: "echo", timeout: 5 * time.Second, wantTransport: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := connectFake(t, tc.mode, tc.timeout)

			_, err := c.CallTool(context.Background(), tc.tool, json.RawMessage(`{"text":"x"}`))
			if err == nil {
				t.Fatalf("expected an error in mode %q calling %q", tc.mode, tc.tool)
			}

			var (
				toolErr      *ToolError
				rpcErr       *RPCError
				protoErr     *ProtocolError
				transportErr *TransportError
			)
			gotTool := errors.As(err, &toolErr)
			gotRPC := errors.As(err, &rpcErr)
			gotProto := errors.As(err, &protoErr)
			gotTransport := errors.As(err, &transportErr)

			if gotTool != tc.wantTool || gotRPC != tc.wantRPC ||
				gotProto != tc.wantProto || gotTransport != tc.wantTransport {
				t.Fatalf("error classification mismatch for %q:\n  err = %v\n  want tool=%v rpc=%v proto=%v transport=%v\n  got  tool=%v rpc=%v proto=%v transport=%v",
					tc.mode, err, tc.wantTool, tc.wantRPC, tc.wantProto, tc.wantTransport,
					gotTool, gotRPC, gotProto, gotTransport)
			}
		})
	}
}

// TestCallTool_TimeoutIsDistinct 覆盖超时。
//
// 超时必须是**独立的**错误（可 errors.Is(context.DeadlineExceeded)），
// 而不是落进 TransportError —— 前者可以重试或调大超时，
// 后者说明连接坏了、必须重连。混在一起会让运维重连一条完全健康的连接。
func TestCallTool_TimeoutIsDistinct(t *testing.T) {
	c := connectFake(t, "slow", 300*time.Millisecond)

	start := time.Now()
	_, err := c.CallTool(context.Background(), "slow", json.RawMessage(`{}`))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected errors.Is(err, context.DeadlineExceeded), got %v", err)
	}
	// 断言「真的超时了」而不是「立刻就错了」：一个连都没连上的实现
	// 也会返回错误，但会在微秒内。
	if elapsed < 250*time.Millisecond {
		t.Errorf("returned after %v; the request timeout was 300ms, so it did not actually wait", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("returned after %v; the request timeout of 300ms was not honoured", elapsed)
	}

	// 反向断言：超时不能被误判成传输错误（连接其实还活着）
	var transportErr *TransportError
	if errors.As(err, &transportErr) {
		t.Error("a request timeout must not be reported as a TransportError")
	}
}

// TestConnect_HangingServerTimesOut 覆盖「server 起不来」。
//
// 这条验证的是 StartTimeout 真的作用在启动路径上：一个不回 initialize 的
// server 必须在 StartTimeout 内失败，且**进程被回收**（不留孤儿）。
func TestConnect_HangingServerTimesOut(t *testing.T) {
	cfg := fakeServerConfig(t, "hang_init", time.Second)
	cfg.StartTimeout = 500 * time.Millisecond

	start := time.Now()
	c, err := Connect(context.Background(), newFakeSandbox(), cfg)
	elapsed := time.Since(start)

	if err == nil {
		_ = c.Close()
		t.Fatal("expected Connect to fail for a server that never answers initialize")
	}
	if elapsed > 5*time.Second {
		t.Errorf("Connect took %v; StartTimeout was 500ms", elapsed)
	}
	// 错误里必须能看出是「握手超时」，而不是一个笼统的失败 ——
	// 排障时「起不来」与「起来了但不说协议」是两种完全不同的原因。
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected a deadline error, got %v", err)
	}
}

// TestConnect_ReapsProcessOnHandshakeFailure 断言握手失败时子进程被回收。
//
// 这是漏了就会慢慢耗尽宿主资源的缺陷：每次连接失败留一个 MCP server。
// 断言方式是**副作用**：找到假 server 的进程（按可执行路径唯一匹配），
// 确认它不存在。
func TestConnect_ReapsProcessOnHandshakeFailure(t *testing.T) {
	bin := fakeServerBin(t)
	cfg := fakeServerConfig(t, "hang_init", time.Second)
	cfg.StartTimeout = 300 * time.Millisecond

	c, err := Connect(context.Background(), newFakeSandbox(), cfg)
	if err == nil {
		_ = c.Close()
		t.Fatal("expected Connect to fail")
	}

	// 给内核一点时间回收
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processRunningWithArg(bin) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("fake server process (%s) is still running after a failed handshake", bin)
}

// processRunningWithArg 报告是否有进程的命令行包含给定路径。
//
// 用 /proc 而不是 pgrep：pgrep 在不同发行版上对长路径的匹配行为不一致，
// 而 /proc/<pid>/cmdline 是内核给出的原文。
func processRunningWithArg(needle string) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		if strings.Contains(strings.ReplaceAll(string(b), "\x00", " "), needle) {
			return true
		}
	}
	return false
}

// TestConcurrentCallsArePairedById 覆盖并发调用。
//
// 配对错误的症状是「A 拿到了 B 的结果」—— 不报错、不失败，
// 只是数据错了。断言方式：每个请求带的 text 都不相同，
// 响应的内容必须与自己的入参一致。
func TestConcurrentCallsArePairedById(t *testing.T) {
	c := connectFake(t, "", 10*time.Second)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			text := "req-" + strconv.Itoa(i)
			args, _ := json.Marshal(map[string]string{"text": text})
			res, err := c.CallTool(context.Background(), "echo", args)
			if err != nil {
				errs <- err
				return
			}
			if len(res.Content) == 0 {
				errs <- errors.New("empty content for " + text)
				return
			}
			if want := "echo: " + text; res.Content[0].Text != want {
				errs <- errors.New("response mismatch: got " + res.Content[0].Text + ", want " + want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

// TestClient_CloseUnblocksPendingRequests 断言连接关闭能唤醒在途请求。
//
// 缺了这条，一个崩溃的 server 会让每个在途请求各自等到 30s 超时 ——
// 而真相（进程死了）在第一刻就知道。这类缺陷在交互场景里表现为
// 「点一下卡半分钟」。
func TestClient_CloseUnblocksPendingRequests(t *testing.T) {
	c := connectFake(t, "slow", 30*time.Second)

	done := make(chan error, 1)
	go func() {
		_, err := c.CallTool(context.Background(), "slow", json.RawMessage(`{}`))
		done <- err
	}()

	// 等请求真的发出去（否则可能在 Send 之前就 Close 了，测不到这条路径）
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the pending call to fail after Close")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("Close took %v to unblock the pending call; it must be immediate", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not unblock the pending request (it would wait for the full request timeout)")
	}
}

// TestClient_StdoutOnlyCarriesProtocol 断言 stderr 的内容不进协议流。
//
// 假 server 在启动时往 stderr 打一行。它出现在 StderrTail 里是**正确**的，
// 出现在响应里就是错的 —— 后者意味着我们把两个流搞混了。
func TestClient_StdoutOnlyCarriesProtocol(t *testing.T) {
	c := connectFake(t, "", 5*time.Second)

	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	tr, ok := c.tr.(*stdioTransport)
	if !ok {
		t.Fatalf("expected stdioTransport, got %T", c.tr)
	}
	if !strings.Contains(tr.StderrTail(), "fake-mcp-server starting") {
		t.Errorf("stderr tail should capture the server's startup line, got %q", tr.StderrTail())
	}
}
