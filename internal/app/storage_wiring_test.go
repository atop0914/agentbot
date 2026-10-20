package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/atop0914/agentbot/internal/config"
	"github.com/atop0914/agentbot/internal/storage"
)

// 本文件覆盖 docs/persistence.md「两套实现如何共存」的四条决策分支。
// 「后端选择」是全局一次性决策，走错分支的后果差异极大
// （从「静默丢数据」到「拒绝启动」），所以四条都要有断言。

func testDSN() string {
	if dsn := strings.TrimSpace(os.Getenv("AGENTBOT_TEST_DSN")); dsn != "" {
		return dsn
	}
	return "postgres://agentbot:agentbot@127.0.0.1:5432/agentbot_test?sslmode=disable"
}

// withoutDatabase 返回一个「没有配置数据库」的配置。
func withoutDatabase() config.Config {
	cfg := config.DevelopmentDefault()
	cfg.Database.Host = ""
	cfg.Database.DBName = ""
	return cfg
}

// withDatabase 返回一个指向真机 PG 的配置；测试时若连不上则跳过。
func withDatabase(t *testing.T) config.Config {
	t.Helper()

	cfg := config.DevelopmentDefault()
	cfg.Database.Host = "127.0.0.1"
	cfg.Database.Port = 5432
	cfg.Database.User = "agentbot"
	cfg.Database.Password = "agentbot"
	cfg.Database.DBName = "agentbot_test"
	cfg.Database.SSLMode = "disable"

	// 先探一次真实可达性再决定跳过：判定依据是「PG 现在能不能连上」，
	// 不是环境变量有没有设置。
	probe, err := storage.Open(context.Background(), storage.Config{DSN: cfg.Database.DSN()})
	if err != nil {
		t.Skipf("skipping: PostgreSQL is not reachable: %v", err)
	}
	_ = probe.Close()
	return cfg
}

// databaseFixture 返回一个「声明了数据库」的配置。
//
// 注意不能直接用 DevelopmentDefault()：它刻意**不带** host
// （开发默认 = 没有数据库），所以要在这里显式补上。
// 用不带 host 的配置去测数据库分支，测的其实是内存分支。
func databaseFixture() config.Config {
	cfg := config.DevelopmentDefault()
	cfg.Database.Host = "db.internal"
	cfg.Database.Port = 5432
	cfg.Database.User = "agentbot"
	cfg.Database.Password = "s3cret"
	cfg.Database.DBName = "agentbot"
	cfg.Database.SSLMode = "disable"
	return cfg
}

func TestSelectStorage_NoDatabaseIsMemoryNotAnError(t *testing.T) {
	// 「没配数据库」是内存后端的合法表达，不是失败。
	// 如果这里报错，所有开发/CI 环境都会启动不了。
	sel, err := selectStorage(context.Background(), withoutDatabase())
	if err != nil {
		t.Fatalf("an unconfigured database must not be an error, got: %v", err)
	}
	if sel.backend != storage.BackendMemory {
		t.Fatalf("backend = %q, want %q", sel.backend, storage.BackendMemory)
	}
	if sel.db != nil {
		t.Fatal("a memory-selected storage must not hand back a DB handle")
	}
}

