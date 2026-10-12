package user

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// repository_postgres.go 是 Repository 的 PostgreSQL 实现。
//
// 表结构由 storage 的 0001_baseline 迁移创建（users），本文件不建表 ——
// 「一个表只有一个归属」见 docs/persistence.md 第 5 节。
//
// 语义必须与 memory.go 的实现逐条对齐，并由
// repository_contract_test.go 里同一组断言强制。三处容易走偏的地方：
//
//  1. **GetByX 未命中返回 (nil, nil)**，不是 ErrNoRows。
//     memory 版就是这么写的，而 service 层靠 `existing != nil` 判断
//     「邮箱已被占用」—— 若这里返回 error，Create 会把「没人用过这个邮箱」
//     报成 "failed to check email"，首次注册直接失败。
//     这是本次迁移中最隐蔽的一条：错成 error 时正常路径全挂，
//     但错误信息看起来像是数据库出了问题。
//
//  2. **唯一约束冲突要翻译成可判定的错误**，不能把裸 SQL 错误往上抛。
//     service 层已经先查后插，但并发注册下「先查」与「插入」之间有窗口，
//     唯一索引是最终防线；裸错误会让调用方看到 pq: duplicate key ...,
//     既无法 409 也无法给用户看。
//
//  3. **OAuth 唯一索引是部分索引**（只约束 provider/id 都非空的行），
//     所以多个未绑定 OAuth 的用户（两列都是空串）必须能共存。
//     若这里自作聪明地补一个「空串也要唯一」的判断，就会把这条正常
//     业务形态变成一个 bug —— 而基线迁移正是为此才用了部分索引。
//
// 曾经的 SQLiteRepository 是**从未被装配过的死代码**（grep 全仓只有定义处），
// 其 SQL 也与基线迁移的表结构不一致（自带 InitTable、用 ? 占位符与 DATETIME），
// 已在本次迁移中删除。保留一份用不上的死实现只会误导后来者。
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository 创建 PostgreSQL 用户仓库。
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// userColumns 抽成常量：SELECT 与 scan 的顺序必须逐列对应。
// 分散手写迟早出现「加了列但 scan 没跟」的错位读取 —— 那种 bug 不报错，
// 只是两个字段的值悄悄对调（如 avatar 与 role 互换）。
const userColumns = `id, email, username, password_hash, oauth_provider,
	oauth_id, avatar, role, status, last_login_at, created_at, updated_at`

// Create 创建用户。
//
// 与 memory 版一样：补全时间戳、默认角色与状态，并**存副本**。
// 唯一约束冲突被翻译成 ErrEmailExists / ErrUsernameExists，
// 供上层返回 409 而不是 500。
func (r *PostgresRepository) Create(u *User) error {
	now := time.Now()
	u.CreatedAt = now
	u.UpdatedAt = now
	if u.Role == "" {
		u.Role = RoleUser
	}
	if u.Status == "" {
		u.Status = StatusActive
	}

	query := `INSERT INTO users (` + userColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	_, err := r.db.Exec(query,
		u.ID, u.Email, u.Username, u.PasswordHash,
		u.OAuthProvider, u.OAuthID, u.Avatar,
		u.Role, u.Status, u.LastLoginAt, u.CreatedAt, u.UpdatedAt,
	)
	if err != nil {
		return translateUniqueViolation(err)
	}
	return nil
}

// translateUniqueViolation 把唯一约束冲突翻译成可判定的哨兵错误。
//
// 判定依据是 pgconn 给出的约束名（idx_users_email / idx_users_username），
// 而不是错误文本 —— 文本会随版本与 locale 改变，约束名是 schema 的一部分。
func translateUniqueViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("failed to write user: %w", err)
	}
	// 23505 = unique_violation
	if pgErr.Code != "23505" {
		// 外键、非空等其它约束错误原样带出：它们代表调用方给的
		// 数据有问题，但不属于「已存在」这一类，不该被归成同一个哨兵。
		return fmt.Errorf("failed to write user: %w", err)
	}
	switch pgErr.ConstraintName {
	case "idx_users_email", "users_email_key":
		return ErrEmailExists
	case "idx_users_username", "users_username_key":
		return ErrUsernameExists
	default:
		// OAuth 部分索引冲突等：仍然归为「已存在」，但保留原文便于排查。
		return fmt.Errorf("%w: %s", ErrUsernameExists, pgErr.ConstraintName)
	}
}

// GetByID 根据 ID 获取用户。未命中返回 (nil, nil)，与 memory 版一致。
func (r *PostgresRepository) GetByID(id string) (*User, error) {
	return r.queryOne(`SELECT `+userColumns+` FROM users WHERE id = $1`, id)
}

// GetByEmail 根据邮箱获取用户。
func (r *PostgresRepository) GetByEmail(email string) (*User, error) {
	return r.queryOne(`SELECT `+userColumns+` FROM users WHERE email = $1`, email)
}

// GetByUsername 根据用户名获取用户。
func (r *PostgresRepository) GetByUsername(username string) (*User, error) {
	return r.queryOne(`SELECT `+userColumns+` FROM users WHERE username = $1`, username)
}

