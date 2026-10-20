package sandbox

// cgroupHandle 是 cgroup 的平台无关句柄。
//
// 为什么需要这个接口：`cgroupV2` 只存在于 linux（cgroup 是内核特性，
// 而且要 statfs 判定 cgroup2fs），而 run.go / types.go 需要在所有平台上
// 编译通过 —— Release 会交叉编译 darwin 与 windows。
//
// 把字段收敛在接口里而不是 `cg *cgroupV2`：非 linux 平台上把这个类型
// 定义成一个永远返回「不支持」的桩，避免在共享代码里散落 build tag。
type cgroupHandle interface {
	// Path 返回 cgroup 目录路径（用于日志与诊断）。
	Path() string
	// CPUEnforced / MemoryEnforced / PidsEnforced 报告**实际写入成功**的限额，
	// 而不是「请求了哪些」—— 这个区别决定了 Applied 是否在撒谎。
	CPUEnforced() bool
	MemoryEnforced() bool
	PidsEnforced() bool
	// PartialWarning 返回「部分控制器写入失败」的说明（空串表示全部成功）。
	PartialWarning() string
	// MemoryPeak 返回内存峰值（字节）—— 内存限额生效的副作用证据。
	MemoryPeak() uint64
	// OOMKilled 报告是否发生过 OOM kill。
	OOMKilled() bool
	// PidsRejected 报告是否有进程因 pids.max 被拒绝。
	PidsRejected() bool
	// Remove 回收 cgroup 目录。
	Remove() error
}
