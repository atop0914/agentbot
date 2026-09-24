package template

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// MarketplaceHandler provides HTTP endpoints for the template marketplace.
type MarketplaceHandler struct {
	svc Marketplace
}

// NewMarketplaceHandler creates a new marketplace handler.
func NewMarketplaceHandler(svc Marketplace) *MarketplaceHandler {
	return &MarketplaceHandler{svc: svc}
}

// RegisterRoutes registers marketplace routes on the given mux.
func (h *MarketplaceHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/marketplace", h.handleMarketplace)
	mux.HandleFunc("/api/v1/marketplace/", h.handleMarketplaceByID)
	mux.HandleFunc("/api/v1/marketplace/stats", h.handleStats)
}

func (h *MarketplaceHandler) handleMarketplace(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.browse(w, r)
	default:
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *MarketplaceHandler) handleMarketplaceByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/marketplace/")
	parts := strings.SplitN(path, "/", 2)
	templateID := parts[0]

	if templateID == "" {
		jsonErr(w, http.StatusBadRequest, "template id required")
		return
	}

	// Handle sub-resources
	if len(parts) > 1 {
		switch parts[1] {
		case "reviews":
			h.handleReviews(w, r, templateID)
			return
		case "install":
			h.handleInstall(w, r, templateID)
			return
		case "publish":
			h.handlePublish(w, r, templateID)
			return
		case "unpublish":
			h.handleUnpublish(w, r, templateID)
			return
		case "featured":
			h.handleSetFeatured(w, r, templateID)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		h.getListing(w, r, templateID)
	case http.MethodPut:
		h.updateListing(w, r, templateID)
	default:
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *MarketplaceHandler) browse(w http.ResponseWriter, r *http.Request) {
	filter := ListingFilter{
		Category:  r.URL.Query().Get("category"),
		Search:    r.URL.Query().Get("search"),
		SortBy:    r.URL.Query().Get("sort_by"),
		SortOrder: r.URL.Query().Get("sort_order"),
	}
	if tags := r.URL.Query().Get("tags"); tags != "" {
		filter.Tags = strings.Split(tags, ",")
	}
	if v := r.URL.Query().Get("min_rating"); v != "" {
		filter.MinRating, _ = strconv.ParseFloat(v, 64)
	}
	if v := r.URL.Query().Get("featured"); v != "" {
		b := v == "true"
		filter.Featured = &b
	}
	if v := r.URL.Query().Get("verified"); v != "" {
		b := v == "true"
		filter.Verified = &b
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		filter.Limit, _ = strconv.Atoi(v)
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		filter.Offset, _ = strconv.Atoi(v)
	}

	entries, templates, err := h.svc.Browse(r.Context(), filter)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Combine entry and template data for response
	type listingView struct {
		*MarketplaceEntry
		Template *Template `json:"template"`
	}
	views := make([]listingView, 0, len(entries))
	tmplMap := make(map[string]*Template, len(templates))
	for _, t := range templates {
		tmplMap[t.ID] = t
	}
	for _, e := range entries {
		views = append(views, listingView{
			MarketplaceEntry: e,
			Template:         tmplMap[e.TemplateID],
		})
	}

	jsonOK(w, map[string]interface{}{
		"listings": views,
		"total":    len(views),
	})
}

func (h *MarketplaceHandler) getListing(w http.ResponseWriter, r *http.Request, templateID string) {
	entry, err := h.svc.GetListing(r.Context(), templateID)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, entry)
}

func (h *MarketplaceHandler) updateListing(w http.ResponseWriter, r *http.Request, templateID string) {
	var entry MarketplaceEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	updated, err := h.svc.UpdateListing(r.Context(), templateID, entry)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, updated)
}

func (h *MarketplaceHandler) handlePublish(w http.ResponseWriter, r *http.Request, templateID string) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var entry MarketplaceEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	published, err := h.svc.Publish(r.Context(), templateID, entry)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(published)
}

func (h *MarketplaceHandler) handleUnpublish(w http.ResponseWriter, r *http.Request, templateID string) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := h.svc.Unpublish(r.Context(), templateID); err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "unpublished"})
}

func (h *MarketplaceHandler) handleInstall(w http.ResponseWriter, r *http.Request, templateID string) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	tmpl, err := h.svc.Install(r.Context(), templateID, req.UserID)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(tmpl)
}

func (h *MarketplaceHandler) handleReviews(w http.ResponseWriter, r *http.Request, templateID string) {
	switch r.Method {
	case http.MethodGet:
		reviews, err := h.svc.GetReviews(r.Context(), templateID)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonOK(w, map[string]interface{}{"reviews": reviews})
	case http.MethodPost:
		var review Review
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		review.TemplateID = templateID
		created, err := h.svc.AddReview(r.Context(), review)
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(created)
	default:
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *MarketplaceHandler) handleSetFeatured(w http.ResponseWriter, r *http.Request, templateID string) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Featured bool `json:"featured"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.svc.SetFeatured(r.Context(), templateID, req.Featured); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, map[string]interface{}{"featured": req.Featured})
}

func (h *MarketplaceHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	stats, err := h.svc.GetStats(r.Context())
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, stats)
}

// jsonErr writes a JSON error response.
func jsonErr(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}