package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/atop0914/agentbot/internal/config"
	"github.com/atop0914/agentbot/internal/storage"
)

// 本文件是「持久化后端选择」的装配点，对应 docs/persistence.md 的「两套实现如何共存」。
//
// 决策表：
//
//	DSN 不可用（未配置数据库）           → 内存后端          storage.backend=memory
//	DSN 可用 + 连接成功                  → Postgres 后端     storage.backend=postgres
//	DSN 可用 + 连接失败 + 未开回退（默认）→ **拒绝启动**
//	DSN 可用 + 连接失败 + 开了回退        → 内存后端 + ERROR 日志
//
// 为什么要在这里、而不是在各业务模块里判断：这是**全局一次性**的决策。
// 若让每个仓库自己判断，会出现「audit 连上了、user 没连上」的混合状态，
// 而「数据的一部分持久、一部分不持久」在排障时几乎无法从外部观察。
//
// 本文件同时负责把结果写进日志。启动日志必须显式写出后端，
// 否则部署时根本无法回答「这个进程的数据到底存在哪」。

// storageSelection 是选择结果：要么是一个可用的 DB 句柄，
// 要么是 nil（内存后端），外加一个用于日志/诊断的后端标识。
type storageSelection struct {
	db      *storage.DB
	backend string
}

// selectStorage 按上述决策表挑选持久化后端。
//
// 返回的 error 表示**应当拒绝启动**。注意 DSN 为空不是错误 ——
// 那是「我用内存后端」的合法表达。
func selectStorage(ctx context.Context, cfg config.Config) (storageSelection, error) {
	dsn := databaseDSN(cfg)

	// 未配置数据库：内存后端。这是开发/演示/无 PG 的 CI 的正常形态。
	if dsn == "" {
		return storageSelection{backend: storage.BackendMemory}, nil
	}

	db, err := storage.Open(ctx, storage.Config{
		DSN: dsn,
		// 连接池参数保持 storage 包的默认值（25/5/30m/5m），
		// 由底座统一决定，避免每个部署形态各自调一套。
	})
	if err == nil {
		// 迁移失败必须拒绝启动：一个连着旧 schema 的进程处理新数据，
		// 症状会是运行时零散的 SQL 报错，而不是一次清晰的启动失败。
		if merr := db.Migrate(ctx); merr != nil {
			_ = db.Close()
			return storageSelection{backend: storage.BackendMemory}, fmt.Errorf(
				"app: database schema migration failed: %w", merr)
		}
		return storageSelection{db: db, backend: storage.BackendPostgres}, nil
	}

	// 到这里说明「配了数据库但用不了」。
	if !errors.Is(err, storage.ErrUnreachable) {
		// 非 unreachable 的错误（例如 DSN 为空却走到这里）说明上面的
		// 分支判断与 storage 包的约定脱节了，属于程序 bug，不该被回退掩盖。
		return storageSelection{backend: storage.BackendMemory},
			fmt.Errorf("app: unexpected storage error: %w", err)
	}

	if !cfg.Storage.AllowMemoryFallback {
		// 默认路径：**拒绝启动**。
		//
		// 静默降级到内存是最坏的一种失败 —— 调用方看到的是 201 Created，
		// 没有任何信号告诉它数据只活在进程里。启动失败是响亮的，
		// 数据丢失是无声的。
		return storageSelection{backend: storage.BackendMemory}, fmt.Errorf(
			"app: database is configured but unreachable; refusing to start "+
				"(set %sSTORAGE_ALLOW_MEMORY_FALLBACK=true only if losing data on restart is acceptable): %w",
			config.EnvPrefix, err)
	}

	// 显式开启的回退。日志在调用方以 ERROR 级别打印（见 storageBackendFields）。
	return storageSelection{backend: storage.BackendMemory}, nil
}

// databaseDSN 从配置拼出 PostgreSQL 连接串。
// 返回空串表示「这个部署没有配置数据库」。判定条件刻意保守：
// 只要 host 或 db_name 缺失，就认为没配 —— 缺 host 的连接串必然连不上，
// 而把它当成「配了但连不上」会让一个**没打算用数据库**的部署
// （例如只改了 db_name 的测试环境）在启动时直接失败。
func databaseDSN(cfg config.Config) string {
	db := cfg.Database
	if !db.Configured() {
		return ""
	}
	// 缺 db_name 归入「没配」还是「配了但错」？这里判为不导出 DSN，
	// 与「host 为空 = 内存后端」保持同一个口径；而 db_name 缺失
	// 已由 config.Validate 在更早的位置拒绝启动，正常路径到不了这里。
	if strings.TrimSpace(db.DBName) == "" {
		return ""
	}
	// 密码留空仍算「配置了数据库」：某些部署用证书 / 信任认证，
	// 此时空密码是合法的。是否可达交给真实连接来回答，
	// 而不是在这里猜一个「空密码必然失败」的结论。
	return db.DSN()
}

// storageHostForLog 返回可用于日志的数据库位置（host:port/dbname）。
//
// **绝不返回 DSN 本身**：DSN 里带密码，而启动日志是最容易被整段贴进
// issue 或聊天窗口的东西。只给位置信息，足够回答「连的是哪个库」。
func storageHostForLog(cfg config.Config) string {
	db := cfg.Database
	if strings.TrimSpace(db.Host) == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d/%s", db.Host, db.Port, db.DBName)
}

// redisHostForLog 返回可用于日志的 Redis 位置（host:port/db）。
//
// 与 storageHostForLog 同一原则：**绝不返回密码**。
// 只给位置信息，足够回答「连的是哪个实例、哪个库」。
func redisHostForLog(cfg config.Config) string {
	r := cfg.Redis
	if !r.Configured() {
		return ""
	}
	return fmt.Sprintf("%s:%d/%d", r.Host, r.Port, r.DB)
}
