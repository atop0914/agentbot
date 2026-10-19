package app

import (
	"context"
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

	// 选择持久化后端。这是一个**全局一次性**决策，必须先于任何仓库构造 ——
	// 一旦有仓库先按内存建好，后面的选择就只能是「部分仓库用 Postgres」，
	// 而这种混合状态无法从外部观察（见 docs/persistence.md「两套实现如何共存」）。
	sel, err := selectStorage(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	logger.Info("persistence backend selected",
		"storage.backend", sel.backend,
		"storage.host", storageHostForLog(cfg),
		"storage.fallback_allowed", cfg.Storage.AllowMemoryFallback)

	// 选择 Redis 后端。与持久化同理：这是**全局一次性**决策，
	// 必须先于 auth.Service 构造 —— 若先按内存建好服务，
	// 后续再想换就只能在运行时替换内部状态（本仓库没有那种路径）。
	rsel, err := selectRedis(context.Background(), cfg)
	if err != nil {
		if sel.db != nil {
			_ = sel.db.Close()
		}
		return nil, err
	}
	// 启动日志必须显式写出 Redis 后端：部署时「吊销状态存在哪」
	// 只能从这行日志回答，不能靠猜。回退到内存时降级为 Warn ——
	// 这是一个**安全性下降**的状态，不该与正常状态同样醒目。
	logRedisBackend(logger, cfg, rsel)

	a, err := build(cfg, logger, sel.db, rsel)
	if err != nil {
		// build 失败时装配未完成，DB 与 Redis 的所有权还在本函数手上 ——
		// 必须都关掉，否则每次启动失败都会漏一个连接池。
		if sel.db != nil {
			_ = sel.db.Close()
		}
		if rsel.client != nil {
			_ = rsel.client.Close()
		}
		return nil, err
	}

	// 启动日志写出指标端点的实际路径：配置里 metrics.path 可改，
	// 若静默使用，排障时会出现「按默认路径抓不到指标」的困惑。
	logMetricsPath(logger, a.MetricsPath())

	// 所有权移交：从这里起由 App 持有，调用方负责 Close。
	// 注意**不能**用 defer 关它 —— 那会在 NewWithConfig 返回的瞬间
	// 把刚交给 App 的句柄关掉，症状是「装配成功但一用就 database is closed」。
	a.Storage = sel.db
	a.StorageBackend = sel.backend
	a.DB = sel.db
	// Redis 客户端同样移交所有权（App.Close 会关它）。
	a.Redis = rsel.client
	a.RedisBackend = rsel.backend
	a.RateLimiter = rsel.limiter
	return a, nil
}

// logRedisBackend 写出 Redis 后端选择。
//
// 「配了 Redis 但回退到内存」必须是 Warn 级：这个状态本身能跑，
// 但多副本下登出与限流只在单副本内生效 —— 一个安全性下降的部署
// 不该与正常部署在日志里长得一样。
func logRedisBackend(logger *slog.Logger, cfg config.Config, sel redisSelection) {
	fields := []any{
		"redis.backend", sel.backend,
		"redis.host", redisHostForLog(cfg),
		"redis.fallback_allowed", cfg.Storage.AllowMemoryFallback,
	}
	if sel.backend == redisBackendRedis {
		logger.Info("redis backend selected", fields...)
		return
	}
	if cfg.Redis.Configured() {
		// 配了却用内存 = 回退。必须响亮。
		logger.Warn("redis configured but falling back to in-process state: "+
			"credential revocation and rate limiting will only apply to this replica", fields...)
		return
	}
	logger.Info("redis backend selected", fields...)
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
