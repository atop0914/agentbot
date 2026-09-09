package agent

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/atop0914/agentbot/internal/pkg/errors"
)

// Handler Agent HTTP 处理器
type Handler struct {
	service Service
}

// NewHandler 创建 Agent 处理器
func NewHandler(service Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes 注册路由
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/agents", h.handleAgents)
	mux.HandleFunc("/api/v1/agents/", h.handleAgentByID)
}

// handleAgents 处理 /api/v1/agents 路由
func (h *Handler) handleAgents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.List(w, r)
	case http.MethodPost:
		h.Create(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleAgentByID 处理 /api/v1/agents/{id} 路由
func (h *Handler) handleAgentByID(w http.ResponseWriter, r *http.Request) {
	// 提取 ID 和可能的 action
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "agent id is required")
		return
	}

	id := parts[0]

	// 处理 action 路由: /api/v1/agents/{id}/{action}
	if len(parts) > 1 {
		action := parts[1]
		h.handleAction(w, r, id, action)
		return
	}

	// 处理 CRUD 路由: /api/v1/agents/{id}
	switch r.Method {
	case http.MethodGet:
		h.Get(w, r, id)
	case http.MethodPut:
		h.Update(w, r, id)
	case http.MethodDelete:
		h.Delete(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleAction 处理 Agent 操作 (start/stop/pause/resume)
func (h *Handler) handleAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var err error
	ctx := r.Context()

	switch action {
	case "start":
		err = h.service.Start(ctx, id)
	case "stop":
		err = h.service.Stop(ctx, id)
	case "pause":
		err = h.service.Pause(ctx, id)
	case "resume":
		err = h.service.Resume(ctx, id)
	default:
		writeError(w, http.StatusBadRequest, "unknown action: "+action)
		return
	}

	if err != nil {
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": action + " agent successfully",
	})
}

// Create 创建 Agent
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	agent, err := h.service.Create(r.Context(), req)
	if err != nil {
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
	}

	writeJSON(w, http.StatusCreated, agent.ToResponse())
}

// Get 获取 Agent
func (h *Handler) Get(w http.ResponseWriter, r *http.Request, id string) {
	agent, err := h.service.Get(r.Context(), id)
	if err != nil {
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
	}

	writeJSON(w, http.StatusOK, agent.ToResponse())
}

// Update 更新 Agent
func (h *Handler) Update(w http.ResponseWriter, r *http.Request, id string) {
	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	agent, err := h.service.Update(r.Context(), id, req)
	if err != nil {
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
	}

	writeJSON(w, http.StatusOK, agent.ToResponse())
}

// Delete 删除 Agent
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.service.Delete(r.Context(), id); err != nil {
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "agent deleted successfully",
	})
}

// List 列出 Agent
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	// 解析分页参数
	offset := 0
	limit := 20

	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	agents, err := h.service.List(r.Context(), offset, limit)
	if err != nil {
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
	}

	// 转换为响应格式
	responses := make([]*AgentResponse, len(agents))
	for i, a := range agents {
		resp := a.ToResponse()
		responses[i] = &resp
	}

	writeJSON(w, http.StatusOK, ListResponse{
		Agents: responses,
		Total:  len(responses),
		Offset: offset,
		Limit:  limit,
	})
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
