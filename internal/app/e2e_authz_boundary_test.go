package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/atop0914/agentbot/internal/auth"
	"github.com/atop0914/agentbot/internal/authz"
	"github.com/atop0914/agentbot/internal/tenant"
)

// 本文件是 Day 29 的第二批：**授权边界**的全链路断言。
//
// 为什么边界要和主线分开写：
//
//	主线只证明「正常路径能走通」，而授权链的价值全在异常路径上。
//	本轮的安全基线（Day 23-27）反复踩到的坑 —— 认证挂在授权内层恒 401、
//	集合路径漏登记导致 403、租户中间件层序反了读不到 claims ——
//	现象都出现在异常路径，且单测全绿。所以这里逐条钉住四种结局：
//
//	  放行（主线已覆盖） / 无身份 401 / 越权 403 / 跨租户 404
//
//	第 4 种（跨租户 404 而不是 403）尤其重要：403 会说「这个资源存在但你不能看」，
//	等于泄漏了其他租户的资源存在性；404 才能让跨租户与「不存在」同形。

// TestE2EAnonymousIsRejectedEverywhere 验证未认证请求在受保护路由上
// 一律被拒，且**拒绝形态统一为 401**（不是 403，也不是 500）。
//
// 覆盖的路径刻意包含 Day 24/26/27 踩过的三类坑：
//   - 集合路径（无尾斜杠），前缀规则最容易漏；
//   - 生命周期子动作 /{id}/start，需要单独的 POST 规则；
//   - 子资源路径 /{id}/status、/{id}/members。
func TestE2EAnonymousIsRejectedEverywhere(t *testing.T) {
	env := newE2EEnv(t)

	// 先建一个真实 Agent，让路径里的 ID 存在 —— 这样 401 只能来自
	// 「无身份」而不能被「资源不存在」混淆。
	agentResp := env.do(http.MethodPost, "/api/v1/agents", `{"name":"anon-probe","description":"d"}`)
	env.mustStatus(agentResp, http.StatusCreated, "建探针 Agent")
	var created struct {
		ID string `json:"id"`
	}
	env.decode(t, agentResp.Body.Bytes(), &created)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/agents"},
		{http.MethodGet, "/api/v1/agents/" + created.ID},
		{http.MethodPost, "/api/v1/agents/" + created.ID + "/start"},
		{http.MethodPost, "/api/v1/tasks"},
		{http.MethodGet, "/api/v1/tasks"},
		{http.MethodGet, "/api/v1/audit/events"},
		{http.MethodGet, "/api/v1/admin/snapshot"},
		{http.MethodGet, "/api/v1/users"},
		{http.MethodGet, "/api/v1/tenants"},
		{http.MethodGet, "/api/v1/tenants/" + string(env.tenantID) + "/members"},
		{http.MethodGet, "/api/v1/roles"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := env.doWithToken(tc.method, tc.path, "", "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("无令牌访问应 401，得到 %d: %s", rec.Code, rec.Body.String())
			}
			// 不得因为「没身份」而泄漏路径是否存在等内部结构。
			if body := rec.Body.String(); body == "" {
				t.Error("401 响应体为空，缺少可诊断的错误消息")
			}
		})
	}

	// 反向确认：这些路径**带**令牌时不是 401（否则上面的断言可能只是因为
	// 路由根本不存在，那样这个测试什么都没验证）。
	for _, tc := range cases {
		rec := env.do(tc.method, tc.path, "")
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("带合法令牌时 %s %s 不应 401: %s", tc.method, tc.path, rec.Body.String())
		}
	}
}

// TestE2EGarbageTokenIsRejected 验证伪造/损坏令牌被拒绝。
//
// 这条与「无令牌」是两回事：无令牌走的是「主体为空」分支，
// 而坏令牌走的是验签失败分支。后者若被静默当成匿名放行，
// 就会变成「带个垃圾 token 就能绕过认证」—— 最危险的一类缺陷。
func TestE2EGarbageTokenIsRejected(t *testing.T) {
	env := newE2EEnv(t)

	tokens := []string{
		"not-a-jwt",
		// 结构像 JWT 但签名无效（三段式，签名段是垃圾）。
		"eyJhbG...VCJ9." +
			"eyJzdW...biJ9." +
			"this-signature-is-invalid",
		// 空 Bearer。
		" ",
	}
	for i, tok := range tokens {
		rec := env.doWithToken(http.MethodGet, "/api/v1/agents", "", tok)
		if rec.Code == http.StatusOK {
			t.Fatalf("第 %d 个伪造令牌被放行了: %s", i, rec.Body.String())
		}
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Errorf("第 %d 个伪造令牌应 401/403，得到 %d: %s", i, rec.Code, rec.Body.String())
		}
	}
}

