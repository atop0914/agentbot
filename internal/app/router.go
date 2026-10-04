package app

import (
	"encoding/json"
	"net/http"

	"github.com/atop0914/agentbot/internal/authz"
)

// NewRouter creates the HTTP handler with all routes wired.
func NewRouter(a *App) http.Handler {
	mux := http.NewServeMux()

	// Health check (unauthenticated)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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

// jsonOK writes a JSON 200 response.
func jsonOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(data)
}

// jsonError writes a JSON error response.
func jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
