package sso

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Handler 提供 SSO 登录的 HTTP 接口。
//
//	POST /api/v1/sso/authorize   发起登录，返回授权 URL（前端跳转）
//	GET  /api/v1/sso/callback    处理 IdP 回调（code + state）
//	GET  /api/v1/sso/status      返回该入口的配置状态（供前端决定是否展示按钮）
//
// 这组路由是**公开**的（登录本身不需要先登录），因此不能靠权限中间件
// 保护；它们的防护手段是协议层面的 state / nonce / PKCE，
// 以及「未配置一律 503」。
type Handler struct {
	svc Service
	// issueSession 把 SSO 账号换成平台自身的会话/令牌。
	//
	// 由装配层注入：sso 包不知道平台的 JWT 结构，也不该知道。
	// 返回 nil 时表示「只返回账号信息，不发令牌」（例如尚未接入账号体系）。
	issueSession func(ctx *http.Request, acct *Account) (interface{}, error)
	// bindTenant 可选：把 SSO 登录的账号落到某个租户（多租户联动）。
	bindTenant func(r *http.Request, acct *Account, tenantID string) error
}

// NewHandler 创建 SSO HTTP 处理器。
func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

// SetSessionIssuer 注入令牌签发函数（装配层调用）。
func (h *Handler) SetSessionIssuer(fn func(r *http.Request, acct *Account) (interface{}, error)) {
	h.issueSession = fn
}

// SetTenantBinder 注入租户绑定函数（装配层调用）。
func (h *Handler) SetTenantBinder(fn func(r *http.Request, acct *Account, tenantID string) error) {
	h.bindTenant = fn
}

// RegisterRoutes 注册 SSO 相关接口。
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// 集合路径无尾斜杠，权限表必须显式登记为 public，否则会落进默认拒绝。
	mux.HandleFunc("/api/v1/sso/authorize", h.handleAuthorize)
	mux.HandleFunc("/api/v1/sso/callback", h.handleCallback)
	mux.HandleFunc("/api/v1/sso/status", h.handleStatus)
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

// statusFor 把 SSO 错误映射为 HTTP 状态码。
//
// 注意 503 的用法：**未配置与 IdP 不可达都是 503**。
// 绝不能因为「配置读不到」就跳过校验 —— 那会把一次配置事故
// 变成一次认证绕过（authz 包从第一天起就是这个基线）。
func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrNotConfigured):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrAccountConflict):
		return http.StatusConflict
	case errors.Is(err, ErrInvalidState), errors.Is(err, ErrSessionExpired):
		// state 不匹配/会话过期：这是未授权的登录尝试，返回 401。
		return http.StatusUnauthorized
	case errors.Is(err, ErrInvalidToken), errors.Is(err, ErrEmailUnverified):
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
}

func (h *Handler) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.svc == nil || !h.svc.Configured() {
		// 未配置：显式 503，绝不返回一个「指向空地址的授权 URL」。
		writeErr(w, http.StatusServiceUnavailable, "sso provider is not configured")
		return
	}
	var req BeginRequest
	if r.Body != nil {
		// body 可选：空 body 也能发起登录（state/nonce 由服务端生成）。
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	res, err := h.svc.BeginAuth(r.Context(), req)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.svc == nil || !h.svc.Configured() {
		writeErr(w, http.StatusServiceUnavailable, "sso provider is not configured")
		return
	}

	cb := CallbackRequest{
		Code:  r.URL.Query().Get("code"),
		State: r.URL.Query().Get("state"),
	}
	// 兼容 form-encoded 回调（部分 IdP 用 POST 回调）。
	if cb.Code == "" && r.Method == http.MethodPost {
		_ = r.ParseForm()
		cb.Code = r.FormValue("code")
		cb.State = r.FormValue("state")
	}
	if strings.TrimSpace(cb.Code) == "" {
		// IdP 传回 error 参数时也要给出明确反馈，而不是含糊的 500。
		if e := r.URL.Query().Get("error"); e != "" {
			writeErr(w, http.StatusUnauthorized, "identity provider rejected the authorization request")
			return
		}
		writeErr(w, http.StatusBadRequest, "authorization code is required")
		return
	}

	acct, err := h.svc.HandleCallback(r.Context(), cb)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}

	// 多租户联动：把这次 SSO 登录的账号加入目标租户。
	if h.bindTenant != nil && acct != nil {
		tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
		if tenantID != "" {
			if err := h.bindTenant(r, acct, tenantID); err != nil {
				writeErr(w, http.StatusForbidden, "unable to bind the sso identity to the requested tenant")
				return
			}
		}
	}

	payload := map[string]interface{}{
		"account": acct,
	}
	if h.issueSession != nil {
		sess, err := h.issueSession(r, acct)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "unable to issue a platform session")
			return
		}
		payload["session"] = sess
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	configured := h.svc != nil && h.svc.Configured()
	out := map[string]interface{}{
		"configured": configured,
		// 暴露 provider 名（不含端点与 client id）：前端据此决定
		// 是否展示「用企业账号登录」入口。
		"provider": h.svc.Provider(),
	}
	writeJSON(w, http.StatusOK, out)
}
