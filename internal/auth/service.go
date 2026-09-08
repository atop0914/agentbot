package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/atop0914/agentbot/internal/user"
	"github.com/google/uuid"
)

// Service 认证服务实现
type Service struct {
	jwtManager      *JWTManager
	passwordManager *PasswordManager
	userService     user.Service
	oauthConfigs    map[string]*OAuthConfig
	tokenBlacklist  map[string]time.Time // 简单实现，生产环境应使用 Redis
}

// NewService 创建认证服务
func NewService(
	jwtManager *JWTManager,
	userService user.Service,
	oauthConfigs map[string]*OAuthConfig,
) *Service {
	return &Service{
		jwtManager:      jwtManager,
		passwordManager: NewPasswordManager(),
		userService:     userService,
		oauthConfigs:    oauthConfigs,
		tokenBlacklist:  make(map[string]time.Time),
	}
}

// Register 用户注册
func (s *Service) Register(req *RegisterRequest) (*LoginResponse, error) {
	// 验证密码强度
	if err := s.passwordManager.ValidatePassword(req.Password); err != nil {
		return nil, fmt.Errorf("weak password: %w", err)
	}

	// 创建用户
	createReq := &user.CreateUserRequest{
		Email:    req.Email,
		Username: req.Username,
		Password: req.Password,
	}

	u, err := s.userService.Create(createReq)
	if err != nil {
		return nil, err
	}

	// 生成 Token
	claims := &Claims{
		UserID:   u.ID,
		Username: u.Username,
		Email:    u.Email,
		Role:     string(u.Role),
	}

	tokenPair, err := s.jwtManager.GenerateTokenPair(claims)
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	return &LoginResponse{
		User:         u,
		AccessToken:  tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		ExpiresIn:    int64(s.jwtManager.config.AccessExpiry.Seconds()),
	}, nil
}

// Login 用户登录
func (s *Service) Login(req *LoginRequest) (*LoginResponse, error) {
	// 验证密码
	u, err := s.userService.ValidatePassword(req.Email, req.Password)
	if err != nil {
		return nil, err
	}

	// 更新最后登录时间
	_ = s.userService.(*user.UserService) // 类型断言
	// 注意：这里需要 user.Repository 来更新，暂时跳过

	// 生成 Token
	claims := &Claims{
		UserID:   u.ID,
		Username: u.Username,
		Email:    u.Email,
		Role:     string(u.Role),
	}

	tokenPair, err := s.jwtManager.GenerateTokenPair(claims)
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	return &LoginResponse{
		User:         u,
		AccessToken:  tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		ExpiresIn:    int64(s.jwtManager.config.AccessExpiry.Seconds()),
	}, nil
}

// RefreshToken 刷新 Token
func (s *Service) RefreshToken(refreshToken string) (*TokenPair, error) {
	// 验证 refresh token
	userID, err := s.jwtManager.ValidateRefreshToken(refreshToken)
	if err != nil {
		return nil, err
	}

	// 获取用户信息
	u, err := s.userService.GetByID(userID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, fmt.Errorf("user not found")
	}

	// 生成新的 token pair
	claims := &Claims{
		UserID:   u.ID,
		Username: u.Username,
		Email:    u.Email,
		Role:     string(u.Role),
	}

	return s.jwtManager.GenerateTokenPair(claims)
}

// ValidateAccessToken 验证 Access Token
func (s *Service) ValidateAccessToken(token string) (*Claims, error) {
	// 检查黑名单
	if _, blacklisted := s.tokenBlacklist[token]; blacklisted {
		return nil, fmt.Errorf("token has been revoked")
	}

	return s.jwtManager.ValidateAccessToken(token)
}

// Logout 用户登出
func (s *Service) Logout(userID, accessToken string) error {
	// 将 token 加入黑名单（简单实现）
	s.tokenBlacklist[accessToken] = time.Now().Add(s.jwtManager.config.AccessExpiry)
	return nil
}

