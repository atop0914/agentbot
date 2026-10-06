package app

import (
	"errors"
	"log/slog"
	"os"

	"github.com/atop0914/agentbot/internal/config"
)

// 本文件把 pkg/../internal/config 的配置对象翻译成装配层能用的形态。
//
// 装配层的原则：**只接收显式传入的配置**，自己不读环境变量、自己不带
// 默认值。这样 app.New 的每一个安全相关取值都能在调用点被看到，
// 而不是藏在某个 os.Getenv 里。
//
// 两个入口的定位：
//   - app.New()        开发/测试：内部调用 config.DevelopmentDefault()，
//                      带一个明确标注「仅本地」的假机密。
//   - app.NewWithConfig(cfg)  生产：由 cmd 读配置（config.Load 已 fail-fast）
//                      后传入，装配层不再做任何兜底。
//
// ⚠️ app.New() 绝不能被生产入口调用 —— 它签发的 token 用的是一个
// 写在源码里的密钥。为避免误用，它在启动时打一条 Warn 日志。

// New 构建一个使用**开发默认配置**的 App。
//
// 保留它是因为测试与本地 `go run ./cmd/agentbot` 需要零外部依赖即可跑起来；
// 生产路径必须走 NewWithConfig。
func New() *App {
	a, err := NewWithConfig(config.DevelopmentDefault())
	if err != nil {
		// 开发默认配置是编译期常量，不可能校验失败；走到这里说明
		// DevelopmentDefault 与 Validate 的约束脱节了，属于程序 bug。
		panic("app: development default config failed validation: " + err.Error())
	}
	a.Logger.Warn("app is running on the DEVELOPMENT config — never use app.New() in production",
		"reason", "development_default_secret")
	return a
}

// NewWithConfig 用给定配置装配 App。
//
// 返回 error 而不是 panic：配置来自进程外部（环境变量 / 挂载文件），
// 出错属于正常的部署失败路径，应该由 cmd 决定如何报告与退出。
//
// 校验在这里**再跑一次**，与 config.Load 的校验不是冗余：
//   - Load 拦「没给」；
//   - 这里拦「调用方自己构造了一个非法 cfg」（测试、嵌入式调用、后续
//     可能的配置热加载都会绕过 Load）。
//
// 校验放在装配层而不是各业务模块，是因为「一个不安全的取值」不该被
// 某个恰好先拿到它的模块悄悄放行。
func NewWithConfig(cfg config.Config) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	logger := newLogger(cfg)

	// 早于任何业务组件检查沙箱根目录：文件系统模块会在这里落盘，
	// 目录不可用应该立刻失败，而不是等到第一个 Agent 上传文件时才报错。
	if err := os.MkdirAll(cfg.FileSystem.Root, 0o750); err != nil {
		return nil, errors.Join(errFileSystemRootUnavailable, err)
	}

	return build(cfg, logger)
}

// newLogger 按配置构造 slog 日志器。
//
// 输出目标在配置层是显式的（stdout / file），但这里只支持 stdout：
// 「日志写文件」在容器化部署里是反模式（日志应该由运行时收集），
// 所以配置层的 Output=file 会退化成 stdout 并告警，而不是真的开文件 ——
// 静默忽略会让一个自以为「日志在文件里」的部署在排障时找不到日志。
func newLogger(cfg config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.Log.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if cfg.Log.Format == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	logger := slog.New(handler)

	if cfg.Log.Output == "file" {
		logger.Warn("log.output=file is not supported, falling back to stdout",
			"requested_file", cfg.Log.File)
	}
	return logger
}

// 装配层哨兵错误。与 errors.go 中的其它哨兵保持一致的风格：
// 不带内部细节，最终去向是启动失败日志。
var (
	// errFileSystemRootUnavailable 表示 Agent 文件系统沙箱根目录不可用。
	errFileSystemRootUnavailable = errors.New("app: filesystem sandbox root is unavailable")
)
