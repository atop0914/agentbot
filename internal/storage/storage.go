// Package storage 是 AgentBot 的持久化底座：连接管理、健康检查、迁移执行。
//
// 设计边界见 docs/persistence.md。三条要记住的约定：
//
//  1. **本包不认识任何业务模型**。它只提供 Open / Ping / Migrate 这类通用能力，
//     业务仓库各自持有 *DB（或 *sql.DB）并自己定义表结构。这样 storage 不会
//     随着业务模块增加而膨胀，也不会形成「改一个业务表要动底座」的耦合。
//
//  2. **迁移脚本内嵌进二进制**（//go:embed）。部署时不需要额外挂载
//     migrations/ 目录，也不会出现「代码更新了但迁移目录还是旧的」这种错配。
//     代价是改迁移必须重新编译 —— 可以接受，迁移本来就随版本发布。
//
//  3. **连不上就是错误，不在这里回退**。回退策略属于装配层的决策
//     （app 层根据 AllowMemoryFallback 决定），本包只如实返回失败。
//     把「要不要降级」藏在底座里，会让调用方无法判断自己拿到的是哪一种存储。
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	// pgx 的 stdlib 兼容层：把 pgx 接到 database/sql 上。
	// 只注册驱动，不导出任何类型 —— 业务仓库一律通过 *sql.DB 访问，
	// 这样换驱动不需要改任何仓库代码。
	_ "github.com/jackc/pgx/v5/stdlib"
)

// DriverName 是注册到 database/sql 的驱动名。
const DriverName = "pgx"

// 默认连接池与超时参数。取值理由见 docs/persistence.md 的「连接池 / 超时 / 迁移」一节：
// PostgreSQL 默认 max_connections=100，单实例开 25 个连接给多副本留余量。
const (
	defaultMaxOpenConns    = 25
	defaultMaxIdleConns    = 5
	defaultConnMaxLifetime = 30 * time.Minute
	defaultConnMaxIdleTime = 5 * time.Minute
	// defaultPingTimeout 是启动期探活超时。长于它说明对端不可达，
	// 应该让部署立刻失败，而不是挂住启动流程。
	defaultPingTimeout = 5 * time.Second

	// BackendPostgres / BackendMemory 是启动日志里出现的那两个取值。
	// 定义成常量而不是散落的字符串字面量，是因为排障时要能 grep 到它。
	BackendPostgres = "postgres"
	BackendMemory   = "memory"
)

// 哨兵错误。调用方（装配层）靠 errors.Is 区分「没配数据库」与
// 「配了但连不上」—— 这两种情况的处置完全不同，不能混成一个错误。
var (
	// ErrNoDSN 表示没有配置数据库连接串。这是**合法状态**（内存后端），
	// 不是失败：显式不配数据库就是「我用内存后端」的正确表达。
	ErrNoDSN = errors.New("storage: no DSN configured")
	// ErrUnreachable 表示连接串可用但数据库不可达。
	ErrUnreachable = errors.New("storage: database is unreachable")
)

// Config 是打开数据库连接所需的全部参数。
//
// 刻意做成扁平结构而不是复用 config.DatabaseConfig：本包属于底座，
// 不应该 import 应用配置包（那会让底座依赖上层）。装配层负责转换。
type Config struct {
	// DSN 是 PostgreSQL 连接串，形如
	// postgres://user:pass@host:port/db?sslmode=disable
	DSN string

	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	PingTimeout     time.Duration
}

