//go:build !linux && !windows

package sandbox

import (
	"errors"
	"syscall"
)

// 非 linux / 非 windows 平台（darwin、*BSD）：**只有墙钟超时与进程组回收**。
//
// 这不是「功能对等」，而是明确的能力边界：
//   - 没有 cgroup v2（linux 内核特性）；
//   - 没有 `ulimit -v`（macOS 的 shell 语义与 Linux 不同，限制也不等价）。
//
// 因此这里只保证「命令会被超时杀掉、子进程树不会留下孤儿」，
// 其余限制一律记进 Warnings，让调用方**看到**它没生效 ——
// 静默降级比没有限制更危险（一个永远报「已限制」的实现会让所有上层假设
// 都建立在假前提上）。

// newPlatformCgroup 在非 linux 平台上恒返回「不支持」。
func newPlatformCgroup(root, base, name string, l Limits) (cgroupHandle, error) {
	return nil, errors.New("cgroup v2 is linux-only")
}

// applyCgroupFD 在非 linux 平台上什么都不做（SysProcAttr 没有该字段）。
func (l *Limited) applyCgroupFD(attr *syscall.SysProcAttr) *syscall.SysProcAttr {
	return attr
}

// attachCgroup 在非 linux 平台上恒失败 —— 没有 cgroup 可挂。
func (l *Limited) attachCgroup(pid int) error {
	return errors.New("cgroup v2 is linux-only")
}

// newSysProcAttr 返回空的 SysProcAttr —— darwin 的 SysProcAttr
// 没有 Setpgid 字段（进程组通过 Setpgid 的等价物不存在）。
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

// withOwnProcessGroup 在 darwin 上无进程组可设，原样返回。
func withOwnProcessGroup(attr *syscall.SysProcAttr) *syscall.SysProcAttr {
	return attr
}

// groupOf 在 darwin 上无法取得进程组。
func groupOf(pid int) (int, error) {
	return 0, errors.New("process groups are not available on this platform")
}

// killGroup 在 darwin 上退化为按 pid 杀单进程。
//
// 明确的代价：孙进程可能存活（会被 launchd 收养）。
// 这不是等价实现，已写进 docs/sandbox.md 的能力边界。
func killGroup(pgid int) error {
	return syscall.Kill(pgid, syscall.SIGKILL)
}

// processAlive 探测 pid 是否仍存在。
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// isNoSuchProcess 判断「查不到这个进程」这类应被视为成功的错误。
func isNoSuchProcess(err error) bool {
	return err == syscall.ESRCH
}
