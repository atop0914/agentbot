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

	// Metrics endpoint (Day 3-7). 与 /health 同级，**匿名可达** ——
	// 抓取端不持有本平台的 JWT，要求认证会让指标形同虚设；
	// 导出内容只有计数与延迟分布，不含用户数据（见 metrics_wiring.go 的取舍说明）。
	mux.Handle(a.MetricsPath(), a.MetricsH)

	// Apply global middleware chain. Go 的包装是**由内向外**叠加：
	// 最后执行的那次赋值在最外层、最先执行。因此实际执行顺序为
	//   Metrics → Recovery → RequestID → CORS → Logging → Tenant → Auth → Authz → mux
	// 每一层的顺序都有理由，改动前请读旁边注释（尤其 metrics 必须在最外层）。
	var handler http.Handler = mux
	handler = loggingMiddleware(a.Logger)(handler)
	handler = corsMiddleware()(handler)
	handler = requestIDMiddleware()(handler)
	handler = recoveryMiddleware(a.Logger, a.httpMetrics)(handler)

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
		// ⚠️ 必须把**配置的**指标路径传进权限表，不能依赖表里写死的 "/metrics"。
		// 指标路径是配置项（AGENTBOT_METRICS_PATH），改配置后路由会挂到新路径，
		// 而权限表若还只认老路径，新路径就落进默认拒绝 → 403 ——
		// 表现为「按配置改了路径，指标端点整个不可用」。
		// 这是「同一件事被两处各存一份」的又一例：改一处不报错，
		// 只是那一处继续按旧值回答。
		authzMW := authz.NewMiddleware(a.AuthzSvc, authz.PublicRouteTable([]string{a.MetricsPath()}))
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

	// ⚠️ metricsMiddleware 必须挂在**最外层**（最后一层包装 = 最先执行）。
	//
	// 第一版把它紧贴 mux（最内层），理由是「只统计业务耗时」。
	// 实测这个位置有一个致命缺陷：authz 中间件对未授权请求会**直接写
	// 401/403 并 return，不调用 next**（fail-closed 短路），
	// 于是所有被拒绝的请求都到不了内层 —— /metrics 里根本没有这些 series。
	//
	// 症状极具误导性：curl 手测返回 403，但 route="/api/v1/agents" 在
	// 导出文本里**完全不存在**。后果是**错误率恒为 0**：
	// 一个永远看不见失败的监控，比没有监控更危险 —— 它会让运维确信
	// 「没有 5xx」。
	//
	// 挂最外层后观测到的是「客户端实际经历的总耗时」，这正是排障时
	// 要回答的问题（业务耗时可以从业务自己的指标里看）。
	// 任何会短路返回错误而不调用 next 的中间件，它的内层都观测不到它 ——
	// 这是中间件分层最容易踩的一个坑。
	handler = metricsMiddleware(a.httpMetrics)(handler)

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
