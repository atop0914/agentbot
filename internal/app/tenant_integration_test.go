package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/atop0914/agentbot/internal/auth"
	"github.com/atop0914/agentbot/internal/authz"
	"github.com/atop0914/agentbot/internal/tenant"
)

// 本文件验证 Day 27 的两件事在**真实路由与中间件链**上确实生效：
//
//  1. 多租户隔离：租户身份只能来自签名过的 JWT，跨租户一律 404；
//  2. SSO 入口可达且默认拒绝（未配置 → 503，绝不静默放行）。
//
// 只写单测不足以证明它们接进了系统 —— 之前踩过的坑（认证挂在授权内层
// 导致受保护路由恒 401、集合路径漏登记权限导致 403）都只在走真实
// router 时才暴露。

// grantAllPermissions 给用户授予内置 coordinator 角色（含全部相关权限）。
//
// Day 27 的端到端测试关注的是**租户隔离**而不是权限矩阵本身
// （权限矩阵在 authz 包有独立测试），所以复用系统角色，
// 让失败信号只来自租户边界。
//
// 刻意复用内置角色而不是在测试里现建一个自定义角色：
// 自定义角色的 ID 与 authz 的角色查找口径不一定一致（实测踩过
// 「unknown role: role xxx not found」），而内置角色是真实存在的路径。
func grantAllPermissions(t *testing.T, a *App, userID string) {
	t.Helper()
	subject := authz.Subject{Type: authz.SubjectUser, ID: userID}
	if _, err := a.AuthzSvc.Assign(context.Background(), subject, "role-coordinator", "test"); err != nil {
		t.Fatalf("assign coordinator role to %s: %v", userID, err)
	}
}

