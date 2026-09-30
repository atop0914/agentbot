package user

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// 用户管理接口的默认分页参数。
const (
	DefaultAdminPageSize = 20
	MaxAdminPageSize     = 200
)

// AdminHandler 提供管理后台的用户管理接口。
//
// 与 auth.Handler 的关注点区分：
//   - auth.Handler 负责「用户自己的事」（注册、登录、刷新 token）；
//   - AdminHandler 负责「管理员对用户的事」（分页列表、详情、启停、删除）。
//
// 权限判定不在这里重做，而是由 authz 中间件按路径 /api/v1/users/* 统一拦截，
// 保证「权限规则只有一处」（见 internal/authz/middleware.go 的 RouteTable）。
type AdminHandler struct {
	svc Service
	// assignments 返回某个用户的角色分配详情（由装配层用 authz 实现，可为 nil）。
	assignments func(userID string) ([]map[string]interface{}, error)
}

// NewAdminHandler 创建用户管理 HTTP 处理器。
func NewAdminHandler(svc Service) *AdminHandler {
	return &AdminHandler{svc: svc}
}

// SetAssignmentLister 注入角色分配查询函数。
//
// 用函数注入而不是直接依赖 authz 包，避免 user ←→ authz 的循环依赖。
func (h *AdminHandler) SetAssignmentLister(fn func(userID string) ([]map[string]interface{}, error)) {
	h.assignments = fn
}

// RegisterRoutes 注册用户管理路由。
//
//	GET    /api/v1/users                 分页列出用户
//	GET    /api/v1/users/{id}            用户详情（含角色分配）
//	POST   /api/v1/users/{id}/status     启用 / 停用 / 封禁
//	DELETE /api/v1/users/{id}            删除用户
func (h *AdminHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/users", h.handleUsers)
	mux.HandleFunc("/api/v1/users/", h.handleUserByID)
}

func (h *AdminHandler) handleUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	page, err := ParsePagination(r.URL.Query(), DefaultAdminPageSize, MaxAdminPageSize)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	users, total, err := h.svc.List(page.Offset, page.Limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// 服务在合法请求下必须返回非 nil 切片，避免前端拿到 null。
	if users == nil {
		users = []*User{}
	}

	views := make([]UserView, 0, len(users))
	for _, u := range users {
		if u == nil {
			continue
		}
		views = append(views, ToView(u))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"users":  views,
		"total":  total,
		"offset": page.Offset,
		"limit":  page.Limit,
		"page":   page.Number,
	})
}

