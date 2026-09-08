package user

import (
	"database/sql"
	"fmt"
	"time"
)

// SQLiteRepository SQLite 用户仓库
type SQLiteRepository struct {
	db *sql.DB
}

// NewSQLiteRepository 创建 SQLite 用户仓库
func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

// InitTable 初始化用户表
func (r *SQLiteRepository) InitTable() error {
	query := `
	CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		email TEXT UNIQUE NOT NULL,
		username TEXT UNIQUE NOT NULL,
		password_hash TEXT,
		oauth_provider TEXT DEFAULT '',
		oauth_id TEXT DEFAULT '',
		avatar TEXT DEFAULT '',
		role TEXT NOT NULL DEFAULT 'user',
		status TEXT NOT NULL DEFAULT 'active',
		last_login_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
	CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
	CREATE INDEX IF NOT EXISTS idx_users_oauth ON users(oauth_provider, oauth_id);
	`
	_, err := r.db.Exec(query)
	return err
}

// Create 创建用户
func (r *SQLiteRepository) Create(user *User) error {
	query := `
	INSERT INTO users (id, email, username, password_hash, oauth_provider, oauth_id, avatar, role, status, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	now := time.Now()
	user.CreatedAt = now
	user.UpdatedAt = now

	if user.Role == "" {
		user.Role = RoleUser
	}
	if user.Status == "" {
		user.Status = StatusActive
	}

	_, err := r.db.Exec(query,
		user.ID, user.Email, user.Username, user.PasswordHash,
		user.OAuthProvider, user.OAuthID, user.Avatar,
		user.Role, user.Status, user.CreatedAt, user.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}
	return nil
}

// GetByID 根据 ID 获取用户
func (r *SQLiteRepository) GetByID(id string) (*User, error) {
	query := `SELECT id, email, username, password_hash, oauth_provider, oauth_id, avatar, role, status, last_login_at, created_at, updated_at FROM users WHERE id = ?`
	return r.scanOne(r.db.QueryRow(query, id))
}

// GetByEmail 根据邮箱获取用户
func (r *SQLiteRepository) GetByEmail(email string) (*User, error) {
	query := `SELECT id, email, username, password_hash, oauth_provider, oauth_id, avatar, role, status, last_login_at, created_at, updated_at FROM users WHERE email = ?`
	return r.scanOne(r.db.QueryRow(query, email))
}

// GetByUsername 根据用户名获取用户
func (r *SQLiteRepository) GetByUsername(username string) (*User, error) {
	query := `SELECT id, email, username, password_hash, oauth_provider, oauth_id, avatar, role, status, last_login_at, created_at, updated_at FROM users WHERE username = ?`
	return r.scanOne(r.db.QueryRow(query, username))
}

// GetByOAuth 根据 OAuth 信息获取用户
func (r *SQLiteRepository) GetByOAuth(provider, oauthID string) (*User, error) {
	query := `SELECT id, email, username, password_hash, oauth_provider, oauth_id, avatar, role, status, last_login_at, created_at, updated_at FROM users WHERE oauth_provider = ? AND oauth_id = ?`
	return r.scanOne(r.db.QueryRow(query, provider, oauthID))
}

// Update 更新用户
func (r *SQLiteRepository) Update(user *User) error {
	query := `
	UPDATE users SET email = ?, username = ?, avatar = ?, role = ?, status = ?, updated_at = ?
	WHERE id = ?
	`
	user.UpdatedAt = time.Now()
	_, err := r.db.Exec(query,
		user.Email, user.Username, user.Avatar,
		user.Role, user.Status, user.UpdatedAt, user.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update user: %w", err)
	}
	return nil
}

// Delete 删除用户
func (r *SQLiteRepository) Delete(id string) error {
	_, err := r.db.Exec("DELETE FROM users WHERE id = ?", id)
	return err
}

// List 列出用户
func (r *SQLiteRepository) List(offset, limit int) ([]*User, int, error) {
	// 获取总数
	var total int
	err := r.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// 获取列表
	query := `SELECT id, email, username, password_hash, oauth_provider, oauth_id, avatar, role, status, last_login_at, created_at, updated_at FROM users ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := r.db.Query(query, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		user, err := r.scanRow(rows)
		if err != nil {
			return nil, 0, err
		}
		users = append(users, user)
	}

	return users, total, nil
}

// UpdateLastLogin 更新最后登录时间
func (r *SQLiteRepository) UpdateLastLogin(id string) error {
	_, err := r.db.Exec("UPDATE users SET last_login_at = ? WHERE id = ?", time.Now(), id)
	return err
}

// scanOne 扫描单行
func (r *SQLiteRepository) scanOne(row *sql.Row) (*User, error) {
	var user User
	var lastLogin sql.NullTime
	err := row.Scan(
		&user.ID, &user.Email, &user.Username, &user.PasswordHash,
		&user.OAuthProvider, &user.OAuthID, &user.Avatar,
		&user.Role, &user.Status, &lastLogin,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if lastLogin.Valid {
		user.LastLoginAt = &lastLogin.Time
	}
	return &user, nil
}

// scanRow 扫描行
func (r *SQLiteRepository) scanRow(rows *sql.Rows) (*User, error) {
	var user User
	var lastLogin sql.NullTime
	err := rows.Scan(
		&user.ID, &user.Email, &user.Username, &user.PasswordHash,
		&user.OAuthProvider, &user.OAuthID, &user.Avatar,
		&user.Role, &user.Status, &lastLogin,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if lastLogin.Valid {
		user.LastLoginAt = &lastLogin.Time
	}
	return &user, nil
}
