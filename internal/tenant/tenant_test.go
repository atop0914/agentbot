package tenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ===== 夹具 =====

func newTestService(t *testing.T) (Service, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	return NewService(store), store
}

// seedTenant 建一个租户并把 owner 加进去。
func seedTenant(t *testing.T, svc Service, slug, owner string) *Tenant {
	t.Helper()
	tn, err := svc.Create(context.Background(), CreateRequest{
		Name: slug, Slug: slug, Plan: PlanTeam, OwnerID: owner,
	})
	if err != nil {
		t.Fatalf("create tenant %q: %v", slug, err)
	}
	return tn
}

// ===== 租户身份必须来自可信来源 =====

func TestResolveRejectsEmptyTenant(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Resolve(context.Background(), None, "user-1"); err == nil {
		t.Fatal("空租户 ID 必须被拒绝：空租户不等于全局可见")
	}
}

func TestResolveRejectsEmptyUser(t *testing.T) {
	svc, _ := newTestService(t)
	tn := seedTenant(t, svc, "acme", "owner-1")
	if _, err := svc.Resolve(context.Background(), tn.ID, ""); err == nil {
		t.Fatal("缺少用户身份时必须拒绝")
	}
}

func TestResolveRejectsNonMember(t *testing.T) {
	svc, _ := newTestService(t)
	tn := seedTenant(t, svc, "acme", "owner-1")
	// 拿着合法租户 ID 但不是成员 —— 这正是「猜到别人的租户 ID」的场景。
	if _, err := svc.Resolve(context.Background(), tn.ID, "intruder"); err == nil {
		t.Fatal("非成员必须无法解析出作用域")
	}
}

func TestResolveAcceptsMember(t *testing.T) {
	svc, _ := newTestService(t)
	tn := seedTenant(t, svc, "acme", "owner-1")
	scope, err := svc.Resolve(context.Background(), tn.ID, "owner-1")
	if err != nil {
		t.Fatalf("成员应能解析出作用域: %v", err)
	}
	if scope.TenantID() != tn.ID || scope.UserID() != "owner-1" {
		t.Fatalf("作用域内容错误: %s / %s", scope.TenantID(), scope.UserID())
	}
}

func TestResolveRejectsSuspendedTenant(t *testing.T) {
	svc, _ := newTestService(t)
	tn := seedTenant(t, svc, "acme", "owner-1")
	if _, err := svc.UpdateStatus(context.Background(), tn.ID, StatusSuspended); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if _, err := svc.Resolve(context.Background(), tn.ID, "owner-1"); err == nil {
		t.Fatal("暂停的租户必须拒绝访问")
	}
}

// ===== 跨租户隔离 =====

func TestCrossTenantAccessReturnsNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	alice := seedTenant(t, svc, "alice-corp", "alice")
	bob := seedTenant(t, svc, "bob-corp", "bob")

	// Alice 的资源登记在 alice 租户下。
	if _, err := svc.Register(context.Background(), alice.ID, ResourceAgent, "agent-a1"); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Bob 访问 → 必须是 ErrNotFound（不是 ErrForbidden）。
	err := svc.Check(context.Background(), bob.ID, ResourceAgent, "agent-a1")
	if err == nil {
		t.Fatal("跨租户访问必须被拒绝")
	}
	if !isErr(err, ErrNotFound) {
		t.Fatalf("跨租户必须返回 ErrNotFound（不可枚举他人 ID），得到 %v", err)
	}

	// Alice 自己访问 → 通过。
	if err := svc.Check(context.Background(), alice.ID, ResourceAgent, "agent-a1"); err != nil {
		t.Fatalf("本租户访问应当通过: %v", err)
	}
}

