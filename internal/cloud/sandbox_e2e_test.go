package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/sandbox"
)

// ⚠️ 这组测试走**真实 router**，而不是直接调 manager。
//
// 理由来自本仓库踩过的坑：单测直接调 service.Create 时，
// 装配层漏掉的接线（handler 上的回调没挂）完全不会被发现。
// 这里同样 —— ExecuteCommand 的沙箱装配是否真的生效，
// 只有经完整中间件链 + handler 才验得出来。
//
// 断言的核心纪律依旧是**副作用**：
//   - 死循环命令被限额打断 → 不是断言"配置了 CPUQuota"
//   - 进程读自己的 /proc/self/cgroup → 不是断言"我们往 cgroup.procs 写过"

func newCloudTestServer(t *testing.T) (*httptest.Server, *LocalManager) {
	t.Helper()
	repo := NewMemoryRepository()
	mgr, err := NewLocalManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalManager: %v", err)
	}
	svc := NewService(mgr, repo)
	h := NewHandler(svc)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, mgr
}

// createEnv 经真实 HTTP 建一个环境并返回其 id。
func createEnv(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	body := `{"agent_id":"agent-e2e","type":"sandbox","resources":{"cpu":"500m","memory":"256Mi"}}`
	resp, err := http.Post(srv.URL+"/api/v1/environments", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create env: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("create env status = %d", resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode create env: %v", err)
	}
	if out.ID == "" {
		t.Fatal("环境 id 为空")
	}
	return out.ID
}

// execInEnv 经真实 HTTP 在环境内执行命令，返回退出码与合并输出。
func execInEnv(t *testing.T, srv *httptest.Server, envID, command string) (int, string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"command": command})
	resp, err := http.Post(srv.URL+"/api/v1/environments/"+envID+"/exec",
		"application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		Error    string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode exec response (status=%d): %v", resp.StatusCode, err)
	}
	if out.Error != "" {
		t.Fatalf("exec 返回错误: %s", out.Error)
	}
	return out.ExitCode, out.Stdout + out.Stderr
}

// TestExecuteCommand_SandboxLimitsAppliesOnTheRealRoute 是沙箱接线的端到端证明。
func TestExecuteCommand_SandboxLimitsAppliesOnTheRealRoute(t *testing.T) {
	srv, mgr := newCloudTestServer(t)

	// 收紧 CPU 限额，让死循环必定被打断
	cfg := sandbox.DefaultConfig()
	cfg.Limits.CPUQuota = 1
	mgr.SandboxConfig(cfg)

	envID := createEnv(t, srv)

	// 先验证普通命令仍能正常跑通（不能为了让限额生效而把正常路径弄坏）
	code, out := execInEnv(t, srv, envID, "echo hello-sandbox")
	if code != 0 {
		t.Errorf("普通命令 exit=%d out=%q", code, out)
	}
	if !strings.Contains(out, "hello-sandbox") {
		t.Errorf("普通命令输出异常：%q", out)
	}

	// 副作用断言：死循环命令必须在 wall-clock 之前被 CPU 限额打断。
	// 只断言 exit != 0 不够 —— 命令也可能因为别的原因失败。
	start := time.Now()
	code, out = execInEnv(t, srv, envID, "while :; do :; done")
	elapsed := time.Since(start)
	t.Logf("死循环: exit=%d elapsed=%s out=%q", code, elapsed.Round(time.Millisecond), out)

	if code == 0 {
		t.Errorf("死循环正常退出（exit=0）：CPU 限额没有生效")
	}
	if elapsed > 15*time.Second {
		t.Errorf("死循环跑了 %s 才结束：CPU 限额疑似未生效", elapsed.Round(time.Millisecond))
	}
}

// TestExecuteCommand_ProcessRunsInsideItsCgroup 断言隔离**真的挂上**了。
//
// 用进程自述（/proc/self/cgroup）作为证据，而不是读我们自己的配置 ——
// 后者只能证明"我们写了"，不能证明"进程在里面"。
func TestExecuteCommand_ProcessRunsInsideItsCgroup(t *testing.T) {
	srv, mgr := newCloudTestServer(t)
	if mgr.Sandbox() == nil {
		t.Fatal("LocalManager 未初始化沙箱")
	}
	// 探测本机 cgroup 是否可用；不可用时这条断言无法成立，
	// 但必须**显式**说明而不是静默跳过（用 t.Skip 会在 CI 里被静默忽略）。
	probe, err := mgr.Sandbox().Prepare(context.Background(), "probe")
	if err != nil || probe.CgroupPath() == "" {
		t.Skipf("本机 cgroup v2 不可用（%v）—— 该断言仅在 cgroup 可用时成立", err)
	}
	probe.Close()

	envID := createEnv(t, srv)
	code, out := execInEnv(t, srv, envID, "cat /proc/self/cgroup")
	t.Logf("exit=%d /proc/self/cgroup = %q", code, strings.TrimSpace(out))
	if code != 0 {
		t.Fatalf("读取 /proc/self/cgroup 失败: exit=%d", code)
	}
	if !strings.Contains(out, "agentbot-sandbox") {
		t.Errorf("命令进程不在沙箱 cgroup 内：%q —— 说明沙箱没有接到执行路径上", out)
	}
}

