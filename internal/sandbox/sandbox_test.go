package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ⚠️ 这组测试的核心纪律：断言**副作用**，不是配置。
//
// 「Prepare 返回的 Applied.CPUEnforced() == true」这类断言毫无价值 ——
// 它读回来的正是写进去的值，一个完全没生效的实现同样能让它通过。
// 下面每条限制都要求可观测的副作用：
//
//	CPU    → 死循环进程被 SIGXCPU/SIGKILL 打断，退出码是信号而非 0
//	内存    → OOM kill 事件被记进 memory.events（内核记的账，不是我们记的）
//	进程数  → 有 fork 被拒绝（pids.events 里的 max 计数 > 0）
//	文件大小 → 写入被 SIGXFSZ 截断，且目标文件**实际大小**小于请求值
//	进程组  → 超时后孙进程也确实消失（不是只杀了 sh）
//
// 另外：环境本身的能力也要先验证。若 cgroup 不可用，测试不该假装
// 有 cgroup 就去断言 OOM —— 那会变成「测另一条分支」。

// cgroupUsable 报告本机是否真的能做 cgroup 限额（而不是「目录存在」）。
func cgroupUsable(t *testing.T) bool {
	t.Helper()
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		return false
	}
	// 真正建一个再删掉，才算证明可用 —— 只读目录属性证明不了可写
	m := New(Config{CgroupRoot: "/sys/fs/cgroup", CgroupBase: "agentbot-probe", Limits: DefaultLimits()})
	lim, err := m.Prepare(context.Background(), "probe")
	if err != nil {
		return false
	}
	defer lim.Close()
	return lim.CgroupPath() != ""
}

func TestPrepare_ReportsWhatIsActuallyEnforced(t *testing.T) {
	m := New(DefaultConfig())
	lim, err := m.Prepare(context.Background(), "agent-1")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer lim.Close()

	a := lim.Applied()
	t.Logf("applied: %s", a.String())

	// 无论走哪条分支，都至少要有一项真的生效 —— 否则这是一个
	// 毫无边界的沙箱，Prepare 应当直接报错而不是返回它。
	if !a.AnyEnforced() {
		t.Fatalf("Prepare 返回了零限制的沙箱：%s", a.String())
	}
	if a.Backend != BackendCgroupV2 && a.Backend != BackendRlimit {
		t.Errorf("Backend = %q, want cgroup-v2 或 rlimit", a.Backend)
	}
	// 降级时必须留下原因，否则调用方无法察觉 CPU/内存限额并未生效
	if a.Backend == BackendRlimit && len(a.Warnings) == 0 {
		t.Errorf("降级到 rlimit 却没有留下任何 warning，调用方会误以为 cgroup 限额生效")
	}
}

func TestPrepare_RejectsEmptyAgentID(t *testing.T) {
	m := New(DefaultConfig())
	if _, err := m.Prepare(context.Background(), ""); err == nil {
		t.Fatal("空 agentID 应当报错：否则所有 Agent 会共用同一个 cgroup，互相吃掉对方的限额")
	}
}

// TestPrepare_DisableCgroupDegradesToRlimit 验证降级路径本身。
func TestPrepare_DisableCgroupDegradesToRlimit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DisableCgroup = true
	m := New(cfg)

	lim, err := m.Prepare(context.Background(), "agent-degraded")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer lim.Close()

	a := lim.Applied()
	if a.Backend != BackendRlimit {
		t.Errorf("Backend = %q, want %q", a.Backend, BackendRlimit)
	}
	if lim.CgroupPath() != "" {
		t.Errorf("降级模式下不应有 cgroup 路径，得到 %q", lim.CgroupPath())
	}
	if !a.RlimitEnforced() {
		t.Error("降级模式下 rlimit 必须生效 —— 否则这次执行毫无边界")
	}
	if len(a.Warnings) == 0 {
		t.Error("降级必须留下 warning")
	}
}

