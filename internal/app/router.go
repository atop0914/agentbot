package app

import (
	"encoding/json"
	"net/http"

	"github.com/atop0914/agentbot/internal/authz"
	"github.com/atop0914/agentbot/internal/tenant"
)

// NewRouter creates the HTTP handler with all routes wired.
func NewRouter(a *App) http.Handler {
	mux := http.NewServeMux()

	// Health check (unauthenticated)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Auth routes (public)
	a.AuthHandler.RegisterRoutes(mux)

	// Agent routes (authenticated)
	a.AgentH.RegisterRoutes(mux)

	// Cloud environment routes (authenticated)
	a.CloudH.RegisterRoutes(mux)

	// Task routes (authenticated)
	a.TaskMgr.RegisterRoutes(mux)

	// Communication routes (authenticated)
	a.CommH.RegisterRoutes(mux, "/api/v1")

	// WebSocket routes
	a.WSHandler.RegisterRoutes(mux, "/api/v1")

	// Browser automation routes
	a.BrowserH.RegisterRoutes(mux)

	// Terminal execution routes
	a.TerminalH.RegisterRoutes(mux)

	// Filesystem routes
	a.FileSystemH.RegisterRoutes(mux)

	// Application adapter routes
	a.AdapterH.RegisterRoutes(mux)

	// Memory system routes
	a.MemoryH.RegisterRoutes(mux)

	// Role system routes
	a.RoleH.RegisterRoutes(mux)

	// User administration (list / detail / activate-deactivate) — 管理后台
	a.UserAdminH.RegisterRoutes(mux)

	// Authorization chain (user/agent -> role -> permission)
	a.AuthzH.RegisterRoutes(mux)

	// Template & workflow recording routes
	a.TemplateH.RegisterRoutes(mux)

	// Marketplace routes
	a.MarketplaceH.RegisterRoutes(mux)

	// Agent health monitoring & alerts
	a.MonitorH.RegisterRoutes(mux)

	// Audit log & operation replay
	a.AuditH.RegisterRoutes(mux)

	// Network egress routing (policy + gateway + outbound traffic audit)
	a.EgressH.RegisterRoutes(mux)

	// Multi-tenant isolation (tenant lifecycle + members + resource ownership)
	a.TenantH.RegisterRoutes(mux)

	// Enterprise SSO / OIDC (authorize + callback + status)
	a.SSOH.RegisterRoutes(mux)

	// Admin console (aggregate view + static hosting for the React build)
	a.AdminH.RegisterRoutes(mux)

	// Apply global middleware chain: Recovery → RequestID → CORS → Logging
	var handler http.Handler = mux
	handler = loggingMiddleware(a.Logger)(handler)
	handler = corsMiddleware()(handler)
	handler = requestIDMiddleware()(handler)
	handler = recoveryMiddleware(a.Logger)(handler)

	// Permission enforcement sits inside the global chain so that a malformed
	// request is still recovered/logged, but outside the business handlers so
	// that no route can bypass it. Unregistered routes are denied by default.
	//
	// 认证不单独挂一层，而是作为 authz 的**内层**：只有路由表判定该路径需要
	// 权限时，才要求 token 并解析 claims。若把 RequireAuth 挂在外层，连
	// /api/v1/auth/register 这类公开路由都会被 401 拦掉。
	//
	// 租户作用域中间件。
	//
	// ⚠️ 顺序陷阱（实测踩过）：Go 的 `handler = mw(handler)` 是**由内向外**
	// 叠加 —— 最后执行的那次赋值在最外层、最先执行。因此租户中间件必须在
	// **authz/OptionalAuth 之前**完成包装，才能落在认证的**内层**，
	// 从而读到 OptionalAuth 刚写进 context 的 claims。
	//
	// 第一版把它放在认证之后包装（也就是放到了最外层），结果是
	// tenantIdentityFromRequest 永远读不到 claims，所有租户端点的
	// 现象都是「authenticated tenant identity required」—— 看起来像
	// 认证坏了，实际是中间件层序反了。
	//
	// required=false：无租户身份的请求仍然放行（公开路由与「已登录但未
	// 绑定租户」的过渡态），由业务层用 tenant.RequireScope 决定哪些入口
	// 必须处于作用域内。取舍是刻意的：旧路由不会因为新增中间件而整体
	// 401（那种失败最容易被当成故障排查掉）。
	if a.TenantSvc != nil {
		tenantMW := tenant.NewMiddleware(a.TenantSvc, tenantIdentityFromRequest, false)
		handler = tenantMW.Handler(handler)
	}

	// 权限与认证在租户作用域之外：claims 先被解析出来，租户层才能用。
	if a.AuthzSvc != nil {
		authzMW := authz.NewMiddleware(a.AuthzSvc, authz.DefaultRouteTable())
		authzMW.ResolveSubject = authzSubjectFromRequest
		// 关键顺序：OptionalAuth 必须在 authz 的**外层**。
		// Authorize 在主体为空时会直接返回 401 并短路，内层中间件永远不会执行；
		// 若把认证放在内层，受保护路由将恒返回 401（claims 从未被写入 context）。
		if a.AuthMW != nil {
			handler = authzMW.Authorize(handler)
			handler = a.AuthMW.OptionalAuth(handler)
		} else {
			handler = authzMW.Authorize(handler)
		}
	}

	return handler
}

// authzSubjectFromRequest 解析请求的授权主体。
//
// 当前策略：**一律以 JWT 中的 user_id 为准**。Agent 以自身身份调用平台 API 的场景
// 留到后续迭代（需要 Agent 身份令牌），在此之前不接受任何客户端自称的主体，
// 避免"改个 header 就提升权限"。
func authzSubjectFromRequest(r *http.Request) authz.Subject {
	return authz.SubjectFromClaims(r)
}
