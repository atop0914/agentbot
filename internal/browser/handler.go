package browser

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Handler provides HTTP endpoints for browser automation.
type Handler struct {
	svc Service
}

// NewHandler creates a new browser handler.
func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers browser HTTP routes.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/browsers", h.handleBrowsers)
	mux.HandleFunc("/api/v1/browsers/", h.handleBrowserByID)
	mux.HandleFunc("/api/v1/browser/profiles", h.handleProfiles)
	mux.HandleFunc("/api/v1/browser/profiles/", h.handleProfileByID)
	mux.HandleFunc("/api/v1/browser/import-profile", h.handleImportProfile)
}

func (h *Handler) handleBrowsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.createBrowser(w, r)
	default:
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleBrowserByID(w http.ResponseWriter, r *http.Request) {
	// Extract browser ID from path: /api/v1/browsers/{id}[/action]
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/browsers/")
	parts := strings.SplitN(path, "/", 2)
	browserID := parts[0]

	if browserID == "" {
		jsonError(w, http.StatusBadRequest, "browser ID required")
		return
	}

	if len(parts) == 1 {
		// /api/v1/browsers/{id}
		switch r.Method {
		case http.MethodGet:
			h.getBrowser(w, r, browserID)
		case http.MethodDelete:
			h.closeBrowser(w, r, browserID)
		default:
			jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	action := parts[1]
	switch action {
	case "navigate":
		h.navigate(w, r, browserID)
	case "page":
		h.getCurrentPage(w, r, browserID)
	case "action":
		h.executeAction(w, r, browserID)
	case "screenshot":
		h.screenshot(w, r, browserID)
	case "extract":
		h.extractText(w, r, browserID)
	case "record/start":
		h.startRecording(w, r, browserID)
	case "record/stop":
		h.stopRecording(w, r, browserID)
	case "replay":
		h.replayActions(w, r, browserID)
	default:
		jsonError(w, http.StatusNotFound, "unknown action: "+action)
	}
}

func (h *Handler) createBrowser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID string   `json:"agent_id"`
		Profile *Profile `json:"profile,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.AgentID == "" {
		jsonError(w, http.StatusBadRequest, "agent_id is required")
		return
	}

	browser, err := h.svc.CreateBrowser(r.Context(), req.AgentID, req.Profile)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, browser)
}

func (h *Handler) getBrowser(w http.ResponseWriter, r *http.Request, browserID string) {
	browser, err := h.svc.GetBrowser(r.Context(), browserID)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, browser)
}

func (h *Handler) closeBrowser(w http.ResponseWriter, r *http.Request, browserID string) {
	if err := h.svc.CloseBrowser(r.Context(), browserID); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "closed"})
}

func (h *Handler) navigate(w http.ResponseWriter, r *http.Request, browserID string) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.URL == "" {
		jsonError(w, http.StatusBadRequest, "url is required")
		return
	}

	page, err := h.svc.Navigate(r.Context(), browserID, req.URL)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, page)
}

func (h *Handler) getCurrentPage(w http.ResponseWriter, r *http.Request, browserID string) {
	page, err := h.svc.GetCurrentPage(r.Context(), browserID)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, page)
}

func (h *Handler) executeAction(w http.ResponseWriter, r *http.Request, browserID string) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var action Action
	if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	result, err := h.svc.ExecuteAction(r.Context(), browserID, action)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"result": result})
}

func (h *Handler) screenshot(w http.ResponseWriter, r *http.Request, browserID string) {
	data, err := h.svc.Screenshot(r.Context(), browserID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(data)
}

func (h *Handler) extractText(w http.ResponseWriter, r *http.Request, browserID string) {
	selector := r.URL.Query().Get("selector")
	if selector == "" {
		jsonError(w, http.StatusBadRequest, "selector query param required")
		return
	}
	text, err := h.svc.ExtractText(r.Context(), browserID, selector)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"text": text})
}

func (h *Handler) startRecording(w http.ResponseWriter, r *http.Request, browserID string) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := h.svc.(Recorder).StartRecording(r.Context(), browserID); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "recording"})
}

func (h *Handler) stopRecording(w http.ResponseWriter, r *http.Request, browserID string) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actions, err := h.svc.(Recorder).StopRecording(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]interface{}{"actions": actions})
}

func (h *Handler) replayActions(w http.ResponseWriter, r *http.Request, browserID string) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Actions []Action `json:"actions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.svc.(Recorder).ReplayActions(r.Context(), browserID, req.Actions); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "replayed"})
}

func (h *Handler) handleProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	profiles, err := h.svc.ListProfiles(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, profiles)
}

func (h *Handler) handleProfileByID(w http.ResponseWriter, r *http.Request) {
	profileID := strings.TrimPrefix(r.URL.Path, "/api/v1/browser/profiles/")
	if profileID == "" {
		jsonError(w, http.StatusBadRequest, "profile ID required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		data, err := h.svc.ExportProfile(r.Context(), profileID)
		if err != nil {
			jsonError(w, http.StatusNotFound, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	case http.MethodDelete:
		repo, ok := h.svc.(*LocalService)
		if !ok {
			jsonError(w, http.StatusInternalServerError, "unsupported operation")
			return
		}
		if err := repo.repo.DeleteProfile(profileID); err != nil {
			jsonError(w, http.StatusNotFound, err.Error())
			return
		}
		jsonOK(w, map[string]string{"status": "deleted"})
	default:
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleImportProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var profileData []byte
	if err := json.NewDecoder(r.Body).Decode(&profileData); err != nil {
		// Try raw bytes
		r.Body.Close()
		jsonError(w, http.StatusBadRequest, "invalid profile data")
		return
	}
	profile, err := h.svc.ImportProfile(r.Context(), profileData)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, profile)
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
