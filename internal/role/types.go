package role

import (
	"context"
	"time"
)

// RoleType defines the predefined role categories.
type RoleType string

const (
	RoleTypeCoordinator RoleType = "coordinator" // Orchestrates other agents, assigns tasks
	RoleTypeWorker      RoleType = "worker"      // Executes tasks, reports progress
	RoleTypeReviewer    RoleType = "reviewer"    // Reviews outputs, provides feedback
	RoleTypeObserver    RoleType = "observer"    // Read-only monitoring, no execution
	RoleTypeCustom      RoleType = "custom"      // User-defined role with custom permissions
)

// Permission represents a specific action that can be authorized.
type Permission string

const (
	// Agent management permissions
	PermAgentCreate  Permission = "agent:create"
	PermAgentRead    Permission = "agent:read"
	PermAgentUpdate  Permission = "agent:update"
	PermAgentDelete  Permission = "agent:delete"
	PermAgentControl Permission = "agent:control" // start/stop/pause/resume

	// Task management permissions
	PermTaskCreate   Permission = "task:create"
	PermTaskRead     Permission = "task:read"
	PermTaskAssign   Permission = "task:assign"
	PermTaskCancel   Permission = "task:cancel"
	PermTaskExecute  Permission = "task:execute"

	// Communication permissions
	PermMessageSend    Permission = "message:send"
	PermMessageRead    Permission = "message:read"
	PermMessageBroadcast Permission = "message:broadcast"

	// Environment permissions
	PermEnvCreate    Permission = "env:create"
	PermEnvRead      Permission = "env:read"
	PermEnvDestroy   Permission = "env:destroy"
	PermEnvExecute   Permission = "env:execute"

	// Filesystem permissions
	PermFileRead     Permission = "file:read"
	PermFileWrite    Permission = "file:write"
	PermFileDelete   Permission = "file:delete"

	// Browser permissions
	PermBrowserUse   Permission = "browser:use"

	// Memory permissions
	PermMemoryRead   Permission = "memory:read"
	PermMemoryWrite  Permission = "memory:write"

	// Role management permissions (meta)
	PermRoleAssign   Permission = "role:assign"
	PermRoleRevoke   Permission = "role:revoke"
	PermRoleManage   Permission = "role:manage"
)

// Role represents an agent role with its capabilities.
type Role struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Type        RoleType     `json:"type"`
	Description string       `json:"description"`
	Permissions []Permission `json:"permissions"`
	IsSystem    bool         `json:"is_system"` // true = predefined, cannot delete
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// AgentRole represents the assignment of a role to an agent.
type AgentRole struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	RoleID    string    `json:"role_id"`
	RoleName  string    `json:"role_name"`
	AssignedBy string   `json:"assigned_by"` // who assigned this role
	AssignedAt time.Time `json:"assigned_at"`
}

// CreateRoleRequest defines the request to create a custom role.
type CreateRoleRequest struct {
	Name        string       `json:"name" validate:"required"`
	Type        RoleType     `json:"type"`
	Description string       `json:"description"`
	Permissions []Permission `json:"permissions" validate:"required"`
}

// UpdateRoleRequest defines the request to update a role.
type UpdateRoleRequest struct {
	Name        *string       `json:"name,omitempty"`
	Description *string       `json:"description,omitempty"`
	Permissions *[]Permission `json:"permissions,omitempty"`
}

// AssignRoleRequest defines the request to assign a role to an agent.
type AssignRoleRequest struct {
	AgentID string `json:"agent_id" validate:"required"`
	RoleID  string `json:"role_id" validate:"required"`
}

// RoleResponse is the API response for a role.
type RoleResponse struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Type        RoleType     `json:"type"`
	Description string       `json:"description"`
	Permissions []Permission `json:"permissions"`
	IsSystem    bool         `json:"is_system"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// AgentRoleResponse is the API response for an agent-role assignment.
type AgentRoleResponse struct {
	ID         string       `json:"id"`
	AgentID    string       `json:"agent_id"`
	Role       RoleResponse `json:"role"`
	AssignedBy string       `json:"assigned_by"`
	AssignedAt time.Time    `json:"assigned_at"`
}

// PermissionCheckRequest checks if an agent has a specific permission.
type PermissionCheckRequest struct {
	AgentID    string     `json:"agent_id" validate:"required"`
	Permission Permission `json:"permission" validate:"required"`
}

// PermissionCheckResponse is the result of a permission check.
type PermissionCheckResponse struct {
	AgentID    string     `json:"agent_id"`
	Permission Permission `json:"permission"`
	Allowed    bool       `json:"allowed"`
}

// Repository defines the interface for role persistence.
type Repository interface {
	// Role CRUD
	CreateRole(ctx context.Context, role *Role) error
	GetRole(ctx context.Context, id string) (*Role, error)
	UpdateRole(ctx context.Context, role *Role) error
	DeleteRole(ctx context.Context, id string) error
	ListRoles(ctx context.Context) ([]*Role, error)

	// Agent-Role assignments
	AssignRole(ctx context.Context, assignment *AgentRole) error
	RevokeRole(ctx context.Context, agentID, roleID string) error
	GetAgentRoles(ctx context.Context, agentID string) ([]*AgentRole, error)
	GetAgentsByRole(ctx context.Context, roleID string) ([]string, error)
}

// Service defines the interface for role operations.
type Service interface {
	// Role management
	CreateRole(ctx context.Context, req CreateRoleRequest) (*Role, error)
	GetRole(ctx context.Context, id string) (*Role, error)
	UpdateRole(ctx context.Context, id string, req UpdateRoleRequest) (*Role, error)
	DeleteRole(ctx context.Context, id string) error
	ListRoles(ctx context.Context) ([]*Role, error)

	// Agent-Role assignment
	AssignRole(ctx context.Context, req AssignRoleRequest) (*AgentRole, error)
	RevokeRole(ctx context.Context, agentID, roleID string) error
	GetAgentRoles(ctx context.Context, agentID string) ([]*AgentRole, error)

	// Permission checking
	CheckPermission(ctx context.Context, agentID string, perm Permission) (bool, error)
	GetAgentPermissions(ctx context.Context, agentID string) ([]Permission, error)
}
