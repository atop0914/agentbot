// Package sandbox 提供**进程级**的资源与文件系统边界。
//
// ⚠️ 前提（不要高估它的强度）：
// 这是 Docker 不可用时的等价替代方案，**不是安全边界**。
// 它防的是「一个 Agent 意外耗光宿主机资源 / 误写越界」，不防「恶意 Agent 主动逃逸」——
// 同 uid 的进程仍可发信号、读 /proc、访问同权限的宿主文件。
// 完整能力边界见 docs/sandbox.md。
package sandbox

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
)

// Limits 描述施加给一个被监管进程的资源上限。零值字段表示**不限制**。
type Limits struct {
	// CPUQuota 是 CPU 时间上限（秒），对应 RLIMIT_CPU。
	// 与 CPUWeight 不同：它约束的是「累计消耗」，用于打断死循环。
	CPUQuota int64
	// MemoryBytes 是地址空间上限（字节），对应 RLIMIT_AS。
	MemoryBytes int64
	// MaxProcesses 是该 uid 可创建的进程/线程总数，对应 RLIMIT_NPROC。
	MaxProcesses int64
	// MaxFileSize 是单文件大小上限（字节），对应 RLIMIT_FSIZE。
	MaxFileSize int64
	// MaxOpenFiles 是文件描述符上限，对应 RLIMIT_NOFILE。
	MaxOpenFiles int64
}

// DefaultLimits 是一组保守的开发默认值，刻意不激进：
// 定得太紧会让正常任务（go build、npm install）误伤，那样的结果是
// 使用者学会直接绕过沙箱。
func DefaultLimits() Limits {
	return Limits{
		CPUQuota:     60,                 // 60s 累计 CPU
		MemoryBytes:  1024 * 1024 * 1024, // 1 GiB
		MaxProcesses: 512,
		MaxFileSize:  256 * 1024 * 1024, // 256 MiB
		MaxOpenFiles: 1024,
	}
}

// Backend 标识实际生效的隔离手段。
//
// ⚠️ 这个字段存在的唯一目的是**让调用方无法误判**：
// 请求了 cgroup 但内核只给了 rlimit 时，调用方必须能从返回值看出来，
// 而不是以为「限制都生效了」。
type Backend string

const (
	// BackendCgroupV2 表示 cgroup v2 限额已生效（cpu.max / memory.max / pids.max）。
	BackendCgroupV2 Backend = "cgroup-v2"
	// BackendRlimit 表示仅 rlimit 生效（cgroup 不可写或不可用时的降级）。
	BackendRlimit Backend = "rlimit"
	// BackendNone 表示两种手段都没能用上 —— 调用方必须把这个状态当作失败处理。
	BackendNone Backend = "none"
)

// Applied 记录一次实际生效的限制，供调用方与日志消费。
//
// 关键设计：**Applied 描述的是事实，不是意图**。
// 因此每种限制都单独记一个布尔值 —— 「请求了 CPUQuota」与
// 「CPUQuota 真的生效了」是两个不同的事实。
type Applied struct {
	Backend Backend
	// CgroupPath 是本次使用的 cgroup 目录（仅 BackendCgroupV2 时非空）。
	CgroupPath string
	// Warnings 记录降级与部分失败的原因，逐条说清「哪一项没生效」。
	Warnings []string

	cpu    bool
	memory bool
	pids   bool
	rlimit bool
	fsize  bool
	nofile bool
}

// CPUEnforced 报告 CPU 限额是否真的生效。
func (a Applied) CPUEnforced() bool { return a.cpu }

// MemoryEnforced 报告内存限额是否真的生效。
func (a Applied) MemoryEnforced() bool { return a.memory }

// PidsEnforced 报告进程数限额是否真的生效。
func (a Applied) PidsEnforced() bool { return a.pids }

// RlimitEnforced 报告 rlimit 是否已附加到进程属性上。
func (a Applied) RlimitEnforced() bool { return a.rlimit }

// FileSizeEnforced 报告单文件大小限额是否生效。
func (a Applied) FileSizeEnforced() bool { return a.fsize }

// OpenFilesEnforced 报告文件描述符限额是否生效。
func (a Applied) OpenFilesEnforced() bool { return a.nofile }

// AnyEnforced 报告是否至少有一项限制真的生效。
// 返回 false 意味着这是一个**毫无边界的沙箱**，调用方应当拒绝执行。
func (a Applied) AnyEnforced() bool {
	return a.cpu || a.memory || a.pids || a.rlimit || a.fsize || a.nofile
}

// String 生成一行可读摘要，适合直接进启动日志。
func (a Applied) String() string {
	parts := []string{"backend=" + string(a.Backend)}
	for _, f := range []struct {
		name string
		on   bool
	}{
		{"cpu", a.cpu}, {"mem", a.memory}, {"pids", a.pids},
		{"rlimit", a.rlimit}, {"fsize", a.fsize}, {"nofile", a.nofile},
	} {
		if f.on {
			parts = append(parts, f.name+"=on")
		} else {
			parts = append(parts, f.name+"=off")
		}
	}
	if len(a.Warnings) > 0 {
		parts = append(parts, "warnings="+strconv.Itoa(len(a.Warnings)))
	}
	return strings.Join(parts, " ")
}

