package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// cgroup2Magic 是 cgroup2fs 的 statfs f_type 值（内核源码中的 CGROUP2_SUPER_MAGIC）。
const cgroup2Magic = 0x63677270

// Limited 是一个已施加限制的执行上下文，由 Manager.Prepare 返回。
type Limited struct {
	manager *Manager
	cg      *cgroupV2
	applied Applied
	limits  Limits
	agentID string
	pid     int
	// pgid 在进程启动的那一刻就抓下来 —— 见 KillGroup 的注释：
	// 等到需要杀的时候再去问内核，主进程可能已被回收，问不到了。
	pgid int
}

// Applied 返回**实际生效**的限制描述（这是事实，不是请求）。
func (l *Limited) Applied() Applied { return l.applied }

// CgroupPath 返回本次使用的 cgroup 路径（未启用 cgroup 时为空）。
func (l *Limited) CgroupPath() string {
	if l.cg == nil {
		return ""
	}
	return l.cg.Path()
}

// AgentID 返回被监管的 Agent 标识。
func (l *Limited) AgentID() string { return l.agentID }

// PID 返回最近一次 Run 启动的进程 pid（未运行时为 0）。
func (l *Limited) PID() int { return l.pid }

// Attach 把进程属性与 rlimit 引导逻辑安装到 cmd 上。
//
// 机制（为什么是这样而不是更简单的写法）：
//
//	本 Go 版本的 syscall.SysProcAttr 没有 Rlimits 字段，因此无法让内核
//	在 exec 前替我们应用 rlimit。替代方案是把「设置 rlimit → exec 真任务」
//	作为一个引导 shell 进程的第一段脚本，用 `exec` 原地替换掉自己：
//
//	    ulimit -c 0; ulimit -t <cpu>; ulimit -v <kb>; ulimit -f <kb>; ulimit -n <n>
//	    exec sh -c '<脚本>'
//
//	`exec` 是关键 —— 它让真任务**继承引导进程的 rlimit**并复用同一个 pid。
//	若写成不带 exec 的嵌套调用，限制虽然也继承了，但会多一层常驻的 sh，
//	杀进程组时更难判断真正被监管的是谁。
//
// ⚠️ 必须在 cmd.Start() **之前**调用：SysProcAttr 在 fork 那一刻就被内核读取，
// Start 之后再改是静默无效的。
func (l *Limited) Attach(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	attr := &syscall.SysProcAttr{Setpgid: true}
	if cmd.SysProcAttr != nil {
		// 保留调用方已设置的其他属性，只补我们要的两项
		attr = cmd.SysProcAttr
		attr.Setpgid = true
	}
	// CgroupFD：让内核在 clone 时就把进程放进 cgroup —— 彻底消除
	// 「已 fork、未 attach」那段不受约束的时间窗。
	if fd := l.cgroupFD(); fd >= 0 {
		attr.UseCgroupFD = true
		attr.CgroupFD = fd
	}
	cmd.SysProcAttr = attr
}

// cgroupFD 打开 cgroup 目录并返回 fd，供 SysProcAttr.CgroupFD 使用。
func (l *Limited) cgroupFD() int {
	if l.cg == nil || l.cg.path == "" {
		return -1
	}
	f, err := os.Open(l.cg.path)
	if err != nil {
		return -1
	}
	return int(f.Fd())
}

// rlimitUlimitScript 把 rlimit 配置翻译成一段 ulimit 引导脚本。
//
// 用 ulimit 而不是在引导进程里调 Setrlimit，是为了让引导逻辑保持纯 shell：
// 父进程只需拼一段字符串，不需要额外编译一个 helper 二进制。
// 单位换算：ulimit -v/-f 以 KiB 计，故字节数除以 1024。
func (l *Limited) rlimitUlimitScript() string {
	var b bytes.Buffer
	b.WriteString("ulimit -c 0") // 关 core dump：被监管进程崩溃时不应把宿主磁盘写满
	if l.limits.CPUQuota > 0 {
		fmt.Fprintf(&b, "; ulimit -t %d", l.limits.CPUQuota)
	}
	if l.limits.MemoryBytes > 0 {
		fmt.Fprintf(&b, "; ulimit -v %d", l.limits.MemoryBytes/1024)
	}
	if l.limits.MaxFileSize > 0 {
		fmt.Fprintf(&b, "; ulimit -f %d", l.limits.MaxFileSize/1024)
	}
	if l.limits.MaxOpenFiles > 0 {
		fmt.Fprintf(&b, "; ulimit -n %d", l.limits.MaxOpenFiles)
	}
	return b.String()
}

