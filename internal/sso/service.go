package sso

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultSessionTTL 是授权会话的有效期。
//
// 10 分钟：足够用户走完 IdP 的登录/二次验证，又短到让「先骗出 state 再
// 慢慢利用」的窗口足够小。会话过期后回调一定失败（不是静默新建会话）。
const DefaultSessionTTL = 10 * time.Minute

// BindingPolicy 决定 SSO 身份与本地账号如何关联。
//
// 必须显式配置，不能有隐含默认 —— 账号绑定是最容易被「顺手写一个
// find-or-create」做错的地方：邮箱相同就静默合并，等于「谁先在 IdP 上
// 注册了这个邮箱，谁就能接管本地账号」。
type BindingPolicy string

const (
	// BindingStrict 只允许绑定到已存在的本地账号，绝不自动创建。
	// 企业部署的推荐默认：账号生命周期由管理员掌控。
	BindingStrict BindingPolicy = "strict"
	// BindingAutoCreate 邮箱不存在时自动创建本地账号。
	// 面向自助注册场景，但仍要求 email_verified。
	BindingAutoCreate BindingPolicy = "auto_create"
)

// Valid 判断策略取值是否合法。
func (p BindingPolicy) Valid() bool {
	return p == BindingStrict || p == BindingAutoCreate
}

// Account 是 SSO 登录最终要落地的本地账号信息。
type Account struct {
	// LocalUserID 为空表示需要上层创建账号（BindingAutoCreate 且邮箱未见）。
	LocalUserID string
	Email       string
	Username    string
	Subject     string
	Issuer      string
	Provider    string
	Groups      []string
	// Created 表示本次登录需要新建账号。
	Created bool
}

// UserDirectory 是本地账号目录的抽象。
//
// sso 包不反向依赖 internal/user：装配层注入一个薄适配器即可。
// 这样 SSO 的校验逻辑可以完全用假目录测试，「邮箱冲突策略」这些
// 安全关键分支才有确定性的覆盖。
type UserDirectory interface {
	// FindByEmail 返回本地账号 ID；不存在返回 ("", false, nil)。
	FindByEmail(ctx context.Context, email string) (userID string, found bool, err error)
	// CreateFromSSO 用 SSO 断言的信息创建本地账号，返回新账号 ID。
	CreateFromSSO(ctx context.Context, email, username, issuer, subject string) (userID string, err error)
	// LinkSSO 把 SSO 身份绑定到已有本地账号。
	LinkSSO(ctx context.Context, userID, issuer, subject string) error
}

// SessionStore 保存授权会话。
type SessionStore interface {
	Save(ctx context.Context, s *Session) error
	// Take 取出并删除会话（一次性使用）。
	//
	// 「取出即删除」是防重放的关键：state 复用一次成功登录后必须失效，
	// 否则截获的 state 可以被反复利用。
	Take(ctx context.Context, state string) (*Session, error)
}

// MemorySessionStore 是会话存储的内存实现。
type MemorySessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	now      func() time.Time
}

