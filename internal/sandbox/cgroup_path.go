package sandbox

// cgroupPathOf 返回 cgroup 句柄的路径（nil 安全）。
//
// 「路径」与「句柄」分开存：cgroup 目录被 Remove 之后句柄还在，
// 但路径要能继续用于日志与警告（例如「leftover dir /sys/fs/cgroup/...」）。
func cgroupPathOf(cg cgroupHandle) string {
	if cg == nil {
		return ""
	}
	return cg.Path()
}