// rlimitsRequested 报告「是否有任何一项 rlimit 被请求」。
//
// 这里只用布尔判断，**不**返回资源号/值：rlimit 是**由子进程内的
// ulimit 引导脚本落地**的（见 rlimitUlimitScript 与 docs/sandbox.md），
// 父进程里根本不需要（也无法）把它们设到子进程上。
//
// ⚠️ 历史教训：这里曾返回 `[]rlimitPair{Resource, Cur, Max}`，看起来像
// 「这些值会被设到子进程上」，实际没有任何调用方消费它们 ——
// 一个「看起来设置了限制、实际只改了父进程」的静默失效。
// 删掉假的参数化路径，只保留真正有语义的判定。
func (l Limits) rlimitsRequested() bool {
	return l.CPUQuota > 0 || l.MaxFileSize > 0 || l.MaxOpenFiles > 0 || l.MemoryBytes > 0
}

// SysProcAttr 返回进程属性。
//
// ⚠️ Setpgid 让子进程成为独立进程组长：超时清理时按 -pgid 杀整个进程树。
// Agent 起的 sh 会再起孙进程（go build、npm install），只杀 sh 会留下孤儿
// 继续吃 CPU —— 这条在"进程级沙箱"里比在容器里更要紧，因为容器能整体销毁。
func (l Limits) SysProcAttr() *syscall.SysProcAttr {
	// 平台差异收敛在 newSysProcAttr：linux 上带 Setpgid（自成进程组，
	// 便于按组回收），macOS/Windows 上该字段不存在。
	return newSysProcAttr()
}

// Manager 负责为一个被监管进程准备限制并在结束后回收。
type Manager struct {
	cfg Config
	seq atomic.Uint64
}

// Config 是 Manager 的配置。
type Config struct {
	// CgroupRoot 是 cgroup v2 的挂载点，通常为 /sys/fs/cgroup。
	CgroupRoot string
	// CgroupBase 是本次运行下所有沙箱 cgroup 的父目录名，用于避免与宿主上
	// 其他程序互相干扰，也便于整批清理。
	CgroupBase string
	// Limits 是默认资源上限。
	Limits Limits
	// DisableCgroup 强制走 rlimit 降级路径（用于测试降级行为）。
	DisableCgroup bool
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		CgroupRoot: "/sys/fs/cgroup",
		CgroupBase: "agentbot-sandbox",
		Limits:     DefaultLimits(),
	}
}

// New 创建 Manager。
//
// ⚠️ 这里**不**探测 cgroup 可用性 —— 探测必须发生在真正的 Apply 时刻，
// 因为「此刻可写」不代表「整个进程生命周期内可写」（cgroup 可能被
// 宿主上的其他程序重新挂载）。把能力探测与使用分开，会产出
// 「启动时说支持、运行时报错」这种最难查的一类问题。
func New(cfg Config) *Manager {
	if cfg.CgroupRoot == "" {
		cfg.CgroupRoot = "/sys/fs/cgroup"
	}
	if cfg.CgroupBase == "" {
		cfg.CgroupBase = "agentbot-sandbox"
	}
	return &Manager{cfg: cfg}
}

// Prepare 为一个即将启动的进程准备限制。
//
// 返回值 Applied 必须被调用方消费：它既用于 exec.Cmd 的属性设置，
// 也用于在结束后回收 cgroup。**不要忽略它。**
func (m *Manager) Prepare(ctx context.Context, agentID string) (*Limited, error) {
	if agentID == "" {
		return nil, fmt.Errorf("sandbox: agentID is required")
	}

	applied := Applied{}
	id := m.seq.Add(1)
	cgroupName := fmt.Sprintf("%s-%d", sanitize(agentID), id)

	var cg cgroupHandle
	if !m.cfg.DisableCgroup {
		var err error
		cg, err = newPlatformCgroup(m.cfg.CgroupRoot, m.cfg.CgroupBase, cgroupName, m.cfg.Limits)
		if err != nil {
			// 降级：cgroup 不可用不是致命错误，但**必须**记进 Warnings，
			// 否则调用方会以为 CPU/内存限额生效了。
			applied.Warnings = append(applied.Warnings,
				"cgroup v2 unavailable, degraded to rlimit-only: "+err.Error())
			cg = nil
		}
	} else {
		applied.Warnings = append(applied.Warnings, "cgroup explicitly disabled by config")
	}

	if cg != nil {
		applied.Backend = BackendCgroupV2
		applied.CgroupPath = cg.Path()
		applied.cpu = cg.CPUEnforced()
		applied.memory = cg.MemoryEnforced()
		applied.pids = cg.PidsEnforced()
		if w := cg.PartialWarning(); w != "" {
			applied.Warnings = append(applied.Warnings, w)
		}
	} else {
		applied.Backend = BackendRlimit
	}

	// rlimit 通过 SysProcAttr 附加到子进程，只要有限制项被请求就算生效。
	if m.cfg.Limits.rlimitsRequested() {
		applied.rlimit = true
		applied.fsize = m.cfg.Limits.MaxFileSize > 0
		applied.nofile = m.cfg.Limits.MaxOpenFiles > 0
	} else if cg == nil {
		applied.Backend = BackendNone
	}

	if !applied.AnyEnforced() {
		if cg != nil {
			_ = cg.Remove()
		}
		return nil, fmt.Errorf("sandbox: no resource limit could be applied for agent %q", agentID)
	}

	return &Limited{
		manager: m,
		cg:      cg,
		cgPath:  cgroupPathOf(cg),
		applied: applied,
		limits:  m.cfg.Limits,
		agentID: agentID,
	}, nil
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 48 {
		out = out[:48]
	}
	if out == "" {
		out = "agent"
	}
	return out
}
