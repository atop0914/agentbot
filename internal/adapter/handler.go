package adapter

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Handler provides HTTP endpoints for adapter management.
type Handler struct {
	svc *Service
}

// NewHandler creates a new adapter HTTP handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers adapter routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/adapters", h.handleAdapters)
	mux.HandleFunc("/api/v1/adapters/", h.handleAdapterByID)
}

// handleAdapters handles /api/v1/adapters (GET list, POST create).
func (h *Handler) handleAdapters(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.list(w, r)
	case http.MethodPost:
		h.create(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAdapterByID handles /api/v1/adapters/{id} and sub-paths.
func (h *Handler) handleAdapterByID(w http.ResponseWriter, r *http.Request) {
	// Extract ID and optional sub-path: /api/v1/adapters/{id}[/actions|execute]
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/adapters/")
	parts := strings.SplitN(path, "/", 2)

	id := parts[0]
	if id == "" {
		jsonError(w, http.StatusBadRequest, "adapter ID is required")
		return
	}

	if len(parts) == 1 {
		// /api/v1/adapters/{id}
		switch r.Method {
		case http.MethodGet:
			h.get(w, r, id)
		case http.MethodDelete:
			h.delete(w, r, id)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// Sub-resource
	sub := parts[1]
	switch {
	case sub == "actions" && r.Method == http.MethodGet:
		h.listActions(w, r, id)
	case sub == "execute" && r.Method == http.MethodPost:
		h.execute(w, r, id)
	default:
		jsonError(w, http.StatusNotFound, "not found")
	}
}

// create registers a new adapter.
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var cfg Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	info, err := h.svc.Register(r.Context(), &cfg)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(info)
}

// list returns all adapters, optionally filtered by agent_id.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	var list []AdapterInfo
	if agentID != "" {
		list = h.svc.List(agentID)
	} else {
		list = h.svc.ListAll()
	}

	if list == nil {
		list = []AdapterInfo{}
	}
	jsonOK(w, list)
}

// get returns a specific adapter by ID.
func (h *Handler) get(w http.ResponseWriter, r *http.Request, id string) {
	info, err := h.svc.GetInfo(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, info)
}

// delete removes an adapter.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.svc.Unregister(r.Context(), id); err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "deleted"})
}

// listActions returns available actions for an adapter.
func (h *Handler) listActions(w http.ResponseWriter, r *http.Request, id string) {
	actions, err := h.svc.ListActions(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, actions)
}

// execute performs an action on an adapter.
func (h *Handler) execute(w http.ResponseWriter, r *http.Request, id string) {
	var req ActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Action == "" {
		jsonError(w, http.StatusBadRequest, "action is required")
		return
	}

	result, err := h.svc.Execute(r.Context(), id, &req)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if result.Success {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	json.NewEncoder(w).Encode(result)
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
