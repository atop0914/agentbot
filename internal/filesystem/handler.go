package filesystem

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Handler 文件系统 HTTP 处理器
type Handler struct {
	svc *Service
}

// NewHandler 创建文件系统 HTTP 处理器
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes 注册路由
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/filesystem/read/", h.handleRead)
	mux.HandleFunc("/api/v1/filesystem/write", h.handleWrite)
	mux.HandleFunc("/api/v1/filesystem/list/", h.handleList)
	mux.HandleFunc("/api/v1/filesystem/info/", h.handleInfo)
	mux.HandleFunc("/api/v1/filesystem/mkdir", h.handleMkdir)
	mux.HandleFunc("/api/v1/filesystem/remove/", h.handleRemove)
	mux.HandleFunc("/api/v1/filesystem/move", h.handleMove)
	mux.HandleFunc("/api/v1/filesystem/copy", h.handleCopy)
	mux.HandleFunc("/api/v1/filesystem/search", h.handleSearch)
	mux.HandleFunc("/api/v1/filesystem/stat/", h.handleStat)
}

func (h *Handler) handleRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/filesystem/read/")
	if path == "" {
		jsonError(w, http.StatusBadRequest, "path is required")
		return
	}
	content, err := h.svc.ReadFile(r.Context(), path)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, content)
}

func (h *Handler) handleWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req UploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := h.svc.WriteFile(r.Context(), &req)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, info)
}

func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/filesystem/list/")
	if path == "" {
		path = "."
	}
	q := r.URL.Query()
	recursive := q.Get("recursive") == "true"
	hidden := q.Get("hidden") == "true"
	files, err := h.svc.ListDir(r.Context(), path, recursive, hidden)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]any{"files": files, "total": len(files)})
}

func (h *Handler) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/filesystem/info/")
	if path == "" {
		jsonError(w, http.StatusBadRequest, "path is required")
		return
	}
	info, err := h.svc.GetFileInfo(r.Context(), path)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, info)
}

func (h *Handler) handleMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req MkdirRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.svc.Mkdir(r.Context(), &req); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "created"})
}

func (h *Handler) handleRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/filesystem/remove/")
	if path == "" {
		jsonError(w, http.StatusBadRequest, "path is required")
		return
	}
	recursive := r.URL.Query().Get("recursive") == "true"
	if err := h.svc.Remove(r.Context(), path, recursive); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "deleted"})
}

func (h *Handler) handleMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req MoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.svc.Move(r.Context(), &req); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "moved"})
}

func (h *Handler) handleCopy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req CopyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.svc.Copy(r.Context(), &req); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "copied"})
}

func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.svc.Search(r.Context(), &req)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, result)
}

func (h *Handler) handleStat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/filesystem/stat/")
	if path == "" {
		jsonError(w, http.StatusBadRequest, "path is required")
		return
	}
	exists, err := h.svc.Stat(r.Context(), path)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]bool{"exists": exists})
}

// === JSON helpers ===

func jsonOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
