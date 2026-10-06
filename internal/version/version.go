// Package version 携带构建期注入的版本信息。
//
// 这三个变量由构建命令用 -ldflags -X 注入（见 Makefile 与
// .github/workflows/release.yml），源码里的字面量是「未注入」时的兜底。
//
// 为什么不放在 main 包里：管理后台的 /snapshot 与 /health 都要展示版本，
// 它们的取值必须在同一处，否则会出现「二进制说 v1.0.0、后台说 dev」
// 这种自相矛盾的状态 —— 而运维恰恰会拿这个字段判断「线上到底跑的是哪个版本」。
package version

import "runtime"

// 构建期注入；未注入时保持默认值。
var (
	// Version 是语义化版本号，如 v1.0.0。
	Version = "dev"
	// GitCommit 是构建时的 commit 短 SHA。
	GitCommit = "unknown"
	// BuildDate 是构建时间（RFC3339，UTC）。
	BuildDate = "unknown"
)

// Info 是版本信息的结构化表示。
type Info struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// Get 返回当前二进制的版本信息。
func Get() Info {
	return Info{
		Version:   Version,
		GitCommit: GitCommit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}
