package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// cgroupV2 是一个已创建并写好限额的 cgroup v2 目录。
//
// ⚠️ 只支持 cgroup v2（unified hierarchy）。本机 /sys/fs/cgroup 的
// statfs 类型是 cgroup2fs，且 cgroup.controllers 里有 cpu/memory/pids。
// 若宿主是 cgroup v1，newCgroupV2 会返回错误 —— 调用方降级到 rlimit，
// 而不是「假装写成功」。
type cgroupV2 struct {
	path string

	cpuSet  bool
	memSet  bool
	pidsSet bool

	// partial 记录「部分控制器没写成功」的原因。
	// 不能因为 cpu.max 写失败就丢掉整个 cgroup：memory.max 可能已经生效了，
	// 那是实打实的保护。但也不能不报 —— 调用方必须知道缺了哪一项。
	partial string
}

// Path 返回 cgroup 目录路径。
func (c *cgroupV2) Path() string { return c.path }

// CPUEnforced / MemoryEnforced / PidsEnforced 报告**实际写入成功**的控制器。
// 刻意不暴露原始字段：调用方要的是「生效了吗」，而不是「结构体里存了什么」。
func (c *cgroupV2) CPUEnforced() bool    { return c.cpuSet }
func (c *cgroupV2) MemoryEnforced() bool { return c.memSet }
func (c *cgroupV2) PidsEnforced() bool   { return c.pidsSet }

// PartialWarning 返回部分控制器写入失败的说明。
func (c *cgroupV2) PartialWarning() string { return c.partial }

// newCgroupV2 在 root/base 下创建一个名为 name 的 cgroup 并写入限额。
func newCgroupV2(root, base, name string, l Limits) (*cgroupV2, error) {
	// 1. 必须是 cgroup2fs —— 否则下面所有写入都会以
	//    "no such file or directory" 形式失败，误导排查方向。
	if err := requireCgroupV2(root); err != nil {
		return nil, err
	}

	parent := filepath.Join(root, base)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fmt.Errorf("create cgroup parent %s: %w", parent, err)
	}

	// 2. 父 cgroup 需要把控制器下放给子目录。
	//    没有这一步，子目录里的 cpu.max / memory.max / pids.max 根本不存在，
	//    表现为写入 "no such file" —— 这是 cgroup v2 最容易卡住的一步。
	if err := enableControllers(parent, "cpu", "memory", "pids"); err != nil {
		return nil, err
	}

	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0o755); err != nil {
		return nil, fmt.Errorf("create cgroup %s: %w", path, err)
	}

	cg := &cgroupV2{path: path}
	var missing []string

	// 3. 写限额。每一项单独判定成功与否 —— 部分成功要如实上报。
	if l.CPUQuota > 0 {
		// cpu.max 格式 "<quota> <period>"（微秒）。这里把 CPUQuota 秒数
		// 当作**每 100ms 周期内的配额**是常见误区：
		//   CPUQuota=1 且 period=100000 → 1us/100ms，等于几乎不给 CPU。
		// 正确做法是把「多少秒 CPU 时间」换算成 period=1s 内的配额：
		//   quota = CPUQuota × 1_000_000 us，即每秒允许消耗 CPUQuota 秒。
		quota := l.CPUQuota * 1_000_000
		if err := writeFile(filepath.Join(path, "cpu.max"),
			fmt.Sprintf("%d %d", quota, 1_000_000)); err != nil {
			missing = append(missing, "cpu.max: "+err.Error())
		} else {
			cg.cpuSet = true
		}
	}
	if l.MemoryBytes > 0 {
		if err := writeFile(filepath.Join(path, "memory.max"),
			strconv.FormatInt(l.MemoryBytes, 10)); err != nil {
			missing = append(missing, "memory.max: "+err.Error())
		} else {
			cg.memSet = true
		}
		// memory.swap.max=0 防止「内存限额被 swap 架空」：
		// 只限 memory.max 时，进程超限后仍可通过 swap 继续分配，
		// 表现为限额"看起来生效了"但压力依旧落在宿主上。
		if err := writeFile(filepath.Join(path, "memory.swap.max"), "0"); err != nil {
			missing = append(missing, "memory.swap.max: "+err.Error())
		}
	}
	if l.MaxProcesses > 0 {
		if err := writeFile(filepath.Join(path, "pids.max"),
			strconv.FormatInt(l.MaxProcesses, 10)); err != nil {
			missing = append(missing, "pids.max: "+err.Error())
		} else {
			cg.pidsSet = true
		}
	}

	if len(missing) > 0 {
		cg.partial = "cgroup partial: " + strings.Join(missing, "; ")
		if !cg.cpuSet && !cg.memSet && !cg.pidsSet {
			// 一项都没成功 → 这个 cgroup 没有意义，删掉并交给上层降级
			_ = cg.Remove()
			return nil, fmt.Errorf("cgroup %s: no controller could be configured: %s",
				path, strings.Join(missing, "; "))
		}
	}

	return cg, nil
}