func TestDatabaseDSN_RequiresHostAndName(t *testing.T) {
	// 缺 host 或 db_name 就视为「没配」。反向情形同样要断言：
	// 两者齐全时必须给出非空连接串，否则一个真的配了数据库的部署
	// 会被静默当成内存后端 —— 这正是「看起来配了、实际没生效」的经典坑。
	cases := []struct {
		name   string
		mutate func(*config.Config)
		want   bool // 期望有 DSN
	}{
		{"both set", func(c *config.Config) {}, true},
		{"no host", func(c *config.Config) { c.Database.Host = "" }, false},
		{"blank host", func(c *config.Config) { c.Database.Host = "   " }, false},
		{"no db name", func(c *config.Config) { c.Database.DBName = "" }, false},
		{"blank db name", func(c *config.Config) { c.Database.DBName = " \t " }, false},
		// 空密码仍算配了：证书/信任认证的部署就是空密码。
		{"empty password still counts", func(c *config.Config) { c.Database.Password = "" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := databaseFixture()
			tc.mutate(&cfg)
			got := databaseDSN(cfg) != ""
			if got != tc.want {
				t.Fatalf("databaseDSN() non-empty = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSelectStorage_UnreachableRefusesToStartByDefault(t *testing.T) {
	// 最重要的一条分支：配了数据库但连不上，且**没有**开回退 →
	// 必须拒绝启动。静默退回内存会让「写成功但重启后消失」
	// 变成一次 201 Created，没有任何信号。
	cfg := config.DevelopmentDefault()
	cfg.Database.Host = "127.0.0.1"
	cfg.Database.Port = 1 // 几乎不可能有人监听
	cfg.Database.DBName = "nonexistent"
	cfg.Storage.AllowMemoryFallback = false

	sel, err := selectStorage(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected startup to be refused when the database is unreachable")
	}
	if !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("error should say startup is refused, got: %v", err)
	}
	// 错误信息必须点出那个逃生开关，否则运维只能去翻源码。
	if !strings.Contains(err.Error(), "ALLOW_MEMORY_FALLBACK") {
		t.Fatalf("error should name the opt-in escape hatch, got: %v", err)
	}
	if sel.db != nil {
		t.Fatal("a refused startup must not hand back a DB handle")
	}
}

func TestSelectStorage_UnreachableFallsBackOnlyWhenExplicitlyAllowed(t *testing.T) {
	cfg := config.DevelopmentDefault()
	cfg.Database.Host = "127.0.0.1"
	cfg.Database.Port = 1
	cfg.Database.DBName = "nonexistent"
	cfg.Storage.AllowMemoryFallback = true

	sel, err := selectStorage(context.Background(), cfg)
	if err != nil {
		t.Fatalf("an explicitly allowed fallback must not error, got: %v", err)
	}
	if sel.backend != storage.BackendMemory {
		t.Fatalf("backend = %q, want %q", sel.backend, storage.BackendMemory)
	}
	if sel.db != nil {
		t.Fatal("a fallback must not hand back a DB handle")
	}
}

func TestSelectStorage_ReachableDatabaseSelectsPostgres(t *testing.T) {
	cfg := withDatabase(t)

	sel, err := selectStorage(context.Background(), cfg)
	if err != nil {
		t.Fatalf("selectStorage against a reachable PostgreSQL failed: %v", err)
	}
	if sel.db == nil {
		t.Fatal("a reachable database must hand back a DB handle")
	}
	defer func() { _ = sel.db.Close() }()
	if sel.backend != storage.BackendPostgres {
		t.Fatalf("backend = %q, want %q", sel.backend, storage.BackendPostgres)
	}

	// 连接必须真的可用，而不只是「Open 返回了 nil error」。
	if err := sel.db.Ping(context.Background()); err != nil {
		t.Fatalf("the selected handle is not usable: %v", err)
	}
}

func TestNewWithConfig_NoDatabaseReportsMemoryBackend(t *testing.T) {
	// 端到端：走完整的 NewWithConfig 装配路径，确认 App 上的后端标识正确。
	// 这条断言的意义在于「部署时能回答数据存在哪」——
	// 标识必须是显式字段，而不是靠「Storage 是不是 nil」去猜。
	a, err := NewWithConfig(withoutDatabase())
	if err != nil {
		t.Fatalf("NewWithConfig without a database failed: %v", err)
	}
	if a.StorageBackend != storage.BackendMemory {
		t.Fatalf("StorageBackend = %q, want %q", a.StorageBackend, storage.BackendMemory)
	}
	if a.Storage != nil || a.DB != nil {
		t.Fatal("no database handle should be exposed in the memory backend")
	}
}

func TestNewWithConfig_ReachableDatabaseReportsPostgresBackend(t *testing.T) {
	cfg := withDatabase(t)
	a, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig against a reachable PostgreSQL failed: %v", err)
	}
	if a.StorageBackend != storage.BackendPostgres {
		t.Fatalf("StorageBackend = %q, want %q", a.StorageBackend, storage.BackendPostgres)
	}
	if a.Storage == nil {
		t.Fatal("expected a database handle in the postgres backend")
	}
	defer func() { _ = a.Storage.Close() }()

	// 迁移必须已经在装配路径上跑过 —— 表应该已经存在。
	var exists bool
	err = a.Storage.SQL().QueryRowContext(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			WHERE table_schema='public' AND table_name='audit_events')`).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to probe the migrated schema: %v", err)
	}
	if !exists {
		t.Fatal("NewWithConfig must run migrations; audit_events is missing")
	}
}

func TestNewWithConfig_UnreachableDatabaseRefusesStartup(t *testing.T) {
	// 端到端反向断言：装配入口也必须拒绝，而不是只在 selectStorage 里拒绝。
	cfg := config.DevelopmentDefault()
	cfg.Database.Host = "127.0.0.1"
	cfg.Database.Port = 1
	cfg.Database.DBName = "nonexistent"

	a, err := NewWithConfig(cfg)
	if err == nil {
		t.Fatal("NewWithConfig must refuse to start when the configured database is unreachable")
	}
	if a != nil {
		t.Fatal("a refused startup must not return a half-built App")
	}
}

func TestStorageHostForLog_NeverLeaksThePassword(t *testing.T) {
	// 启动日志最容易被人整段贴进 issue。断言它只带位置、不带凭据。
	cfg := databaseFixture()
	cfg.Database.Password = "super-secret-password-value"

	got := storageHostForLog(cfg)
	if strings.Contains(got, "super-secret-password-value") {
		t.Fatalf("log helper leaked the password: %q", got)
	}
	if !strings.Contains(got, cfg.Database.DBName) {
		t.Fatalf("log helper should still say which database: %q", got)
	}

	// 没配 host 时返回空串，而不是拼出一个 ":/" 这样的怪东西。
	cfg.Database.Host = ""
	if storageHostForLog(cfg) != "" {
		t.Fatalf("expected an empty location when no host is configured, got %q", storageHostForLog(cfg))
	}
}
