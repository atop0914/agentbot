package auth

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/atop0914/agentbot/internal/pkg/errors"
)

// Handler 认证 HTTP 处理器
type Handler struct {
	service AuthService
	// loginGuard 是登录暴力破解防护；nil 表示不限流。
	//
	// 设计成可选的（nil 即不限流），因为限流依赖共享存储 ——
	// 未配置 Redis 的部署没有跨副本计数能力。此时宁愿不限流，
	// 也不要退回一个「多副本下等于没限」的假限流。
	loginGuard *LoginGuard
	// logger 用于记录 fail-open 事件。
	logger *slog.Logger
}

// NewHandler 创建认证处理器
func NewHandler(service AuthService) *Handler {
	return &Handler{service: service}
}

// WithLoginGuard 挂上登录限流。
//
// 单独一个 Option 而不是塞进 NewHandler 的参数列表：
// 现有调用方（含大量测试）不该因为一个可选能力而全部改签名。
func (h *Handler) WithLoginGuard(g *LoginGuard, logger *slog.Logger) *Handler {
	h.loginGuard = g
	h.logger = logger
	return h
}

// Register 处理用户注册
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// 验证必填字段
	if req.Email == "" || req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email, username and password are required")
		return
	}

	resp, err := h.service.Register(&req)
	if err != nil {
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
	}

	writeJSON(w, http.StatusCreated, resp)
}

// Login 处理用户登录
//
// 暴力破解防护（本轮新增）：失败次数按账号 + 来源 IP 两个维度
// 在共享存储里计数，任一超限返回 429 并带 Retry-After。
// 细节见 loginguard.go —— 特别是「只对失败计数」这个刻意的取舍。
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	// 限流判定在**解析凭据之后、验证之前**：越早拒绝越省资源，
	// 但必须等拿到 email 才能按账号维度计数。
	//
	// 放在验证之前还有一个安全理由：若放在之后，被限流的请求
	// 仍会完整执行一次密码哈希校验（bcrypt 是 CPU 密集型），
	// 于是「限流」本身变成了放大 CPU 负载的手段。
	if h.loginGuard != nil {
		retryAfter, gerr := h.loginGuard.Check(req.Email, r)
		if gerr != nil {
			// fail-open：限流器故障不应导致无人能登录。
			// 只记日志，不改变响应 —— 但必须留下痕迹，
			// 否则「限流一直没生效」无人知晓。
			h.logRateLimiterFailure(r, gerr)
		} else if retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			writeError(w, http.StatusTooManyRequests,
				"too many login attempts, please try again later")
			return
		}
	}

	resp, err := h.service.Login(&req)
	if err != nil {
		// 只在**失败**时计数（见 loginguard.go 的取舍说明）。
		// 成功不计数，避免共享出口 IP 的办公室被误锁。
		if h.loginGuard != nil {
			if rerr := h.loginGuard.RecordFailure(req.Email, r); rerr != nil {
				h.logRateLimiterFailure(r, rerr)
			}
		}
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
	}

	// 成功即清零该账号的失败计数：用户已证明自己是本人，
	// 继续按之前的失败次数惩罚他没有任何意义
	// （否则「输错 9 次后终于想起来密码」的用户下一次仍会被锁）。
	if h.loginGuard != nil {
		h.loginGuard.ClearFailures(req.Email)
	}

	writeJSON(w, http.StatusOK, resp)
}

// RefreshToken 处理 Token 刷新
func (h *Handler) RefreshToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "refresh_token is required")
		return
	}

	tokenPair, err := h.service.RefreshToken(req.RefreshToken)
	if err != nil {
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
	}

	writeJSON(w, http.StatusOK, tokenPair)
}

// Logout 处理用户登出
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 从 header 获取 token
	token := extractToken(r)
	if token == "" {
		writeError(w, http.StatusBadRequest, "authorization token required")
		return
	}

	// 验证 token 获取用户 ID
	claims, err := h.service.ValidateAccessToken(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	if err := h.service.Logout(claims.UserID, token); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to logout")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "logged out successfully"})
}

// GetOAuthURL 获取 OAuth 授权 URL
func (h *Handler) GetOAuthURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 从路径提取 provider
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}
	provider := parts[4] // /api/v1/auth/{provider}

	state, err := GenerateState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate state")
		return
	}

	authURL, err := h.service.GetOAuthURL(provider, state)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"url":   authURL,
		"state": state,
	})
}

// HandleOAuthCallback 处理 OAuth 回调
func (h *Handler) HandleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 从路径提取 provider
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 6 {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}
	provider := parts[4] // /api/v1/auth/{provider}/callback

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if code == "" || state == "" {
		writeError(w, http.StatusBadRequest, "code and state are required")
		return
	}

	resp, err := h.service.HandleOAuthCallback(provider, code, state)
	if err != nil {
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// RegisterRoutes 注册路由
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/auth/register", h.Register)
	mux.HandleFunc("/api/v1/auth/login", h.Login)
	mux.HandleFunc("/api/v1/auth/logout", h.Logout)
	mux.HandleFunc("/api/v1/auth/refresh", h.RefreshToken)
	mux.HandleFunc("/api/v1/auth/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/callback"):
			h.HandleOAuthCallback(w, r)
		default:
			h.GetOAuthURL(w, r)
		}
	})
}

// extractToken 从 Authorization header 提取 token
func extractToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

// writeJSON 写入 JSON 响应
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// writeError 写入错误响应
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
