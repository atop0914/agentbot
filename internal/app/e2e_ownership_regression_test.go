package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/atop0914/agentbot/internal/authz"
	"github.com/atop0914/agentbot/internal/tenant"
)

// 本文件是 Day 29 全链路测试**暴露出来的真实缺陷**的回归测试。
//
// 缺陷：`registerTenantOwnership` 在装配层被定义了但**从未被调用**。
// 后果不是「少一条数据」，而是租户隔离边界上出现了一个洞：
//
//  1. 通过 HTTP 建出来的 Agent 没有任何租户归属，于是它不属于任何人；
//  2. `CountResources(ResourceAgent)` 恒为 0 —— 配额检查彻底失效；
//  3. 租户资源清单（/tenants/{id}/resources）看不到自己刚建的 Agent，
//     而跨租户检查对**所有人**返回 404 —— 连合法的所有者都访问不了它；
//  4. 症状表现为「建完就查不到」，最容易被误判成「创建失败」。
//
// 这个缺陷只在走真实 router + 中间件链时才会出现：单元测试直接调
// service.Create 根本不经过 handler，因此归属登记这条路径从未被执行。
// 正是 Day 29 要「把过程中暴露的真实缺陷修掉并补成回归测试」的意义所在。

// TestE2EAgentCreationRegistersTenantOwnership 是缺陷的直接回归。
//
// 断言的是「资源归属在 HTTP 链路上确实被登记了」，而不是
// 「service.Create 返回了 Agent」—— 后者在缺陷存在时同样为真。
func TestE2EAgentCreationRegistersTenantOwnership(t *testing.T) {
	ctx := context.Background()
	env := newE2EEnv(t)

	before := env.app.TenantSvc.CountResources(ctx, env.tenantID, tenant.ResourceAgent)

	agentResp := env.do(http.MethodPost, "/api/v1/agents",
		`{"name":"ownership-probe","description":"验证归属登记"}`)
	env.mustStatus(agentResp, http.StatusCreated, "建 Agent")
	var created struct {
		ID string `json:"id"`
	}
	env.decode(t, agentResp.Body.Bytes(), &created)

	after := env.app.TenantSvc.CountResources(ctx, env.tenantID, tenant.ResourceAgent)
	if after != before+1 {
		t.Fatalf("建 Agent 后租户资源计数应 +1（%d → %d），得到 %d —— "+
			"说明 registerTenantOwnership 未被调用，资源成了无归属的孤儿",
			before, before+1, after)
	}

	// 归属必须能被正向查出来（不只是计数对得上）。
	owner, err := env.app.TenantSvc.Owner(ctx, tenant.ResourceAgent, created.ID)
	if err != nil {
		t.Fatalf("查资源归属失败: %v（Agent 未被登记到任何租户）", err)
	}
	if owner.TenantID != env.tenantID {
		t.Fatalf("资源归属租户错误: got %q want %q", owner.TenantID, env.tenantID)
	}

	// 所有者必须能通过租户作用域访问到它 —— 这是「归属登记」的实际用途。
	scope, err := env.app.TenantSvc.Resolve(ctx, env.tenantID, env.userID)
	if err != nil {
		t.Fatalf("解析租户作用域失败: %v", err)
	}
	if _, err := scope.Owner(ctx, tenant.ResourceAgent, created.ID); err != nil {
		t.Fatalf("所有者应能访问自己租户下的 Agent: %v", err)
	}
}

