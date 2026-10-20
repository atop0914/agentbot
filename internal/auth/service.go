package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/atop0914/agentbot/internal/user"
)

// Service 认证服务实现
type Service struct {
	jwtManager      *JWTManager
	passwordManager *PasswordManager
	userService     user.Service
	oauthConfigs    map[string]*OAuthConfig
	// revocations 是凭据吊销表。
	//
	// 此前这里是 tokenBlacklist map[string]time.Time —— 进程内状态，
	// 多副本部署下 A 副本登出后 B 副本仍接受同一 token 直到自然过期。
	// 改成抽象后，装配层按配置注入内存实现（单副本）或 Redis 实现
	// （多副本共享）。接口是同步的，Redis 实现内部自带超时边界。
	revocations RevocationStore
	// revokeTTL 是吊销记录的存活时长，通常等于 access token 的有效期。
	revokeTTL time.Duration
}

// NewService 创建认证服务。
//
// 不提供不带 RevocationStore 的重载：让「吊销表」成为必填参数，
// 是为了避免出现一个「忘了传、于是静默退回进程内」的装配路径 ——
// 那正是本次要修的问题。显式传 NewMemoryRevocationStore() 也可以，
// 但那是一个看得见的决定。
func NewService(
	jwtManager *JWTManager,
	userService user.Service,
	oauthConfigs map[string]*OAuthConfig,
	revocations RevocationStore,
) *Service {
	ttl := jwtManager.config.AccessExpiry
	if ttl <= 0 {
		// 配置异常时给一个保守值：吊销记录宁可活得久一点，
		// 也不能因为 0 而被拒绝写入（那会让登出彻底失效）。
		ttl = time.Hour
	}
	return &Service{
		jwtManager:      jwtManager,
		passwordManager: NewPasswordManager(),
		userService:     userService,
		oauthConfigs:    oauthConfigs,
		revocations:     revocations,
		revokeTTL:       ttl,
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

// IssueTokens 直接为给定的 claims 签发 Token 对。
//
// 存在的理由：SSO 登录走的是**外部**身份提供方，登录成功时并不经过
// Register/Login；若只能从这两个入口签发令牌，SSO 用户就只能被迫
// 「拿一个随机密码再走一遍 Login」，既多一次密码哈希开销，也让
// 「SSO 用户没有密码」这个事实变得不可表达。
//
// 安全前提：调用方必须已经把 claims 中的每一个字段都绑定到**校验过的**
// 凭据上（sso 包校验完 ID Token 才拿到邮箱与 subject）。这里的
// TenantID 尤其如此 —— 它必须在写进令牌之前完成成员校验，否则
// 「换个 URL 参数就换个租户」。
func (s *Service) IssueTokens(claims *Claims) (*TokenPair, error) {
	if claims == nil || strings.TrimSpace(claims.UserID) == "" {
		return nil, fmt.Errorf("cannot issue a token without a user id")
	}
	return s.jwtManager.GenerateTokenPair(claims)
}

// ValidateAccessToken 验证 Access Token
func (s *Service) ValidateAccessToken(token string) (*Claims, error) {
	claims, err := s.jwtManager.ValidateAccessToken(token)
	if err != nil {
		return nil, err
	}

	// 先验签、再查吊销表：签名无效的 token 不该触发一次存储查询
	// （否则伪造 token 就成了放大 Redis 负载的手段）。
	//
	// 注意 UserID 取自**已验证的 claims**，而不是可由调用方传入的参数 ——
	// 否则「伪造任意 userID」就能查询/影响别人的吊销记录。
	fp := Fingerprint(token)
	revoked, err := s.revocations.IsRevoked(claims.UserID, fp)
	if err != nil {
		// 查不到 ≠ 没被吊销。把存储故障当成「未吊销」会让一次抖动
		// 等于「所有已登出的 token 全部复活」——那比拒绝服务更糟，
		// 因为它静默地撤销了用户的安全动作。
		// 这里选择 fail-closed：认证失败，但错误信息指明是存储问题，
		// 而不是伪装成「token 无效」让人去查 token 本身。
		return nil, fmt.Errorf("%w: %w", ErrRevocationUnavailable, err)
	}
	if revoked {
		return nil, ErrTokenRevoked
	}

	return claims, nil
}

// Logout 用户登出
//
// 吊销是按**具体凭据指纹**而不是按用户：按用户吊销会连带废掉
// 该用户刚签发的新 token（例如在多设备上重新登录后，
// 其中一个设备登出会让所有设备掉线）。
func (s *Service) Logout(userID, accessToken string) error {
	if accessToken == "" {
		return nil
	}
	// TTL 取自 token 的自然有效期：凭据一旦自然过期，吊销记录
	// 就再无意义，让它自动消失即可，不需要任何清理任务。
	if err := s.revocations.Revoke(userID, Fingerprint(accessToken), s.revokeTTL); err != nil {
		return fmt.Errorf("failed to revoke credential: %w", err)
	}
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