// NewMemorySessionStore 创建内存会话存储。
func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{
		sessions: make(map[string]*Session),
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// SetClock 注入时钟（测试用）。
func (s *MemorySessionStore) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// Save 保存会话（按 state 索引）。
func (s *MemorySessionStore) Save(_ context.Context, sess *Session) error {
	if sess == nil || sess.State == "" {
		return ErrInvalidState
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	cp := *sess
	s.sessions[cp.State] = &cp
	return nil
}

// Take 取出会话并删除（一次性）。
func (s *MemorySessionStore) Take(_ context.Context, state string) (*Session, error) {
	if trimSpace(state) == "" {
		return nil, ErrInvalidState
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 刻意不在这里 gcLocked()（见 gcLocked 的注释）：
	// 过期会话要能被取到并报出 ErrSessionExpired，而不是伪装成伪造 state。
	sess, ok := s.sessions[state]
	if !ok {
		// 未知 state：可能是伪造，也可能是重放已用过的 state。
		// 两种情况返回同一个错误，不给攻击者反馈。
		return nil, ErrInvalidState
	}
	delete(s.sessions, state)
	cp := *sess
	return &cp, nil
}

// Len 返回当前未过期会话数（测试用）。
func (s *MemorySessionStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	return len(s.sessions)
}

// gcLocked 清理过期会话。调用方必须持锁。
//
// 注意 GC 与 Take 的先后顺序：Take **不做** GC，只在 Save 时清理。
//
// 原因是语义而不是性能：如果 Take 先清理再查找，一个「确实发出去过、
// 但已过期」的 state 会退化成 ErrInvalidState，与「伪造的 state」
// 无法区分。运维看到的现象是「用户点登录晚了两分钟就一直报 CSRF 失败」，
// 而真正的原因（会话超时）被错误分类掩盖了。让 Take 能拿到已过期的会话，
// 才能给出 ErrSessionExpired 这个准确的诊断。
func (s *MemorySessionStore) gcLocked() {
	now := s.now()
	for k, sess := range s.sessions {
		if sess.Expired(now) {
			delete(s.sessions, k)
		}
	}
}

// HTTPDoer 抽象 HTTP 客户端（测试注入假 IdP）。
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// ServiceConfig 是 SSO 服务的构造参数。
type ServiceConfig struct {
	Config   Config
	JWKS     *JWKSClient
	Sessions SessionStore
	Users    UserDirectory
	Client   HTTPDoer
	// Binding 是账号绑定策略；空值按 BindingStrict（最保守）处理。
	Binding BindingPolicy
	Now     func() time.Time
}

// Service 是 OIDC 登录的业务门面。
type Service interface {
	// BeginAuth 生成授权 URL 与服务端会话（state / nonce / PKCE）。
	BeginAuth(ctx context.Context, req BeginRequest) (*BeginResult, error)
	// HandleCallback 处理回调：校验 state → 换码 → 校验 ID Token → 绑定账号。
	HandleCallback(ctx context.Context, req CallbackRequest) (*Account, error)
	// Configured 返回该入口是否已配置（未配置时调用方应返回 503）。
	Configured() bool
	// Provider 返回提供方标识（用于审计与后台展示）。
	Provider() string
}

// BeginRequest 是发起登录的请求。
type BeginRequest struct {
	// RedirectTo 是登录成功后的前端落点（可选，仅用于回跳）。
	RedirectTo string
	// Realm / TenantID 用于把 SSO 身份关联到某个租户（Day 27 多租户联动）。
	Realm    string
	TenantID string
}

// BeginResult 是发起登录的结果。
type BeginResult struct {
	AuthURL   string    `json:"auth_url"`
	State     string    `json:"state"`
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CallbackRequest 是回调请求。
type CallbackRequest struct {
	Code  string
	State string
}

type service struct {
	cfg      Config
	jwks     *JWKSClient
	sessions SessionStore
	users    UserDirectory
	client   HTTPDoer
	binding  BindingPolicy
	now      func() time.Time
}

// NewService 创建 SSO 服务。配置不完整时返回的服务所有入口都会失败
// （errNotConfigured），这是刻意的：**未配置 ≠ 放行**。
func NewService(cfg ServiceConfig) Service {
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	sessions := cfg.Sessions
	if sessions == nil {
		sessions = NewMemorySessionStore()
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	binding := cfg.Binding
	if binding == "" {
		binding = BindingStrict
	}
	normalized, err := cfg.Config.Normalize()
	s := &service{
		sessions: sessions,
		users:    cfg.Users,
		client:   client,
		binding:  binding,
		now:      nowFn,
		jwks:     cfg.JWKS,
	}
	if err != nil {
		// 保留 err 状态：Configured() 返回 false，所有入口返回 503。
		return &unconfigured{reason: err}
	}
	s.cfg = normalized
	if s.jwks == nil {
		s.jwks = NewJWKSClient(JWKSConfig{URL: normalized.JWKSURL, Now: nowFn})
	}
	return s
}

// unconfigured 是「未配置」状态下的服务实现。
//
// 用独立类型而不是「service 上打个 bool」：这样任何未配置的入口都
// 走不到真正的校验代码，从结构上排除「配置缺失时某条路径忘了检查」。
type unconfigured struct{ reason error }

func (u *unconfigured) BeginAuth(context.Context, BeginRequest) (*BeginResult, error) {
	return nil, ErrNotConfigured
}
func (u *unconfigured) HandleCallback(context.Context, CallbackRequest) (*Account, error) {
	return nil, ErrNotConfigured
}
func (u *unconfigured) Configured() bool { return false }
func (u *unconfigured) Provider() string { return "" }

func (s *service) Configured() bool { return true }

func (s *service) Provider() string {
	if s.cfg.Realm != "" {
		return "oidc:" + s.cfg.Realm
	}
	return "oidc"
}

func (s *service) BeginAuth(_ context.Context, req BeginRequest) (*BeginResult, error) {
	state, err := randomToken(32)
	if err != nil {
		return nil, fmt.Errorf("%w: unable to generate state", ErrUnavailable)
	}
	nonce, err := randomToken(32)
	if err != nil {
		return nil, fmt.Errorf("%w: unable to generate nonce", ErrUnavailable)
	}
	// PKCE：verifier 只留在服务端，challenge 才发给 IdP。
	// 授权码被中间人截获时，没有 verifier 就换不到 token。
	verifier, err := randomToken(64)
	if err != nil {
		return nil, fmt.Errorf("%w: unable to generate pkce verifier", ErrUnavailable)
	}
	challenge := pkceChallenge(verifier)

	sess := &Session{
		ID:        "sso-sess-" + state[:16],
		State:     state,
		Nonce:     nonce,
		Verifier:  verifier,
		Realm:     req.Realm,
		TenantID:  req.TenantID,
		CreatedAt: s.now(),
		ExpiresAt: s.now().Add(DefaultSessionTTL),
	}
	if err := s.sessions.Save(context.Background(), sess); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", s.cfg.ClientID)
	params.Set("redirect_uri", s.cfg.RedirectURL)
	params.Set("scope", strings.Join(s.cfg.Scopes, " "))
	params.Set("state", state)
	// nonce 让 ID Token 与本次授权请求绑定，防止重放别人的 token。
	params.Set("nonce", nonce)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")

	return &BeginResult{
		AuthURL:   s.cfg.AuthURL + "?" + params.Encode(),
		State:     state,
		Nonce:     nonce,
		ExpiresAt: sess.ExpiresAt,
	}, nil
}

func (s *service) HandleCallback(ctx context.Context, req CallbackRequest) (*Account, error) {
	code := trimSpace(req.Code)
	if code == "" {
		return nil, fmt.Errorf("%w: authorization code is required", ErrInvalidToken)
	}

	// 1. state 必须来自服务端会话，且一次性。
	sess, err := s.sessions.Take(ctx, req.State)
	if err != nil {
		return nil, err
	}
	if sess.Expired(s.now()) {
		return nil, ErrSessionExpired
	}

	// 2. 用 code + verifier 换 token（同时做 PKCE 校验）。
	tokens, err := s.exchangeCode(ctx, code, sess.Verifier)
	if err != nil {
		return nil, err
	}
	if trimSpace(tokens.IDToken) == "" {
		// 没有 ID Token 就没有身份断言。此处绝不回落到用 access_token
		// 当身份 —— access_token 不是签给本应用的，它的 aud 也不受我们控制。
		return nil, fmt.Errorf("%w: token response has no id_token", ErrInvalidToken)
	}

	// 3. 校验 ID Token，kid 未命中时强制刷新一次 JWKS（密钥轮转）。
	claims, err := s.verifyWithRefresh(ctx, tokens.IDToken, sess.Nonce)
	if err != nil {
		return nil, err
	}

	// 4. 邮箱必须存在且已由 IdP 验证：账号绑定完全依赖邮箱。
	if claims.Email == "" {
		return nil, fmt.Errorf("%w: id token has no email", ErrInvalidToken)
	}
	if !claims.EmailVerified {
		return nil, ErrEmailUnverified
	}

	// 5. 账号绑定。
	return s.bind(ctx, claims)
}

// verifyWithRefresh 校验 ID Token；kid 未命中时强制刷新一次 JWKS 再试。
//
// 只重试一次：无限重试会让「kid 永远对不上」变成对 IdP 的拒绝服务，
// 而且真正的原因是配置错误时重试也没用。
func (s *service) verifyWithRefresh(ctx context.Context, token, nonce string) (*IDTokenClaims, error) {
	opts := VerifyOptions{
		Issuer:     s.cfg.Issuer,
		ClientID:   s.cfg.ClientID,
		Nonce:      nonce,
		AllowedAlg: s.cfg.AllowedAlgs,
		ClockSkew:  s.cfg.ClockSkew,
		Now:        s.now(),
	}
	keys, err := s.jwks.Keys(ctx, false)
	if err != nil {
		return nil, err
	}
	claims, err := VerifyIDToken(token, keys, opts)
	if err == nil {
		return claims, nil
	}
	// 只有「kid 未命中」才触发刷新：签名错误/iss 不符等情况刷新没用，
	// 反而会把配置错误掩盖成「再多试一次」。
	if !isErr(err, ErrKeyNotFound) {
		return nil, err
	}
	fresh, ferr := s.jwks.Keys(ctx, true)
	if ferr != nil {
		return nil, ferr
	}
	return VerifyIDToken(token, fresh, opts)
}

func (s *service) bind(ctx context.Context, claims *IDTokenClaims) (*Account, error) {
	if s.users == nil {
		// 没有账号目录就没有可绑定的对象。拒绝而不是返回一个无主账号。
		return nil, fmt.Errorf("%w: user directory is not configured", ErrUnavailable)
	}
	acct := &Account{
		Email:    claims.Email,
		Username: firstNonEmpty(claims.Name, claims.Email),
		Subject:  claims.Subject,
		Issuer:   claims.Issuer,
		Provider: s.Provider(),
		Groups:   claims.Groups,
	}

	userID, found, err := s.users.FindByEmail(ctx, claims.Email)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if found {
		// 已存在账号：显式关联（关联动作本身可审计），不修改账号资料。
		if err := s.users.LinkSSO(ctx, userID, claims.Issuer, claims.Subject); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		acct.LocalUserID = userID
		return acct, nil
	}

	// 邮箱不存在。是否创建取决于显式配置的策略。
	if s.binding != BindingAutoCreate {
		// strict：拒绝并明确告知冲突，让管理员显式处理。
		// 绝不静默创建（可能与对方组织的 IdP 断言撞车）或静默合并。
		return nil, ErrAccountConflict
	}
	newID, err := s.users.CreateFromSSO(ctx, claims.Email, acct.Username, claims.Issuer, claims.Subject)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	acct.LocalUserID = newID
	acct.Created = true
	return acct, nil
}

// tokenResponse 是换码端点的响应。
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

func (s *service) exchangeCode(ctx context.Context, code, verifier string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", s.cfg.RedirectURL)
	form.Set("client_id", s.cfg.ClientID)
	form.Set("code_verifier", verifier)
	if s.cfg.ClientSecret != "" {
		form.Set("client_secret", s.cfg.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		// 不回显 IdP 的响应体：里面可能含有 token 或内部错误细节。
		return nil, fmt.Errorf("%w: token endpoint returned %d", ErrUnavailable, resp.StatusCode)
	}

	var out tokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: token response is not valid JSON", ErrUnavailable)
	}
	return &out, nil
}

// ===== 工具 =====

// randomToken 生成加密安全的随机串（base64url，无填充）。
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// pkceChallenge 按 RFC 7636 计算 S256 challenge。
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// isErr 用标准库 errors.Is 做哨兵错误判定。
//
// 不走字符串比较：哨兵错误被包装（比如加上 kid 便于排障）之后字符串
// 比较会静默失效，而失效的方向恰好是「kid 未命中不再触发 JWKS 刷新」。
func isErr(err, target error) bool {
	return errors.Is(err, target)
}
