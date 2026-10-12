package user

import (
	"errors"
	"time"
)

// User 用户模型
type User struct {
	ID            string     `json:"id" db:"id"`
	Email         string     `json:"email" db:"email"`
	Username      string     `json:"username" db:"username"`
	PasswordHash  string     `json:"-" db:"password_hash"` // 不输出到 JSON
	OAuthProvider string     `json:"oauth_provider,omitempty" db:"oauth_provider"`
	OAuthID       string     `json:"-" db:"oauth_id"` // 不输出到 JSON
	Avatar        string     `json:"avatar,omitempty" db:"avatar"`
	Role          Role       `json:"role" db:"role"`
	Status        Status     `json:"status" db:"status"`
	LastLoginAt   *time.Time `json:"last_login_at,omitempty" db:"last_login_at"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" db:"updated_at"`
}

// Role 用户角色
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleUser   Role = "user"
	RoleViewer Role = "viewer"
)

// Status 用户状态
type Status string

const (
	StatusActive   Status = "active"
	StatusInactive Status = "inactive"
	StatusBanned   Status = "banned"
)

// Valid 判断状态是否受支持。
func (s Status) Valid() bool {
	switch s {
	case StatusActive, StatusInactive, StatusBanned:
		return true
	}
	return false
}

// IsUsable 判断该状态下用户是否还能登录。
//
// 只有 active 可以登录：inactive 是管理员主动停用，banned 是违规封禁。
func (s Status) IsUsable() bool { return s == StatusActive }

// CreateUserRequest 创建用户请求
type CreateUserRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Username string `json:"username" validate:"required,min=3,max=50"`
	Password string `json:"password" validate:"required,min=8"`
}

// UpdateUserRequest 更新用户请求
type UpdateUserRequest struct {
	Email    *string `json:"email,omitempty" validate:"omitempty,email"`
	Username *string `json:"username,omitempty" validate:"omitempty,min=3,max=50"`
	Avatar   *string `json:"avatar,omitempty"`
}

// LoginRequest 登录请求
type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

// LoginResponse 登录响应
type LoginResponse struct {
	User         *User  `json:"user"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"` // 秒
}

// RefreshRequest 刷新 Token 请求
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

// OAuthCallbackRequest OAuth 回调请求
type OAuthCallbackRequest struct {
	Code  string `json:"code" validate:"required"`
	State string `json:"state" validate:"required"`
}

// 用户模块的哨兵错误。
//
// 做成哨兵（而不是裸的 fmt.Errorf 字符串）是为了让 handler 能用
// errors.Is 稳定地映射状态码 —— 靠匹配错误文本会在文案一改就静默
// 退化成 500，而 500 与 409 对调用方的含义完全不同（「稍后重试」
// vs「换个邮箱」）。
var (
	// ErrEmailExists 表示邮箱已被占用。
	ErrEmailExists = errors.New("user: email already exists")
	// ErrUsernameExists 表示用户名已被占用。
	ErrUsernameExists = errors.New("user: username already exists")
	// ErrNotFound 表示指定用户不存在。
	ErrNotFound = errors.New("user: not found")
)

// Repository 用户数据访问接口
type Repository interface {
	Create(user *User) error
	GetByID(id string) (*User, error)
	GetByEmail(email string) (*User, error)
	GetByUsername(username string) (*User, error)
	GetByOAuth(provider, oauthID string) (*User, error)
	Update(user *User) error
	Delete(id string) error
	List(offset, limit int) ([]*User, int, error)
	UpdateLastLogin(id string) error
}

// Service 用户服务接口
type Service interface {
	Create(req *CreateUserRequest) (*User, error)
	GetByID(id string) (*User, error)
	GetByEmail(email string) (*User, error)
	Update(id string, req *UpdateUserRequest) (*User, error)
	Delete(id string) error
	List(offset, limit int) ([]*User, int, error)
	ValidatePassword(email, password string) (*User, error)
	UpdatePassword(id, oldPassword, newPassword string) error
	LinkOAuth(userID, provider, oauthID string) error
	// SetStatus 启用 / 停用 / 封禁用户（管理后台用）。
	SetStatus(id string, status Status) (*User, error)
}
