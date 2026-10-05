package tenant

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Handler 提供租户管理的 HTTP 接口。
//
//	GET    /api/v1/tenants                     列出全部租户（平台管理员）
//	POST   /api/v1/tenants                     创建租户
//	GET    /api/v1/tenants/{id}                租户详情
//	POST   /api/v1/tenants/{id}/status         暂停 / 恢复租户
//	GET    /api/v1/tenants/{id}/members        成员列表
//	POST   /api/v1/tenants/{id}/members        添加成员
//	DELETE /api/v1/tenants/{id}/members/{uid}  移除成员
//	GET    /api/v1/tenants/{id}/resources      租户资源归属清单（自查边界）
//	GET    /api/v1/tenants/current             当前请求所属租户（自查）
//	GET    /api/v1/tenants/stats               租户汇总
//
// 注意这些是**无尾斜杠的集合路径**，前缀权限规则覆盖不到，权限表必须
// 逐条登记，否则会落进默认拒绝返回 403（Day 24 已经踩过这个坑）。
type Handler struct {
	svc Service
	// resolve 从请求解析可信租户身份；由装配层注入（读 JWT claims）。
	resolve IdentityResolver
}

// NewHandler 创建租户 HTTP 处理器。
func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

// SetIdentityResolver 注入身份解析器（装配层调用）。
func (h *Handler) SetIdentityResolver(r IdentityResolver) { h.resolve = r }

// RegisterRoutes 注册租户相关接口。
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/tenants", h.handleTenants)
	mux.HandleFunc("/api/v1/tenants/", h.handleTenantByID)
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// handleTenants 处理集合路径：GET 列表 / POST 创建 / GET stats。
func (h *Handler) handleTenants(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// 支持 ?stats=1 直接返回汇总，省一条路由（集合路径已登记权限）。
		if r.URL.Query().Get("stats") == "1" {
			st, err := h.svc.Stats(r.Context())
			if err != nil {
				writeErr(w, statusFor(err), err.Error())
				return
			}
			writeJSON(w, http.StatusOK, st)
			return
		}
		list, err := h.svc.List(r.Context())
		if err != nil {
			writeErr(w, statusFor(err), err.Error())
			return
		}
		if list == nil {
			list = []*Tenant{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"tenants": list, "count": len(list)})
	case http.MethodPost:
		var req CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		t, err := h.svc.Create(r.Context(), req)
		if err != nil {
			writeErr(w, statusFor(err), err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, t)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleTenantByID 处理 /api/v1/tenants/{id}[/{sub}/{...}]。
func (h *Handler) handleTenantByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/tenants/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		writeErr(w, http.StatusBadRequest, "tenant id is required")
		return
	}
	parts := strings.Split(rest, "/")
	id := ID(parts[0])

	// /api/v1/tenants/current —— 返回当前请求的租户作用域（自查端点）。
	//
	// 这个端点刻意**只**读 context 里的作用域，不读任何请求参数：
	// 它存在的意义就是让使用者能确认「服务端认为我是哪个租户」。
	if id == "current" {
		scope := ScopeFromContext(r.Context())
		if scope == nil {
			writeErr(w, http.StatusUnauthorized, "authenticated tenant identity required")
			return
		}
		st, err := h.svc.StatsFor(r.Context(), scope.TenantID())
		if err != nil {
			writeErr(w, statusFor(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"tenant":    scope.Tenant(),
			"user_id":   scope.UserID(),
			"resources": st.Resources,
			"members":   st.TotalMembers,
		})
		return
	}

	switch {
	case len(parts) == 1:
		h.handleSingle(w, r, id)
	case len(parts) == 2 && parts[1] == "status":
		h.handleStatus(w, r, id)
	case len(parts) == 2 && parts[1] == "members":
		h.handleMembers(w, r, id)
	case len(parts) == 2 && parts[1] == "resources":
		h.handleResources(w, r, id)
	case len(parts) == 3 && parts[1] == "members":
		h.handleMemberByID(w, r, id, parts[2])
	default:
		writeErr(w, http.StatusNotFound, "resource not found")
	}
}

func (h *Handler) handleSingle(w http.ResponseWriter, r *http.Request, id ID) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// 非平台管理员也可以查自己所属租户的详情：调用方传入的 ID 会与
	// 请求作用域比对，跨租户查询返回 404。
	if scope := ScopeFromContext(r.Context()); scope != nil && scope.TenantID() != id {
		t, err := h.svc.Get(r.Context(), id)
		if err != nil || t == nil {
			writeErr(w, http.StatusNotFound, "resource not found")
			return
		}
		if !h.isPlatformAdmin(r) {
			// 与「不存在」同形，不泄漏租户是否存在。
			writeErr(w, http.StatusNotFound, "resource not found")
			return
		}
	}
	t, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request, id ID) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Status Status `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	t, err := h.svc.UpdateStatus(r.Context(), id, body.Status)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handler) handleMembers(w http.ResponseWriter, r *http.Request, id ID) {
	switch r.Method {
	case http.MethodGet:
		members, err := h.svc.ListMembers(r.Context(), id)
		if err != nil {
			writeErr(w, statusFor(err), err.Error())
			return
		}
		if members == nil {
			members = []*Member{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"members": members, "count": len(members)})
	case http.MethodPost:
		var body struct {
			UserID string `json:"user_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		invitedBy := ""
		if scope := ScopeFromContext(r.Context()); scope != nil {
			invitedBy = scope.UserID()
		}
		m, err := h.svc.AddMember(r.Context(), id, body.UserID, invitedBy)
		if err != nil {
			writeErr(w, statusFor(err), err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, m)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleMemberByID(w http.ResponseWriter, r *http.Request, id ID, userID string) {
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := h.svc.RemoveMember(r.Context(), id, userID); err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// handleResources 返回租户的资源归属清单。
//
// 存在的意义是「让边界可自查」：隔离最怕的是「某类资源忘了登记归属」，
// 那样它既不属于任何人、也永远不会出现在任何租户的清单里，静默地
// 变成一个谁都能碰（或谁都不能碰）的孤儿。这个端点让这类遗漏可见。
func (h *Handler) handleResources(w http.ResponseWriter, r *http.Request, id ID) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// 只允许查自己所属的租户，跨租户一律 404。
	if scope := ScopeFromContext(r.Context()); scope == nil || scope.TenantID() != id {
		writeErr(w, http.StatusNotFound, "resource not found")
		return
	}
	out := map[string]int{}
	for _, rt := range []ResourceType{ResourceAgent, ResourceTask, ResourceAudit, ResourceEgressRule, ResourceEnvironment} {
		out[string(rt)] = h.svc.CountResources(r.Context(), id, rt)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"tenant_id": id, "resources": out})
}

// isPlatformAdmin 判断调用方是否是平台管理员。
//
// 目前以「请求作用域租户为空但通过了权限中间件」为准的保守实现：
// 具体的平台管理员判定由权限链负责，这里不重复发明一套。
func (h *Handler) isPlatformAdmin(r *http.Request) bool {
	return ScopeFromContext(r.Context()) == nil
}

// statusFor 把租户错误映射为 HTTP 状态码（与中间件保持一致）。
func statusFor(err error) int {
	switch {
	case isErr(err, ErrIdentityRequired):
		return http.StatusUnauthorized
	case isErr(err, ErrNotFound), isErr(err, ErrSuspended):
		return http.StatusNotFound
	case isErr(err, ErrUnavailable):
		return http.StatusServiceUnavailable
	case isErr(err, ErrQuotaExceeded):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}