// GetByOAuth 根据 OAuth 信息获取用户。
//
// 两个字段都为空时直接返回 (nil, nil)：未绑定 OAuth 的用户在表里是
// 两列空串（基线用了部分索引让它们能共存），若拿空串去查会**随机**
// 命中其中一个未绑定账号 —— 那会让「第三方登录」把用户登成别人。
func (r *PostgresRepository) GetByOAuth(provider, oauthID string) (*User, error) {
	if provider == "" || oauthID == "" {
		return nil, nil
	}
	return r.queryOne(
		`SELECT `+userColumns+` FROM users WHERE oauth_provider = $1 AND oauth_id = $2`,
		provider, oauthID)
}

// Update 更新用户的可变字段。
//
// 只更新 email/username/avatar/role/status：password_hash 与 oauth 走
// 各自的专用方法（UpdatePassword / LinkOAuth），混在一个 UPDATE 里会让
// 「改个头像」意外把密码写成空。这与 memory 版的字段范围一致。
func (r *PostgresRepository) Update(u *User) error {
	u.UpdatedAt = time.Now()
	res, err := r.db.Exec(`
		UPDATE users SET email = $1, username = $2, avatar = $3,
			role = $4, status = $5, updated_at = $6
		WHERE id = $7`,
		u.Email, u.Username, u.Avatar, u.Role, u.Status, u.UpdatedAt, u.ID)
	if err != nil {
		return translateUniqueViolation(err)
	}
	// 更新不存在的行不是错误（幂等），但这里仍检查一下受影响行数：
	// 0 行通常意味着调用方拿了一个已删除用户的 ID，
	// 静默成功会让「更新成功但看不到变化」成为最难查的一类问题。
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("failed to update user: no user with id %s", u.ID)
	}
	return nil
}

// Delete 删除用户。
func (r *PostgresRepository) Delete(id string) error {
	if _, err := r.db.Exec(`DELETE FROM users WHERE id = $1`, id); err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}
	return nil
}

// List 列出用户，返回当前页与总数。
//
// 排序用 (created_at DESC, id DESC) 保证**全序**：同一毫秒创建的用户
// （测试里很常见，批量注册就是）若只按 created_at 排，两次查询的相对
// 顺序可能不同，分页时会出现「某一行在两页各出现一次」。
// 这与 memory 版遍历 map 时**必须排序**是同一类问题。
func (r *PostgresRepository) List(offset, limit int) ([]*User, int, error) {
	var total int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count users: %w", err)
	}

	// offset/limit 为负时归零：负的 OFFSET 在 PostgreSQL 里是语法错误，
	// 会让一个「参数没传好」的前端请求变成 500。
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultListLimit
	}

	rows, err := r.db.Query(
		`SELECT `+userColumns+` FROM users
		 ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list users: %w", err)
	}
	defer rows.Close()

	out := make([]*User, 0, limit)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to read users: %w", err)
	}
	return out, total, nil
}

// defaultListLimit 是 List 未指定条数时的默认页大小。
const defaultListLimit = 20

// UpdateLastLogin 更新最后登录时间。
func (r *PostgresRepository) UpdateLastLogin(id string) error {
	if _, err := r.db.Exec(`UPDATE users SET last_login_at = $1 WHERE id = $2`, time.Now(), id); err != nil {
		return fmt.Errorf("failed to update last login: %w", err)
	}
	return nil
}

// Ping 探活，供健康检查与端到端断言使用。
func (r *PostgresRepository) Ping() error { return r.db.Ping() }

// ListAll 返回全部用户（按 id 排序），主要用于测试与装配层的一次性扫描。
//
// 单独提供一个方法而不是用 List(0, 0)：List 的 limit<=0 会被补成默认页
// 大小，「要全部」与「没传 limit」是两件不同的事，混在一个方法里
// 迟早会有人以为 List(0,0) 返回全量。
func (r *PostgresRepository) ListAll() ([]*User, error) {
	rows, err := r.db.Query(`SELECT ` + userColumns + ` FROM users ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("failed to list all users: %w", err)
	}
	defer rows.Close()

	out := make([]*User, 0, 16)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read users: %w", err)
	}
	// 显式排序兜底：ORDER BY id 已保证顺序，这里再排一次是为了让
	// 「接口输出稳定」这条约定不依赖调用方的 SQL 细节。
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// queryOne 执行单行查询。**未命中返回 (nil, nil)**，与 memory 版一致 ——
// 见文件头第 1 条：service 层靠 nil 判断「不存在」，返回 error 会把
// 正常注册流程变成失败。
func (r *PostgresRepository) queryOne(query string, args ...interface{}) (*User, error) {
	u, err := scanUser(r.db.QueryRow(query, args...))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return u, nil
}

// rowScanner 是 sql.Row 与 sql.Rows 的最小公共接口，让单行/多行共用 scan。
type rowScanner interface {
	Scan(dest ...interface{}) error
}

// scanUser 从一行读取 User。变量顺序必须与 userColumns 逐列对应。
func scanUser(row rowScanner) (*User, error) {
	var (
		u         User
		lastLogin sql.NullTime
	)
	err := row.Scan(
		&u.ID, &u.Email, &u.Username, &u.PasswordHash,
		&u.OAuthProvider, &u.OAuthID, &u.Avatar,
		&u.Role, &u.Status, &lastLogin,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	// last_login_at 可空：从未登录过的用户是 NULL，必须用 sql.NullTime
	// 接住，否则 scan 会报 "converting NULL to time.Time is unsupported"。
	if lastLogin.Valid {
		t := lastLogin.Time
		u.LastLoginAt = &t
	}
	return &u, nil
}
