package role

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// memoryRepository implements Repository using an in-memory store.
type memoryRepository struct {
	mu          sync.RWMutex
	roles       map[string]*Role
	assignments map[string][]*AgentRole // key: agentID
}

// NewMemoryRepository creates a new in-memory role repository
// preloaded with system roles.
func NewMemoryRepository() Repository {
	repo := &memoryRepository{
		roles:       make(map[string]*Role),
		assignments: make(map[string][]*AgentRole),
	}
	// Load system roles
	for _, r := range SystemRoles() {
		repo.roles[r.ID] = r
	}
	return repo
}

func (r *memoryRepository) CreateRole(_ context.Context, role *Role) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.roles[role.ID]; exists {
		return fmt.Errorf("role %s already exists", role.ID)
	}
	r.roles[role.ID] = role
	return nil
}

func (r *memoryRepository) GetRole(_ context.Context, id string) (*Role, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	role, ok := r.roles[id]
	if !ok {
		return nil, fmt.Errorf("role %s not found", id)
	}
	return role, nil
}

func (r *memoryRepository) UpdateRole(_ context.Context, role *Role) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.roles[role.ID]; !exists {
		return fmt.Errorf("role %s not found", role.ID)
	}
	role.UpdatedAt = time.Now().UTC()
	r.roles[role.ID] = role
	return nil
}

func (r *memoryRepository) DeleteRole(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	role, ok := r.roles[id]
	if !ok {
		return fmt.Errorf("role %s not found", id)
	}
	if role.IsSystem {
		return fmt.Errorf("cannot delete system role %s", id)
	}
	delete(r.roles, id)
	return nil
}

func (r *memoryRepository) ListRoles(_ context.Context) ([]*Role, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	roles := make([]*Role, 0, len(r.roles))
	for _, role := range r.roles {
		roles = append(roles, role)
	}
	return roles, nil
}

func (r *memoryRepository) AssignRole(_ context.Context, assignment *AgentRole) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Check for duplicate assignment
	for _, existing := range r.assignments[assignment.AgentID] {
		if existing.RoleID == assignment.RoleID {
			return fmt.Errorf("agent %s already has role %s", assignment.AgentID, assignment.RoleID)
		}
	}
	r.assignments[assignment.AgentID] = append(r.assignments[assignment.AgentID], assignment)
	return nil
}

func (r *memoryRepository) RevokeRole(_ context.Context, agentID, roleID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	assignments := r.assignments[agentID]
	for i, a := range assignments {
		if a.RoleID == roleID {
			r.assignments[agentID] = append(assignments[:i], assignments[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("agent %s does not have role %s", agentID, roleID)
}

func (r *memoryRepository) GetAgentRoles(_ context.Context, agentID string) ([]*AgentRole, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.assignments[agentID], nil
}

func (r *memoryRepository) GetAgentsByRole(_ context.Context, roleID string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var agentIDs []string
	for agentID, assignments := range r.assignments {
		for _, a := range assignments {
			if a.RoleID == roleID {
				agentIDs = append(agentIDs, agentID)
				break
			}
		}
	}
	return agentIDs, nil
}

// service implements the Service interface.
type service struct {
	repo Repository
}

// NewService creates a new role service.
func NewService(repo Repository) Service {
	return &service{repo: repo}
}

func (s *service) CreateRole(ctx context.Context, req CreateRoleRequest) (*Role, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("role name is required")
	}
	if len(req.Permissions) == 0 {
		return nil, fmt.Errorf("at least one permission is required")
	}
	// Validate all permissions
	for _, p := range req.Permissions {
		if !ValidatePermission(p) {
			return nil, fmt.Errorf("unknown permission: %s", p)
		}
	}
	if req.Type == "" {
		req.Type = RoleTypeCustom
	}

	role := NewCustomRole(req)
	if err := s.repo.CreateRole(ctx, role); err != nil {
		return nil, err
	}
	return role, nil
}

func (s *service) GetRole(ctx context.Context, id string) (*Role, error) {
	return s.repo.GetRole(ctx, id)
}

func (s *service) UpdateRole(ctx context.Context, id string, req UpdateRoleRequest) (*Role, error) {
	role, err := s.repo.GetRole(ctx, id)
	if err != nil {
		return nil, err
	}
	if role.IsSystem {
		return nil, fmt.Errorf("cannot modify system role %s", id)
	}

	if req.Name != nil {
		role.Name = *req.Name
	}
	if req.Description != nil {
		role.Description = *req.Description
	}
	if req.Permissions != nil {
		for _, p := range *req.Permissions {
			if !ValidatePermission(p) {
				return nil, fmt.Errorf("unknown permission: %s", p)
			}
		}
		role.Permissions = *req.Permissions
	}
	role.UpdatedAt = time.Now().UTC()

	if err := s.repo.UpdateRole(ctx, role); err != nil {
		return nil, err
	}
	return role, nil
}

func (s *service) DeleteRole(ctx context.Context, id string) error {
	return s.repo.DeleteRole(ctx, id)
}

func (s *service) ListRoles(ctx context.Context) ([]*Role, error) {
	return s.repo.ListRoles(ctx)
}

func (s *service) AssignRole(ctx context.Context, req AssignRoleRequest) (*AgentRole, error) {
	if req.AgentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	if req.RoleID == "" {
		return nil, fmt.Errorf("role_id is required")
	}
	// Verify role exists
	role, err := s.repo.GetRole(ctx, req.RoleID)
	if err != nil {
		return nil, fmt.Errorf("role not found: %w", err)
	}

	assignment := &AgentRole{
		ID:         "ar-" + uuid.New().String()[:8],
		AgentID:    req.AgentID,
		RoleID:     req.RoleID,
		RoleName:   role.Name,
		AssignedBy: "system", // TODO: inject from context
		AssignedAt: time.Now().UTC(),
	}
	if err := s.repo.AssignRole(ctx, assignment); err != nil {
		return nil, err
	}
	return assignment, nil
}

func (s *service) RevokeRole(ctx context.Context, agentID, roleID string) error {
	if agentID == "" || roleID == "" {
		return fmt.Errorf("agent_id and role_id are required")
	}
	return s.repo.RevokeRole(ctx, agentID, roleID)
}

func (s *service) GetAgentRoles(ctx context.Context, agentID string) ([]*AgentRole, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	return s.repo.GetAgentRoles(ctx, agentID)
}

func (s *service) CheckPermission(ctx context.Context, agentID string, perm Permission) (bool, error) {
	if agentID == "" {
		return false, fmt.Errorf("agent_id is required")
	}
	if !ValidatePermission(perm) {
		return false, fmt.Errorf("unknown permission: %s", perm)
	}

	roles, err := s.repo.GetAgentRoles(ctx, agentID)
	if err != nil {
		return false, err
	}

	// Check all assigned roles for the permission
	for _, ar := range roles {
		role, err := s.repo.GetRole(ctx, ar.RoleID)
		if err != nil {
			continue
		}
		for _, p := range role.Permissions {
			if p == perm {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *service) GetAgentPermissions(ctx context.Context, agentID string) ([]Permission, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}

	roles, err := s.repo.GetAgentRoles(ctx, agentID)
	if err != nil {
		return nil, err
	}

	// Deduplicate permissions from all assigned roles
	seen := make(map[Permission]bool)
	var perms []Permission
	for _, ar := range roles {
		role, err := s.repo.GetRole(ctx, ar.RoleID)
		if err != nil {
			continue
		}
		for _, p := range role.Permissions {
			if !seen[p] {
				seen[p] = true
				perms = append(perms, p)
			}
		}
	}
	return perms, nil
}
