package auth

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/atop0914/agentbot/internal/pkg/errors"
)

// Handler 认证 HTTP 处理器
type Handler struct {
	service AuthService
}

// NewHandler 创建认证处理器
func NewHandler(service AuthService) *Handler {
	return &Handler{service: service}
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

	resp, err := h.service.Login(&req)
	if err != nil {
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
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
	json.NewEncoder(w).Encode(data)
}

// writeError 写入错误响应
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
