package authz

import (
	"net/http"
	"testing"

	"github.com/atop0914/agentbot/internal/role"
)

// 本文件锁定一处实测发现的**权限错配**，防止回归。
//
// 起因：Day 27 引入多租户后，租户的「暂停/恢复」路由是
// `/api/v1/tenants/{id}/status`，它与已有的用户启停路由
// `/api/v1/users/{id}/status` 尾部完全相同。
//
// 旧的 Lookup 实现是「先找后缀规则、命中即返回」，两条 /status 规则
// 长度相同，胜者取决于登记顺序 —— 结果是租户暂停按 `user:activate`
// 判定。这类缺陷的危险之处在于：**不报错、不失败、权限判定悄悄换了
// 一个动作**，只有专门的断言才能发现。

func TestRouteTableDistinguishesTenantAndUserStatus(t *testing.T) {
	table := DefaultRouteTable()

	tenantRule, ok := table.Lookup(http.MethodPost, "/api/v1/tenants/t-1/status")
	if !ok {
		t.Fatal("/api/v1/tenants/{id}/status 必须已登记（否则默认拒绝 403）")
	}
	// 租户暂停是平台级元权限：它决定「谁能拥有资源」，
	// 与角色矩阵同级。若这里退化成 user:activate，说明被用户规则接走了。
	if tenantRule.Action != role.PermRoleManage {
		t.Fatalf("租户状态变更应为 role:manage，得到 %q —— 是否被用户 /status 规则抢走？", tenantRule.Action)
	}

	userRule, ok := table.Lookup(http.MethodPut, "/api/v1/users/u-1/status")
	if !ok {
		t.Fatal("/api/v1/users/{id}/status 必须已登记")
	}
	if userRule.Action != role.PermUserActivate {
		t.Fatalf("用户状态变更应为 user:activate，得到 %q", userRule.Action)
	}

	// 两条规则必须真的不同，否则上面的相等断言会同时通过而失去意义。
	if tenantRule.Action == userRule.Action {
		t.Fatal("租户与用户的 status 规则不得共用同一个权限动作")
	}
}

func TestRouteTableUserSubResourcesKeepTheirActions(t *testing.T) {
	table := DefaultRouteTable()
	cases := []struct {
		method string
		path   string
		want   role.Permission
	}{
		// 用户子资源必须由后缀规则接管，不能被更长的
		// /api/v1/users/ 前缀规则（user:update）吞掉。
		{http.MethodGet, "/api/v1/users/u1/status", role.PermUserRead},
		{http.MethodPut, "/api/v1/users/u1/status", role.PermUserActivate},
		{http.MethodPost, "/api/v1/users/u1/status", role.PermUserActivate},
		{http.MethodGet, "/api/v1/users/u1/roles", role.PermUserRead},
		{http.MethodPost, "/api/v1/users/u1/roles", role.PermRoleAssign},
		{http.MethodDelete, "/api/v1/users/u1/roles", role.PermRoleRevoke},
	}
	for _, tc := range cases {
		got, ok := table.Lookup(tc.method, tc.path)
		if !ok {
			t.Fatalf("%s %s 未登记", tc.method, tc.path)
		}
		if got.Action != tc.want {
			t.Fatalf("%s %s: action = %q, want %q", tc.method, tc.path, got.Action, tc.want)
		}
	}
}

func TestNamespaceRestrictsSuffixRule(t *testing.T) {
	table := NewRouteTable([]RouteRule{
		{Method: http.MethodPost, Pattern: "/status", Namespace: "/api/v1/tenants/", Action: role.PermRoleManage, Suffix: true},
		{Method: http.MethodPost, Pattern: "/status", Namespace: "/api/v1/users/", Action: role.PermUserActivate, Suffix: true},
	})

	// 命名空间命中：各自拿到自己的动作。
	r, ok := table.Lookup(http.MethodPost, "/api/v1/tenants/t1/status")
	if !ok || r.Action != role.PermRoleManage {
		t.Fatalf("租户命名空间应命中 role:manage，得到 %q ok=%v", r.Action, ok)
	}
	r, ok = table.Lookup(http.MethodPost, "/api/v1/users/u1/status")
	if !ok || r.Action != role.PermUserActivate {
		t.Fatalf("用户命名空间应命中 user:activate，得到 %q ok=%v", r.Action, ok)
	}

	// 命名空间不匹配：不得命中（否则 Namespace 形同虚设）。
	if _, ok := table.Lookup(http.MethodPost, "/api/v1/other/x/status"); ok {
		t.Fatal("命名空间外的路径不得被带 Namespace 的规则命中")
	}
}

func TestNamespaceRuleLosesToExactPath(t *testing.T) {
	table := NewRouteTable([]RouteRule{
		{Method: http.MethodPost, Pattern: "/status", Namespace: "/api/v1/tenants/", Action: role.PermRoleManage, Suffix: true},
		// 精确路径必须压过带命名空间的后缀规则。
		{Method: http.MethodPost, Pattern: "/api/v1/tenants/status", Action: role.PermUserRead},
	})
	r, ok := table.Lookup(http.MethodPost, "/api/v1/tenants/status")
	if !ok {
		t.Fatal("应命中精确规则")
	}
	if r.Action != role.PermUserRead {
		t.Fatalf("精确路径应优先，得到 %q", r.Action)
	}
}

func TestTenantRoutesAreRegistered(t *testing.T) {
	table := DefaultRouteTable()
	// 逐条确认 Day 27 新增的路由都已登记：漏登记的表现是 403 默认拒绝。
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/tenants"},
		{http.MethodPost, "/api/v1/tenants"},
		{http.MethodGet, "/api/v1/tenants/current"},
		{http.MethodGet, "/api/v1/tenants/t1"},
		{http.MethodGet, "/api/v1/tenants/t1/members"},
		{http.MethodGet, "/api/v1/tenants/t1/resources"},
		{http.MethodPost, "/api/v1/tenants/t1/status"},
	}
	for _, tc := range cases {
		if _, ok := table.Lookup(tc.method, tc.path); !ok {
			t.Fatalf("%s %s 未登记 —— 会落进默认拒绝返回 403", tc.method, tc.path)
		}
	}
}

func TestSSORoutesArePublic(t *testing.T) {
	table := DefaultRouteTable()
	// SSO 登录入口必须显式登记为 Public：未登记 = 403，
	// 前端表现为「点了登录按钮没反应」。
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/sso/authorize"},
		{http.MethodGet, "/api/v1/sso/callback"},
		{http.MethodPost, "/api/v1/sso/callback"},
		{http.MethodGet, "/api/v1/sso/status"},
	}
	for _, tc := range cases {
		r, ok := table.Lookup(tc.method, tc.path)
		if !ok {
			t.Fatalf("%s %s 未登记（SSO 入口将 403）", tc.method, tc.path)
		}
		if !r.Public {
			t.Fatalf("%s %s 必须登记为 Public，得到 action=%q", tc.method, tc.path, r.Action)
		}
	}
}
