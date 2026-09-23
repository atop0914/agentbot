package template

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Handler provides HTTP endpoints for templates.
type Handler struct {
	svc      Service
	recorder *InMemoryRecorder
}

// NewHandler creates a new template handler.
func NewHandler(svc Service, recorder *InMemoryRecorder) *Handler {
	return &Handler{svc: svc, recorder: recorder}
}

// RegisterRoutes registers template routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/templates", h.handleTemplates)
	mux.HandleFunc("/api/v1/templates/", h.handleTemplateByID)
	mux.HandleFunc("/api/v1/templates/popular", h.handlePopular)
	mux.HandleFunc("/api/v1/recordings/start", h.handleStartRecording)
	mux.HandleFunc("/api/v1/recordings/step", h.handleRecordStep)
	mux.HandleFunc("/api/v1/recordings/stop", h.handleStopRecording)
}

func (h *Handler) handleTemplates(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listTemplates(w, r)
	case http.MethodPost:
		h.createTemplate(w, r)
	default:
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) listTemplates(w http.ResponseWriter, r *http.Request) {
	filter := Filter{
		Author:    r.URL.Query().Get("author"),
		Category:  r.URL.Query().Get("category"),
		Search:    r.URL.Query().Get("search"),
		SortBy:    r.URL.Query().Get("sort_by"),
		SortOrder: r.URL.Query().Get("sort_order"),
	}
	if tags := r.URL.Query().Get("tags"); tags != "" {
		filter.Tags = strings.Split(tags, ",")
	}
	if v := r.URL.Query().Get("public"); v != "" {
		b := v == "true"
		filter.Public = &b
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		filter.Limit, _ = strconv.Atoi(v)
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		filter.Offset, _ = strconv.Atoi(v)
	}

	templates, err := h.svc.List(r.Context(), filter)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]interface{}{"templates": templates})
}

func (h *Handler) createTemplate(w http.ResponseWriter, r *http.Request) {
	var tmpl Template
	if err := json.NewDecoder(r.Body).Decode(&tmpl); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json")
		return
	}
	created, err := h.svc.Create(r.Context(), tmpl)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(created)
}

func (h *Handler) handleTemplateByID(w http.ResponseWriter, r *http.Request) {
	// Extract ID from /api/v1/templates/{id} or sub-paths
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/templates/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]

	if id == "" {
		jsonError(w, http.StatusBadRequest, "template id required")
		return
	}

	// Handle sub-resources
	if len(parts) > 1 {
		switch parts[1] {
		case "export":
			h.exportTemplate(w, r, id)
			return
		case "executions":
			h.listExecutions(w, r, id)
			return
		case "execute":
			h.executeTemplate(w, r, id)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		h.getTemplate(w, r, id)
	case http.MethodPut:
		h.updateTemplate(w, r, id)
	case http.MethodDelete:
		h.deleteTemplate(w, r, id)
	default:
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) getTemplate(w http.ResponseWriter, r *http.Request, id string) {
	tmpl, err := h.svc.Get(r.Context(), id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "template not found")
		return
	}
	jsonOK(w, tmpl)
}

func (h *Handler) updateTemplate(w http.ResponseWriter, r *http.Request, id string) {
	var tmpl Template
	if err := json.NewDecoder(r.Body).Decode(&tmpl); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json")
		return
	}
	updated, err := h.svc.Update(r.Context(), id, tmpl)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, updated)
}

func (h *Handler) deleteTemplate(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.svc.Delete(r.Context(), id); err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "deleted"})
}

func (h *Handler) exportTemplate(w http.ResponseWriter, r *http.Request, id string) {
	data, err := h.svc.Export(r.Context(), id)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=template-"+id+".json")
	w.Write(data)
}

func (h *Handler) executeTemplate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		AgentID   string            `json:"agent_id"`
		Variables map[string]string `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json")
		return
	}
	exec, err := h.svc.Execute(r.Context(), id, req.AgentID, req.Variables)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(exec)
}

func (h *Handler) listExecutions(w http.ResponseWriter, r *http.Request, templateID string) {
	execs, err := h.svc.ListExecutions(r.Context(), templateID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]interface{}{"executions": execs})
}

func (h *Handler) handlePopular(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	templates, err := h.svc.GetPopular(r.Context(), limit)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]interface{}{"templates": templates})
}

func (h *Handler) handleStartRecording(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		AgentID string `json:"agent_id"`
		Name    string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.recorder.StartRecording(r.Context(), req.AgentID, req.Name); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "recording"})
}

func (h *Handler) handleRecordStep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Step   Step       `json:"step"`
		Result StepResult `json:"result"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.recorder.RecordStep(r.Context(), req.Step, req.Result); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, map[string]interface{}{
		"status":      "recorded",
		"step_count":  h.recorder.StepCount(),
	})
}

func (h *Handler) handleStopRecording(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tmpl, err := h.recorder.StopRecording(r.Context())
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(tmpl)
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