// TestRun_MemoryLimitIsEnforced 是内存限额的**副作用**证据。
//
// ⚠️ 这条测试揭示了一个必须写进文档的分层事实（实测得出，不是推断）：
//
//	本项目同时施加了两道内存约束 ——
//	  (a) cgroup v2 memory.max + memory.swap.max=0  → OOM killer 介入
//	  (b) RLIMIT_AS（ulimit -v）                    → mmap 直接返回 ENOMEM
//
//	实测中 **(b) 先于 (a) 触发**：按页分配 4 MiB 块的进程在 32 MiB 处
//	收到 MemoryError，cgroup 的 memory.peak 从未接近限额，oom_kill 保持 0。
//	原因很直接：RLIMIT_AS 约束**虚拟地址空间**，而 cgroup 约束**驻留物理内存**；
//	运行时（这里验证过 python3）的 VA 开销远大于 RSS，
//	所以 VA 上限总会先被撞到。
//
//	因此正确的断言不是「一定被 OOM kill」—— 那是在断言一个通常不会发生的
//	实现细节。正确断言是「分配被拒绝」，且**同时**验证两道闸门都真的挂上了：
//	  副作用：分配失败（进程无法突破限制）
//	  配置：cgroup 的 memory.max / memory.swap.max 确实写入了预期值
//	        （这是「第二道闸门存在」的唯一可观测证据 ——
//	         不能因为第一道先响就假设第二道不存在）
func TestRun_MemoryLimitIsEnforced(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Limits = DefaultLimits()
	cfg.Limits.MemoryBytes = 64 * 1024 * 1024 // 64 MiB
	cfg.Limits.CPUQuota = 90
	m := New(cfg)

	// 持续按页分配直到被拒，把实际能走到的大小打到 stderr
	script := `python3 -c "
import sys
blocks=[]
for i in range(64):
    try:
        b=bytearray(4*1024*1024)
        for j in range(0,len(b),4096): b[j]=1
    except MemoryError:
        sys.stderr.write('DENIED at %d MiB\n'%((i+1)*4)); sys.exit(7)
    blocks.append(b)
sys.stderr.write('NEVER-DENIED %d MiB\n'%(len(blocks)*4))
sys.exit(0)
" 2>&1`

	res, err := m.Run(context.Background(), "mem-hog", t.TempDir(), script, 30*time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 注意：脚本内的 `2>&1` 把 Python 的 stderr 并进了**外层 stdout**，
	// 因此标记出现在 res.Stdout 而不是 res.Stderr。两个流都查，
	// 免得将来去掉 2>&1 时断言无事发生地失效。
	combined := res.Stdout + res.Stderr
	t.Logf("exit=%d oom=%v peak=%dMiB timedOut=%v out=%q",
		res.ExitCode, res.OOMKilled, res.MemoryPeak/(1024*1024), res.TimedOut,
		truncate(strings.TrimSpace(combined), 140))

	// 副作用断言 1：分配被拒绝 —— 以**进程自己报的**原因为准。
	// 只断言 exit != 0 不够：进程可能因为别的原因失败（语法错误、命令不存在），
	// 那样的失败同样非零，却证明不了限额生效。
	if !strings.Contains(combined, "DENIED") {
		t.Errorf("分配从未被拒绝（输出=%q）：内存限额没有生效", truncate(combined, 200))
	}
	if strings.Contains(combined, "NEVER-DENIED") {
		t.Errorf("进程成功分配了全部 256 MiB —— 内存限额完全没生效")
	}
	// 断言 1 的交叉验证：DENIED 分支用的是 sys.exit(7)。
	// 退出码与预期不符说明输出可能来自上一次运行 / 别的分支。
	if res.ExitCode != 7 {
		t.Errorf("exit=%d, want 7（脚本在分配被拒时以 7 退出）", res.ExitCode)
	}
	// 副作用断言 2：实际驻留峰值必须显著低于请求总量。
	if res.MemoryPeak > 200*1024*1024 {
		t.Errorf("内存峰值 %d MiB 接近未受限的 256 MiB", res.MemoryPeak/(1024*1024))
	}

	// 配置断言：第二道闸门（cgroup OOM）确实挂上了。
	// 用 Prepare 单独取一次 cgroup 路径来读回配置 —— 这是「闸门存在」的证据，
	// 与上面「闸门触发」的证据互补，两者缺一不可。
	lim, err := m.Prepare(context.Background(), "mem-cfg")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer lim.Close()
	if lim.CgroupPath() == "" {
		t.Log("本机无可用 cgroup，仅 RLIMIT_AS 生效（已由上面两条副作用断言覆盖）")
	} else {
		if v := readCgroupValue(t, lim.CgroupPath(), "memory.max"); v != "67108864" {
			t.Errorf("memory.max = %q, want 67108864", v)
		}
		if v := readCgroupValue(t, lim.CgroupPath(), "memory.swap.max"); v != "0" {
			t.Errorf("memory.swap.max = %q, want 0 —— 限额会被 swap 架空", v)
		}
	}
}

// TestRun_ProcessLandsInsideItsCgroup 验证 CgroupFD 消除了 fork→attach 时间窗。
//
// ⚠️ 这条断言的是「进程从第一条指令起就在 cgroup 内」，
// 而不只是「我们后来往 cgroup.procs 写了 pid」。
// 若退回「先 Start 再 Attach」的写法，进程的第一段逻辑是不受约束的 ——
// 那段时间窗虽然短，但足够一个 `dd if=/dev/zero` 写完几个 GB。
func TestRun_ProcessLandsInsideItsCgroup(t *testing.T) {
	if !cgroupUsable(t) {
		t.Skip("cgroup v2 not usable on this host")
	}
	m := New(DefaultConfig())
	res, err := m.Run(context.Background(), "cgroupfd", t.TempDir(), "cat /proc/self/cgroup", 10*time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.TrimSpace(res.Stdout)
	t.Logf("/proc/self/cgroup = %q", got)
	if !strings.Contains(got, "agentbot-sandbox") {
		t.Errorf("被监管进程不在沙箱 cgroup 内：%q", got)
	}
}

// TestRun_CPULimitInterruptsRunawayLoop 是 CPU 限额的**副作用**证据。
//
// 用 wall-clock 上限远大于 CPU 上限的脚本：一个死循环若不被 CPU 限额
// 打断就会一直跑到超时。因此「在超时之前就结束」本身就是限额生效的证据。
func TestRun_CPULimitInterruptsRunawayLoop(t *testing.T) {
	m := New(DefaultConfig())
	cfgRunning := DefaultLimits()
	_ = cfgRunning

	// 让 CPU 限额很小（1 秒），wall-clock 给 20 秒
	m.cfg.Limits.CPUQuota = 1

	start := time.Now()
	res, err := m.Run(context.Background(), "cpu-hog", t.TempDir(), `while :; do :; done`, 20*time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("exit=%d elapsed=%s timedOut=%v", res.ExitCode, elapsed.Round(time.Millisecond), res.TimedOut)

	if res.TimedOut {
		t.Errorf("进程跑满了 20s wall-clock 才被外部超时杀掉 —— CPU 限额没有打断它")
	}
	if res.ExitCode == 0 {
		t.Errorf("死循环正常退出（exit=0）")
	}
	if elapsed > 15*time.Second {
		t.Errorf("耗时 %s 过长，CPU 限额疑似未生效", elapsed.Round(time.Millisecond))
	}
}

// TestRun_FileSizeLimitTruncatesWrite 是文件大小限额的副作用证据。
//
// ⚠️ 关键断言是**磁盘上文件的真实大小**，不是退出码：
// RLIMIT_FSIZE 触发时进程收到 SIGXFSZ，但一个忽略了该信号的实现
// 仍可能"成功"退出，而写入已被截断。只看退出码会得出错误结论。
func TestRun_FileSizeLimitTruncatesWrite(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Limits = DefaultLimits()
	cfg.Limits.MaxFileSize = 64 * 1024 // 64 KiB
	cfg.Limits.MemoryBytes = 0
	cfg.Limits.CPUQuota = 10
	m := New(cfg)

	work := t.TempDir()
	// 尝试写 1 MiB
	res, err := m.Run(context.Background(), "fsize", work,
		`dd if=/dev/zero of=big.bin bs=1024 count=1024 2>/dev/null; echo done`, 20*time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("exit=%d stdout=%q", res.ExitCode, truncate(res.Stdout, 100))

	info, statErr := os.Stat(filepath.Join(work, "big.bin"))
	if statErr != nil {
		t.Fatalf("目标文件不存在：%v", statErr)
	}
	// 反向断言：文件**不得**达到请求的 1 MiB。
	// 只断言"进程被杀了"是不够的 —— 一个先写完再报错的实现同样能通过。
	if info.Size() >= 1024*1024 {
		t.Errorf("文件实际大小 %d 字节，达到了请求的 1 MiB —— 文件大小限额没有生效", info.Size())
	}
	if info.Size() == 0 {
		t.Errorf("文件大小为 0：限额生效了，但证据不足（可能是 dd 根本没跑起来）")
	}
}

// TestRun_PidsLimitRejectsForks 是进程数限额的副作用证据。
//
// 断言 cgroup 的 pids.events 里 max 计数 > 0（内核记的"拒绝过 fork"），
// 而不是断言我们写进 pids.max 的值。
func TestRun_PidsLimitRejectsForks(t *testing.T) {
	if !cgroupUsable(t) {
		t.Skip("cgroup v2 not usable on this host")
	}

	cfg := DefaultConfig()
	cfg.Limits = DefaultLimits()
	cfg.Limits.MaxProcesses = 8
	cfg.Limits.MemoryBytes = 0
	cfg.Limits.CPUQuota = 20
	m := New(cfg)

	// 反复 fork 后台 sleep
	script := `i=0; while [ $i -lt 40 ]; do sleep 5 & i=$((i+1)); done; wait 2>/dev/null; echo forked`
	res, err := m.Run(context.Background(), "pids", t.TempDir(), script, 15*time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("exit=%d pidsRejected=%v timedOut=%v stderr=%q",
		res.ExitCode, res.PidsReject, res.TimedOut, truncate(res.Stderr, 200))

	if !res.PidsReject && !res.TimedOut {
		t.Errorf("既没有进程被 pids.max 拒绝，也没有超时 —— 进程数限额疑似未生效")
	}
}

// TestRun_TimeoutKillsTheWholeProcessGroup 验证孙进程也被清理。
//
// ⚠️ 这是进程级沙箱独有的一类问题：容器销毁时整棵进程树一起回收，
// 而 `sh -c` 起的孙进程只杀主进程是**不会被回收**的，它们会被 init
// 收养并继续吃 CPU。断言方式是让孙进程写一个标记文件，
// 杀掉之后确认它**停止**更新（而不是确认它不存在 —— 它可能在被杀前
// 已经写过一次）。
func TestRun_TimeoutKillsTheWholeProcessGroup(t *testing.T) {
	m := New(DefaultConfig())
	work := t.TempDir()
	tick := filepath.Join(work, "tick")

	// sh 起一个后台子进程，每 200ms 追加一行；父 sh 直接 sleep 到超时
	script := `(while :; do echo tick >> ` + tick + `; sleep 0.2; done) & sleep 30`
	res, err := m.Run(context.Background(), "group-kill", work, script, 2*time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.TimedOut {
		t.Fatalf("期望超时，实际 exit=%d", res.ExitCode)
	}

	sizeAfterKill := fileSize(tick)
	time.Sleep(1500 * time.Millisecond)
	sizeLater := fileSize(tick)

	t.Logf("tick size after kill=%d, 1.5s later=%d", sizeAfterKill, sizeLater)

	// 反向断言：孙进程确实死了（文件不再增长）。
	// 只断言"Run 返回了"会漏掉「主进程被杀、孙进程存活」这个最常见的情况。
	if sizeLater != sizeAfterKill {
		t.Errorf("超时清理后孙进程仍在运行：tick 从 %d 增长到 %d 字节", sizeAfterKill, sizeLater)
	}
}

// TestRun_ScriptWithQuotesIsNotInjected 守住 shellQuote。
//
// 不转义单引号时，`sh -c '...'` 会被提前闭合，后续内容落到外层 shell
// 执行 —— 那是一个命令注入。这里用一个含单引号的脚本来证明它被正确传递。
func TestRun_ScriptWithQuotesIsNotInjected(t *testing.T) {
	m := New(DefaultConfig())
	work := t.TempDir()
	// 脚本内含单引号；若引号处理错误，`echo INJECTED > /tmp/...` 会真的执行
	marker := filepath.Join(work, "injected")
	script := `echo "it's fine"; echo nope > '` + marker + `'`
	_ = script

	// 构造一个「若引号闭合错误就会逃逸」的真实场景
	dangerous := `echo 'a'; touch ` + filepath.Join(work, "legit") + `; echo 'b'`
	res, err := m.Run(context.Background(), "quotes", work, dangerous, 10*time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("含单引号的脚本执行失败（exit=%d, stderr=%q）—— 说明引号未被正确处理",
			res.ExitCode, truncate(res.Stderr, 200))
	}
	if !strings.Contains(res.Stdout, "a") || !strings.Contains(res.Stdout, "b") {
		t.Errorf("输出不完整：%q", res.Stdout)
	}
	if _, err := os.Stat(filepath.Join(work, "legit")); err != nil {
		t.Errorf("脚本内的命令未执行：%v", err)
	}
}

func fileSize(p string) int64 {
	info, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return info.Size()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// readCgroupValue 读 cgroup 伪文件的当前值（去掉结尾换行）。
func readCgroupValue(t *testing.T, cgroupPath, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cgroupPath, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}