// TestE2EInsufficientPermissionIs403 验证「已认证但无权限」是 403
// 而不是 401 —— 这两种结局的诊断含义完全不同：
// 401 让用户去重新登录（无效操作），403 才是「找管理员要权限」。
func TestE2EInsufficientPermissionIs403(t *testing.T) {
	a := New()
	router := NewRouter(a)

	// 一个已注册但**没有**任何角色授权的用户：认证通过、授权拒绝。
	userID := "user-without-role"
	token := issueRawToken(t, a, userID, "")

	rec := doToken(t, router, http.MethodGet, "/api/v1/agents", "", token)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无角色用户访问应 403，得到 %d: %s", rec.Code, rec.Body.String())
	}

	// 拿到**只读**角色后：读放行，写仍拒绝。
	subject := authz.Subject{Type: authz.SubjectUser, ID: userID}
	if _, err := a.AuthzSvc.Assign(context.Background(), subject, "role-observer", "test"); err != nil {
		t.Fatalf("授予 observer 角色失败: %v", err)
	}
	if rec := doToken(t, router, http.MethodGet, "/api/v1/agents", "", token); rec.Code != http.StatusOK {
		t.Errorf("只读角色应能读 Agent，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if rec := doToken(t, router, http.MethodPost, "/api/v1/agents",
		`{"name":"should-be-denied","description":"d"}`, token); rec.Code != http.StatusForbidden {
		t.Errorf("只读角色建 Agent 应 403，得到 %d: %s", rec.Code, rec.Body.String())
	}
	// 管理类动作（改角色矩阵）同样必须拒绝：observer 不该碰元权限。
	if rec := doToken(t, router, http.MethodPost, "/api/v1/roles",
		`{"name":"evil-role","permissions":["role:manage"]}`, token); rec.Code != http.StatusForbidden {
		t.Errorf("只读角色建角色应 403，得到 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestE2ECrossTenantResourceAccessIs404 验证跨租户访问返回 404 而不是 403。
//
// 这是隔离语义的核心：403 等于承认「该资源存在，只是你没权限」，
// 攻击者可以借此枚举其他租户的资源 ID。404 让跨租户与「不存在」不可区分。
func TestE2ECrossTenantResourceAccessIs404(t *testing.T) {
	ctx := context.Background()
	a := New()
	router := NewRouter(a)

	// 两个租户，各自的成员与资源。
	mkTenant := func(slug, owner string) *tenant.Tenant {
		t.Helper()
		tn, err := a.TenantSvc.Create(ctx, tenant.CreateRequest{
			Name: slug, Slug: slug, Plan: tenant.PlanTeam, OwnerID: owner,
		})
		if err != nil {
			t.Fatalf("建租户 %s 失败: %v", slug, err)
		}
		return tn
	}
	alice := mkTenant("alice-corp", "alice")
	bob := mkTenant("bob-corp", "bob")

	// Alice 名下的资源登记。
	if _, err := a.TenantSvc.Register(ctx, alice.ID, tenant.ResourceAgent, "agent-alice-secret"); err != nil {
		t.Fatalf("登记 Alice 资源失败: %v", err)
	}

	// 两个用户都授予 coordinator 角色，让失败信号只来自租户边界
	// 而不是权限矩阵 —— 否则这条测试会因为「权限不足」而通过，
	// 伪装成隔离生效。
	for _, uid := range []string{"alice", "bob"} {
		subject := authz.Subject{Type: authz.SubjectUser, ID: uid}
		if _, err := a.AuthzSvc.Assign(ctx, subject, "role-coordinator", "test"); err != nil {
			t.Fatalf("授予 %s 角色失败: %v", uid, err)
		}
	}
	bobToken := issueRawToken(t, a, "bob", string(bob.ID))
	aliceToken := issueRawToken(t, a, "alice", string(alice.ID))

	// Bob 查 Alice 的资源清单：必须 404（不是 403，不是 200）。
	rec := doToken(t, router, http.MethodGet,
		"/api/v1/tenants/"+string(alice.ID)+"/resources", "", bobToken)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Bob 查 Alice 的资源清单应 404，得到 %d: %s", rec.Code, rec.Body.String())
	}
	// 响应不得泄漏 Alice 租户的任何信息。
	if body := rec.Body.String(); containsAny(body, "alice-corp", "agent-alice-secret") {
		t.Errorf("404 响应泄漏了其他租户的信息: %s", body)
	}

	// Alice 查自己的资源清单应当成功，且能看到自己登记的资源。
	rec = doToken(t, router, http.MethodGet,
		"/api/v1/tenants/"+string(alice.ID)+"/resources", "", aliceToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("Alice 查自己的资源清单应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}

	// Bob 查 Alice 的租户详情同样必须 404。
	rec = doToken(t, router, http.MethodGet, "/api/v1/tenants/"+string(alice.ID), "", bobToken)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Bob 查 Alice 租户详情应 404，得到 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestE2ETenantSuspendedCutsAccessImmediately 验证暂停租户后，
// 同一张尚未过期的令牌立即失效。
//
// 若租户状态只在「签发令牌时」检查，暂停就只能等令牌自然过期 ——
// 那不是暂停，只是延迟生效。状态必须是服务端每请求判定的事实。
func TestE2ETenantSuspendedCutsAccessImmediately(t *testing.T) {
	ctx := context.Background()
	a := New()
	router := NewRouter(a)

	tn, err := a.TenantSvc.Create(ctx, tenant.CreateRequest{
		Name: "suspend-corp", Slug: "suspend-corp", Plan: tenant.PlanTeam, OwnerID: "owner",
	})
	if err != nil {
		t.Fatalf("建租户失败: %v", err)
	}
	subject := authz.Subject{Type: authz.SubjectUser, ID: "owner"}
	if _, err := a.AuthzSvc.Assign(ctx, subject, "role-coordinator", "test"); err != nil {
		t.Fatalf("授予角色失败: %v", err)
	}
	token := issueRawToken(t, a, "owner", string(tn.ID))

	if rec := doToken(t, router, http.MethodGet, "/api/v1/tenants/current", "", token); rec.Code != http.StatusOK {
		t.Fatalf("暂停前应可自查租户，得到 %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := a.TenantSvc.UpdateStatus(ctx, tn.ID, tenant.StatusSuspended); err != nil {
		t.Fatalf("暂停租户失败: %v", err)
	}

	// 同一张令牌（未过期、签名有效）必须立刻被拒。
	rec := doToken(t, router, http.MethodGet, "/api/v1/tenants/current", "", token)
	if rec.Code == http.StatusOK {
		t.Fatal("暂停租户后同一张令牌仍可用，暂停语义未生效")
	}
}

// TestE2EDefaultDeniedRoutesAreExplicitlyRegistered 验证关键路由
// 没有落进「默认拒绝」分支。
//
// 默认拒绝本身是安全设计，但「因为漏登记而被拒」和「因为没权限而被拒」
// 的响应体不同：前者是 "no permission rule registered for this route"。
// 这条测试把「漏登记」这个接线错误单独钉出来 —— 它是最常见的自伤，
// 而且症状（功能莫名其妙 403）会被误当成权限问题去查。
func TestE2EDefaultDeniedRoutesAreExplicitlyRegistered(t *testing.T) {
	env := newE2EEnv(t)

	// 这些路径在主线里会被调用；只要有一条漏登记，主线也会跟着挂。
	// 这里独立断言，是为了让「漏登记」有独立的失败信号。
	agentResp := env.do(http.MethodPost, "/api/v1/agents", `{"name":"reg-probe","description":"d"}`)
	env.mustStatus(agentResp, http.StatusCreated, "建 Agent")
	var created struct {
		ID string `json:"id"`
	}
	env.decode(t, agentResp.Body.Bytes(), &created)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/agents"},
		{http.MethodGet, "/api/v1/agents/" + created.ID},
		{http.MethodPost, "/api/v1/agents/" + created.ID + "/start"},
		{http.MethodPost, "/api/v1/agents/" + created.ID + "/stop"},
		{http.MethodPost, "/api/v1/tasks"},
		{http.MethodGet, "/api/v1/tasks"},
		{http.MethodGet, "/api/v1/audit/events"},
		{http.MethodPost, "/api/v1/audit/events"},
		{http.MethodGet, "/api/v1/admin/snapshot"},
		{http.MethodGet, "/api/v1/users"},
		{http.MethodGet, "/api/v1/tenants"},
		{http.MethodGet, "/api/v1/tenants/current"},
		{http.MethodGet, "/api/v1/tenants/" + string(env.tenantID) + "/members"},
		{http.MethodGet, "/api/v1/tenants/" + string(env.tenantID) + "/resources"},
		{http.MethodGet, "/api/v1/roles"},
		{http.MethodGet, "/api/v1/permissions"},
		{http.MethodGet, "/api/v1/monitor/agents"},
		{http.MethodGet, "/api/v1/network/rules"},
		{http.MethodGet, "/api/v1/memory/stats"},
		{http.MethodGet, "/api/v1/templates"},
		{http.MethodGet, "/api/v1/marketplace"},
		{http.MethodGet, "/api/v1/messages?agent_id=x"},
		{http.MethodGet, "/api/v1/ws/status"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			resp := env.do(tc.method, tc.path, "")
			if resp.Code == http.StatusForbidden &&
				containsAny(resp.Body.String(), "no permission rule registered") {
				t.Fatalf("%s %s 落进了默认拒绝：权限表漏登记", tc.method, tc.path)
			}
			if resp.Code >= 500 {
				t.Fatalf("%s %s 返回 %d（服务端错误）: %s", tc.method, tc.path, resp.Code, resp.Body.String())
			}
		})
	}
}

// --- 本文件内的小工具 ---

// issueRawToken 直接由装配好的 auth 服务签发令牌（不经过 HTTP），
// 用于构造「已认证」的边界场景。
func issueRawToken(t *testing.T, a *App, userID, tenantID string) string {
	t.Helper()
	pair, err := a.Auth.IssueTokens(&auth.Claims{
		UserID: userID, Username: userID, Email: userID + "@corp.example",
		Role: "user", TenantID: tenantID,
	})
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}
	return pair.AccessToken
}

// doToken 用给定令牌向真实 router 发一个请求。
//
// 直接手搓 *http.Request 而不是复用 e2eEnv.do：这里的令牌是刻意
// 构造出来的（无角色用户、跨租户用户），与 e2eEnv 的主体令牌不同。
func doToken(t *testing.T, h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// containsAny 判断 s 是否包含任意一个待查子串。
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
