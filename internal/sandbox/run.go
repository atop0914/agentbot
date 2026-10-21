package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// exitTimeout 是「命令因 wall-clock 超时被中断」的退出码。
// 沿用 coreutils timeout(1) 的 124，让运维不必额外查文档就能认出来。
const exitTimeout = 124

// Limited 是一个已施加限制的执行上下文，由 Manager.Prepare 返回。
type Limited struct {
	manager *Manager
	// cg 是 cgroup 句柄。类型定义只在 linux 存在，因此这里用接口持有：
	// 非 linux 平台上它恒为 nil，且 Prepare 会把「不支持」记进 Warnings。
	cg      cgroupHandle
	cgPath  string
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
	return l.cgPath
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
	attr := newSysProcAttr()
	if cmd.SysProcAttr != nil {
		// 保留调用方已设置的其他属性，只补我们要的两项
		attr = cmd.SysProcAttr
		attr = withOwnProcessGroup(attr)
	}
	// CgroupFD：让内核在 clone 时就把进程放进 cgroup —— 彻底消除
	// 「已 fork、未 attach」那段不受约束的时间窗。
	// 该字段只存在于 linux 的 SysProcAttr，故由平台文件提供实现。
	attr = l.applyCgroupFD(attr)
	cmd.SysProcAttr = attr
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
	if pgid, err := groupOf(pid); err == nil {
		l.pgid = pgid
	}
	if l.cg == nil {
		return nil
	}
	if err := l.attachCgroup(pid); err != nil {
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
		if g, err := groupOf(l.pid); err == nil {
			pgid = g
		}
	}
	if pgid <= 0 {
		return fmt.Errorf("sandbox: no process group to kill (pid=%d pgid=%d)", l.pid, l.pgid)
	}
	if err := killGroup(pgid); err != nil {
		if isNoSuchProcess(err) {
			// 整组都已退出 —— 这是成功
			return nil
		}
		return fmt.Errorf("sandbox: kill process group %d: %w", pgid, err)
	}
	return nil
}

// Pgid 返回缓存的进程组 id（未启动时为 0）。
func (l *Limited) Pgid() int { return l.pgid }

// Supervisor 是一个**长驻进程**的监管句柄，由 Manager.Supervise 返回。
//
// 与 Run 的区别：Run 是「一次性执行一段脚本，等它结束」，而 MCP server 这类
// 进程是「启动一次、长期活着、反复收发消息」。之前只有 Run 一个入口，
// 想监管长驻进程就只能绕过沙箱自己 exec —— 那等于把「进程组回收」
// 与「墙钟超时」这两条纪律复制出第二份，而复制出来的第二份必然漏掉某一步
// （本仓库已在 executor/cloud 上踩过一次）。
//
// ⚠️ Stop 必须被调用（defer 即可）：它负责杀整个进程组并回收 cgroup。
// 漏掉的代价是宿主上留一个吃 CPU 的孤儿 + 一个无法删除的 cgroup 目录。
type Supervisor struct {
	lim *Limited
	cmd *exec.Cmd
	// ctx 是启动时传入的那个 —— exec.Cmd 没有公开的 ctx 访问器，
	// 而不保存它就无法在 Wait 里区分「进程自己退了」与「ctx 被取消了」。
	ctx context.Context
	// waitCh 只会有一次写入，由 waitLoop 消费。
	waitCh chan error
	// exitCh 在进程退出时被关闭，exitErr 保存退出错误。
	//
	// ⚠️ 为什么不是「让 Stop 去 waitCh 里探一下」：Wait() 是公开方法，
	// 任何调用方都可能先把 waitCh 里的值取走。实测就是这么翻车的 ——
	// 测试里先 Wait() 再 Stop()，Stop 的非阻塞探针扑空，于是仍走
	// 杀组+drain 路径，白等 3 秒。**共享一个「已退出」的事实**才行，
	// 不能共享一个「只能被消费一次的通道」。
	exitOnce sync.Once
	exitCh   chan struct{}
	exitErr  error
	exitMu   sync.Mutex
	// 一旦 Stop 开始回收，Wait 的返回值不再代表「进程自己结束了」。
	stopped atomic.Bool
}

// Start 启动一个长驻进程并把它纳入沙箱监管。
//
// name/args 直接传给 exec.CommandContext（不经 shell）：MCP server 是
// 一个具体的可执行文件，经过 shell 会多一层无关进程，也让参数转义成为
// 一处注入面。需要 shell 的场景仍用 Run。
func (m *Manager) Start(ctx context.Context, agentID, workDir, name string, args []string) (*Supervisor, error) {
	if name == "" {
		return nil, fmt.Errorf("sandbox: program name is required")
	}
	lim, err := m.Prepare(ctx, agentID)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, name, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	lim.Attach(cmd)

	if err := cmd.Start(); err != nil {
		lim.Close()
		return nil, fmt.Errorf("sandbox: start %s: %w", name, err)
	}
	// Track 内部会立刻缓存 pgid —— 见 KillGroup 的注释，
	// 等到需要杀的时候再问内核可能已经问不到了。
	if err := lim.Track(cmd.Process.Pid); err != nil {
		// attach 失败不致命（已进 Warnings），但必须让调用方看得到。
		_ = err
	}

	s := &Supervisor{
		lim:    lim,
		cmd:    cmd,
		ctx:    ctx,
		waitCh: make(chan error, 1),
		exitCh: make(chan struct{}),
	}
	go func() { s.waitCh <- cmd.Wait() }()
	go s.waitLoop()
	return s, nil
}

