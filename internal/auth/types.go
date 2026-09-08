package auth

import (
	"time"
)

// Claims JWT 声明
type Claims struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Role     string `json:"role"`
}

// TokenPair Token 对
type TokenPair struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// TokenConfig Token 配置
type TokenConfig struct {
	Secret          string        `json:"secret"`
	AccessExpiry    time.Duration `json:"access_expiry"`    // Access Token 过期时间
	RefreshExpiry   time.Duration `json:"refresh_expiry"`   // Refresh Token 过期时间
	Issuer          string        `json:"issuer"`           // 签发者
}

// OAuthConfig OAuth 配置
type OAuthConfig struct {
	Provider     string   `json:"provider"`      // github, google
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	RedirectURL  string   `json:"redirect_url"`
	Scopes       []string `json:"scopes"`
	AuthURL      string   `json:"auth_url"`
	TokenURL     string   `json:"token_url"`
	UserInfoURL  string   `json:"user_info_url"`
}

// OAuthUserInfo OAuth 用户信息
type OAuthUserInfo struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Username string `json:"username"`
	Avatar   string `json:"avatar"`
	Provider string `json:"provider"`
}

// AuthService 认证服务接口
type AuthService interface {
	// Register 用户注册
	Register(req *RegisterRequest) (*LoginResponse, error)
	// Login 用户登录
	Login(req *LoginRequest) (*LoginResponse, error)
	// RefreshToken 刷新 Token
	RefreshToken(refreshToken string) (*TokenPair, error)
	// ValidateAccessToken 验证 Access Token
	ValidateAccessToken(token string) (*Claims, error)
	// Logout 用户登出（将 Token 加入黑名单）
	Logout(userID, accessToken string) error
	// GetOAuthURL 获取 OAuth 授权 URL
	GetOAuthURL(provider, state string) (string, error)
	// HandleOAuthCallback 处理 OAuth 回调
	HandleOAuthCallback(provider, code, state string) (*LoginResponse, error)
}

// RegisterRequest 注册请求
type RegisterRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Username string `json:"username" validate:"required,min=3,max=50"`
	Password string `json:"password" validate:"required,min=8"`
}

// LoginRequest 登录请求
type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

// LoginResponse 登录响应
type LoginResponse struct {
	User         interface{} `json:"user"`
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	ExpiresIn    int64       `json:"expires_in"` // 秒
}