// Track 在进程启动后把 pid 移入 cgroup。
//
// ⚠️ 与 Attach 的 CgroupFD 的关系：如果内核支持 UseCgroupFD 且调用方
// 传了有效的 fd，进程在 clone 时就已经在 cgroup 里了，这里再写一次
// cgroup.procs 是幂等的。Track 真正兜底的是「CgroupFD 不被内核接受」
// 的平台 —— 那种情况下存在一个不受约束的时间窗，必须在 Warnings 里说明。
func (l *Limited) Track(pid int) error {
	l.pid = pid
	// 立刻缓存 pgid：这是清理阶段唯一的句柄，晚一步主进程可能已被回收。
	if pgid, err := syscall.Getpgid(pid); err == nil {
		l.pgid = pgid
	}
	if l.cg == nil {
		return nil
	}
	if err := l.cg.Attach(pid); err != nil {
		l.applied.Warnings = append(l.applied.Warnings,
			fmt.Sprintf("cgroup attach(pid=%d) failed, rlimit-only in effect: %v", pid, err))
		return err
	}
	return nil
}

// KillGroup 按进程组终止整棵进程树。
//
// ⚠️ 为什么必须按 -pgid：Agent 执行的是 `sh -c "go build ./..."`，
// 杀 sh 不会杀掉 go build 及其编译子进程，它们会被 init 收养并继续吃 CPU。
// 容器方案里销毁容器即全部回收，进程级方案必须自己处理这件事。
//
// ⚠️⚠️ 这里修的是一个**静默 no-op**，实测才暴露出来：
// 原实现每次现问 syscall.Getpgid(l.pid)。主进程一旦被回收（exec 路径下
// 管道关闭即返回，命令本身可能还在跑；CommandContext 的 cancel 也会回收），
// Getpgid 返回 ESRCH，于是函数走进"进程可能已退出"分支 **直接 return nil** ——
// 调用方看到成功，进程组里却一个都没死，孙进程继续吃 CPU。
// 现场实测：主进程 pid 已被回收，`ps -g <pgid>` 里还躺着一个 sleep 0.2 循环。
//
// 因此 pgid 在进程启动那一刻就抓下来缓存（见 TrackPID），
// 并且识别 ESRCH 之外的失败必须返回错误 —— 清理失败不能报平安。
//
// 用 -pgid 而不是逐个杀 pid：进程组是唯一能覆盖未知孙进程的句柄。
// 若组里恰好还有别的进程（不该发生：Setpgid 让每个沙箱自成一组长），
// 代价是误杀；但漏杀的代价是宿主机上永久跑着一个失控循环，
// 两者相权取前者。
func (l *Limited) KillGroup() error {
	pgid := l.pgid
	if pgid <= 0 && l.pid > 0 {
		// 兜底：TrackPID 未被调用时现问一次
		if g, err := syscall.Getpgid(l.pid); err == nil {
			pgid = g
		}
	}
	if pgid <= 0 {
		return fmt.Errorf("sandbox: no process group to kill (pid=%d pgid=%d)", l.pid, l.pgid)
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		if err == syscall.ESRCH {
			// 整组都已退出 —— 这是成功
			return nil
		}
		return fmt.Errorf("sandbox: kill process group %d: %w", pgid, err)
	}
	return nil
}

// Pgid 返回缓存的进程组 id（未启动时为 0）。
func (l *Limited) Pgid() int { return l.pgid }

