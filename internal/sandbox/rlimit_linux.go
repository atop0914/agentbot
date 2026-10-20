//go:build linux

package sandbox

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
