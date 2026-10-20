//go:build linux

package sandbox

import (
	"errors"
	"os"
	"syscall"
)

// 本文件存放**只存在于 linux 的 SysProcAttr / cgroup** 相关实现。
//
// 为什么必须分平台：`syscall.SysProcAttr` 是各平台独立的类型，
// `CgroupFD` / `UseCgroupFD` 只有 linux 才有；cgroup v2 也只在 linux 存在。
// 把这段代码留在 run.go 里会让 `GOOS=darwin` / `GOOS=windows` 的 Release
// 交叉编译直接失败 —— 而 CI 的 test 只跑 linux，于是一个「本地全绿、
// 打 tag 才炸」的缺陷可以一路活到发版。
//
// 平台差异只有这四处：进程组、cgroup FD、进程组查杀、statfs 类型判定。
// 其余（进程树回收策略、墙钟超时、ulimit 引导脚本解释）在所有平台语义相同，
// 因此留在 run.go 共享。

// cgroup2Magic 是 cgroup2fs 的 statfs f_type 值（内核源码中的 CGROUP2_SUPER_MAGIC）。
const cgroup2Magic = 0x63677270

// newPlatformCgroup 在 linux 上尝试创建一个 cgroup v2 目录并写入限额。
// 返回的错误由调用方降级处理（记进 Warnings，退回 rlimit-only）。
func newPlatformCgroup(root, base, name string, l Limits) (cgroupHandle, error) {
	return newCgroupV2(root, base, name, l)
}

// applyCgroupFD 让内核在 clone 时就把子进程放入 cgroup。
//
// 用 CgroupFD 而不是 Start 之后再写 cgroup.procs：后者存在「已 fork、
// 未 attach」的不受约束时间窗。
func (l *Limited) applyCgroupFD(attr *syscall.SysProcAttr) *syscall.SysProcAttr {
	if fd := l.cgroupFD(); fd >= 0 {
		attr.UseCgroupFD = true
		attr.CgroupFD = fd
	}
	return attr
}

// cgroupFD 打开 cgroup 目录并返回 fd，供 SysProcAttr.CgroupFD 使用。
func (l *Limited) cgroupFD() int {
	if l.cg == nil || l.cg.Path() == "" {
		return -1
	}
	f, err := os.Open(l.cg.Path())
	if err != nil {
		return -1
	}
	return int(f.Fd())
}

// attachCgroup 把 pid 移入 cgroup（CgroupFD 不被内核接受时的兜底路径）。
func (l *Limited) attachCgroup(pid int) error {
	v2, ok := l.cg.(*cgroupV2)
	if !ok {
		return errors.New("cgroup handle is not a v2 cgroup")
	}
	return v2.Attach(pid)
}

// newSysProcAttr 返回 linux 上带「自成进程组」属性的 SysProcAttr。
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// withOwnProcessGroup 保留调用方属性，只补 Setpgid。
func withOwnProcessGroup(attr *syscall.SysProcAttr) *syscall.SysProcAttr {
	attr.Setpgid = true
	return attr
}

// groupOf 返回 pid 所在的进程组。
func groupOf(pid int) (int, error) {
	return syscall.Getpgid(pid)
}

// killGroup 按进程组发送 SIGKILL。
func killGroup(pgid int) error {
	return syscall.Kill(-pgid, syscall.SIGKILL)
}

// processAlive 探测 pid 是否仍存在（signal 0）。
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// isNoSuchProcess 判断「查不到这个进程/组」这类应被视为成功的错误。
func isNoSuchProcess(err error) bool {
	return err == syscall.ESRCH
}