func (h *AdminHandler) handleUserByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/users/"), "/")
	if rest == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user id is required"})
		return
	}
	parts := strings.Split(rest, "/")
	id := parts[0]

	// 子资源：/api/v1/users/{id}/status
	if len(parts) >= 2 {
		switch parts[1] {
		case "status":
			h.handleStatus(w, r, id)
			return
		case "roles", "assignments":
			h.handleAssignments(w, r, id)
			return
		default:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown sub-resource: " + parts[1]})
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		h.getUser(w, r, id)
	case http.MethodDelete:
		if err := h.svc.Delete(id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *AdminHandler) getUser(w http.ResponseWriter, r *http.Request, id string) {
	u, err := h.svc.GetByID(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if u == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}

	resp := map[string]interface{}{"user": ToView(u)}
	if h.assignments != nil {
		list, err := h.assignments(id)
		if err != nil {
			// 角色信息取不到不影响用户详情本身，但要如实告知。
			resp["assignments_error"] = err.Error()
		} else {
			if list == nil {
				list = []map[string]interface{}{}
			}
			resp["assignments"] = list
			resp["role_count"] = len(list)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// SetStatusRequest 是启用/停用请求体。
//
// 同时接受 status 与 action 两种写法：
//
//	{"status":"inactive"} 或 {"action":"deactivate"}
//
// 后者更贴近管理员的心智模型，前者更适合脚本调用。
type SetStatusRequest struct {
	Status string `json:"status"`
	Action string `json:"action"`
}

func (h *AdminHandler) handleStatus(w http.ResponseWriter, r *http.Request, id string) {
	// POST 切换状态；GET 查询当前状态（避免必须拉整个用户详情）。
	switch r.Method {
	case http.MethodGet:
		u, err := h.svc.GetByID(id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if u == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"id":     u.ID,
			"status": u.Status,
			"usable": u.Status.IsUsable(),
		})
	case http.MethodPost, http.MethodPut:
		var req SetStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		status, err := ResolveStatus(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		u, err := h.svc.SetStatus(id, status)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"user":   ToView(u),
			"status": u.Status,
			"usable": u.Status.IsUsable(),
		})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *AdminHandler) handleAssignments(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if h.assignments == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"user_id":     id,
			"assignments": []map[string]interface{}{},
			"total":       0,
			"note":        "assignment lister is not configured",
		})
		return
	}
	list, err := h.assignments(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if list == nil {
		list = []map[string]interface{}{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user_id":     id,
		"assignments": list,
		"total":       len(list),
	})
}

// ResolveStatus 把请求体解析为合法状态。
//
// 同时支持 status 与 action，但两者都为空时**必须报错**：
// 「没写状态」不能被当成「启用」，否则一个字段拼写错误就等于误操作放行。
func ResolveStatus(req SetStatusRequest) (Status, error) {
	if raw := strings.TrimSpace(req.Status); raw != "" {
		s := Status(strings.ToLower(raw))
		if !s.Valid() {
			return "", errors.New("invalid status: " + raw)
		}
		return s, nil
	}
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "activate", "enable", "enable_user":
		return StatusActive, nil
	case "deactivate", "disable", "disable_user", "suspend":
		return StatusInactive, nil
	case "ban", "ban_user":
		return StatusBanned, nil
	}
	return "", errors.New("status or action is required (activate|deactivate|ban)")
}

// Pagination 是分页参数。
type Pagination struct {
	Offset int
	Limit  int
	Number int // 从 1 开始的页码（仅用于回显）
}

// ParsePagination 解析分页参数。
//
// 支持两种写法：
//
//	offset/limit  —— 直接指定偏移与条数（后台默认）
//	page/size     —— 页码与每页条数（更贴近人工翻页）
func ParsePagination(values url.Values, defaultSize, maxSize int) (Pagination, error) {
	if defaultSize <= 0 {
		defaultSize = DefaultAdminPageSize
	}
	if maxSize <= 0 {
		maxSize = MaxAdminPageSize
	}

	var p Pagination
	p.Limit = defaultSize

	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return p, errors.New("limit must be an integer")
		}
		if n <= 0 {
			return p, errors.New("limit must be positive")
		}
		p.Limit = n
	}
	if raw := strings.TrimSpace(values.Get("size")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return p, errors.New("size must be an integer")
		}
		if n <= 0 {
			return p, errors.New("size must be positive")
		}
		p.Limit = n
	}
	if p.Limit > maxSize {
		p.Limit = maxSize
	}

	if raw := strings.TrimSpace(values.Get("offset")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return p, errors.New("offset must be an integer")
		}
		if n < 0 {
			return p, errors.New("offset must not be negative")
		}
		p.Offset = n
	}
	if raw := strings.TrimSpace(values.Get("page")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return p, errors.New("page must be an integer")
		}
		if n < 1 {
			return p, errors.New("page must be >= 1")
		}
		p.Offset = (n - 1) * p.Limit
		p.Number = n
	} else {
		p.Number = p.Offset/p.Limit + 1
	}

	return p, nil
}

// UserView 是用户的管理后台视图（不含任何凭据字段）。
type UserView struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Username      string `json:"username"`
	Avatar        string `json:"avatar,omitempty"`
	Role          Role   `json:"role"`
	Status        Status `json:"status"`
	Usable        bool   `json:"usable"`
	OAuthProvider string `json:"oauth_provider,omitempty"`
	LastLoginAt   string `json:"last_login_at,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// ToView 把用户模型转换为后台视图。
//
// 明确不暴露 PasswordHash / OAuthID：后台列表是最容易被拿来"顺手看一眼"的地方，
// 凭据字段绝不能出现在这里（呼应 research.md 里 CSA 统计的 AI 密钥泄漏问题）。
func ToView(u *User) UserView {
	if u == nil {
		return UserView{}
	}
	view := UserView{
		ID:            u.ID,
		Email:         u.Email,
		Username:      u.Username,
		Avatar:        u.Avatar,
		Role:          u.Role,
		Status:        u.Status,
		Usable:        u.Status.IsUsable(),
		OAuthProvider: u.OAuthProvider,
		CreatedAt:     u.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:     u.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if u.LastLoginAt != nil {
		view.LastLoginAt = u.LastLoginAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return view
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}
