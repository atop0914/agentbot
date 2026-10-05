// Package sso 实现企业单点登录（OIDC 授权码流 + ID Token 校验）。
//
// 为什么 OIDC 校验必须自己写清楚，不能「解析一下 payload 就信」：
//
//	2025-2026 年多次身份接入事故的根因都是**校验链少了一环**：
//	只看 `exp` 不看 `aud`（把发给别的应用的 token 拿来登录）、
//	只看签名不看 `iss`（接受任意 IdP 签发的 token）、
//	不校验 `nonce`（重放一个截获的 ID Token 即可登录）、
//	JWKS 永久缓存（IdP 轮转密钥后所有人登录失败，或者更糟 —— 缓存里
//	留着已废弃的密钥继续信任）。
//
//	因此本包把「一条 ID Token 到底要过多少关」显式写出来：
//
//	  1. JWS 结构合法（三段、alg 属于允许清单）；
//	  2. 签名能用 JWKS 中对应 kid 的公钥验证通过；
//	  3. iss 精确等于配置的 Issuer；
//	  4. aud 包含配置的 ClientID（数组与字符串两种写法都支持）；
//	  5. exp 未过期、iat 不在未来（允许小量时钟偏移）、nbf 未生效则拒绝；
//	  6. nonce 与本次授权请求发起时下发的一致（防重放）；
//	  7. sub 非空（否则无法定位账号）。
//
// 设计约束：**零新增第三方依赖**，只依赖标准库 crypto/rsa、crypto/ecdsa、
// encoding/json 与 net/http。这是刻意的：身份校验代码是信任链的根，
// 它的依赖面就是攻击面，能自己写清楚的部分不引入外部实现。
//
// 未配置 SSO 时所有入口返回 503 而**不是**静默放行 —— 默认拒绝是本项目
// 从 authz 包延续下来的基线。
package sso

import (
	"errors"
	"time"
)

// Config 是一个 OIDC 提供方的配置。
type Config struct {
	// Issuer 是 IdP 的 issuer 标识，必须与 ID Token 的 iss 精确相等。
	Issuer string `json:"issuer"`
	// ClientID / ClientSecret 是本应用在 IdP 注册的凭据。
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// RedirectURL 是回调地址，必须与 IdP 侧登记的一致。
	RedirectURL string `json:"redirect_url"`
	// Scopes 默认 openid profile email；openid 是强制的。
	Scopes []string `json:"scopes,omitempty"`
	// JWKSURL 是公钥集地址；为空时由 Issuer + 标准路径推导。
	JWKSURL string `json:"jwks_url,omitempty"`
	// AuthURL / TokenURL 授权与换码端点；为空时由 Issuer + 标准路径推导。
	AuthURL  string `json:"auth_url,omitempty"`
	TokenURL string `json:"token_url,omitempty"`
	// AllowedAlgs 是允许的 JWS 签名算法；为空时默认 RS256。
	//
	// 必须显式限制算法：`alg: none` 与 HMAC 混淆攻击（用公钥当 HMAC 密钥）
	// 都是靠「接受任意 alg」成立的。
	AllowedAlgs []string `json:"allowed_algs,omitempty"`
	// ClockSkew 允许的时钟偏移，默认 60 秒。
	ClockSkew time.Duration `json:"-"`
	// Realm 是租户域名（企业 SSO 常按域名分流），用于账号绑定与展示。
	Realm string `json:"realm,omitempty"`
}

// Normalize 补齐默认值并校验必填项。
func (c Config) Normalize() (Config, error) {
	out := c
	out.Issuer = trimSpace(out.Issuer)
	out.ClientID = trimSpace(out.ClientID)
	out.RedirectURL = trimSpace(out.RedirectURL)
	if out.Issuer == "" || out.ClientID == "" || out.RedirectURL == "" {
		return Config{}, ErrNotConfigured
	}
	if out.AuthURL == "" {
		out.AuthURL = out.Issuer + "/authorize"
	}
	if out.TokenURL == "" {
		out.TokenURL = out.Issuer + "/oauth/token"
	}
	if out.JWKSURL == "" {
		out.JWKSURL = out.Issuer + "/.well-known/jwks.json"
	}
	if len(out.Scopes) == 0 {
		out.Scopes = []string{"openid", "profile", "email"}
	} else if !containsStr(out.Scopes, "openid") {
		// 缺 openid 的请求拿不到 ID Token，属于配置错误，直接拒绝而不是
		// 静默补上：静默补会让「配置看起来生效了」但实际行为与预期不同。
		return Config{}, ErrNotConfigured
	}
	if len(out.AllowedAlgs) == 0 {
		out.AllowedAlgs = []string{"RS256"}
	}
	if out.ClockSkew <= 0 {
		out.ClockSkew = 60 * time.Second
	}
	return out, nil
}

