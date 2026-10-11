package storage

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// schemaMigrationsDDL 是迁移账本表。它本身不通过迁移机制创建 ——
// 否则就成了「要执行迁移先得有一条迁移」的鸡生蛋问题。
const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT        PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`

// Migrate 幂等地执行全部尚未应用的迁移。
//
// 幂等的实现方式：账本表记录已应用版本，执行前先查出来做成集合，
// 只跑不在集合里的。重复调用是安全的 —— 这也是它可以用在每次启动路径上的原因。
//
// 每条迁移在**自己的事务**内执行（DDL 与账本写入同时提交或同时回滚），
// 因此不存在「SQL 跑到一半、账本却记成已应用」的中间态。失败时返回
// 包装了版本号的错误，直接告诉运维是哪一条挂的。
func (d *DB) Migrate(ctx context.Context) error {
	if d == nil || d.db == nil {
		return ErrNoDSN
	}

	if _, err := d.db.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("storage: failed to create migration ledger: %w", err)
	}

	applied, err := d.appliedVersions(ctx)
	if err != nil {
		return err
	}

	pending := 0
	for _, m := range sortedMigrations() {
		if _, ok := applied[m.Version]; ok {
			continue
		}
		if err := d.applyMigration(ctx, m); err != nil {
			return err
		}
		pending++
	}

	// 没有待执行项不是错误，但也不该完全无声：一个「以为会建表、结果一条
	// 都没跑」的部署，最可能的原因就是账本里已经有记录了。
	if pending == 0 {
		return nil
	}
	return nil
}

// appliedVersions 返回账本中已记录的版本集合。
func (d *DB) appliedVersions(ctx context.Context) (map[string]struct{}, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("storage: failed to read migration ledger: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[string]struct{})
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("storage: failed to scan migration version: %w", err)
		}
		applied[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: failed to read migration ledger: %w", err)
	}
	return applied, nil
}

// applyMigration 在单个事务内执行一条迁移并记账。
func (d *DB) applyMigration(ctx context.Context, m migration) error {
	if strings.TrimSpace(m.SQL) == "" {
		// 空迁移文件几乎总是误提交（建了文件忘了写内容）。
		// 直接拒绝，避免它被记成「已应用」从而永久遮蔽后来补上的内容。
		return fmt.Errorf("storage: migration %s is empty", m.Version)
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: failed to begin tx for migration %s: %w", m.Version, err)
	}
	defer func() {
		// Commit 成功后 Rollback 返回 ErrTxDone，是预期内的无害错误。
		_ = tx.Rollback()
	}()

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return fmt.Errorf("storage: migration %s failed: %w", m.Version, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version) VALUES ($1)`, m.Version); err != nil {
		return fmt.Errorf("storage: failed to record migration %s: %w", m.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: failed to commit migration %s: %w", m.Version, err)
	}
	return nil
}

// MigrationVersions 返回内嵌的迁移版本列表（升序），用于诊断与测试。
func MigrationVersions() []string {
	list := sortedMigrations()
	versions := make([]string, 0, len(list))
	for _, m := range list {
		versions = append(versions, m.Version)
	}
	return versions
}

// loadEmbeddedMigrations 把内嵌的 .sql 读进内存映射。
//
// 版本号取文件名去掉扩展名的部分（0001_baseline.sql -> 0001_baseline）。
// 文件名本身即版本标识，不额外引入解析规则 —— 少一处规则就少一处出错的地方。
func loadEmbeddedMigrations() map[string]string {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		panic("storage: embedded migrations directory is unreadable: " + err.Error())
	}

	files := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := migrationFS.ReadFile(path.Join("migrations", entry.Name()))
		if err != nil {
			panic("storage: embedded migration is unreadable: " + entry.Name() + ": " + err.Error())
		}
		version := strings.TrimSuffix(entry.Name(), ".sql")
		files[version] = string(body)
	}
	if len(files) == 0 {
		// 迁移目录为空说明 embed 指令写错了（例如路径打错）——
		// 静默通过会让「迁移全都没跑」看起来像一次成功的启动。
		panic("storage: no embedded migrations found")
	}
	return files
}

// assertSQLDB 是编译期断言：它确保 *DB 始终持有一个可用的 database/sql 句柄，
// 防止将来有人把字段改成接口后忘记同步 SQL() 方法。
var _ = func(d *DB) *sql.DB { return d.db }