// TestE2ETenantResourceListShowsCreatedAgent 从接口层面验证修复：
// 租户资源清单必须能看到自己刚建的 Agent。
//
// 这条走的是 /api/v1/tenants/{id}/resources，也就是用户能直接观察到的
// 那个入口 —— 缺陷在修复前会让这个清单永远是 0。
func TestE2ETenantResourceListShowsCreatedAgent(t *testing.T) {
	env := newE2EEnv(t)

	for i := 0; i < 2; i++ {
		resp := env.do(http.MethodPost, "/api/v1/agents",
			fmt.Sprintf(`{"name":"listed-%d","description":"d"}`, i))
		env.mustStatus(resp, http.StatusCreated, fmt.Sprintf("建第 %d 个 Agent", i))
	}

	rec := env.do(http.MethodGet,
		"/api/v1/tenants/"+string(env.tenantID)+"/resources", "")
	env.mustStatus(rec, http.StatusOK, "查租户资源清单")

	var body struct {
		TenantID  string         `json:"tenant_id"`
		Resources map[string]int `json:"resources"`
	}
	env.decode(t, rec.Body.Bytes(), &body)

	if body.TenantID != string(env.tenantID) {
		t.Errorf("资源清单的租户 ID 错误: got %q", body.TenantID)
	}
	if body.Resources["agent"] != 2 {
		t.Fatalf("资源清单应显示 2 个 Agent，得到 %d —— "+
			"建完就查不到说明归属登记缺失: %s", body.Resources["agent"], rec.Body.String())
	}
}

// TestE2EAgentOwnershipIsPerTenant 验证归属登记是按租户隔离的：
// 一个租户建 Agent 不会抬高另一个租户的资源计数。
//
// 这条防的是「修 bug 时引入更宽的洞」——如果实现里写死了某个租户 ID
// 或者用全局计数，A 租户的资源会出现在 B 租户的清单里，
// 那是比原缺陷更严重的越权。
func TestE2EAgentOwnershipIsPerTenant(t *testing.T) {
	ctx := context.Background()
	env := newE2EEnv(t)

	// 另建一个租户，它不该看到主租户的 Agent。
	other, err := env.app.TenantSvc.Create(ctx, tenant.CreateRequest{
		Name: "other-corp", Slug: "other-corp", Plan: tenant.PlanTeam, OwnerID: "other-owner",
	})
	if err != nil {
		t.Fatalf("建第二个租户失败: %v", err)
	}

	env.mustStatus(env.do(http.MethodPost, "/api/v1/agents",
		`{"name":"tenant-a-agent","description":"d"}`), http.StatusCreated, "建 Agent")

	if n := env.app.TenantSvc.CountResources(ctx, env.tenantID, tenant.ResourceAgent); n != 1 {
		t.Errorf("主租户资源计数应为 1，得到 %d", n)
	}
	if n := env.app.TenantSvc.CountResources(ctx, other.ID, tenant.ResourceAgent); n != 0 {
		t.Errorf("另一个租户不应看到主租户的 Agent，计数得到 %d", n)
	}
}

// TestE2EPlatformLevelAgentHasNoTenantOwner 钉住修复实现的边界语义：
// 无租户作用域的平台级操作为 Agent 建出来后**不**登记归属。
//
// 这是刻意的：凭空挑一个租户登记等于把资源塞进别人家。
// 未归属的资源在隔离语义下对所有人不可见 —— 这个取舍是明确的，
// 因此用测试把它固定下来，避免后来者「顺手」改成一个隐式的默认租户。
func TestE2EPlatformLevelAgentHasNoTenantOwner(t *testing.T) {
	ctx := context.Background()
	a := New()
	router := NewRouter(a)

	// 平台管理员令牌：有权限、但**无**租户声明。
	adminToken := issueRawToken(t, a, "platform-admin", "")
	subject := authz.Subject{Type: authz.SubjectUser, ID: "platform-admin"}
	if _, err := a.AuthzSvc.Assign(ctx, subject, "role-coordinator", "test"); err != nil {
		t.Fatalf("授予角色失败: %v", err)
	}

	rec := doToken(t, router, http.MethodPost, "/api/v1/agents",
		`{"name":"platform-agent","description":"d"}`, adminToken)
	if rec.Code != http.StatusCreated {
		t.Fatalf("平台管理员建 Agent 应 201，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	// 无归属：查 Owner 必须失败（而不是落到某个隐式租户）。
	if _, err := a.TenantSvc.Owner(ctx, tenant.ResourceAgent, created.ID); err == nil {
		t.Fatal("平台级操作建出的 Agent 不应被登记到任何租户（凭空挑一个租户＝塞进别人家）")
	}
}