// Attach 把指定 pid 移入本 cgroup。
//
// ⚠️ 必须在进程**启动后**才能调用（需要 pid）。这意味着存在一个
// 「已 fork、未 attach」的时间窗：进程在这段时间内不受 cgroup 约束。
// 对 CPU/内存限额而言这个窗口可接受（毫秒级），但必须在文档里写明，
// 不能声称「从第一纳秒起就被限制」。
func (c *cgroupV2) Attach(pid int) error {
	if c == nil || c.path == "" {
		return fmt.Errorf("sandbox: nil cgroup")
	}
	return writeFile(filepath.Join(c.path, "cgroup.procs"), strconv.Itoa(pid))
}

// PeekMemory 读取峰值内存用量（字节），用于验证 memory.max 真的起作用。
// 返回 0 表示该控制器未启用或读取失败。
func (c *cgroupV2) MemoryPeak() uint64 {
	if c == nil {
		return 0
	}
	b, err := os.ReadFile(filepath.Join(c.path, "memory.peak"))
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// OOMKilled 报告本 cgroup 是否发生过 OOM kill。
// 这是「内存限额真的生效」的**副作用证据** —— 比读回配置值有意义得多。
func (c *cgroupV2) OOMKilled() bool {
	if c == nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(c.path, "memory.events"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "oom_kill" {
			n, err := strconv.Atoi(fields[1])
			return err == nil && n > 0
		}
	}
	return false
}

// PidsPeak 读取进程数峰值，用于验证 pids.max 生效。
func (c *cgroupV2) PidsPeak() uint64 {
	if c == nil {
		return 0
	}
	b, err := os.ReadFile(filepath.Join(c.path, "pids.events"))
	if err != nil {
		// 回退到 pids.peak（部分内核版本才有）
		b2, err2 := os.ReadFile(filepath.Join(c.path, "pids.peak"))
		if err2 != nil {
			return 0
		}
		v, _ := strconv.ParseUint(strings.TrimSpace(string(b2)), 10, 64)
		return v
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "max" {
			n, _ := strconv.ParseUint(fields[1], 10, 64)
			return n
		}
	}
	return 0
}

// PidsRejected 报告是否有进程因 pids.max 被拒绝创建。
// 这是 pids 限额生效的副作用证据。
func (c *cgroupV2) PidsRejected() bool {
	if c == nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(c.path, "pids.events"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "max" {
			n, err := strconv.Atoi(fields[1])
			return err == nil && n > 0
		}
	}
	return false
}

// Remove 删除 cgroup 目录。进程已全部退出后才能删成功，
// 否则内核返回 EBUSY —— 重试几次，最终失败也不阻塞调用方（只记 warning）。
func (c *cgroupV2) Remove() error {
	if c == nil || c.path == "" {
		return nil
	}
	var lastErr error
	for i := 0; i < 20; i++ {
		err := syscall.Rmdir(c.path)
		if err == nil {
			return nil
		}
		lastErr = err
		if err != syscall.EBUSY {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return lastErr
}

// requireCgroupV2 断言 root 是 cgroup2fs。
func requireCgroupV2(root string) error {
	if _, err := os.Stat(filepath.Join(root, "cgroup.controllers")); err != nil {
		return fmt.Errorf("%s is not a cgroup v2 hierarchy: %w", root, err)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return fmt.Errorf("statfs %s: %w", root, err)
	}
	if st.Type != cgroup2Magic {
		return fmt.Errorf("%s is not cgroup2fs (f_type=%#x)", root, st.Type)
	}
	return nil
}

// enableControllers 向父 cgroup 的 cgroup.subtree_control 写入 "+cpu +memory +pids"。
//
// 幂等：重复写同名控制器在 cgroup v2 上是允许的。
// 注意**不能**把控制器写在已经有进程的 cgroup 上（no internal process 约束），
// 所以限额必须写在新建的**子**目录里，而不是父目录。
func enableControllers(parent string, controllers ...string) error {
	available, err := os.ReadFile(filepath.Join(parent, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("read cgroup.controllers of %s: %w", parent, err)
	}
	have := map[string]bool{}
	for _, c := range strings.Fields(string(available)) {
		have[c] = true
	}

	var wanted []string
	for _, c := range controllers {
		if have[c] {
			wanted = append(wanted, c)
		}
	}
	if len(wanted) == 0 {
		return fmt.Errorf("no requested controller available under %s (have: %s)",
			parent, strings.TrimSpace(string(available)))
	}

	// 逐个子树控制：一次性写多个时，只要其中一个不可用整条写入都会失败，
	// 那样会丢掉其余可用的控制器。逐个写，失败的那个单独记。
	var failed []string
	for _, c := range wanted {
		if err := writeFile(filepath.Join(parent, "cgroup.subtree_control"), "+"+c); err != nil {
			failed = append(failed, c)
		}
	}
	if len(failed) == len(wanted) {
		return fmt.Errorf("could not enable any controller under %s: %v", parent, failed)
	}
	return nil
}

// writeFile 写 cgroup 伪文件。
//
// ⚠️ 不能用 os.WriteFile 的 O_TRUNC：cgroup 伪文件对 O_TRUNC 返回 EINVAL。
func writeFile(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(value); err != nil {
		return err
	}
	return nil
}