// CleanupAfter 轮询等待进程退出，超时则杀整个进程组。
func (l *Limited) CleanupAfter(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if l.pid <= 0 {
			return
		}
		if err := syscall.Kill(l.pid, 0); err != nil {
			return // 已退出
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = l.KillGroup()
}

// Close 回收 cgroup。**必须**在进程结束后调用（defer 即可）。
func (l *Limited) Close() {
	if l.cg == nil {
		return
	}
	if err := l.cg.Remove(); err != nil {
		l.applied.Warnings = append(l.applied.Warnings,
			fmt.Sprintf("cgroup cleanup failed (leftover dir %s): %v", l.cg.Path(), err))
	}
	l.cg = nil
}

// MemoryPeak 返回本次沙箱观测到的内存峰值（字节）；未启用 cgroup 时为 0。
func (l *Limited) MemoryPeak() uint64 {
	if l.cg == nil {
		return 0
	}
	return l.cg.PeekMemory()
}

// OOMKilled 报告本次沙箱是否发生过 OOM kill。
// 这是「内存限额真的生效」的副作用证据。
func (l *Limited) OOMKilled() bool {
	if l.cg == nil {
		return false
	}
	return l.cg.OOMKilled()
}

// PidsRejected 报告是否有进程因 pids.max 被拒绝。
func (l *Limited) PidsRejected() bool {
	if l.cg == nil {
		return false
	}
	return l.cg.PidsRejected()
}

// RunContext 是被监管任务的执行结果。
//
// ⚠️ 这里刻意**同时**给出「配置值」与「副作用证据」两类字段。
// 只有前者时，一个限制完全没生效的实现也能让断言通过
// （配置读回来必然等于写进去，那是在测自己）。
type RunContext struct {
	Applied    Applied
	ExitCode   int
	Stdout     string
	Stderr     string
	Duration   time.Duration
	MemoryPeak uint64
	OOMKilled  bool
	PidsReject bool
	TimedOut   bool
}

// Run 在沙箱中执行一段 shell 脚本，并返回可验证的执行结果。
//
// 这是给 executor 与 cloud 模块共用的入口，避免每个模块各自复制
// 一遍「Prepare → Attach → Start → Track → Wait → Close」的样板 ——
// 复制出来的第二份几乎必然漏掉 Track 或 Close 中的某一步。
func (m *Manager) Run(ctx context.Context, agentID, workDir, script string, timeout time.Duration) (*RunContext, error) {
	lim, err := m.Prepare(ctx, agentID)
	if err != nil {
		return nil, err
	}
	defer lim.Close()

	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 引导脚本：先落 rlimit，再用 exec 原地替换成真任务。
	// 用 exec 而非嵌套调用 → 真任务继承限制且复用同一 pid，
	// 杀进程组时不会多出一层无意义的 sh。
	full := lim.rlimitUlimitScript() + "; exec sh -c " + shellQuote(script)

	cmd := exec.CommandContext(runCtx, "sh", "-c", full)
	if workDir != "" {
		cmd.Dir = workDir
	}
	lim.Attach(cmd)

	var stdout, stderr lockedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// 见下方 waitWithDeadline 的注释：Wait 的阻塞可能来自管道，不由 cmd 控制，
	// 因此必须在独立的 goroutine 里等，失败路径才能继续往下走。
	waitCh := make(chan error, 1)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sandbox: start process: %w", err)
	}
	// 内核支持 UseCgroupFD 时进程已在 cgroup 内，这次写入是幂等的兜底
	_ = lim.Track(cmd.Process.Pid)

	// ⚠️ 不能直接 `cmd.Wait()` —— 这一步用实测证明过一个会**永久挂住**的坑。
	//
	// 脚本形如 `(后台循环) & sleep 30` 时，Wait 并不返回：
	// 实测在 sh 被 SIGKILL 之后 Wait 仍然阻塞，因为
	// **Wait 等的不只是进程退出，还包括输出管道被关闭**。
	// 后台孙进程继承了 stdout 的写端，只要它还活着，管道就不关闭，
	// Wait 就一直等下去 —— 与主进程的存活状态无关。
	//
	// 且超时路径也救不了：exec.CommandContext 的默认取消只杀主进程，
	// 孙进程照样活着持有写端。结果就是一个吃满 CPU 的孤儿 + 一个永不返回的调用。
	// （本仓库的 executor 会在 `sh -c` 下跑 go build / npm install，
	// 这类工具链大量使用后台任务，撞上的概率不低。）
	//
	// 正确做法：给 Wait 加一个 wall-clock 上限，超时后**按进程组**杀整棵树，
	// 再等管道自然关闭（此时孙进程已死，写端必然释放）。
	waitErr, waitTimedOut := waitWithDeadline(cmd, waitCh, timeout)
	duration := time.Since(start)

	res := &RunContext{
		Applied:    lim.Applied(),
		Duration:   duration,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		MemoryPeak: lim.MemoryPeak(),
		OOMKilled:  lim.OOMKilled(),
		PidsReject: lim.PidsRejected(),
		TimedOut:   runCtx.Err() == context.DeadlineExceeded || waitTimedOut,
	}

	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				res.ExitCode = 128 + int(ws.Signal())
			} else {
				res.ExitCode = exitErr.ExitCode()
			}
		} else {
			res.ExitCode = -1
		}
	}

	// 超时（外部 ctx 或 Wait 自身超限）时 CommandContext 只杀了主进程，
	// 孙进程仍在跑并持有管道写端 → 按进程组补刀，整棵树一起回收。
	// ⚠️ KillGroup 的错误不能吞：清理失败意味着宿主上留了一个失控进程，
	// 那是必须让调用方知道的事实（记进 Warnings，随 Applied 一起返回）。
	if res.TimedOut || waitTimedOut {
		if err := lim.KillGroup(); err != nil {
			res.Applied.Warnings = append(res.Applied.Warnings,
				"process group cleanup failed: "+err.Error())
		}
		lim.CleanupAfter(2 * time.Second)
		drainWait(waitCh, 5*time.Second)
	}

	return res, nil
}

