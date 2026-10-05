package sso

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// ===== 测试夹具：真实的 RSA 密钥与自制 IdP =====

// testKey 是一把用于签测试令牌的 RSA 私钥。
type testKey struct {
	kid string
	key *rsa.PrivateKey
}

func newTestKey(t *testing.T, kid string) *testKey {
	t.Helper()
	// 2048 位：测试跑得动，且与生产算法一致（用 512 位会掩盖
	// 一些与密钥长度相关的边界问题）。
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	return &testKey{kid: kid, key: priv}
}

// jwk 导出公钥的 JWK 表示。
func (k *testKey) jwk() JWK {
	pub := k.key.Public().(*rsa.PublicKey)
	return JWK{
		Kty: "RSA",
		Kid: k.kid,
		Use: "sig",
		Alg: "RS256",
		N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

// sign 签一条 JWS（RS256）。
func (k *testKey) sign(t *testing.T, claims map[string]interface{}, alg string) string {
	t.Helper()
	if alg == "" {
		alg = "RS256"
	}
	header := map[string]interface{}{"alg": alg, "typ": "JWT"}
	if k.kid != "" {
		header["kid"] = k.kid
	}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signingIn := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)

	var sig []byte
	switch alg {
	case "RS256":
		sum := sha256.Sum256([]byte(signingIn))
		var err error
		sig, err = rsa.SignPKCS1v15(rand.Reader, k.key, cryptoSHA256, sum[:])
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
	case "none":
		sig = nil
	default:
		t.Fatalf("unsupported test alg %q", alg)
	}
	return signingIn + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func baseClaims(issuer, audience, nonce string, now time.Time) map[string]interface{} {
	c := map[string]interface{}{
		"iss":            issuer,
		"aud":            audience,
		"sub":            "idp-user-42",
		"email":          "alice@corp.example",
		"email_verified": true,
		"name":           "Alice",
		"iat":            now.Unix(),
		"exp":            now.Add(10 * time.Minute).Unix(),
	}
	if nonce != "" {
		c["nonce"] = nonce
	}
	return c
}

// fakeDirectory 是 UserDirectory 的可控实现。
type fakeDirectory struct {
	mu         sync.Mutex
	byEmail    map[string]string
	created    int
	links      int
	findErr    error
	createErr  error
	linkErr    error
	lastCreate struct{ email, username, issuer, subject string }
}

func newFakeDirectory() *fakeDirectory {
	return &fakeDirectory{byEmail: map[string]string{}}
}

func (d *fakeDirectory) FindByEmail(_ context.Context, email string) (string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.findErr != nil {
		return "", false, d.findErr
	}
	id, ok := d.byEmail[email]
	return id, ok, nil
}

func (d *fakeDirectory) CreateFromSSO(_ context.Context, email, username, issuer, subject string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.createErr != nil {
		return "", d.createErr
	}
	d.created++
	d.lastCreate = struct{ email, username, issuer, subject string }{email, username, issuer, subject}
	id := fmt.Sprintf("local-%d", d.created)
	d.byEmail[email] = id
	return id, nil
}

func (d *fakeDirectory) LinkSSO(_ context.Context, userID, issuer, subject string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.linkErr != nil {
		return d.linkErr
	}
	d.links++
	return nil
}

// fakeIdP 是一个最小的 OIDC 提供方：token 端点签发指定 claims 的 ID Token。
type fakeIdP struct {
	server    *httptest.Server
	key       *testKey
	mu        sync.Mutex
	idToken   string
	failToken bool
	reqCount  int
	lastForm  url.Values
}

func newFakeIdP(t *testing.T, key *testKey) *fakeIdP {
	idp := &fakeIdP{key: key}
	idp.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idp.mu.Lock()
		defer idp.mu.Unlock()
		idp.reqCount++
		_ = r.ParseForm()
		idp.lastForm = r.Form
		if idp.failToken {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "at-" + fmt.Sprint(idp.reqCount),
			"id_token":     idp.idToken,
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	t.Cleanup(idp.server.Close)
	return idp
}

func (idp *fakeIdP) setIDToken(tok string) {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	idp.idToken = tok
}

func (idp *fakeIdP) forms() url.Values {
	idp.mu.Lock()
	defer idp.mu.Unlock()
	cp := url.Values{}
	for k, v := range idp.lastForm {
		cp[k] = append([]string(nil), v...)
	}
	return cp
}

// newTestService 装配一个可用的 SSO 服务（含密钥集与假 IdP）。
func newTestService(t *testing.T, key *testKey, dir UserDirectory, binding BindingPolicy) (Service, *fakeIdP, *JWKSClient) {
	t.Helper()
	idp := newFakeIdP(t, key)
	jwks := NewJWKSClient(JWKSConfig{URL: idp.server.URL + "/jwks", Client: idp.server.Client()})
	jwks.Prime(&KeySet{Keys: []JWK{key.jwk()}})

	svc := NewService(ServiceConfig{
		Config: Config{
			Issuer:      "https://idp.corp.example",
			ClientID:    "agentbot-client",
			RedirectURL: "https://agentbot.example/api/v1/sso/callback",
			AuthURL:     idp.server.URL + "/authorize",
			TokenURL:    idp.server.URL + "/token",
			JWKSURL:     idp.server.URL + "/jwks",
			ClockSkew:   time.Minute,
		},
		JWKS:     jwks,
		Sessions: NewMemorySessionStore(),
		Users:    dir,
		Client:   idp.server.Client(),
		Binding:  binding,
	})
	return svc, idp, jwks
}

// beginAndCallback 走一次完整流程：发起登录 → 构造 ID Token → 回调。
func beginAndCallback(t *testing.T, svc Service, idp *fakeIdP, key *testKey, mutate func(map[string]interface{}), tokenKey *testKey, alg string) (*Account, error) {
	t.Helper()
	begin, err := svc.BeginAuth(context.Background(), BeginRequest{})
	if err != nil {
		t.Fatalf("BeginAuth: %v", err)
	}
	k := key
	if tokenKey != nil {
		k = tokenKey
	}
	claims := baseClaims("https://idp.corp.example", "agentbot-client", begin.Nonce, time.Now().UTC())
	if mutate != nil {
		mutate(claims)
	}
	idp.setIDToken(k.sign(t, claims, alg))
	return svc.HandleCallback(context.Background(), CallbackRequest{Code: "auth-code-1", State: begin.State})
}

// ===== 正常路径 =====

func TestHappyPathBindsExistingAccount(t *testing.T) {
	key := newTestKey(t, "kid-1")
	dir := newFakeDirectory()
	dir.byEmail["alice@corp.example"] = "user-1"
	svc, idp, _ := newTestService(t, key, dir, BindingStrict)

	acct, err := beginAndCallback(t, svc, idp, key, nil, nil, "")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if acct.LocalUserID != "user-1" {
		t.Fatalf("应绑定到已有账号，得到 %q", acct.LocalUserID)
	}
	if acct.Created {
		t.Fatal("已有账号不应被标记为新建")
	}
	if dir.links != 1 {
		t.Fatalf("应显式关联 SSO 身份，links=%d", dir.links)
	}
	if dir.created != 0 {
		t.Fatal("strict 策略下不得创建账号")
	}
}

func TestAutoCreatePolicyCreatesAccount(t *testing.T) {
	key := newTestKey(t, "kid-1")
	dir := newFakeDirectory()
	svc, idp, _ := newTestService(t, key, dir, BindingAutoCreate)

	acct, err := beginAndCallback(t, svc, idp, key, nil, nil, "")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if !acct.Created || acct.LocalUserID == "" {
		t.Fatalf("auto_create 策略下应创建账号: %+v", acct)
	}
	if dir.created != 1 {
		t.Fatalf("应恰好创建 1 个账号，得到 %d", dir.created)
	}
	if dir.lastCreate.username != "Alice" {
		t.Fatalf("用户名应取 name 声明，得到 %q", dir.lastCreate.username)
	}
}

func TestStrictPolicyRejectsUnknownEmail(t *testing.T) {
	key := newTestKey(t, "kid-1")
	dir := newFakeDirectory()
	svc, idp, _ := newTestService(t, key, dir, BindingStrict)

	// 这是「邮箱冲突策略必须显式」的核心断言：strict 下未知邮箱必须拒绝，
	// 绝不能静默创建一个可能与对方组织撞车的账号。
	_, err := beginAndCallback(t, svc, idp, key, nil, nil, "")
	if err == nil {
		t.Fatal("strict 策略下未知邮箱必须拒绝")
	}
	if !isErr(err, ErrAccountConflict) {
		t.Fatalf("应返回 ErrAccountConflict，得到 %v", err)
	}
	if dir.created != 0 {
		t.Fatal("strict 策略下绝不能创建账号")
	}
}

// ===== ID Token 校验链：每一关都必须单独可证伪 =====

func TestRejectsWrongIssuer(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, idp, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	_, err := beginAndCallback(t, svc, idp, key, func(c map[string]interface{}) {
		c["iss"] = "https://evil.example"
	}, nil, "")
	if !isErr(err, ErrInvalidToken) {
		t.Fatalf("iss 不匹配必须拒绝，得到 %v", err)
	}
}

func TestRejectsWrongAudience(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, idp, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	// 这一条防的是「拿发给别的应用的 ID Token 来登录」。
	_, err := beginAndCallback(t, svc, idp, key, func(c map[string]interface{}) {
		c["aud"] = "some-other-app"
	}, nil, "")
	if !isErr(err, ErrInvalidToken) {
		t.Fatalf("aud 不含本客户端必须拒绝，得到 %v", err)
	}
}

func TestAcceptsAudienceArray(t *testing.T) {
	key := newTestKey(t, "kid-1")
	dir := newFakeDirectory()
	svc, idp, _ := newTestService(t, key, dir, BindingAutoCreate)
	// aud 数组写法：只支持字符串会让「IdP 换了个写法」变成全员登录失败。
	acct, err := beginAndCallback(t, svc, idp, key, func(c map[string]interface{}) {
		c["aud"] = []string{"other", "agentbot-client"}
	}, nil, "")
	if err != nil {
		t.Fatalf("aud 数组必须被支持: %v", err)
	}
	if acct == nil {
		t.Fatal("应返回账号")
	}
}

func TestRejectsExpiredToken(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, idp, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	past := time.Now().UTC().Add(-2 * time.Hour)
	_, err := beginAndCallback(t, svc, idp, key, func(c map[string]interface{}) {
		// 注意 skew 是 1 分钟，这里过期 2 小时，远超出容差。
		c["iat"] = past.Unix()
		c["exp"] = past.Add(time.Minute).Unix()
	}, nil, "")
	if !isErr(err, ErrInvalidToken) {
		t.Fatalf("过期 token 必须拒绝，得到 %v", err)
	}
}

func TestRejectsFutureIssuedToken(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, idp, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	future := time.Now().UTC().Add(2 * time.Hour)
	_, err := beginAndCallback(t, svc, idp, key, func(c map[string]interface{}) {
		c["iat"] = future.Unix()
		c["exp"] = future.Add(time.Hour).Unix()
	}, nil, "")
	if !isErr(err, ErrInvalidToken) {
		t.Fatalf("iat 在未来必须拒绝，得到 %v", err)
	}
}

func TestRejectsNonceMismatch(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, idp, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	// 重放场景：token 里的 nonce 与本次会话不一致。
	_, err := beginAndCallback(t, svc, idp, key, func(c map[string]interface{}) {
		c["nonce"] = "some-other-nonce"
	}, nil, "")
	if !isErr(err, ErrInvalidToken) {
		t.Fatalf("nonce 不匹配必须拒绝，得到 %v", err)
	}
}

func TestRejectsMissingNonceWhenSessionHasOne(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, idp, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	// 关键方向：服务端发了 nonce，token 必须回带。若「没带就跳过校验」，
	// 攻击者只要拿一个不含 nonce 的旧 token 就能重放。
	_, err := beginAndCallback(t, svc, idp, key, func(c map[string]interface{}) {
		delete(c, "nonce")
	}, nil, "")
	if !isErr(err, ErrInvalidToken) {
		t.Fatalf("会话有 nonce 时 token 缺失必须拒绝，得到 %v", err)
	}
}

func TestRejectsAlgNone(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, idp, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	// alg=none：签名段清空 + header 声明 none，是最经典的降级攻击。
	_, err := beginAndCallback(t, svc, idp, key, nil, nil, "none")
	if err == nil {
		t.Fatal("alg=none 必须被拒绝")
	}
	if !strings.Contains(err.Error(), "alg") {
		t.Fatalf("应因算法被拒，得到 %v", err)
	}
}

func TestRejectsWrongSigningKey(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, idp, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	// 另一把密钥（同 kid）：验签必须失败，否则等于不校验签名。
	rogue := newTestKey(t, "kid-1")
	_, err := beginAndCallback(t, svc, idp, key, nil, rogue, "")
	if !isErr(err, ErrInvalidToken) {
		t.Fatalf("签名不匹配必须拒绝，得到 %v", err)
	}
}

func TestRejectsUnverifiedEmail(t *testing.T) {
	key := newTestKey(t, "kid-1")
	dir := newFakeDirectory()
	dir.byEmail["alice@corp.example"] = "user-1"
	svc, idp, _ := newTestService(t, key, dir, BindingAutoCreate)
	// 未验证邮箱 + 绑定已有账号 = 账号接管。必须拒绝。
	_, err := beginAndCallback(t, svc, idp, key, func(c map[string]interface{}) {
		c["email_verified"] = false
	}, nil, "")
	if !isErr(err, ErrEmailUnverified) {
		t.Fatalf("未验证邮箱必须拒绝，得到 %v", err)
	}
}

func TestRejectsMissingSub(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, idp, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	_, err := beginAndCallback(t, svc, idp, key, func(c map[string]interface{}) {
		delete(c, "sub")
	}, nil, "")
	if !isErr(err, ErrInvalidToken) {
		t.Fatalf("缺少 sub 必须拒绝，得到 %v", err)
	}
}

// ===== state / 会话 =====

func TestRejectsUnknownState(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, idp, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	_, err := beginAndCallback(t, svc, idp, key, nil, nil, "")
	if err != nil {
		t.Fatalf("首次回调应成功: %v", err)
	}
	// 同一个 state 再用一次：Take 是一次性的，这里应失败。
	claims := baseClaims("https://idp.corp.example", "agentbot-client", "n", time.Now().UTC())
	idp.setIDToken(key.sign(t, claims, ""))
	if _, err := svc.HandleCallback(context.Background(), CallbackRequest{Code: "c", State: "forged-state"}); !isErr(err, ErrInvalidState) {
		t.Fatalf("未知 state 必须拒绝，得到 %v", err)
	}
}

func TestStateIsSingleUse(t *testing.T) {
	key := newTestKey(t, "kid-1")
	dir := newFakeDirectory()
	dir.byEmail["alice@corp.example"] = "user-1"
	svc, idp, _ := newTestService(t, key, dir, BindingStrict)

	begin, err := svc.BeginAuth(context.Background(), BeginRequest{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	claims := baseClaims("https://idp.corp.example", "agentbot-client", begin.Nonce, time.Now().UTC())
	idp.setIDToken(key.sign(t, claims, ""))

	if _, err := svc.HandleCallback(context.Background(), CallbackRequest{Code: "c", State: begin.State}); err != nil {
		t.Fatalf("首次回调应成功: %v", err)
	}
	// 重放同一 state：必须失败，否则截获的 state 可被反复利用。
	if _, err := svc.HandleCallback(context.Background(), CallbackRequest{Code: "c", State: begin.State}); !isErr(err, ErrInvalidState) {
		t.Fatalf("state 必须一次性，得到 %v", err)
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	key := newTestKey(t, "kid-1")
	dir := newFakeDirectory()
	dir.byEmail["alice@corp.example"] = "u1"

	idp := newFakeIdP(t, key)
	store := NewMemorySessionStore()
	now := time.Now().UTC()
	store.SetClock(func() time.Time { return now })

	// 服务自身的时钟也必须注入：会话过期是**服务层**的判断
	// （sess.Expired(s.now())），只改存储的时钟不会让这一关生效 ——
	// 这正是本测试第一版没抓到任何东西的原因。
	svc := NewService(ServiceConfig{
		Now: func() time.Time { return now },
		Config: Config{
			Issuer:      "https://idp.corp.example",
			ClientID:    "agentbot-client",
			RedirectURL: "https://agentbot.example/cb",
			AuthURL:     idp.server.URL + "/authorize",
			TokenURL:    idp.server.URL + "/token",
			JWKSURL:     idp.server.URL + "/jwks",
		},
		JWKS:     primeJWKS(t, key),
		Sessions: store,
		Users:    dir,
		Client:   idp.server.Client(),
		Binding:  BindingStrict,
	})
	begin, err := svc.BeginAuth(context.Background(), BeginRequest{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	claims := baseClaims("https://idp.corp.example", "agentbot-client", begin.Nonce, now)
	idp.setIDToken(key.sign(t, claims, ""))

	// 时间前进 11 分钟（会话 TTL 是 10 分钟）。
	now = now.Add(11 * time.Minute)

	// 过期会话必须报出**准确**的错误（ErrSessionExpired），
	// 而不是退化成 ErrInvalidState —— 后者会让「会话超时」看起来像
	// 「CSRF state 被伪造」，用户与运维都会查错方向。
	if _, err := svc.HandleCallback(context.Background(), CallbackRequest{Code: "c", State: begin.State}); !isErr(err, ErrSessionExpired) {
		t.Fatalf("过期会话必须返回 ErrSessionExpired，得到 %v", err)
	}
	// 且过期会话不得消耗掉换码请求（IdP 不该被调用）。
	if n := idp.reqCount; n != 0 {
		t.Fatalf("过期会话不应发起换码请求，IdP 被调用 %d 次", n)
	}
}

// ===== JWKS 缓存与密钥轮转 =====

func TestJWKSRefreshOnKidMiss(t *testing.T) {
	// 这是本模块最关键的一条：IdP 轮转签名密钥后，旧缓存里没有新 kid。
	// 不做强制刷新 = 全员登录失败；回落用旧密钥 = 继续信任已废弃密钥。
	oldKey := newTestKey(t, "kid-old")
	newKey := newTestKey(t, "kid-new")

	idp := newFakeIdP(t, newKey)
	var rotate sync.Mutex
	rotated := false
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rotate.Lock()
		defer rotate.Unlock()
		keys := []JWK{oldKey.jwk()}
		if rotated {
			keys = []JWK{newKey.jwk()}
		}
		_ = json.NewEncoder(w).Encode(KeySet{Keys: keys})
	}))
	t.Cleanup(jwksSrv.Close)

	jwks := NewJWKSClient(JWKSConfig{URL: jwksSrv.URL, Client: jwksSrv.Client()})
	jwks.Prime(&KeySet{Keys: []JWK{oldKey.jwk()}})

	dir := newFakeDirectory()
	svc := NewService(ServiceConfig{
		Config: Config{
			Issuer:      "https://idp.corp.example",
			ClientID:    "agentbot-client",
			RedirectURL: "https://agentbot.example/cb",
			AuthURL:     idp.server.URL + "/authorize",
			TokenURL:    idp.server.URL + "/token",
			JWKSURL:     jwksSrv.URL,
		},
		JWKS:     jwks,
		Sessions: NewMemorySessionStore(),
		Users:    dir,
		Client:   idp.server.Client(),
		Binding:  BindingAutoCreate,
	})

	begin, err := svc.BeginAuth(context.Background(), BeginRequest{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	claims := baseClaims("https://idp.corp.example", "agentbot-client", begin.Nonce, time.Now().UTC())
	idp.setIDToken(newKey.sign(t, claims, ""))

	// 第一把是旧缓存：kid 未命中。此时把 IdP 侧也切到新密钥（模拟轮转完成）。
	rotate.Lock()
	rotated = true
	rotate.Unlock()

	acct, err := svc.HandleCallback(context.Background(), CallbackRequest{Code: "c", State: begin.State})
	if err != nil {
		t.Fatalf("密钥轮转后必须能自动恢复（kid 未命中 → 强制刷新）: %v", err)
	}
	if acct.LocalUserID == "" {
		t.Fatal("应返回账号")
	}
	if n := jwks.RefreshCount(); n != 1 {
		t.Fatalf("应恰好强制刷新 1 次，得到 %d", n)
	}
}

func TestJWKSRefreshNotTriggeredForOtherErrors(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, idp, jwks := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	// iss 不匹配：刷新 JWKS 没用，不应该浪费一次 IdP 请求。
	_, err := beginAndCallback(t, svc, idp, key, func(c map[string]interface{}) {
		c["iss"] = "https://evil.example"
	}, nil, "")
	if err == nil {
		t.Fatal("应拒绝")
	}
	if n := jwks.RefreshCount(); n != 0 {
		t.Fatalf("非 kid 未命中不应触发刷新，得到 %d 次", n)
	}
}

func TestJWKSFetchFailureIsUnavailable(t *testing.T) {
	// 指向一个必然失败的端点。
	jwks := NewJWKSClient(JWKSConfig{URL: "http://127.0.0.1:1/jwks"})
	if _, err := jwks.Keys(context.Background(), true); !isErr(err, ErrUnavailable) {
		t.Fatalf("JWKS 拉取失败必须返回 ErrUnavailable（fail-closed），得到 %v", err)
	}
}

func TestJWKSEmptyKeySetRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	t.Cleanup(srv.Close)
	jwks := NewJWKSClient(JWKSConfig{URL: srv.URL})
	// 空密钥集绝不能当成「暂时没有密钥」放行 —— 那等于关掉签名校验。
	if _, err := jwks.Keys(context.Background(), true); !isErr(err, ErrUnavailable) {
		t.Fatalf("空密钥集必须拒绝，得到 %v", err)
	}
}

func TestKeySetFindByAlgRequiresKidWhenAmbiguous(t *testing.T) {
	k1 := newTestKey(t, "k1")
	k2 := newTestKey(t, "k2")
	ks := &KeySet{Keys: []JWK{k1.jwk(), k2.jwk()}}
	// 两把密钥却没有 kid：必须拒绝，而不是随便挑一把验。
	if _, err := ks.FindByAlg("", "RS256"); err == nil {
		t.Fatal("kid 缺失且密钥集不唯一时必须拒绝")
	}
	if _, err := ks.FindByAlg("k2", "RS256"); err != nil {
		t.Fatalf("kid 命中应返回公钥: %v", err)
	}
}

func TestKeySetSkipsEncryptionKeys(t *testing.T) {
	key := newTestKey(t, "enc-key")
	jwk := key.jwk()
	jwk.Use = "enc"
	ks := &KeySet{Keys: []JWK{jwk}}
	// use=enc 是加密密钥，不能拿来验签。
	if _, err := ks.FindByAlg("enc-key", "RS256"); err == nil {
		t.Fatal("加密用途的密钥不得用于验签")
	}
}

// ===== 未配置 =====

func TestUnconfiguredServiceFailsClosed(t *testing.T) {
	// 一个字段都不填：必须进入 unconfigured 状态。
	svc := NewService(ServiceConfig{})

	if svc.Configured() {
		t.Fatal("空配置必须被判定为未配置")
	}
	if _, err := svc.BeginAuth(context.Background(), BeginRequest{}); !isErr(err, ErrNotConfigured) {
		t.Fatalf("未配置时 BeginAuth 必须返回 ErrNotConfigured，得到 %v", err)
	}
	if _, err := svc.HandleCallback(context.Background(), CallbackRequest{Code: "c", State: "s"}); !isErr(err, ErrNotConfigured) {
		t.Fatalf("未配置时 HandleCallback 必须返回 ErrNotConfigured，得到 %v", err)
	}
}

func TestConfigRequiresOpenIDScope(t *testing.T) {
	_, err := Config{
		Issuer:      "https://idp.example",
		ClientID:    "c",
		RedirectURL: "https://app/cb",
		Scopes:      []string{"profile", "email"},
	}.Normalize()
	// 缺 openid 拿不到 ID Token：这是配置错误，必须拒绝而不是静默补上。
	if !isErr(err, ErrNotConfigured) {
		t.Fatalf("缺少 openid scope 必须被拒绝，得到 %v", err)
	}
}

// ===== Handler =====

func TestHandlerStatusIsPublicAndReportsUnconfigured(t *testing.T) {
	h := NewHandler(NewService(ServiceConfig{}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sso/status", nil)
	h.handleStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status 应返回 200，得到 %d", rec.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body["configured"] != false {
		t.Fatalf("未配置时必须如实上报 configured=false，得到 %v", body["configured"])
	}
}

func TestHandlerAuthorizeReturns503WhenUnconfigured(t *testing.T) {
	h := NewHandler(NewService(ServiceConfig{}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sso/authorize", nil)
	h.handleAuthorize(rec, req)
	// 未配置 → 503。绝不能返回一个指向空地址的授权 URL。
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置时必须 503，得到 %d", rec.Code)
	}
}

func TestHandlerCallbackReturns503WhenUnconfigured(t *testing.T) {
	h := NewHandler(NewService(ServiceConfig{}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sso/callback?code=x&state=y", nil)
	h.handleCallback(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置时必须 503，得到 %d", rec.Code)
	}
}

func TestHandlerCallbackRejectsMissingCode(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, _, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	h := NewHandler(svc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sso/callback?state=s", nil)
	h.handleCallback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 code 应 400，得到 %d", rec.Code)
	}
}

func TestHandlerCallbackSurfacesIdPError(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, _, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	h := NewHandler(svc)
	rec := httptest.NewRecorder()
	// IdP 拒绝授权时会带 error 参数回来，应给出明确反馈而不是含糊的 500。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sso/callback?error=access_denied", nil)
	h.handleCallback(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("IdP 拒绝应 401，得到 %d", rec.Code)
	}
}

func TestHandlerAuthorizeProducesPKCEAndNonce(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, _, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	h := NewHandler(svc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sso/authorize", nil)
	h.handleAuthorize(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize 应 200，得到 %d", rec.Code)
	}
	var body BeginResult
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	u, err := url.Parse(body.AuthURL)
	if err != nil {
		t.Fatalf("auth url: %v", err)
	}
	q := u.Query()
	for _, p := range []string{"state", "nonce", "code_challenge", "code_challenge_method", "response_type", "client_id"} {
		if q.Get(p) == "" {
			t.Fatalf("授权 URL 缺少参数 %q: %s", p, body.AuthURL)
		}
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("PKCE 必须用 S256，得到 %q", q.Get("code_challenge_method"))
	}
	if q.Get("state") != body.State || q.Get("nonce") != body.Nonce {
		t.Fatal("返回的 state/nonce 必须与授权 URL 中一致")
	}
}

// ===== 换码 =====

func TestExchangeCodeSendsPKCEVerifierAndSecret(t *testing.T) {
	key := newTestKey(t, "kid-1")
	dir := newFakeDirectory()
	dir.byEmail["alice@corp.example"] = "u1"

	idp := newFakeIdP(t, key)
	svc := NewService(ServiceConfig{
		Config: Config{
			Issuer:       "https://idp.corp.example",
			ClientID:     "agentbot-client",
			ClientSecret: "s3cret-value",
			RedirectURL:  "https://agentbot.example/cb",
			AuthURL:      idp.server.URL + "/authorize",
			TokenURL:     idp.server.URL + "/token",
			JWKSURL:      idp.server.URL + "/jwks",
		},
		JWKS:     primeJWKS(t, key),
		Sessions: NewMemorySessionStore(),
		Users:    dir,
		Client:   idp.server.Client(),
		Binding:  BindingStrict,
	})

	begin, err := svc.BeginAuth(context.Background(), BeginRequest{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	claims := baseClaims("https://idp.corp.example", "agentbot-client", begin.Nonce, time.Now().UTC())
	idp.setIDToken(key.sign(t, claims, ""))
	if _, err := svc.HandleCallback(context.Background(), CallbackRequest{Code: "code-xyz", State: begin.State}); err != nil {
		t.Fatalf("callback: %v", err)
	}

	form := idp.forms()
	if form.Get("code") != "code-xyz" {
		t.Fatalf("应把授权码传给 token 端点，得到 %q", form.Get("code"))
	}
	if form.Get("code_verifier") == "" {
		t.Fatal("必须发送 PKCE code_verifier（否则 PKCE 形同虚设）")
	}
	if form.Get("client_secret") != "s3cret-value" {
		t.Fatal("配置了 client secret 时必须发送（否则机密客户端的换码会被拒）")
	}
	if got := form.Get("grant_type"); got != "authorization_code" {
		t.Fatalf("grant_type 错误: %q", got)
	}
	// state 与 verifier 不得出现在发给 IdP 的换码请求里（state 只走回调）。
	if form.Get("state") != "" {
		t.Fatal("换码请求不应包含 state")
	}
}

func TestExchangeCodeRejectedByIdP(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, idp, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	begin, err := svc.BeginAuth(context.Background(), BeginRequest{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	idp.mu.Lock()
	idp.failToken = true
	idp.mu.Unlock()

	// IdP 拒绝换码 → fail-closed，不得回落到其它身份来源。
	if _, err := svc.HandleCallback(context.Background(), CallbackRequest{Code: "c", State: begin.State}); !isErr(err, ErrUnavailable) {
		t.Fatalf("换码失败必须返回 ErrUnavailable，得到 %v", err)
	}
}

func TestMissingIDTokenRejected(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, idp, _ := newTestService(t, key, newFakeDirectory(), BindingAutoCreate)
	begin, err := svc.BeginAuth(context.Background(), BeginRequest{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// 只返回 access_token：绝不能拿它当身份断言（它的 aud 不受我们控制）。
	idp.setIDToken("")
	if _, err := svc.HandleCallback(context.Background(), CallbackRequest{Code: "c", State: begin.State}); !isErr(err, ErrInvalidToken) {
		t.Fatalf("缺少 id_token 必须拒绝，得到 %v", err)
	}
}

// ===== 目录故障 =====

func TestDirectoryReadErrorIsNotAbsence(t *testing.T) {
	key := newTestKey(t, "kid-1")
	dir := newFakeDirectory()
	dir.findErr = fmt.Errorf("db is down")
	svc, idp, _ := newTestService(t, key, dir, BindingAutoCreate)

	// 目录读取失败绝不能被当成「邮箱不存在」—— 那会给已存在的邮箱
	// 创建一个重复账号。必须失败。
	_, err := beginAndCallback(t, svc, idp, key, nil, nil, "")
	if err == nil {
		t.Fatal("目录故障必须让登录失败")
	}
	if !isErr(err, ErrUnavailable) {
		t.Fatalf("应返回 ErrUnavailable，得到 %v", err)
	}
}

func TestDirectoryUnavailableWithoutUsers(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc, idp, _ := newTestService(t, key, nil, BindingAutoCreate)
	_, err := beginAndCallback(t, svc, idp, key, nil, nil, "")
	if !isErr(err, ErrUnavailable) {
		t.Fatalf("没有账号目录时必须拒绝，得到 %v", err)
	}
}

func TestEmailIsLowercasedBeforeBinding(t *testing.T) {
	key := newTestKey(t, "kid-1")
	dir := newFakeDirectory()
	dir.byEmail["alice@corp.example"] = "u1"
	svc, idp, _ := newTestService(t, key, dir, BindingStrict)

	// IdP 返回大小写混写的邮箱：必须归一到小写再查，
	// 否则「Alice@Corp.Example」会被当成新账号。
	acct, err := beginAndCallback(t, svc, idp, key, func(c map[string]interface{}) {
		c["email"] = "ALICE@Corp.Example"
	}, nil, "")
	if err != nil {
		t.Fatalf("邮箱大小写不应影响绑定: %v", err)
	}
	if acct.LocalUserID != "u1" || acct.Email != "alice@corp.example" {
		t.Fatalf("邮箱应先归一化，得到 %+v", acct)
	}
}

func TestProviderNameReflectsRealm(t *testing.T) {
	key := newTestKey(t, "kid-1")
	svc := NewService(ServiceConfig{
		Config: Config{
			Issuer:      "https://idp.example",
			ClientID:    "c",
			RedirectURL: "https://app/cb",
			Realm:       "acme",
		},
		JWKS: primeJWKS(t, key),
	})
	if got := svc.Provider(); got != "oidc:acme" {
		t.Fatalf("provider 名应带上 realm，得到 %q", got)
	}
}

// ===== 辅助 =====

func testConfig() Config {
	return Config{
		Issuer:      "https://idp.corp.example",
		ClientID:    "agentbot-client",
		RedirectURL: "https://agentbot.example/cb",
		AuthURL:     "https://idp.corp.example/authorize",
		TokenURL:    "https://idp.corp.example/token",
		JWKSURL:     "https://idp.corp.example/jwks",
	}
}

func primeJWKS(t *testing.T, key *testKey) *JWKSClient {
	t.Helper()
	j := NewJWKSClient(JWKSConfig{URL: "http://unused.invalid/jwks"})
	j.Prime(&KeySet{Keys: []JWK{key.jwk()}})
	return j
}