func TestCrossTenantAndMissingAreIndistinguishable(t *testing.T) {
	svc, _ := newTestService(t)
	alice := seedTenant(t, svc, "alice", "alice")
	bob := seedTenant(t, svc, "bob", "bob")
	if _, err := svc.Register(context.Background(), alice.ID, ResourceAgent, "secret-agent"); err != nil {
		t.Fatalf("register: %v", err)
	}

	crossErr := svc.Check(context.Background(), bob.ID, ResourceAgent, "secret-agent")
	missingErr := svc.Check(context.Background(), bob.ID, ResourceAgent, "does-not-exist-at-all")

	// 两条错误的文案必须完全一致：否则攻击者比字符串就能判断 ID 是否存在。
	if crossErr.Error() != missingErr.Error() {
		t.Fatalf("跨租户与不存在必须不可区分:\n  cross   = %v\n  missing = %v", crossErr, missingErr)
	}
}

func TestScopeAllowsIsFailClosedWithoutScope(t *testing.T) {
	svc, _ := newTestService(t)
	tn := seedTenant(t, svc, "acme", "owner-1")
	if _, err := svc.Register(context.Background(), tn.ID, ResourceAgent, "a1"); err != nil {
		t.Fatalf("register: %v", err)
	}

	var nilScope *Scope
	if nilScope.Allows(context.Background(), ResourceAgent, "a1") {
		t.Fatal("nil 作用域必须一律拒绝（最后一层兜底）")
	}
}

func TestOwnerMatchesRejectsUnboundResource(t *testing.T) {
	// 未绑定租户的资源不是「公共资源」。这条如果反了，
	// 一次「忘了登记归属」就会变成一次全平台可见的泄漏。
	unbound := Owner{TenantID: None, Type: ResourceAgent, ResID: "orphan"}
	if unbound.Matches("any-tenant") {
		t.Fatal("未绑定租户的资源不得对任何租户可见")
	}
	// 反向：无租户的调用方也不得看到已绑定的资源。
	bound := Owner{TenantID: "t1", Type: ResourceAgent, ResID: "a1"}
	if bound.Matches(None) {
		t.Fatal("无租户身份不得访问已绑定资源")
	}
}

