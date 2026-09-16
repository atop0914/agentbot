package terminal

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Handler 终端模块 HTTP 处理器
type Handler struct {
	svc *Service
}

// NewHandler 创建终端处理器
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes 注册终端路由
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/terminals", h.handleSessions)
	mux.HandleFunc("/api/v1/terminals/", h.handleSessionByID)
}

func (h *Handler) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listSessions(w, r)
	case http.MethodPost:
		h.createSession(w, r)
	default:
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleSessionByID(w http.ResponseWriter, r *http.Request) {
	// /api/v1/terminals/{id}[/action]
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/terminals/")
	parts := strings.SplitN(path, "/", 2)
	sessionID := parts[0]

	if sessionID == "" {
		jsonError(w, http.StatusBadRequest, "session ID required")
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			h.getSession(w, r, sessionID)
		case http.MethodDelete:
			h.closeSession(w, r, sessionID)
		default:
			jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	action := parts[1]
	switch action {
	case "execute":
		h.execute(w, r, sessionID)
	case "resize":
		h.resize(w, r, sessionID)
	default:
		jsonError(w, http.StatusNotFound, "unknown action: "+action)
	}
}

func (h *Handler) createSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.AgentID == "" {
		jsonError(w, http.StatusBadRequest, "agent_id is required")
		return
	}

	// 默认本地连接
	if req.Type == "" {
		req.Type = ConnTypeLocal
	}

	session, err := h.svc.CreateSession(r.Context(), req)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonOK(w, session)
}

func (h *Handler) getSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, err := h.svc.GetSession(r.Context(), sessionID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "session not found")
		return
	}

	jsonOK(w, session)
}

func (h *Handler) closeSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	if err := h.svc.CloseSession(r.Context(), sessionID); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonOK(w, map[string]string{"status": "closed"})
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	filter := SessionFilter{
		AgentID: r.URL.Query().Get("agent_id"),
		State:   SessionState(r.URL.Query().Get("state")),
	}

	sessions, err := h.svc.ListSessions(r.Context(), filter)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonOK(w, sessions)
}

func (h *Handler) execute(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Command == "" {
		jsonError(w, http.StatusBadRequest, "command is required")
		return
	}

	result, err := h.svc.Execute(r.Context(), sessionID, req)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonOK(w, result)
}

func (h *Handler) resize(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req ResizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Rows <= 0 || req.Cols <= 0 {
		jsonError(w, http.StatusBadRequest, "rows and cols must be positive")
		return
	}

	if err := h.svc.Resize(r.Context(), sessionID, req.Rows, req.Cols); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonOK(w, map[string]string{"status": "resized"})
}

// jsonOK writes a JSON 200 response.
func jsonOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(data)
}

// jsonError writes a JSON error response.
func jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