// issueTenantToken 为某个用户签发一张带租户声明的平台令牌。
//
// 走的是生产路径（auth.Service.IssueTokens → JWT），而不是手搓一个
// header：被验证的必须是真实签发链。
func issueTenantToken(t *testing.T, a *App, userID string, tenantID tenant.ID) string {
	t.Helper()
	pair, err := a.Auth.IssueTokens(&auth.Claims{
		UserID:   userID,
		Username: userID,
		Email:    userID + "@corp.example",
		Role:     "admin",
		TenantID: string(tenantID),
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return pair.AccessToken
}

// doAuthed 发一个带 Bearer 令牌的请求。
func doAuthed(t *testing.T, h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// seedTenantForUser 建租户并把用户加为成员。
func seedTenantForUser(t *testing.T, a *App, slug, userID string) *tenant.Tenant {
	t.Helper()
	tn, err := a.TenantSvc.Create(context.Background(), tenant.CreateRequest{
		Name: slug, Slug: slug, Plan: tenant.PlanTeam, OwnerID: userID,
	})
	if err != nil {
		t.Fatalf("create tenant %q: %v", slug, err)
	}
	return tn
}

// ===== 多租户端到端 =====

func TestE2ETenantStatusEndpointIsReachable(t *testing.T) {
	a := New()
	router := NewRouter(a)
	grantAllPermissions(t, a, "user-acme")
	tn := seedTenantForUser(t, a, "acme", "user-acme")
	token := issueTenantToken(t, a, "user-acme", tn.ID)

	// /api/v1/tenants/current 只输出 context 里的作用域，
	// 是「服务端认为我是谁」的可观测入口。
	rec := doAuthed(t, router, http.MethodGet, "/api/v1/tenants/current", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("租户自查端点应可达，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Tenant struct {
			ID string `json:"id"`
		} `json:"tenant"`
		UserID    string         `json:"user_id"`
		Resources map[string]int `json:"resources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if body.Tenant.ID != string(tn.ID) || body.UserID != "user-acme" {
		t.Fatalf("自查结果与令牌不符: %+v", body)
	}
}

func TestE2ETenantScopedBySignedClaimsNotQuery(t *testing.T) {
	a := New()
	router := NewRouter(a)
	grantAllPermissions(t, a, "alice")
	alice := seedTenantForUser(t, a, "alice-corp", "alice")
	seedTenantForUser(t, a, "bob-corp", "bob")

	aliceToken := issueTenantToken(t, a, "alice", alice.ID)

	// Alice 的令牌去查 Bob 的租户详情，并在 query 里声称自己是 Bob 的租户：
	// 租户身份必须**只**认签名声明，query 一律无效。
	rec := doAuthed(t, router, http.MethodGet,
		"/api/v1/tenants/bob-corp?tenant_id=bob-corp", aliceToken)
	if rec.Code == http.StatusOK {
		t.Fatalf("伪造 tenant_id query 不得改变租户归属，得到 200: %s", rec.Body.String())
	}
	// 422/404 都可以接受，唯一不可接受的是「看到了别人的租户」。
	if strings.Contains(rec.Body.String(), "bob-corp") {
		t.Fatalf("响应泄漏了其他租户的信息: %s", rec.Body.String())
	}
}

func TestE2ECrossTenantResourceListIs404(t *testing.T) {
	ctx := context.Background()
	a := New()
	router := NewRouter(a)
	grantAllPermissions(t, a, "alice")
	grantAllPermissions(t, a, "bob")
	alice := seedTenantForUser(t, a, "alice", "alice")
	bob := seedTenantForUser(t, a, "bob", "bob")

	// Alice 名下登记一个 Agent。
	if _, err := a.TenantSvc.Register(ctx, alice.ID, tenant.ResourceAgent, "agent-secret"); err != nil {
		t.Fatalf("register: %v", err)
	}

	bobToken := issueTenantToken(t, a, "bob", bob.ID)
	rec := doAuthed(t, router, http.MethodGet, "/api/v1/tenants/"+string(alice.ID)+"/resources", bobToken)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Bob 查 Alice 的资源清单必须 404，得到 %d: %s", rec.Code, rec.Body.String())
	}

	// Alice 自己查应当成功。
	aliceToken := issueTenantToken(t, a, "alice", alice.ID)
	rec = doAuthed(t, router, http.MethodGet, "/api/v1/tenants/"+string(alice.ID)+"/resources", aliceToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("Alice 查自己的资源清单应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Resources map[string]int `json:"resources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Resources["agent"] != 1 {
		t.Fatalf("资源清单应含 1 个 agent，得到 %+v", body.Resources)
	}
}

func TestE2EUnboundTokenGetsNoTenantScope(t *testing.T) {
	a := New()
	router := NewRouter(a)
	// 已认证但没有租户声明的令牌：中间件按「无作用域」放行，
	// 自查端点必须拒绝（而不是给一个空作用域让它看起来正常）。
	grantAllPermissions(t, a, "lonely-user")
	token := issueTenantToken(t, a, "lonely-user", "")
	rec := doAuthed(t, router, http.MethodGet, "/api/v1/tenants/current", token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无租户声明时自查端点应 401，得到 %d: %s", rec.Code, rec.Body.String())
	}
}

func TestE2ESuspendedTenantLosesAccess(t *testing.T) {
	ctx := context.Background()
	a := New()
	router := NewRouter(a)
	grantAllPermissions(t, a, "user-acme")
	tn := seedTenantForUser(t, a, "acme", "user-acme")
	token := issueTenantToken(t, a, "user-acme", tn.ID)

	// 暂停前可用。
	if rec := doAuthed(t, router, http.MethodGet, "/api/v1/tenants/current", token); rec.Code != http.StatusOK {
		t.Fatalf("暂停前应可访问，得到 %d", rec.Code)
	}

	if _, err := a.TenantSvc.UpdateStatus(ctx, tn.ID, tenant.StatusSuspended); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	// 暂停后同一张令牌立刻失效：租户状态是**服务端**的事实，
	// 不依赖令牌重新签发。
	rec := doAuthed(t, router, http.MethodGet, "/api/v1/tenants/current", token)
	if rec.Code == http.StatusOK {
		t.Fatal("暂停租户后必须立即失去访问能力")
	}
}

func TestE2ETrulyMissingTenantIs404NotLeak(t *testing.T) {
	a := New()
	router := NewRouter(a)
	grantAllPermissions(t, a, "user-acme")
	tn := seedTenantForUser(t, a, "acme", "user-acme")
	token := issueTenantToken(t, a, "user-acme", tn.ID)

	// 令牌里声称的租户根本不存在：必须被拒，且不得泄漏任何细节。
	ghostToken := issueTenantToken(t, a, "user-acme", tenant.ID("ghost-tenant-id"))
	rec := doAuthed(t, router, http.MethodGet, "/api/v1/tenants/current", ghostToken)
	if rec.Code == http.StatusOK {
		t.Fatal("不存在的租户必须被拒绝")
	}
	if strings.Contains(rec.Body.String(), "acme") {
		t.Fatalf("不得泄漏任何既有租户的信息: %s", rec.Body.String())
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的租户应 404（与跨租户同形），得到 %d", rec.Code)
	}
	_ = token
}

// ===== 租户生命周期端到端 =====

func TestE2ETenantLifecycleOverRouter(t *testing.T) {
	a := New()
	router := NewRouter(a)
	// 平台管理员令牌（无租户声明，走 role:manage 权限）。
	grantAllPermissions(t, a, "platform-admin")
	adminToken := issueTenantToken(t, a, "platform-admin", "")

	// 建租户：POST /api/v1/tenants 需要 role:manage。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants",
		strings.NewReader(`{"name":"Acme Corp","slug":"acme-corp","plan":"team","owner_id":"u-owner"}`))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("建租户应 201，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var created tenant.Tenant
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Slug != "acme-corp" {
		t.Fatalf("slug 错误: %q", created.Slug)
	}

	// 列表可见。
	rec = doAuthed(t, router, http.MethodGet, "/api/v1/tenants", adminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if list.Count != 1 {
		t.Fatalf("应恰好 1 个租户，得到 %d", list.Count)
	}

	// 成员列表（新增成员后可见）。
	rec = doAuthed(t, router, http.MethodPost, "/api/v1/tenants/"+string(created.ID)+"/members",
		adminToken)
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		// 这是空 body 的 POST：可能 400（缺 user_id）而不该是 5xx。
		if rec.Code >= 500 {
			t.Fatalf("空 body 不应产生 5xx，得到 %d: %s", rec.Code, rec.Body.String())
		}
	}

	rec = doAuthed(t, router, http.MethodGet, "/api/v1/tenants/"+string(created.ID)+"/members", adminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("成员列表应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var members struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &members); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// owner_id 存在 → 创建者自动成为成员。漏了这一步新租户会「建完就 404」。
	if members.Count != 1 {
		t.Fatalf("创建者应自动成为成员，得到 %d 名成员", members.Count)
	}

	// 暂停。
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+string(created.ID)+"/status",
		strings.NewReader(`{"status":"suspended"}`))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("暂停应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var suspended tenant.Tenant
	_ = json.Unmarshal(rec.Body.Bytes(), &suspended)
	if suspended.Status != tenant.StatusSuspended {
		t.Fatalf("状态未变更: %+v", suspended)
	}
}

func TestE2ETenantRoutesAreNotDefaultDenied(t *testing.T) {
	// Day 24/26 反复踩过的坑：新增的**无尾斜杠集合路径**若没登记权限，
	// 会落进默认拒绝返回 403。这条测试逐个走一遍，确保确实登记了。
	a := New()
	router := NewRouter(a)
	grantAllPermissions(t, a, "user-acme")
	tn := seedTenantForUser(t, a, "acme", "user-acme")
	token := issueTenantToken(t, a, "user-acme", tn.ID)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/tenants"},
		{http.MethodGet, "/api/v1/tenants/" + string(tn.ID)},
		{http.MethodGet, "/api/v1/tenants/current"},
		{http.MethodGet, "/api/v1/tenants/" + string(tn.ID) + "/members"},
		{http.MethodGet, "/api/v1/tenants/" + string(tn.ID) + "/resources"},
	}
	for _, tc := range cases {
		rec := doAuthed(t, router, tc.method, tc.path, token)
		if rec.Code == http.StatusForbidden {
			t.Fatalf("%s %s 落进了默认拒绝（403）—— 权限表漏登记？", tc.method, tc.path)
		}
	}
}

// ===== SSO 端到端 =====

func TestE2ESSOStatusIsPubliclyReachable(t *testing.T) {
	a := New()
	router := NewRouter(a)

	// 无令牌访问：SSO 状态端点必须公开可达（否则前端连按钮该不该显示
	// 都判断不了）。它若落进默认拒绝会是 403 而不是 200。
	rec := doAuthed(t, router, http.MethodGet, "/api/v1/sso/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("SSO 状态端点应公开可达，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["configured"]; !ok {
		t.Fatalf("响应应含 configured 字段: %s", rec.Body.String())
	}
}

func TestE2ESSOAuthorizeIsPublicButUnconfigured(t *testing.T) {
	a := New()
	router := NewRouter(a)

	// 默认装配没有企业 IdP：授权入口公开可达，但必须 503。
	// 「公开」与「放行」是两件事 —— 未配置的 SSO 绝不能变成认证绕过。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sso/authorize", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("SSO 授权入口落进了默认拒绝（403）—— 权限表未登记为 Public")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置 SSO 时必须 503（默认拒绝），得到 %d: %s", rec.Code, rec.Body.String())
	}
}

func TestE2ESSOCallbackIsPublicButUnconfigured(t *testing.T) {
	a := New()
	router := NewRouter(a)

	rec := doAuthed(t, router, http.MethodGet, "/api/v1/sso/callback?code=x&state=y", "")
	if rec.Code == http.StatusForbidden {
		t.Fatalf("SSO 回调入口落进了默认拒绝（403）")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置 SSO 时回调必须 503，得到 %d: %s", rec.Code, rec.Body.String())
	}
}

// ===== 集成：SSO 登录 → 绑定租户（装配层逻辑）=====

func TestSSOTenantBindingIsIdempotentAndValidated(t *testing.T) {
	ctx := context.Background()
	a := New()
	tn := seedTenantForUser(t, a, "acme", "existing-owner")

	// 第一次绑定：成功。
	if err := bindSSOToTenant(ctx, a.TenantSvc, "sso-user-1", string(tn.ID)); err != nil {
		t.Fatalf("首次绑定应成功: %v", err)
	}
	// 重复绑定：幂等成功（用户重试登录不该失败）。
	if err := bindSSOToTenant(ctx, a.TenantSvc, "sso-user-1", string(tn.ID)); err != nil {
		t.Fatalf("重复绑定应幂等成功: %v", err)
	}
	// 绑定到不存在的租户：必须拒绝，不得静默建一个租户。
	if err := bindSSOToTenant(ctx, a.TenantSvc, "sso-user-1", "no-such-tenant"); err == nil {
		t.Fatal("绑定到不存在的租户必须拒绝")
	}
	// 绑定后确实成为成员。
	if _, err := a.TenantSvc.Resolve(ctx, tn.ID, "sso-user-1"); err != nil {
		t.Fatalf("绑定后应能解析出资质作用域: %v", err)
	}
}

func TestSSOTenantBindingRejectsSuspended(t *testing.T) {
	ctx := context.Background()
	a := New()
	tn := seedTenantForUser(t, a, "acme", "owner")
	if _, err := a.TenantSvc.UpdateStatus(ctx, tn.ID, tenant.StatusSuspended); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	// 暂停的租户不得再接受新成员（否则暂停语义可被绕过）。
	if err := bindSSOToTenant(ctx, a.TenantSvc, "new-user", string(tn.ID)); err == nil {
		t.Fatal("暂停租户不得接受新成员")
	}
}

func TestSSOSessionIssuerOnlyStampsMembership(t *testing.T) {
	ctx := context.Background()
	a := New()
	alice := seedTenantForUser(t, a, "alice", "alice")
	bob := seedTenantForUser(t, a, "bob", "bob")

	// Alice 是 alice 租户的成员，但不是 bob 租户的成员。
	// 即使 URL 里写了 bob 的租户 ID，令牌也不得盖上 bob 的章。
	issuer := ssoSessionIssuer(a.Auth, a.TenantSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sso/callback?tenant_id="+string(bob.ID), nil)
	out, err := issuer(req, &ssoTestAccount)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	pair, ok := out.(*auth.TokenPair)
	if !ok || pair == nil {
		t.Fatalf("应返回 TokenPair，得到 %T", out)
	}
	claims, err := a.Auth.ValidateAccessToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.TenantID == string(bob.ID) {
		t.Fatal("非成员不得被写入租户声明（否则换个 URL 参数就能换租户）")
	}

	// 是成员时应当写入。
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/sso/callback?tenant_id="+string(alice.ID), nil)
	out2, err := issuer(req2, &ssoTestAccount)
	if err != nil {
		t.Fatalf("issue2: %v", err)
	}
	claims2, err := a.Auth.ValidateAccessToken(out2.(*auth.TokenPair).AccessToken)
	if err != nil {
		t.Fatalf("validate2: %v", err)
	}
	if claims2.TenantID != string(alice.ID) {
		t.Fatalf("成员应被写入租户声明，得到 %q", claims2.TenantID)
	}
	_ = ctx
}
