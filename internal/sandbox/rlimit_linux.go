//go:build linux

package sandbox

import "syscall"

// rlimitPair 表示一项要设置到子进程上的资源限制。
type rlimitPair struct {
	Resource int
	Cur      uint64
	Max      uint64
}

// 显式声明资源号，避免不同 Go 版本对 syscall 常量的可见性差异。
// 数值取自内核 uapi/asm-generic/resource.h。
const (
	rlimitCPU    = 0 // RLIMIT_CPU    — CPU 累计秒数
	rlimitFSize  = 1 // RLIMIT_FSIZE  — 单文件字节数
	rlimitNOFile = 7 // RLIMIT_NOFILE — 文件描述符数
	rlimitAS     = 9 // RLIMIT_AS     — 地址空间字节数
)

// applyRlimits 在当前进程上设置限制。
//
// ⚠️ 这个函数**必须**在被监管进程自己的上下文里调用（fork 之后、exec 之前），
// 典型实现是把它作为一个引导子进程的第一段逻辑。
// 在父进程里直接调它只会限制父进程 —— 一个常见的静默失效写法：
// 代码看起来"设置了限制"，实际被监管的进程毫无边界。
func applyRlimits(pairs []rlimitPair) error {
	for _, p := range pairs {
		rl := syscall.Rlimit{Cur: p.Cur, Max: p.Max}
		if err := syscall.Setrlimit(p.Resource, &rl); err != nil {
			return err
		}
	}
	return nil
}
