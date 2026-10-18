package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// 测试用连接串。与文档 docs/persistence.md 记录的一致：
// 本地 PostgreSQL 16（apt 安装，非容器），角色 agentbot / 库 agentbot_test。
//
// 允许用 AGENTBOT_TEST_DSN 覆盖，这样 CI（无 PG）可以显式跳过而不是硬编码失败。
func testDSN() string {
	if dsn := strings.TrimSpace(os.Getenv("AGENTBOT_TEST_DSN")); dsn != "" {
		return dsn
	}
	return "postgres://agentbot:agentbot@127.0.0.1:5432/agentbot_test?sslmode=disable"
}

// requirePostgres 建立到真机的连接；不可用时跳过。
//
// ⚠️ 这里必须做真实的**连接探测**而不是只检查环境变量：判断依据是
// 「PG 现在到底能不能连上」。判定为不可用就 t.Skip，绝不 t.Fatal ——
// CI runner 上无 PostgreSQL 是常态，让 -short / CI 因环境缺失而红，
// 会让真正的失败淹没在噪音里。
func requirePostgres(t *testing.T) *DB {
	t.Helper()

	db, err := Open(context.Background(), Config{DSN: testDSN(), PingTimeout: 2 * time.Second})
	if err != nil {
		t.Skipf("skipping: PostgreSQL is not reachable at %s: %v", redactDSN(testDSN()), err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		_ = db.Close()
		// 连上了却迁移失败属于真问题，必须报错而不是跳过。
		t.Fatalf("migrations failed against a reachable PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// redactDSN 去掉连接串里的密码再用于日志。
// 测试失败信息经常被贴到 issue 里，别顺手泄露密码。
func redactDSN(dsn string) string {
	at := strings.Index(dsn, "@")
	if at < 0 {
		return dsn
	}
	scheme := strings.Index(dsn, "://")
	if scheme < 0 || scheme+3 > at {
		return "<dsn>@@" + dsn[at:]
	}
	return dsn[:scheme+3] + "***@@" + dsn[at+1:]
}

func TestOpen_EmptyDSNIsNotAnError(t *testing.T) {
	// 反面同样重要：DSN 为空是「我用内存后端」的合法表达，
	// 必须返回可判定的 ErrNoDSN，而不是一个笼统的失败。
	for _, dsn := range []string{"", "   ", "\t\n"} {
		db, err := Open(context.Background(), Config{DSN: dsn})
		if !errors.Is(err, ErrNoDSN) {
			t.Fatalf("Open(%q) error = %v, want ErrNoDSN", dsn, err)
		}
		if db != nil {
			t.Fatalf("Open(%q) returned a non-nil DB along with an error", dsn)
		}
	}
}

func TestOpen_UnreachableIsDistinguishableFromNoDSN(t *testing.T) {
	// 这两种情况的处置完全不同（内存后端 vs 拒绝启动），
	// 因此必须能被 errors.Is 区分开。用一个几乎不可能开的端口。
	db, err := Open(context.Background(),
		Config{DSN: "postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable",
			PingTimeout: 2 * time.Second})
	if err == nil {
		_ = db.Close()
		t.Fatal("expected an error connecting to a closed port")
	}
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("error = %v, want ErrUnreachable", err)
	}
	if errors.Is(err, ErrNoDSN) {
		t.Fatal("an unreachable database must not be reported as an absent DSN")
	}
	if db != nil {
		t.Fatal("Open must not return a usable handle when the ping failed")
	}
}

func TestConfig_WithDefaultsClampsIdleToOpen(t *testing.T) {
	// 空闲连接数超过最大连接数时 database/sql 会静默丢弃多余的，
	// 表现为「配了 10 个空闲连接实际只有 3 个」。这里断言我们主动夹紧。
	cfg := Config{DSN: "x", MaxOpenConns: 3, MaxIdleConns: 10}.withDefaults()
	if cfg.MaxIdleConns != 3 {
		t.Fatalf("MaxIdleConns = %d, want it clamped to MaxOpenConns (3)", cfg.MaxIdleConns)
	}

	// 零值 = 未指定，应补默认值而不是留 0。
	def := Config{DSN: "x"}.withDefaults()
	if def.MaxOpenConns != defaultMaxOpenConns || def.PingTimeout != defaultPingTimeout {
		t.Fatalf("zero-value config was not defaulted: %+v", def)
	}
	// ⚠️ MaxIdleConns 必须**单独**断言。
	//
	// 这里曾经漏掉：上面那行只检查了 MaxOpenConns 与 PingTimeout，
	// 而 MaxIdleConns 的守卫写成了 `if c.MaxIdleConns < 0`
	// ——「未指定」的表示就是 0，`0 < 0` 为假，于是它停在 0。
	// database/sql 对 0 的语义是「不保留任何空闲连接」：
	// 每个查询都要重新建连（TCP + TLS + 认证）。
	//
	// 实测（本机，100 次插入）：MaxIdleConns=0 → 16.6 ms/op，
	// MaxIdleConns=5 → 2.7 ms/op。**6 倍**，而且不报任何错、
	// 功能全部正常、测试全绿 —— 只有基准能看见它。
	//
	// 教训：断言「默认值补齐」时必须逐个字段覆盖，
	// 只挑两个字段检查等于给其余字段留了一个静默的后门。
	if def.MaxIdleConns != defaultMaxIdleConns {
		t.Fatalf("MaxIdleConns = %d, want %d — 0 means 'keep no idle connections' "+
			"to database/sql, so every query would pay a fresh connection "+
			"handshake (~6x slower on inserts, measured)",
			def.MaxIdleConns, defaultMaxIdleConns)
	}
	if def.ConnMaxLifetime != defaultConnMaxLifetime || def.ConnMaxIdleTime != defaultConnMaxIdleTime {
		t.Fatalf("connection lifetime defaults not applied: %+v", def)
	}
}

// TestConfig_ExplicitZeroIdleIsNotExpressible 记录一个有意的语义取舍。
//
// 「不保留空闲连接」在 database/sql 里是合法配置（MaxIdleConns=0），
// 但本项目把 0 定义为「未指定」。要用 0 就必须显式传一个非零的
// MaxOpenConns 并自行设置，或直接改这里的约定。
//
// 写这条测试不是为了「测」什么，而是把这个取舍**钉在代码里**：
// 否则将来有人想配 MaxIdleConns=0，会以为配置生效了，
// 实际上被 withDefaults 改成了 5 —— 又一个静默分歧。
func TestConfig_ExplicitZeroIdleIsNotExpressible(t *testing.T) {
	cfg := Config{DSN: "x", MaxIdleConns: 0}.withDefaults()
	if cfg.MaxIdleConns != defaultMaxIdleConns {
		t.Fatalf("约定已变：MaxIdleConns=0 现在被解释为 %d，请更新注释与文档",
			cfg.MaxIdleConns)
	}
}

func TestMigrationVersions_AreSortedAndNonEmpty(t *testing.T) {
	versions := MigrationVersions()
	if len(versions) == 0 {
		t.Fatal("no embedded migrations were found — the embed directive is wrong")
	}
	// 零填充的版本号应保证字符串序 == 数值序。这里断言排序确实生效。
	for i := 1; i < len(versions); i++ {
		if versions[i-1] >= versions[i] {
			t.Fatalf("migration versions are not strictly ascending: %v", versions)
		}
	}
}

func TestMigrate_IsIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping PostgreSQL-backed test in short mode")
	}
	db := requirePostgres(t)
	ctx := context.Background()

	// requirePostgres 已经跑过一次 Migrate；再跑两次应当都是无操作。
	// 幂等是它可以用在每次启动路径上的前提 —— 重复调用不能报错，
	// 更不能因为重复执行 DDL 而失败。
	for i := 0; i < 2; i++ {
		if err := db.Migrate(ctx); err != nil {
			t.Fatalf("repeated Migrate call %d failed: %v", i+1, err)
		}
	}

	// 账本里每个版本只能出现一次。
	var dupes int
	err := db.SQL().QueryRowContext(ctx, `
		SELECT count(*) FROM (
			SELECT version FROM schema_migrations GROUP BY version HAVING count(*) > 1
		) AS d`).Scan(&dupes)
	if err != nil {
		t.Fatalf("failed to check the migration ledger: %v", err)
	}
	if dupes != 0 {
		t.Fatalf("migration ledger contains %d duplicated version(s)", dupes)
	}

	// 每个内嵌版本都必须在账本里。
	for _, v := range MigrationVersions() {
		var n int
		if err := db.SQL().QueryRowContext(ctx,
			`SELECT count(*) FROM schema_migrations WHERE version = $1`, v).Scan(&n); err != nil {
			t.Fatalf("failed to look up version %s: %v", v, err)
		}
		if n != 1 {
			t.Fatalf("version %s recorded %d times, want exactly 1", v, n)
		}
	}
}

func TestMigrate_CreatesBaselineTables(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping PostgreSQL-backed test in short mode")
	}
	db := requirePostgres(t)

	// 断言表真的存在，而不是只断言「Migrate 没报错」——
	// 一个把 SQL 写错成 COMMENT 的迁移同样不会报错。
	for _, table := range []string{"audit_events", "users", "schema_migrations"} {
		var exists bool
		err := db.SQL().QueryRowContext(context.Background(),
			`SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = 'public' AND table_name = $1
			)`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("failed to probe table %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("table %q was not created by the baseline migration", table)
		}
	}
}

// TestPostgresRoundTrip 是任务书要求的「真机读写往返」：
// 真连本地 PostgreSQL 写一行再读回来，不经过业务模块。
func TestPostgresRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping PostgreSQL-backed test in short mode")
	}
	db := requirePostgres(t)
	ctx := context.Background()

	id := "roundtrip-" + time.Now().UTC().Format("20060102150405.000000000")
	_, err := db.SQL().ExecContext(ctx, `
		INSERT INTO audit_events (id, event_type, action, actor_id, details)
		VALUES ($1, $2, $3, $4, $5::jsonb)`,
		id, "storage.roundtrip", "storage.roundtrip", "tester", `{"ok":true}`)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.SQL().ExecContext(context.Background(), `DELETE FROM audit_events WHERE id = $1`, id)
	})

	var (
		action  string
		details string
		seq     int64
	)
	err = db.SQL().QueryRowContext(ctx,
		`SELECT action, details::text, seq FROM audit_events WHERE id = $1`, id).
		Scan(&action, &details, &seq)
	if err != nil {
		t.Fatalf("read-back failed: %v", err)
	}
	if action != "storage.roundtrip" {
		t.Fatalf("action = %q, want %q", action, "storage.roundtrip")
	}
	if !strings.Contains(details, "true") {
		t.Fatalf("jsonb details did not round-trip: %q", details)
	}
	// seq 是 BIGSERIAL，写入时必须由数据库分配（不能是 0）。
	if seq <= 0 {
		t.Fatalf("seq = %d, want a positive sequence value assigned by PostgreSQL", seq)
	}
}

