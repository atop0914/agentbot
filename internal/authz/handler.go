package authz

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Handler 暴露授权链的 HTTP 接口。
//
//	GET    /api/v1/authorizations/me                     查看自己的权限链
//	GET    /api/v1/authorizations/{subjectType}/{id}      查看指定主体的权限链（需 role:manage）
//	POST   /api/v1/authorizations                         授予角色（需 role:assign）
//	DELETE /api/v1/authorizations?subject_type=&subject_id=&role_id=  撤销角色（需 role:revoke）
//	GET    /api/v1/authorization-audit                    最近判定记录（需 role:manage）
//	GET    /api/v1/authorization-stats                    授权链统计（需 role:manage）
//	GET    /api/v1/permission-routes                      路由权限矩阵（需 role:manage）
type Handler struct {
	svc   Service
	table *RouteTable
	// listUsers 供管理后台在授权列表里补全用户信息（可为 nil）。
	resolveSubjectName func(subjectType, id string) string
}

// NewHandler 创建授权链 HTTP 处理器。
func NewHandler(svc Service, table *RouteTable) *Handler {
	if table == nil {
		table = DefaultRouteTable()
	}
	return &Handler{svc: svc, table: table}
}

// SetSubjectNameResolver 注入「主体 ID → 可读名字」的解析函数。
func (h *Handler) SetSubjectNameResolver(fn func(subjectType, id string) string) {
	h.resolveSubjectName = fn
}

// RegisterRoutes 注册授权链路由。
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/authorizations", h.handleAuthorizations)
	mux.HandleFunc("/api/v1/authorizations/", h.handleAuthorizationByPath)
	mux.HandleFunc("/api/v1/authorization-audit", h.handleAudit)
	mux.HandleFunc("/api/v1/authorization-stats", h.handleStats)
	mux.HandleFunc("/api/v1/permission-routes", h.handleRouteRules)
}

func (h *Handler) handleAuthorizations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.assign(w, r)
	case http.MethodDelete:
		h.revoke(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// AssignRequest 是授予角色的请求体。
type AssignRequest struct {
	SubjectType string `json:"subject_type"` // user / agent
	SubjectID   string `json:"subject_id"`
	RoleID      string `json:"role_id"`
}

func (h *Handler) assign(w http.ResponseWriter, r *http.Request) {
	var req AssignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	subject := Subject{Type: SubjectType(req.SubjectType), ID: strings.TrimSpace(req.SubjectID)}
	if subject.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "subject_type (user|agent) and subject_id are required"})
		return
	}

	assignment, err := h.svc.Assign(r.Context(), subject, req.RoleID, grantedByFromRequest(r))
	if err != nil {
		writeJSON(w, statusForError(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, assignment)
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	subject := Subject{Type: SubjectType(q.Get("subject_type")), ID: strings.TrimSpace(q.Get("subject_id"))}
	if subject.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "subject_type and subject_id are required"})
		return
	}
	roleID := strings.TrimSpace(q.Get("role_id"))
	if roleID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "role_id is required"})
		return
	}
	if err := h.svc.Revoke(r.Context(), subject, roleID); err != nil {
		writeJSON(w, statusForError(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// handleAuthorizationByPath 处理 /api/v1/authorizations/... 下的读操作。
//
//	/me                              当前登录用户的权限链
//	/{subjectType}/{id}              指定主体的权限链
//	/{subjectType}/{id}/assignments  仅授予记录
func (h *Handler) handleAuthorizationByPath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/authorizations/"), "/")
	parts := strings.Split(rest, "/")

	if parts[0] == "me" {
		subject := subjectFromRequest(r)
		if subject.IsZero() {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated identity required"})
			return
		}
		h.writeAuthorization(w, r, subject, "self")
		return
	}

	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected /api/v1/authorizations/{subject_type}/{id}"})
		return
	}
	subject := Subject{Type: SubjectType(parts[0]), ID: parts[1]}
	if subject.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid subject type"})
		return
	}

	wantAssignmentsOnly := len(parts) >= 3 && parts[2] == "assignments"
	if wantAssignmentsOnly {
		list, err := h.svc.ListAssignments(r.Context(), subject)
		if err != nil {
			writeJSON(w, statusForError(err), map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"subject":     subject,
			"assignments": h.decorate(list),
			"total":       len(list),
		})
		return
	}
	h.writeAuthorization(w, r, subject, "admin")
}

func (h *Handler) writeAuthorization(w http.ResponseWriter, r *http.Request, subject Subject, scope string) {
	auth, err := h.svc.Authorize(r.Context(), subject)
	if err != nil {
		writeJSON(w, statusForError(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"scope":         scope,
		"subject":       auth.Subject,
		"subject_name":  h.subjectName(auth.Subject),
		"assignments":   h.decorate(auth.Assignments),
		"roles":         auth.Roles,
		"permissions":   auth.Permissions,
		"by_permission": auth.ByPermission,
		"updated_at":    auth.UpdatedAt,
	})
}

// handleAudit 返回最近的权限判定记录。
func (h *Handler) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be a non-negative integer"})
			return
		}
		limit = n
	}
	entries, err := h.svc.Audit(r.Context(), limit)
	if err != nil {
		writeJSON(w, statusForError(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entries": entries,
		"total":   len(entries),
	})
}

func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	stats, err := h.svc.Stats(r.Context())
	if err != nil {
		writeJSON(w, statusForError(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (h *Handler) handleRouteRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	rules := h.table.Rules()
	out := make([]map[string]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, map[string]string{
			"method":      rule.Method,
			"pattern":     rule.Pattern,
			"action":      string(rule.Action),
			"target_type": rule.TargetType,
			"public":      strconv.FormatBool(rule.Public),
			"description": DescribeRoute(rule),
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"rules": out,
		"total": len(out),
	})
}

// decorate 给授予记录补上主体可读名，便于后台直接展示。
func (h *Handler) decorate(assignments []Assignment) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(assignments))
	for i := range assignments {
		a := assignments[i]
		out = append(out, map[string]interface{}{
			"id":           a.ID,
			"subject":      a.Subject,
			"subject_name": h.subjectName(a.Subject),
			"role_id":      a.RoleID,
			"role_name":    a.RoleName,
			"granted_by":   a.GrantedBy,
			"granted_at":   a.GrantedAt,
			"expires_at":   a.ExpiresAt,
			"expired":      a.Expired(nowUTC()),
		})
	}
	return out
}

func (h *Handler) subjectName(subject Subject) string {
	if h.resolveSubjectName == nil {
		return subject.ID
	}
	if name := h.resolveSubjectName(string(subject.Type), subject.ID); name != "" {
		return name
	}
	return subject.ID
}

// subjectFromRequest 从请求上下文取当前登录用户（复用中间件的解析规则）。
func subjectFromRequest(r *http.Request) Subject {
	return SubjectFromClaims(r)
}

// grantedByFromRequest 记录"是谁授予的"，用于事后追责。
func grantedByFromRequest(r *http.Request) string {
	if subject := SubjectFromClaims(r); !subject.IsZero() {
		return subject.Key()
	}
	return "system"
}

// statusForError 把授权错误映射为合适的 HTTP 状态码。
func statusForError(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case IsUnavailable(err):
		return http.StatusServiceUnavailable
	case IsForbidden(err):
		return http.StatusForbidden
	default:
		return http.StatusBadRequest
	}
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}