// IDTokenClaims 是校验通过的 ID Token 中我们关心的声明。
type IDTokenClaims struct {
	Subject  string
	Issuer   string
	Audience []string
	Email    string
	// EmailVerified 是 IdP 对邮箱归属的断言。
	//
	// 关系到账号绑定的安全：未验证的邮箱可能与本地已有账号撞车，
	// 静默合并等于「注册一个同邮箱的 IdP 账号即可接管他人账号」。
	EmailVerified bool
	Name          string
	Groups        []string
	IssuedAt      time.Time
	ExpiresAt     time.Time
	Nonce         string
	Raw           map[string]interface{}
}

// JWK 是一个 JSON Web Key（只支持 RSA / EC 签名密钥）。
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// KeySet 是一组公钥，按 kid 索引。
type KeySet struct {
	Keys []JWK `json:"keys"`
}

// Session 是一次 SSO 登录流程的服务端状态。
//
// State 与 Nonce 都必须由服务端生成并存储：state 防 CSRF（回调必须带着
// 我们发出去的 state），nonce 防 ID Token 重放（token 里的 nonce 必须
// 与我们发出去的一致）。两者都**不能**由客户端提供。
type Session struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Nonce     string    `json:"nonce"`
	Verifier  string    `json:"-"` // PKCE code_verifier，绝不外泄
	Realm     string    `json:"realm,omitempty"`
	TenantID  string    `json:"tenant_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Expired 判断会话是否过期。
func (s *Session) Expired(now time.Time) bool {
	return s == nil || now.After(s.ExpiresAt)
}

// ===== 错误 =====
//
// 全部按「拒绝」语义定义：任何一条校验不通过都是 ErrInvalidToken 的子类，
// 调用方不需要（也不能）从错误里区分出「差一点就对了」的中间态。
var (
	// ErrNotConfigured 表示该 SSO 入口未配置。默认拒绝：返回 503，不放行。
	ErrNotConfigured = errors.New("sso: provider is not configured")
	// ErrUnavailable 表示 JWKS 或 IdP 不可达；fail-closed。
	ErrUnavailable = errors.New("sso: identity provider unavailable")
	// ErrInvalidToken 是全部 ID Token 校验失败的基类错误。
	ErrInvalidToken = errors.New("sso: invalid id token")
	// ErrKeyNotFound 表示 token 的 kid 不在当前 JWKS 中。
	//
	// 单独定义一个错误是因为它驱动一个**特定动作**：强制刷新一次 JWKS。
	// IdP 轮转密钥后，第一次请求必然带着旧 kid 或新 kid 落在旧缓存上，
	// 不刷新就表现为「密钥轮转后全员登录失败」—— 这正是本包要防的场景。
	ErrKeyNotFound = errors.New("sso: signing key not found in the key set")
	// ErrInvalidState 表示回调的 state 与会话不匹配（CSRF 防护）。
	ErrInvalidState = errors.New("sso: state does not match the authorization session")
	// ErrSessionExpired 表示授权会话已过期。
	ErrSessionExpired = errors.New("sso: authorization session expired")
	// ErrEmailUnverified 表示 IdP 未验证邮箱，无法安全绑定账号。
	ErrEmailUnverified = errors.New("sso: id token email is not verified")
	// ErrAccountConflict 表示邮箱与已有本地账号冲突且策略不允许自动关联。
	ErrAccountConflict = errors.New("sso: email conflicts with an existing local account")
)

// containsStr 判断字符串切片是否包含某值（大小写不敏感）。
func containsStr(list []string, want string) bool {
	for _, s := range list {
		if equalFold(s, want) {
			return true
		}
	}
	return false
}