// PID 返回被监管进程的 pid。
func (s *Supervisor) PID() int { return s.cmd.Process.Pid }

// Applied 返回实际生效的限制描述。
func (s *Supervisor) Applied() Applied { return s.lim.Applied() }

// waitLoop 把「进程是何时退出的、退出错误是什么」变成一份**可多次读取**的事实。
func (s *Supervisor) waitLoop() {
	err := <-s.waitCh
	s.exitOnce.Do(func() {
		s.exitMu.Lock()
		s.exitErr = err
		s.exitMu.Unlock()
		close(s.exitCh)
	})
}

// Wait 等待进程自行退出；Start 传入的 ctx 被取消时返回 ctx 的错误。
func (s *Supervisor) Wait() error {
	select {
	case <-s.exitCh:
		s.exitMu.Lock()
		defer s.exitMu.Unlock()
		return s.exitErr
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// Exited 报告进程是否已自行退出（非阻塞）。
//
// 这是给「server 崩溃后我要立刻知道、而不是等下一次请求超时」用的。
func (s *Supervisor) Exited() bool {
	select {
	case <-s.exitCh:
		return true
	default:
		return false
	}
}

// Stop 结束被监管进程**及整棵进程树**，并回收 cgroup。
//
// 顺序是刻意的：先按进程组 SIGKILL（覆盖未知孙进程），再等 Wait 收尾
// （管道被孙进程持有，不杀干净就永远等不到），最后回收 cgroup。
// 幂等：重复调用不会报错，方便在 defer 与显式清理里各写一次。
func (s *Supervisor) Stop() error {
	var errs []string
	if !s.stopped.Swap(true) {
		// ⚠️ 这一步不是优化，是修一个实测到的缺陷：无条件杀组 + drainWait
		// 会在「进程早就退了」时白等满 3 秒 —— 而崩溃恰恰是长驻 server
		// 最常见的收场，症状是每次崩溃调用方都多阻塞 3 秒。
		//
		// 判据用 exitCh（一份可多次读取的事实），不用 waitCh（只能被消费一次）。
		if !s.Exited() {
			// 进程可能已经自己退出了（崩溃场景），KillGroup 对 ESRCH 返回 nil
			if err := s.lim.KillGroup(); err != nil {
				errs = append(errs, err.Error())
			}
			// ⚠️ 等 exitCh，**不要**等 waitCh：waitCh 的唯一消费者是 waitLoop，
			// 这里再去读它只会在 waitLoop 已取走值之后白等满 3 秒。
			// 实测就是这么翻车的 —— 「共享一个只能被消费一次的通道」把
			// 「等待进程退出」变成了「赌谁先读」。
			select {
			case <-s.exitCh:
			case <-time.After(3 * time.Second):
				errs = append(errs, "process did not exit within 3s of SIGKILL")
			}
		}
	}
	if s.lim.cg != nil {
		if err := s.lim.cg.Remove(); err != nil {
			errs = append(errs, "cgroup cleanup failed: "+err.Error())
		}
		s.lim.cg = nil
		s.lim.cgPath = ""
	}
	if len(errs) > 0 {
		return fmt.Errorf("sandbox: stop supervisor: %s", strings.Join(errs, "; "))
	}
	return nil
}

// CleanupAfter 轮询等待进程退出，超时则杀整个进程组。
func (l *Limited) CleanupAfter(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if l.pid <= 0 {
			return
		}
		if !processAlive(l.pid) {
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
			fmt.Sprintf("cgroup cleanup failed (leftover dir %s): %v", l.cgPath, err))
	}
	l.cg = nil
	l.cgPath = ""
}

// MemoryPeak 返回本次沙箱观测到的内存峰值（字节）；未启用 cgroup 时为 0。
func (l *Limited) MemoryPeak() uint64 {
	if l.cg == nil {
		return 0
	}
	return l.cg.MemoryPeak()
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
	} else if res.TimedOut {
		// ⚠️ 这里修的是一个会**误导调用方**的返回值：
		// 超时被打断的命令，waitErr 可能是 nil —— `sh -c "cmd & sleep 300"`
		// 的 sh 在收到 SIGKILL 前已被 cmd.Wait 判定结束（管道由我们关闭），
		// 或者主命令早就跑完了、只是 sh 还在等后台任务。
		// 两种情况下 exitCode 都是 0，调用方会把「被中断」读成「成功完成」，
		// 进而把一个只做了一半的任务标记为已完成 —— 这比报错更糟。
		//
		// 用 124 表达「超时」是 coreutils timeout(1) 的约定，运维一眼能认出来。
		res.ExitCode = exitTimeout
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