// GetOAuthURL 获取 OAuth 授权 URL
func (s *Service) GetOAuthURL(provider, state string) (string, error) {
	config, ok := s.oauthConfigs[provider]
	if !ok {
		return "", fmt.Errorf("unsupported OAuth provider: %s", provider)
	}

	params := url.Values{}
	params.Set("client_id", config.ClientID)
	params.Set("redirect_uri", config.RedirectURL)
	params.Set("scope", strings.Join(config.Scopes, " "))
	params.Set("state", state)
	params.Set("response_type", "code")

	return config.AuthURL + "?" + params.Encode(), nil
}

// HandleOAuthCallback 处理 OAuth 回调
func (s *Service) HandleOAuthCallback(provider, code, state string) (*LoginResponse, error) {
	config, ok := s.oauthConfigs[provider]
	if !ok {
		return nil, fmt.Errorf("unsupported OAuth provider: %s", provider)
	}

	// 用 code 换取 access token
	accessToken, err := s.exchangeCodeForToken(config, code)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}

	// 获取用户信息
	oauthUser, err := s.fetchOAuthUser(config, accessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user info: %w", err)
	}

	// 查找或创建用户
	u, err := s.userService.GetByEmail(oauthUser.Email)
	if err != nil {
		return nil, err
	}

	if u == nil {
		// 创建新用户
		createReq := &user.CreateUserRequest{
			Email:    oauthUser.Email,
			Username: oauthUser.Username,
			Password: uuid.New().String(), // 随机密码，OAuth 用户不使用密码登录
		}
		u, err = s.userService.Create(createReq)
		if err != nil {
			return nil, err
		}

		// 关联 OAuth
		if err := s.userService.LinkOAuth(u.ID, provider, oauthUser.ID); err != nil {
			return nil, err
		}
	}

	// 生成 Token
	claims := &Claims{
		UserID:   u.ID,
		Username: u.Username,
		Email:    u.Email,
		Role:     string(u.Role),
	}

	tokenPair, err := s.jwtManager.GenerateTokenPair(claims)
	if err != nil {
		return nil, err
	}

	return &LoginResponse{
		User:         u,
		AccessToken:  tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		ExpiresIn:    int64(s.jwtManager.config.AccessExpiry.Seconds()),
	}, nil
}

// exchangeCodeForToken 用 code 换取 access token
func (s *Service) exchangeCodeForToken(config *OAuthConfig, code string) (string, error) {
	data := url.Values{}
	data.Set("client_id", config.ClientID)
	data.Set("client_secret", config.ClientSecret)
	data.Set("code", code)
	data.Set("redirect_uri", config.RedirectURL)
	data.Set("grant_type", "authorization_code")

	resp, err := http.PostForm(config.TokenURL, data)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange failed: %s", string(body))
	}

	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}

	return result.AccessToken, nil
}

// fetchOAuthUser 获取 OAuth 用户信息
func (s *Service) fetchOAuthUser(config *OAuthConfig, accessToken string) (*OAuthUserInfo, error) {
	req, err := http.NewRequest("GET", config.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch user info: %s", string(body))
	}

	// 根据不同 provider 解析不同的响应格式
	switch config.Provider {
	case "github":
		return s.parseGitHubUser(body)
	case "google":
		return s.parseGoogleUser(body)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", config.Provider)
	}
}

// parseGitHubUser 解析 GitHub 用户信息
func (s *Service) parseGitHubUser(body []byte) (*OAuthUserInfo, error) {
	var user struct {
		ID        int    `json:"id"`
		Email     string `json:"email"`
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, err
	}

	return &OAuthUserInfo{
		ID:       fmt.Sprintf("%d", user.ID),
		Email:    user.Email,
		Username: user.Login,
		Avatar:   user.AvatarURL,
		Provider: "github",
	}, nil
}

// parseGoogleUser 解析 Google 用户信息
func (s *Service) parseGoogleUser(body []byte) (*OAuthUserInfo, error) {
	var user struct {
		ID      string `json:"id"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, err
	}

	return &OAuthUserInfo{
		ID:       user.ID,
		Email:    user.Email,
		Username: user.Name,
		Avatar:   user.Picture,
		Provider: "google",
	}, nil
}
