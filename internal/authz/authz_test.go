package authz_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/authz"
	"github.com/atop0914/agentbot/internal/role"
)

// newTestSvc 构造一个使用内存存储、真实角色服务与空目标表的授权服务。
func newTestSvc(t *testing.T) authz.Service {
	t.Helper()
	roleSvc := role.NewService(role.NewMemoryRepository())
	return authz.NewService(authz.NewMemoryStore(), roleSvc, authz.NewTargetRegistry())
}

func userSubject(id string) authz.Subject {
	return authz.Subject{Type: authz.SubjectUser, ID: id}
}

// --- 授权链解析 ---

func TestAuthorizeUnionsPermissionsAcrossRoles(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()
	sub := userSubject("u1")

	if _, err := svc.Assign(ctx, sub, "role-worker", "test"); err != nil {
		t.Fatalf("assign worker: %v", err)
	}
	if _, err := svc.Assign(ctx, sub, "role-reviewer", "test"); err != nil {
		t.Fatalf("assign reviewer: %v", err)
	}

	auth, err := svc.Authorize(ctx, sub)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if len(auth.Roles) != 2 {
		t.Fatalf("roles = %v, want 2", auth.Roles)
	}

	// 权限必须是两个角色的并集：worker 的 task:execute 与 reviewer 的读权限都要在。
	perms := make(map[role.Permission]bool, len(auth.Permissions))
	for _, p := range auth.Permissions {
		perms[p] = true
	}
	if !perms[role.PermTaskExecute] {
		t.Errorf("union missing worker permission %s: %v", role.PermTaskExecute, auth.Permissions)
	}
	if !perms[role.PermAgentRead] {
		t.Errorf("union missing reviewer permission %s: %v", role.PermAgentRead, auth.Permissions)
	}

	// 权限列表必须去重且有序（稳定输出，避免接口抖动）。
	for i := 1; i < len(auth.Permissions); i++ {
		if auth.Permissions[i-1] == auth.Permissions[i] {
			t.Fatalf("duplicate permission at %d: %s", i, auth.Permissions[i])
		}
		if auth.Permissions[i-1] > auth.Permissions[i] {
			t.Fatalf("permissions not sorted at %d: %s > %s", i, auth.Permissions[i-1], auth.Permissions[i])
		}
	}
}

func TestAuthorizeWithoutAssignmentsIsEmpty(t *testing.T) {
	svc := newTestSvc(t)
	auth, err := svc.Authorize(context.Background(), userSubject("nobody"))
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if len(auth.Roles) != 0 || len(auth.Permissions) != 0 {
		t.Fatalf("expected empty chain, got roles=%v perms=%v", auth.Roles, auth.Permissions)
	}
}

func TestAssignRejectsUnknownRole(t *testing.T) {
	svc := newTestSvc(t)
	if _, err := svc.Assign(context.Background(), userSubject("u1"), "role-does-not-exist", "test"); err == nil {
		t.Fatal("expected error for unknown role")
	}
}

