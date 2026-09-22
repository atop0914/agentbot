package role

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Handler provides HTTP endpoints for the role system.
type Handler struct {
	svc Service
}

// NewHandler creates a new role handler.
func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers all role-related HTTP routes.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/roles", h.handleRoles)
	mux.HandleFunc("/api/v1/roles/", h.handleRoleByID)
	mux.HandleFunc("/api/v1/agents/", h.handleAgentRoles)
	mux.HandleFunc("/api/v1/permissions/check", h.handlePermissionCheck)
	mux.HandleFunc("/api/v1/permissions", h.handleListPermissions)
}

func (h *Handler) handleRoles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listRoles(w, r)
	case http.MethodPost:
		h.createRole(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleRoleByID(w http.ResponseWriter, r *http.Request) {
	// Extract role ID from path: /api/v1/roles/{id}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/roles/")
	if id == "" {
		http.Error(w, "role ID required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getRole(w, r, id)
	case http.MethodPut:
		h.updateRole(w, r, id)
	case http.MethodDelete:
		h.deleteRole(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleAgentRoles(w http.ResponseWriter, r *http.Request) {
	// Path: /api/v1/agents/{id}/roles
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 || parts[1] != "roles" {
		http.NotFound(w, r)
		return
	}
	agentID := parts[0]
	if agentID == "" {
		http.Error(w, "agent ID required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getAgentRoles(w, r, agentID)
	case http.MethodPost:
		h.assignRole(w, r, agentID)
	case http.MethodDelete:
		h.revokeRole(w, r, agentID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) listRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.svc.ListRoles(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	responses := make([]RoleResponse, len(roles))
	for i, role := range roles {
		responses[i] = toRoleResponse(role)
	}
	jsonOK(w, map[string]interface{}{
		"roles": responses,
		"total": len(responses),
	})
}

func (h *Handler) createRole(w http.ResponseWriter, r *http.Request) {
	var req CreateRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	role, err := h.svc.CreateRole(r.Context(), req)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(toRoleResponse(role))
}

func (h *Handler) getRole(w http.ResponseWriter, r *http.Request, id string) {
	role, err := h.svc.GetRole(r.Context(), id)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	jsonOK(w, toRoleResponse(role))
}

func (h *Handler) updateRole(w http.ResponseWriter, r *http.Request, id string) {
	var req UpdateRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	role, err := h.svc.UpdateRole(r.Context(), id, req)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, toRoleResponse(role))
}

func (h *Handler) deleteRole(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.svc.DeleteRole(r.Context(), id); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "deleted"})
}

func (h *Handler) getAgentRoles(w http.ResponseWriter, r *http.Request, agentID string) {
	assignments, err := h.svc.GetAgentRoles(r.Context(), agentID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Build response with full role details
	responses := make([]AgentRoleResponse, 0, len(assignments))
	for _, ar := range assignments {
		role, err := h.svc.GetRole(r.Context(), ar.RoleID)
		if err != nil {
			continue
		}
		responses = append(responses, AgentRoleResponse{
			ID:         ar.ID,
			AgentID:    ar.AgentID,
			Role:       toRoleResponse(role),
			AssignedBy: ar.AssignedBy,
			AssignedAt: ar.AssignedAt,
		})
	}
	jsonOK(w, map[string]interface{}{
		"agent_id": agentID,
		"roles":    responses,
		"total":    len(responses),
	})
}

func (h *Handler) assignRole(w http.ResponseWriter, r *http.Request, agentID string) {
	var req AssignRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.AgentID = agentID // Override from URL path
	assignment, err := h.svc.AssignRole(r.Context(), req)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "assigned",
		"id":     assignment.ID,
	})
}

func (h *Handler) revokeRole(w http.ResponseWriter, r *http.Request, agentID string) {
	roleID := r.URL.Query().Get("role_id")
	if roleID == "" {
		jsonError(w, http.StatusBadRequest, "role_id query parameter required")
		return
	}
	if err := h.svc.RevokeRole(r.Context(), agentID, roleID); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "revoked"})
}

func (h *Handler) handlePermissionCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req PermissionCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	allowed, err := h.svc.CheckPermission(r.Context(), req.AgentID, req.Permission)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, PermissionCheckResponse{
		AgentID:    req.AgentID,
		Permission: req.Permission,
		Allowed:    allowed,
	})
}

func (h *Handler) handleListPermissions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jsonOK(w, map[string]interface{}{
		"permissions": AllPermissions(),
		"total":       len(AllPermissions()),
	})
}

// toRoleResponse converts a Role to a RoleResponse.
func toRoleResponse(r *Role) RoleResponse {
	return RoleResponse{
		ID:          r.ID,
		Name:        r.Name,
		Type:        r.Type,
		Description: r.Description,
		Permissions: r.Permissions,
		IsSystem:    r.IsSystem,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

func jsonOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