// waitWithDeadline 在 wall-clock 上限内等待进程结束。
//
// 返回 (waitErr, timedOut)。timedOut 为真时调用方必须杀进程组 ——
// 只返回错误而不清理会留下孤儿进程，那比直接超时更糟。
func waitWithDeadline(cmd *exec.Cmd, waitCh chan error, timeout time.Duration) (error, bool) {
	go func() { waitCh <- cmd.Wait() }()

	// +5s 是留给「进程正常收尾」的余量：
	// 外部 ctx 已由 CommandContext 处理，这里的上限只负责兜住
	// 「主进程已退、管道被孙进程占着」这种 Wait 自身不会结束的情况。
	select {
	case err := <-waitCh:
		return err, false
	case <-time.After(timeout + 5*time.Second):
		return nil, true
	}
}

// drainWait 在进程组被杀之后回收 Wait 的返回值，避免 goroutine 泄漏。
// 拿不到也无妨 —— 通道有缓冲，goroutine 不会因此卡住。
func drainWait(waitCh chan error, d time.Duration) {
	select {
	case <-waitCh:
	case <-time.After(d):
	}
}

// shellQuote 用单引号包裹脚本。
//
// ⚠️ 必须处理脚本里本身含单引号的情况，否则 `sh -c '...'` 会被提前闭合，
// 后续内容被当成外层 shell 的命令执行 —— 那是一处典型的命令注入。
// 标准做法：把每个 ' 替换成 '\”（结束单引号 → 转义的单引号 → 重新开始）。
func shellQuote(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\\', '\'', '\'')
			continue
		}
		out = append(out, s[i])
	}
	out = append(out, '\'')
	return string(out)
}

// lockedBuffer 是并发安全的输出缓冲。
//
// 为什么不用 bytes.Buffer：cmd.Stdout 与 cmd.Stderr 若共用同一个
// bytes.Buffer，os/exec 会为两个非 *os.File 的 writer 各起一个 goroutine，
// 并发写入产生数据竞争 —— `go test -race` 会直接报出来。
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