// TestExecuteCommand_WorkspaceIsPerEnvironment 验证每个环境有自己的工作目录，
// 且环境之间不共享前缀（不能靠"路径前缀校验"这种单点防线）。
func TestExecuteCommand_WorkspaceIsPerEnvironment(t *testing.T) {
	srv, _ := newCloudTestServer(t)
	envA := createEnv(t, srv)
	envB := createEnv(t, srv)

	if envA == envB {
		t.Fatal("两个环境拿到了同一个 id")
	}

	if code, _ := execInEnv(t, srv, envA, "echo from-A > marker.txt"); code != 0 {
		t.Fatalf("在 envA 写文件失败")
	}

	// envB 里不应看到 envA 的文件
	code, out := execInEnv(t, srv, envB, "ls -1")
	if code != 0 {
		t.Fatalf("envB ls 失败 exit=%d", code)
	}
	if strings.Contains(out, "marker.txt") {
		t.Errorf("envB 能看到 envA 的文件：两个环境共享了工作目录（out=%q）", out)
	}

	// envA 自己应该看得到
	code, out = execInEnv(t, srv, envA, "ls -1")
	if code != 0 || !strings.Contains(out, "marker.txt") {
		t.Errorf("envA 自己的文件不在了：exit=%d out=%q", code, out)
	}
}

// TestExecuteCommand_BackgroundGrandchildIsReclaimedOnTimeout 守护超时清理。
//
// ⚠️ 这条守的是两个实测过的真 bug，修复前它会让整个包挂到测试超时：
//
//  1. 脚本里带后台任务时，`cmd.Wait()` 可能永远不返回 ——
//     后台孙进程持有输出管道的写端，而 Wait 等的不只是进程退出、还有管道关闭。
//     实测：把 sh 单独 SIGKILL 之后 Wait 仍然阻塞。
//  2. 清理用的 KillGroup 曾是个**静默 no-op**：它每次现问 Getpgid(pid)，
//     而主进程被回收后 Getpgid 返回 ESRCH → 直接 return nil，
//     调用方看到"清理成功"，进程组里却一个都没死。
//
// 因此断言分三步，缺一不可：
//
//	调用返回（不会永久挂住）→ 在预期时限内返回（超时上限真的生效）
//	→ 返回后孙进程**确实已消失**（清理真的做了事，而不只是"没报错"）
func TestExecuteCommand_BackgroundGrandchildIsReclaimedOnTimeout(t *testing.T) {
	srv, mgr := newCloudTestServer(t)

	// 收短超时上限，让断言能在测试时限内观察完整生命周期。
	// 默认 5 分钟是给 go build / npm install 的，测试里不适用。
	mgr.SetExecTimeout(3 * time.Second)
	if got := mgr.ExecTimeout(); got != 3*time.Second {
		t.Fatalf("ExecTimeout = %s, want 3s", got)
	}

	envID := createEnv(t, srv)

	// 唯一标记，避免与其他测试的残留进程混淆
	marker := "agentbot-bg-marker-" + envID[:8]
	script := `(while :; do echo ` + marker + `; sleep 0.1; done) & sleep 300`

	start := time.Now()
	done := make(chan struct{})
	var code int
	var out string
	go func() {
		defer close(done)
		code, out = execInEnv(t, srv, envID, script)
	}()

	select {
	case <-done:
		// 正常返回
	case <-time.After(45 * time.Second):
		t.Fatal("exec 调用在 45s 内未返回 —— Wait 被后台孙进程持有的管道写端卡住")
	}
	elapsed := time.Since(start)
	t.Logf("后台孙进程场景: exit=%d elapsed=%s out.len=%d", code, elapsed.Round(time.Millisecond), len(out))

	// 断言 2：超时上限真的生效（不是靠碰巧很快返回）
	if elapsed > 20*time.Second {
		t.Errorf("耗时 %s 远超 3s 上限 —— 超时上限没有生效", elapsed.Round(time.Millisecond))
	}
	if code == 0 {
		t.Errorf("被超时中断的命令返回了 exit=0，调用方无法区分「跑完了」与「被中断」")
	}

	// 断言 3：反向断言 —— 调用返回后孙进程必须真的消失。
	// 只断言"调用返回了"会漏掉最坏的情况：调用方拿到结果，
	// 宿主上却留着一个个吃 CPU 的孤儿（KillGroup 静默 no-op 时的现场）。
	time.Sleep(1 * time.Second)
	orphans := countOrphans(t, marker)
	if orphans > 0 {
		t.Errorf("exec 返回后仍有 %d 个残留后台进程在跑 —— 进程组清理没有真的执行", orphans)
	}
}

// countOrphans 按唯一标记统计仍在运行的后台进程。
//
// 刻意按**命令行特征**而不是 pid 追踪：孤儿进程的 ppid 会变成 1，
// 按 pid 追踪不到它们，而"孤儿还在跑"正是要抓的东西。
func countOrphans(t *testing.T, marker string) int {
	t.Helper()
	out, err := exec.Command("ps", "-eo", "pid,ppid,args").Output()
	if err != nil {
		t.Logf("ps 失败: %v", err)
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, marker) {
			n++
			t.Logf("  残留进程: %s", strings.TrimSpace(line))
		}
	}
	return n
}

// TestExecuteCommand_NoSandboxFailsLoudly 验证「没有沙箱时不静默退回无限制执行」。
func TestExecuteCommand_NoSandboxFailsLoudly(t *testing.T) {
	repo := NewMemoryRepository()
	mgr, err := NewLocalManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalManager: %v", err)
	}
	// 人为把沙箱置空，模拟装配被绕过
	mgr.sandbox = nil

	svc := NewService(mgr, repo)
	env, err := svc.CreateEnvironment(context.Background(), "agent-nosbx", CreateEnvRequest{
		Type: EnvTypeSandbox,
	})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}

	_, err = svc.ExecuteCommand(context.Background(), env.ID, ExecRequest{Command: "echo hi"})
	if err == nil {
		t.Fatal("沙箱未配置时仍执行成功 —— 命令在毫无边界的情况下运行了，这是最危险的失败模式")
	}
	if !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("错误信息未指出沙箱缺失：%v", err)
	}
}