func TestRevokeRemovesPermission(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()
	sub := userSubject("u1")

	if _, err := svc.Assign(ctx, sub, "role-worker", "test"); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if err := svc.Require(ctx, sub, role.PermTaskExecute, authz.Target{}); err != nil {
		t.Fatalf("expected permitted before revoke: %v", err)
	}

	if err := svc.Revoke(ctx, sub, "role-worker"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := svc.Require(ctx, sub, role.PermTaskExecute, authz.Target{}); !authz.IsForbidden(err) {
		t.Fatalf("expected forbidden after revoke, got %v", err)
	}
}

// --- 判定语义 ---

func TestDecideDefaultDeny(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()

	// Decide 在拒绝时既返回 Decision{Allowed:false}，也返回 ErrForbidden，
	// 两者都要能表达「拒绝」。这里同时断言，防止未来只改一边。
	decision, err := svc.Decide(ctx, userSubject("u1"), role.PermAgentDelete, authz.Target{})
	if decision != nil && decision.Allowed {
		t.Fatal("subject with no roles must not be allowed")
	}
	if !authz.IsForbidden(err) {
		t.Fatalf("decide must report forbidden, got decision=%+v err=%v", decision, err)
	}
	if !authz.IsForbidden(svc.Require(ctx, userSubject("u1"), role.PermAgentDelete, authz.Target{})) {
		t.Fatal("require must return forbidden")
	}
}

func TestDecideAllowsGrantedPermission(t *testing.T) {
	svc := newTestSvc(t)
	ctx := context.Background()
	sub := userSubject("u1")

	if _, err := svc.Assign(ctx, sub, "role-coordinator", "test"); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if err := svc.Require(ctx, sub, role.PermAgentControl, authz.Target{}); err != nil {
		t.Fatalf("coordinator should control agents: %v", err)
	}
}

// --- 路由表 ---

func TestRouteTablePrefersExactOverPrefix(t *testing.T) {
	table := authz.DefaultRouteTable()

	// /api/v1/authorizations/me 是精确公开规则，不能被 /api/v1/authorizations/ 前缀抢走。
	rule, ok := table.Lookup(http.MethodGet, "/api/v1/authorizations/me")
	if !ok {
		t.Fatal("expected a rule for /authorizations/me")
	}
	if !rule.Public {
		t.Errorf("expected /authorizations/me to be public, got %+v", rule)
	}
}

func TestRouteTableMaskesAgentLifecycleActions(t *testing.T) {
	table := authz.DefaultRouteTable()

	// Agent 生命周期动作必须映射到 agent:control，而不是集合路径的 agent:create。
	rule, ok := table.Lookup(http.MethodPost, "/api/v1/agents/agent-1/start")
	if !ok {
		t.Fatal("expected a rule for agent lifecycle actions")
	}
	if rule.Action != role.PermAgentControl {
		t.Errorf("action = %s, want %s", rule.Action, role.PermAgentControl)
	}
	if rule.TargetType != "agent" {
		t.Errorf("target type = %q, want agent", rule.TargetType)
	}
}

func TestRouteTableDeniesUnregisteredPath(t *testing.T) {
	table := authz.DefaultRouteTable()
	if _, ok := table.Lookup(http.MethodGet, "/api/v1/definitely-not-registered"); ok {
		t.Fatal("unregistered path must not match a rule (default deny)")
	}
}

// --- 中间件判定 ---

// stubSvc 是可控的 Service 替身，用于覆盖中间件的各条分支。
type stubSvc struct {
	decision *authz.Decision
	err      error
	seen     []role.Permission
}

func (s *stubSvc) Assign(context.Context, authz.Subject, string, string) (*authz.Assignment, error) {
	return nil, nil
}
func (s *stubSvc) Revoke(context.Context, authz.Subject, string) error { return nil }
func (s *stubSvc) ListAssignments(context.Context, authz.Subject) ([]authz.Assignment, error) {
	return nil, nil
}
func (s *stubSvc) Authorize(context.Context, authz.Subject) (*authz.Authorization, error) {
	return nil, nil
}
func (s *stubSvc) Decide(_ context.Context, _ authz.Subject, action role.Permission, _ authz.Target) (*authz.Decision, error) {
	s.seen = append(s.seen, action)
	return s.decision, s.err
}
func (s *stubSvc) Require(context.Context, authz.Subject, role.Permission, authz.Target) error {
	return nil
}
func (s *stubSvc) Audit(context.Context, int) ([]authz.AuditEntry, error) { return nil, nil }
func (s *stubSvc) Stats(context.Context) (*authz.Stats, error)            { return nil, nil }

func serve(t *testing.T, svc authz.Service, method, path string, withIdentity bool) *httptest.ResponseRecorder {
	t.Helper()
	mw := authz.NewMiddleware(svc, authz.DefaultRouteTable())
	mw.ResolveSubject = func(*http.Request) authz.Subject {
		if !withIdentity {
			return authz.Subject{}
		}
		return userSubject("u1")
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	rec := httptest.NewRecorder()
	mw.Authorize(next).ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestMiddlewareAllowsGrantedRequest(t *testing.T) {
	svc := &stubSvc{decision: &authz.Decision{Allowed: true}}
	rec := serve(t, svc, http.MethodGet, "/api/v1/agents", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestMiddlewareDeniesWithoutIdentity(t *testing.T) {
	svc := &stubSvc{decision: &authz.Decision{Allowed: true}}
	rec := serve(t, svc, http.MethodGet, "/api/v1/agents", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestMiddlewareDeniesForbidden(t *testing.T) {
	svc := &stubSvc{decision: &authz.Decision{Allowed: false}}
	rec := serve(t, svc, http.MethodGet, "/api/v1/agents", true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestMiddlewareFailsClosedOnServiceError(t *testing.T) {
	svc := &stubSvc{err: authz.ErrUnavailable}
	rec := serve(t, svc, http.MethodGet, "/api/v1/agents", true)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (fail-closed)", rec.Code)
	}
}

func TestMiddlewareDeniesUnregisteredRoute(t *testing.T) {
	svc := &stubSvc{decision: &authz.Decision{Allowed: true}}
	rec := serve(t, svc, http.MethodGet, "/api/v1/whatever", true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for unregistered route", rec.Code)
	}
	if len(svc.seen) != 0 {
		t.Fatalf("service must not be consulted for unregistered routes, saw %v", svc.seen)
	}
}

func TestMiddlewareAllowsPublicRouteWithoutIdentity(t *testing.T) {
	svc := &stubSvc{}
	rec := serve(t, svc, http.MethodGet, "/health", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for public route", rec.Code)
	}
}

func TestMiddlewareErrorBodyIsJSON(t *testing.T) {
	svc := &stubSvc{decision: &authz.Decision{Allowed: false}}
	rec := serve(t, svc, http.MethodGet, "/api/v1/agents", true)
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if body["error"] == "" {
		t.Fatalf("error body missing message: %q", rec.Body.String())
	}
}

// --- 导出 ---

func TestDescribeRoute(t *testing.T) {
	pub := authz.DescribeRoute(authz.RouteRule{Method: http.MethodGet, Pattern: "/health", Public: true})
	if pub == "" {
		t.Fatal("describe public route returned empty")
	}
	got := authz.DescribeRoute(authz.RouteRule{Method: http.MethodGet, Pattern: "/api/v1/agents", Action: role.PermAgentRead})
	if got != "GET /api/v1/agents -> agent:read" {
		t.Fatalf("describe = %q", got)
	}
}

func TestAssignmentExpired(t *testing.T) {
	now := time.Now()
	pastAt := now.Add(-time.Minute)
	// 指针字段：nil 表示永不失效。
	past := &authz.Assignment{ExpiresAt: &pastAt}
	if !past.Expired(now) {
		t.Error("assignment with past expiry must be expired")
	}
	futureAt := now.Add(time.Minute)
	future := &authz.Assignment{ExpiresAt: &futureAt}
	if future.Expired(now) {
		t.Error("assignment with future expiry must not be expired")
	}
	never := &authz.Assignment{}
	if never.Expired(now) {
		t.Error("assignment without expiry must never expire")
	}
}