func TestScopeFilterDropsForeignResources(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	alice := seedTenant(t, svc, "alice", "alice")
	bob := seedTenant(t, svc, "bob", "bob")
	for _, id := range []string{"a1", "a2"} {
		if _, err := svc.Register(ctx, alice.ID, ResourceAgent, id); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	if _, err := svc.Register(ctx, bob.ID, ResourceAgent, "b1"); err != nil {
		t.Fatalf("register b1: %v", err)
	}

	scope, err := svc.Resolve(ctx, alice.ID, "alice")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := scope.Filter(ctx, ResourceAgent, []string{"a1", "b1", "a2", "nope"})
	if len(got) != 2 || got[0] != "a1" || got[1] != "a2" {
		t.Fatalf("列表过滤必须只保留本租户资源，得到 %v", got)
	}
}

// ===== 配额 =====

func TestQuotaEnforcedForAgents(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	// Free 套餐 MaxAgents=5。
	tn := seedTenant(t, svc, "small", "owner")
	if _, err := svc.UpdateStatus(ctx, tn.ID, StatusActive); err != nil {
		t.Fatalf("status: %v", err)
	}

	// 降级到 free 以固定配额（seedTenant 用的是 team）。
	free, err := svc.Create(ctx, CreateRequest{Name: "freebie", Slug: "freebie", Plan: PlanFree, OwnerID: "owner"})
	if err != nil {
		t.Fatalf("create free: %v", err)
	}
	for i := 0; i < free.Quota.MaxAgents; i++ {
		if _, err := svc.Register(ctx, free.ID, ResourceAgent, "agent-"+string(rune('a'+i))); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	if err := svc.EnforceQuota(ctx, free.ID, ResourceAgent); !isErr(err, ErrQuotaExceeded) {
		t.Fatalf("配额用尽后必须拒绝，得到 %v", err)
	}
}

func TestQuotaAllowsUnlimited(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	tn, err := svc.Create(ctx, CreateRequest{Name: "ent", Slug: "ent", Plan: PlanEnterprise, OwnerID: "o"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// enterprise MaxAgents=500：远未用满时不应报配额错误。
	if err := svc.EnforceQuota(ctx, tn.ID, ResourceAgent); err != nil {
		t.Fatalf("企业套餐不应误报配额: %v", err)
	}
}

func TestMemberQuotaEnforced(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	free, err := svc.Create(ctx, CreateRequest{Name: "f", Slug: "f", Plan: PlanFree, OwnerID: "owner"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// owner 已占 1 个名额，MaxMembers=10。
	for i := 0; i < free.Quota.MaxMembers-1; i++ {
		if _, err := svc.AddMember(ctx, free.ID, "member-"+string(rune('a'+i)), "owner"); err != nil {
			t.Fatalf("add member %d: %v", i, err)
		}
	}
	if _, err := svc.AddMember(ctx, free.ID, "one-too-many", "owner"); !isErr(err, ErrQuotaExceeded) {
		t.Fatalf("成员超限必须拒绝，得到 %v", err)
	}
}

// ===== 幂等与稳定性 =====

func TestAddMemberIsIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	tn := seedTenant(t, svc, "acme", "owner")
	first, err := svc.AddMember(ctx, tn.ID, "u1", "owner")
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	second, err := svc.AddMember(ctx, tn.ID, "u1", "owner")
	if err != nil {
		t.Fatalf("重复加入必须幂等成功（用户重试登录不该失败）: %v", err)
	}
	if !first.JoinedAt.Equal(second.JoinedAt) {
		t.Fatal("幂等加入不应刷新加入时间")
	}
	if n := svc.CountResources(ctx, tn.ID, ResourceAudit); n != 0 {
		t.Fatalf("空租户资源计数应为 0，得到 %d", n)
	}
}

func TestListTenantsIsSorted(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	for _, slug := range []string{"c", "a", "b"} {
		seedTenant(t, svc, slug, "owner")
	}
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("期望 3 个租户，得到 %d", len(list))
	}
	// 排序保证：同一创建时刻的租户按 ID 升序（map 迭代顺序不可依赖）。
	for i := 1; i < len(list); i++ {
		if list[i-1].CreatedAt.After(list[i].CreatedAt) {
			t.Fatal("租户列表必须按创建时间升序")
		}
	}
}

func TestStoreReturnsCopies(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	svc := NewService(store)
	tn := seedTenant(t, svc, "acme", "owner")

	// 修改读出来的副本，不应污染存储。
	got, err := svc.Get(ctx, tn.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got.Name = "hacked"
	again, err := svc.Get(ctx, tn.ID)
	if err != nil {
		t.Fatalf("get again: %v", err)
	}
	if again.Name == "hacked" {
		t.Fatal("存储层必须返回副本，调用方修改不得污染已存数据")
	}
}

func TestRemoveMemberRevokesAccess(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	tn := seedTenant(t, svc, "acme", "owner")
	if _, err := svc.AddMember(ctx, tn.ID, "temp", "owner"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := svc.Resolve(ctx, tn.ID, "temp"); err != nil {
		t.Fatalf("加入后应能解析: %v", err)
	}
	if err := svc.RemoveMember(ctx, tn.ID, "temp"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := svc.Resolve(ctx, tn.ID, "temp"); err == nil {
		t.Fatal("移出成员后必须立即失去访问能力")
	}
}

func TestDuplicateSlugRejected(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	seedTenant(t, svc, "acme", "owner-a")
	if _, err := svc.Create(ctx, CreateRequest{Name: "other", Slug: "acme", OwnerID: "owner-b"}); err == nil {
		t.Fatal("重复 slug 必须拒绝（否则 SSO 域与展示名歧义）")
	}
}

func TestSlugNormalizationCollapsesSeparators(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	// "Acme  Corp" 与 "acme-corp" 必须归一为同一个 slug，
	// 否则「看起来同名」的两个租户会共存。
	first, err := svc.Create(ctx, CreateRequest{Name: "Acme  Corp", Slug: "Acme  Corp", OwnerID: "o1"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	if first.Slug != "acme-corp" {
		t.Fatalf("slug 归一化错误: %q", first.Slug)
	}
	if _, err := svc.Create(ctx, CreateRequest{Name: "dup", Slug: "acme-corp", OwnerID: "o2"}); err == nil {
		t.Fatal("归一化后同名的 slug 必须被拒绝")
	}
}

func TestCreateMakesOwnerAMember(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	tn := seedTenant(t, svc, "acme", "owner")
	// 创建者必须立刻是成员，否则新租户表现为「建完就 404」。
	if _, err := svc.Resolve(ctx, tn.ID, "owner"); err != nil {
		t.Fatalf("租户创建者必须自动成为成员: %v", err)
	}
}

// ===== HTTP 中间件 =====

func TestMiddlewareRejectsMissingIdentityWhenRequired(t *testing.T) {
	svc, _ := newTestService(t)
	mw := NewMiddleware(svc, func(*http.Request) (ID, string, bool) {
		return None, "", false
	}, true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	mw.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("无身份时不应进入业务 handler")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("期望 401，得到 %d", rec.Code)
	}
}

func TestMiddlewarePassesThroughWhenNotRequired(t *testing.T) {
	svc, _ := newTestService(t)
	mw := NewMiddleware(svc, func(*http.Request) (ID, string, bool) {
		return None, "", false
	}, false)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 无租户身份时必须**没有**作用域，而不是「空作用域」。
		if ScopeFromContext(r.Context()) != nil {
			t.Fatal("无身份请求不应被注入作用域")
		}
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("公开路径应放行，得到 %d", rec.Code)
	}
}

func TestMiddlewareCrossTenantResolvesTo404(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	alice := seedTenant(t, svc, "alice", "alice")
	seedTenant(t, svc, "bob", "bob")

	// 模拟「Bob 的 token 里带着 Alice 的租户 ID」：claims 与成员关系不符。
	mw := NewMiddleware(svc, func(*http.Request) (ID, string, bool) {
		return alice.ID, "bob", true
	}, true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	mw.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("非成员不得进入业务 handler")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨租户必须以 404 呈现（不泄漏租户存在），得到 %d", rec.Code)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("unused ctx: %v", err)
	}
}

func TestMiddlewareFailsClosedWhenServiceMissing(t *testing.T) {
	// 中间件没有服务时必须 503，绝不能「因为没得查所以放行」。
	mw := &Middleware{svc: nil, required: false}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	mw.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("服务缺失时不应进入业务 handler")
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望 503（fail-closed），得到 %d", rec.Code)
	}
}

func TestRequireScopeBlocksUnscopedRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", nil)
	RequireScope(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("无作用域不应进入受保护 handler")
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("期望 401，得到 %d", rec.Code)
	}
}

// ===== Handler =====

func TestHandlerResourcesRequiresMatchingScope(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	alice := seedTenant(t, svc, "alice", "alice")
	bob := seedTenant(t, svc, "bob", "bob")

	h := NewHandler(svc)
	scope, err := svc.Resolve(ctx, bob.ID, "bob")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Bob 的 scope 去查 Alice 的资源清单 → 404。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+string(alice.ID)+"/resources", nil)
	req = req.WithContext(WithScope(req.Context(), scope))
	rec := httptest.NewRecorder()
	h.handleTenantByID(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨租户资源清单必须以 404 呈现，得到 %d", rec.Code)
	}
}

func TestHandlerStatusRejectsUnknownValue(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	tn := seedTenant(t, svc, "acme", "owner")
	if _, err := svc.UpdateStatus(ctx, tn.ID, Status("bogus")); err == nil {
		t.Fatal("未知状态值必须被拒绝")
	}
}

func TestStatusValid(t *testing.T) {
	if Status("").Valid() {
		t.Fatal("空状态不合法")
	}
	if !StatusActive.Valid() || !StatusSuspended.Valid() {
		t.Fatal("已定义状态必须合法")
	}
}

func TestTenantIDRejectsConfusableValues(t *testing.T) {
	for _, bad := range []ID{"", "  ", "a/b", "a?b", "a#b", "a%b", ID(strings.Repeat("x", 65))} {
		if bad.Valid() {
			t.Fatalf("租户 ID %q 必须被判为非法", bad)
		}
	}
	if !ID("acme-corp_01").Valid() {
		t.Fatal("正常租户 ID 必须合法")
	}
}

func TestCountMembersAndResources(t *testing.T) {
	ctx := context.Background()
	svc, store := newTestService(t)
	tn := seedTenant(t, svc, "acme", "owner")
	if _, err := svc.AddMember(ctx, tn.ID, "u2", "owner"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if n := svc.CountResources(ctx, tn.ID, ResourceAgent); n != 0 {
		t.Fatalf("初始资源数应为 0，得到 %d", n)
	}
	if _, err := svc.Register(ctx, tn.ID, ResourceAgent, "a1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if n := svc.CountResources(ctx, tn.ID, ResourceAgent); n != 1 {
		t.Fatalf("登记后资源数应为 1，得到 %d", n)
	}
	if n := store.CountMembers(ctx, tn.ID); n != 2 {
		t.Fatalf("成员数应为 2（owner + u2），得到 %d", n)
	}
}

func TestStatsAggregatesAcrossTenants(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	a := seedTenant(t, svc, "a", "owner-a")
	b := seedTenant(t, svc, "b", "owner-b")
	if _, err := svc.Register(ctx, a.ID, ResourceAgent, "a1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := svc.Register(ctx, b.ID, ResourceTask, "t1"); err != nil {
		t.Fatalf("register: %v", err)
	}

	all, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if all.TotalTenants != 2 || all.TotalMembers != 2 {
		t.Fatalf("全局统计错误: %+v", all)
	}
	if all.Resources[string(ResourceAgent)] != 1 || all.Resources[string(ResourceTask)] != 1 {
		t.Fatalf("资源统计错误: %+v", all.Resources)
	}

	scoped, err := svc.StatsFor(ctx, a.ID)
	if err != nil {
		t.Fatalf("stats for: %v", err)
	}
	if !scoped.Scoped || scoped.TenantID != a.ID {
		t.Fatalf("单租户统计标记错误: %+v", scoped)
	}
	if scoped.Resources[string(ResourceTask)] != 0 {
		t.Fatal("单租户视角不得包含其他租户的资源")
	}
}

func TestUnregisterRequiresOwnership(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	alice := seedTenant(t, svc, "alice", "alice")
	bob := seedTenant(t, svc, "bob", "bob")
	if _, err := svc.Register(ctx, alice.ID, ResourceAgent, "a1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Bob 不能删 Alice 的资源，即使知道 ID。
	if err := svc.Unregister(ctx, bob.ID, ResourceAgent, "a1"); !isErr(err, ErrNotFound) {
		t.Fatalf("跨租户删除必须被拒绝，得到 %v", err)
	}
	// Alice 可以。
	if err := svc.Unregister(ctx, alice.ID, ResourceAgent, "a1"); err != nil {
		t.Fatalf("本租户删除应当成功: %v", err)
	}
}

func TestTenantsOfUserReturnsSorted(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	for _, slug := range []string{"c", "a", "b"} {
		tn := seedTenant(t, svc, slug, "multi-user")
		_ = tn
	}
	ids, err := svc.TenantsOfUser(ctx, "multi-user")
	if err != nil {
		t.Fatalf("tenants of user: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("期望 3 个租户，得到 %d", len(ids))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i-1] > ids[i] {
			t.Fatal("用户所属租户必须升序返回")
		}
	}
}