// withDefaults 补齐零值。零值在这里表示「没指定」，而非「设为零」——
// 一个 0 连接数的池是没有任何用处的配置，所以不做「显式设零」的语义。
func (c Config) withDefaults() Config {
	if c.MaxOpenConns <= 0 {
		c.MaxOpenConns = defaultMaxOpenConns
	}
	if c.MaxIdleConns < 0 {
		// 负数才是「显式要求不保留空闲连接」—— 见下方对 <= 的说明。
		c.MaxIdleConns = defaultMaxIdleConns
	}
	// 零值表示「没指定」，补默认值。
	//
	// ⚠️ 这里**不能**用 `c.MaxIdleConns < 0`（原本就是 `<`）来判断零值：
	// 「没指定」在 Config 里的表示就是 0，而 `0 < 0` 为假，于是
	// MaxIdleConns 停在 0 —— database/sql 的语义是「不保留任何空闲连接」，
	// 每个查询都要重新建连（TCP + TLS + 认证，实测约 14ms）。
	//
	// 实测（本机，100 次插入）：MaxIdleConns=0 → 16.6 ms/op，
	// MaxIdleConns=5 → 2.7 ms/op，差 6 倍，且 sql.DB.Stats() 的
	// MaxIdleClosed 达 206（连接被逐个丢弃）。这是一个**静默**的
	// 性能缺陷：功能全部正常、测试全绿，只是每条 SQL 慢 6 倍。
	if c.MaxIdleConns == 0 {
		c.MaxIdleConns = defaultMaxIdleConns
	}
	if c.ConnMaxLifetime <= 0 {
		c.ConnMaxLifetime = defaultConnMaxLifetime
	}
	if c.ConnMaxIdleTime <= 0 {
		c.ConnMaxIdleTime = defaultConnMaxIdleTime
	}
	if c.PingTimeout <= 0 {
		c.PingTimeout = defaultPingTimeout
	}
	// 空闲连接数不能超过最大连接数：database/sql 对超出的部分会静默丢弃，
	// 表现为「配了 10 个空闲连接但实际只有 3 个」，属于难以察觉的配置错误。
	if c.MaxIdleConns > c.MaxOpenConns {
		c.MaxIdleConns = c.MaxOpenConns
	}
	return c
}

// DB 是对一个已建立连接的数据库句柄的包装。
//
// 包装而不是直接返回 *sql.DB，是为了让「这个进程实际用了哪个后端」
// 可以被显式回答（Backend()），而不是靠猜有没有配 DSN。
type DB struct {
	db      *sql.DB
	backend string
}

// Open 建立数据库连接并做一次探活。
//
// DSN 为空时返回 ErrNoDSN —— 这不是异常，调用方应当据此选择内存后端。
// DSN 非空但连不上时返回 ErrUnreachable 包装的错误，调用方要么拒绝启动，
// 要么在显式开启回退的情况下降级（见 docs/persistence.md「两套实现如何共存」）。
//
// 返回的 *DB 即使探活失败也已关闭底层连接：否则调用方拿到 error 后
// 无处释放，会漏连接。
func Open(ctx context.Context, cfg Config) (*DB, error) {
	dsn := strings.TrimSpace(cfg.DSN)
	if dsn == "" {
		return nil, ErrNoDSN
	}
	cfg = cfg.withDefaults()

	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		// sql.Open 只做参数解析，这里失败说明 DSN 本身语法不合法。
		// 归为 unreachable 一类对调用方足够：两种情况都是「用不了这个库」。
		return nil, fmt.Errorf("%w: invalid DSN: %v", ErrUnreachable, err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.PingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}

	return &DB{db: db, backend: BackendPostgres}, nil
}

// Backend 返回该句柄对应的后端标识，用于启动日志。
func (d *DB) Backend() string {
	if d == nil {
		return BackendMemory
	}
	return d.backend
}

// SQL 返回底层 *sql.DB，供业务仓库执行查询。
func (d *DB) SQL() *sql.DB { return d.db }

// Ping 探活。用于健康检查端点，也可在每次 cron 开工前确认数据库还活着。
func (d *DB) Ping(ctx context.Context) error {
	if d == nil {
		return ErrNoDSN
	}
	return d.db.PingContext(ctx)
}

// Close 关闭连接池。
func (d *DB) Close() error {
	if d == nil {
		return nil
	}
	return d.db.Close()
}

// migrations 保存内嵌的迁移脚本，按版本号升序执行。
//
// 版本号用零填充的 "0001_xxx" 形式，因此**字符串序即数值序**。
// 这条约定让执行器不需要解析数字，也避免了「10 排在 2 前面」这类经典 bug。
type migration struct {
	Version string
	SQL     string
}

// sortedMigrations 返回按版本号升序排列的迁移列表。
//
// 排序在运行时做（而不是依赖 embed 的文件顺序）：embed 的遍历顺序是
// 字典序，今天恰好等于数值序，但一旦有人在文件里写错序号，
// 排序这一层能挡住，而不是让迁移以错误顺序执行。
func sortedMigrations() []migration {
	list := make([]migration, 0, len(migrationFiles))
	for version, body := range migrationFiles {
		list = append(list, migration{Version: version, SQL: body})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Version < list[j].Version })
	return list
}
