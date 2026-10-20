//go:build windows

package sandbox

import (
	"errors"
	"os"
	"syscall"
)

// Windows 平台的沙箱：**只有墙钟超时**。
//
// 能力边界（必须显式说明，否则上层会假设它们生效了）：
//   - 无 cgroup（linux 内核特性）；
//   - 无 `ulimit`（Windows 的 shell 里没有这个内建命令，
//     rlimit 引导脚本在 Windows 上会失败 —— 这是**已知且可接受**的：
//     Release 的 Windows 产物是给「本地试用」用的，不是生产执行环境）；
//   - 无进程组语义（对应的是 Job Object，本文件未实现），
//     因此杀进程只能按 pid，孙进程可能存活。
//
// 这三条一律通过 Warnings 暴露给调用方，绝不假装已生效。

// newPlatformCgroup 在 Windows 上恒返回「不支持」。
func newPlatformCgroup(root, base, name string, l Limits) (cgroupHandle, error) {
	return nil, errors.New("cgroup v2 is linux-only")
}

// applyCgroupFD 在 Windows 上什么都不做（SysProcAttr 没有该字段）。
func (l *Limited) applyCgroupFD(attr *syscall.SysProcAttr) *syscall.SysProcAttr {
	return attr
}

// attachCgroup 在 Windows 上恒失败 —— 没有 cgroup 可挂。
func (l *Limited) attachCgroup(pid int) error {
	return errors.New("cgroup v2 is linux-only")
}

// newSysProcAttr 在 Windows 上返回空的 SysProcAttr（无 Setpgid 字段）。
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

// withOwnProcessGroup 在 Windows 上无进程组可设，原样返回。
func withOwnProcessGroup(attr *syscall.SysProcAttr) *syscall.SysProcAttr {
	return attr
}

// groupOf 在 Windows 上无法取得进程组。
func groupOf(pid int) (int, error) {
	return 0, errors.New("process groups are not available on windows")
}

// killGroup 在 Windows 上只能按 pid 终止单进程（无进程组）。
func killGroup(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// processAlive 在 Windows 上用 FindProcess 探测（Windows 没有 signal 0）。
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Windows 的 FindProcess 总是成功，只能靠 Kill(0) 之外的探测；
	// 这里退化为「能找到就认为还活着」—— 由上层 wall-clock 超时兜底。
	_ = p
	return true
}

// isNoSuchProcess 判断「进程已不存在」。
func isNoSuchProcess(err error) bool {
	return errors.Is(err, os.ErrProcessDone)
}
