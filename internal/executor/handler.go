package executor

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/atop0914/agentbot/internal/pkg/errors"
	"github.com/atop0914/agentbot/internal/task"
)

// Handler 任务 HTTP 处理器
type Handler struct {
	manager task.Manager
}

// NewHandler 创建任务处理器
func NewHandler(manager task.Manager) *Handler {
	return &Handler{manager: manager}
}

// RegisterRoutes 注册路由
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/tasks", h.handleTasks)
	mux.HandleFunc("/api/v1/tasks/", h.handleTaskByID)
}

// handleTasks 处理 /api/v1/tasks 路由
func (h *Handler) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.List(w, r)
	case http.MethodPost:
		h.Create(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleTaskByID 处理 /api/v1/tasks/{id} 路由
func (h *Handler) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "task id is required")
		return
	}

	id := parts[0]

	// 处理 action 路由: /api/v1/tasks/{id}/{action}
	if len(parts) > 1 {
		action := parts[1]
		h.handleAction(w, r, id, action)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.Get(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleAction 处理任务操作 (start/cancel/retry/progress)
func (h *Handler) handleAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost && action != "progress" {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	switch action {
	case "start":
		if err := h.manager.Start(ctx, id); err != nil {
			appErr := errors.FromError(err)
			writeError(w, appErr.StatusCode, appErr.Message)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "task started"})

	case "cancel":
		if err := h.manager.Cancel(ctx, id); err != nil {
			appErr := errors.FromError(err)
			writeError(w, appErr.StatusCode, appErr.Message)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "task cancelled"})

	case "retry":
		if err := h.manager.Retry(ctx, id); err != nil {
			appErr := errors.FromError(err)
			writeError(w, appErr.StatusCode, appErr.Message)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "task retrying"})

	case "progress":
		progress, err := h.manager.GetProgress(ctx, id)
		if err != nil {
			appErr := errors.FromError(err)
			writeError(w, appErr.StatusCode, appErr.Message)
			return
		}
		writeJSON(w, http.StatusOK, progress)

	default:
		writeError(w, http.StatusBadRequest, "unknown action: "+action)
	}
}

// CreateRequest 创建任务请求
type CreateRequest struct {
	AgentID string `json:"agent_id"`
	Goal    string `json:"goal"`
}

// Create 创建任务
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	t, err := h.manager.Create(r.Context(), req.AgentID, req.Goal)
	if err != nil {
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
	}

	writeJSON(w, http.StatusCreated, t)
}

// Get 获取任务
func (h *Handler) Get(w http.ResponseWriter, r *http.Request, id string) {
	t, err := h.manager.Get(r.Context(), id)
	if err != nil {
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
	}

	writeJSON(w, http.StatusOK, t)
}

// List 列出任务
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	state := task.TaskState(r.URL.Query().Get("state"))

	tasks, err := h.manager.List(r.Context(), agentID, state)
	if err != nil {
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tasks": tasks,
		"total": len(tasks),
	})
}

// ListWithOffset 带偏移的列表查询
func (h *Handler) ListWithOffset(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	state := task.TaskState(r.URL.Query().Get("state"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}

	tasks, err := h.manager.List(r.Context(), agentID, state)
	if err != nil {
		appErr := errors.FromError(err)
		writeError(w, appErr.StatusCode, appErr.Message)
		return
	}

	// 手动分页
	if offset >= len(tasks) {
		tasks = []*task.Task{}
	} else {
		end := offset + limit
		if end > len(tasks) {
			end = len(tasks)
		}
		tasks = tasks[offset:end]
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tasks":  tasks,
		"total":  len(tasks),
		"offset": offset,
		"limit":  limit,
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
