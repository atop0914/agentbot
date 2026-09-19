package memory

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Handler 记忆 HTTP 处理器
type Handler struct {
	service Service
}

// NewHandler 创建记忆处理器
func NewHandler(service Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes 注册路由
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/memory", h.handleMemory)
	mux.HandleFunc("/api/v1/memory/", h.handleMemoryByID)
	mux.HandleFunc("/api/v1/memory/agent/", h.handleMemoryByAgent)
	mux.HandleFunc("/api/v1/memory/user/", h.handleMemoryByUser)
	mux.HandleFunc("/api/v1/memory/stats", h.handleStats)
	mux.HandleFunc("/api/v1/memory/cleanup", h.handleCleanup)
}

// handleMemory handles /api/v1/memory (POST create, GET search)
func (h *Handler) handleMemory(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.create(w, r)
	case http.MethodGet:
		h.search(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleMemoryByID handles /api/v1/memory/{id}
func (h *Handler) handleMemoryByID(w http.ResponseWriter, r *http.Request) {
	// 跳过 agent/ 和 user/ 子路径
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/memory/")
	if strings.HasPrefix(path, "agent/") || strings.HasPrefix(path, "user/") {
		http.NotFound(w, r)
		return
	}

	id := path
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.get(w, r, id)
	case http.MethodPut:
		h.update(w, r, id)
	case http.MethodDelete:
		h.delete(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleMemoryByAgent handles /api/v1/memory/agent/{agent_id}
func (h *Handler) handleMemoryByAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agentID := strings.TrimPrefix(r.URL.Path, "/api/v1/memory/agent/")
	if agentID == "" {
		http.Error(w, "agent_id required", http.StatusBadRequest)
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	entries, total, err := h.service.GetByAgent(r.Context(), agentID, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries, "total": total})
}

// handleMemoryByUser handles /api/v1/memory/user/{user_id}
func (h *Handler) handleMemoryByUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID := strings.TrimPrefix(r.URL.Path, "/api/v1/memory/user/")
	if userID == "" {
		http.Error(w, "user_id required", http.StatusBadRequest)
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	entries, total, err := h.service.GetByUser(r.Context(), userID, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries, "total": total})
}

// handleStats handles /api/v1/memory/stats
func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stats, err := h.service.Stats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// handleCleanup handles /api/v1/memory/cleanup
func (h *Handler) handleCleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	count, err := h.service.Cleanup(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleaned": count})
}

// create 创建记忆
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req CreateMemoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	entry, err := h.service.Create(r.Context(), &req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

// get 获取记忆
func (h *Handler) get(w http.ResponseWriter, r *http.Request, id string) {
	entry, err := h.service.Get(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// update 更新记忆
func (h *Handler) update(w http.ResponseWriter, r *http.Request, id string) {
	var req UpdateMemoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	entry, err := h.service.Update(r.Context(), id, &req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// delete 删除记忆
func (h *Handler) delete(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.service.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// search 搜索记忆
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	req := &SearchMemoryRequest{
		AgentID: r.URL.Query().Get("agent_id"),
		UserID:  r.URL.Query().Get("user_id"),
		Type:    MemoryType(r.URL.Query().Get("type")),
		Scope:   MemoryScope(r.URL.Query().Get("scope")),
		Query:   r.URL.Query().Get("q"),
	}

	if tags := r.URL.Query()["tags"]; len(tags) > 0 {
		req.Tags = tags
	}
	if minScore := r.URL.Query().Get("min_score"); minScore != "" {
		if v, err := strconv.ParseFloat(minScore, 64); err == nil {
			req.MinScore = v
		}
	}
	if limit := r.URL.Query().Get("limit"); limit != "" {
		if v, err := strconv.Atoi(limit); err == nil {
			req.Limit = v
		}
	}
	if offset := r.URL.Query().Get("offset"); offset != "" {
		if v, err := strconv.Atoi(offset); err == nil {
			req.Offset = v
		}
	}

	entries, total, err := h.service.Search(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries, "total": total})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