// TestMigrate_RejectsEmptyMigration 覆盖「空迁移文件」这条守卫。
// 空迁移如果被放行并记账，后来补上内容也不会再执行 —— 永久遮蔽。
func TestMigrate_RejectsEmptyMigration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping PostgreSQL-backed test in short mode")
	}
	db := requirePostgres(t)
	err := db.applyMigration(context.Background(), migration{Version: "9999_empty", SQL: "   \n\t "})
	if err == nil {
		t.Fatal("an empty migration must be rejected, not silently recorded as applied")
	}
	if !strings.Contains(err.Error(), "9999_empty") {
		t.Fatalf("error should name the offending version, got: %v", err)
	}

	// 反向断言：被拒绝的版本不得留在账本里。
	var n int
	if err := db.SQL().QueryRowContext(context.Background(),
		`SELECT count(*) FROM schema_migrations WHERE version = '9999_empty'`).Scan(&n); err != nil {
		t.Fatalf("ledger lookup failed: %v", err)
	}
	if n != 0 {
		t.Fatalf("rejected migration was recorded %d time(s)", n)
	}
}

func TestDB_NilReceiverIsSafe(t *testing.T) {
	// 装配层在「无数据库」分支下会把 DB 留成 nil，此时方法必须安全可调用，
	// 否则每个调用点都要先判空。
	var db *DB
	if db.Backend() != BackendMemory {
		t.Fatalf("nil DB Backend() = %q, want %q", db.Backend(), BackendMemory)
	}
	if err := db.Ping(context.Background()); !errors.Is(err, ErrNoDSN) {
		t.Fatalf("nil DB Ping() = %v, want ErrNoDSN", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("nil DB Close() = %v, want nil", err)
	}
	if err := db.Migrate(context.Background()); !errors.Is(err, ErrNoDSN) {
		t.Fatalf("nil DB Migrate() = %v, want ErrNoDSN", err)
	}
}

var _ = sql.ErrNoRows // 保持 database/sql 的 import 被显式使用
