package role

import (
	"time"

	"github.com/google/uuid"
)

// SystemRoles returns all predefined system roles.
func SystemRoles() []*Role {
	now := time.Now().UTC()
	return []*Role{
		{
			ID:          "role-coordinator",
			Name:        "Coordinator",
			Type:        RoleTypeCoordinator,
			Description: "Orchestrates multi-agent workflows, assigns tasks to workers, and manages collaboration. Has full control over task lifecycle and agent communication.",
			Permissions: coordinatorPermissions(),
			IsSystem:    true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "role-worker",
			Name:        "Worker",
			Type:        RoleTypeWorker,
			Description: "Executes assigned tasks, uses tools (browser, terminal, filesystem), and reports progress. Cannot assign tasks or manage other agents.",
			Permissions: workerPermissions(),
			IsSystem:    true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "role-reviewer",
			Name:        "Reviewer",
			Type:        RoleTypeReviewer,
			Description: "Reviews task outputs and agent work, provides feedback, and can approve or reject results. Has read access to most resources.",
			Permissions: reviewerPermissions(),
			IsSystem:    true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "role-observer",
			Name:        "Observer",
			Type:        RoleTypeObserver,
			Description: "Read-only access to monitor agent activity, view tasks, and observe communication. Cannot execute actions or modify state.",
			Permissions: observerPermissions(),
			IsSystem:    true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}
}

// NewCustomRole creates a new custom role with a generated ID.
func NewCustomRole(req CreateRoleRequest) *Role {
	now := time.Now().UTC()
	return &Role{
		ID:          "role-" + uuid.New().String()[:8],
		Name:        req.Name,
		Type:        RoleTypeCustom,
		Description: req.Description,
		Permissions: req.Permissions,
		IsSystem:    false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// coordinatorPermissions returns all permissions for the coordinator role.
// Coordinators have full control over the system.
func coordinatorPermissions() []Permission {
	return []Permission{
		// Full agent management
		PermAgentCreate, PermAgentRead, PermAgentUpdate, PermAgentDelete, PermAgentControl,
		// Full task management
		PermTaskCreate, PermTaskRead, PermTaskAssign, PermTaskCancel, PermTaskExecute,
		// Full communication
		PermMessageSend, PermMessageRead, PermMessageBroadcast,
		// Full environment access
		PermEnvCreate, PermEnvRead, PermEnvDestroy, PermEnvExecute,
		// Full filesystem access
		PermFileRead, PermFileWrite, PermFileDelete,
		// Browser access
		PermBrowserUse,
		// Full memory access
		PermMemoryRead, PermMemoryWrite,
		// Role management
		PermRoleAssign, PermRoleRevoke, PermRoleManage,
	}
}

// workerPermissions returns permissions for the worker role.
// Workers can execute tasks and use tools, but cannot manage other agents.
func workerPermissions() []Permission {
	return []Permission{
		// Read-only agent info
		PermAgentRead,
		// Task execution (not assignment)
		PermTaskRead, PermTaskExecute,
		// Basic communication
		PermMessageSend, PermMessageRead,
		// Environment usage
		PermEnvRead, PermEnvExecute,
		// Filesystem access
		PermFileRead, PermFileWrite,
		// Browser access
		PermBrowserUse,
		// Memory access
		PermMemoryRead, PermMemoryWrite,
	}
}

// reviewerPermissions returns permissions for the reviewer role.
// Reviewers can read and provide feedback but cannot execute.
func reviewerPermissions() []Permission {
	return []Permission{
		// Read-only agent info
		PermAgentRead,
		// Read-only task access
		PermTaskRead,
		// Communication (read + send for feedback)
		PermMessageSend, PermMessageRead,
		// Read-only environment
		PermEnvRead,
		// Read-only filesystem
		PermFileRead,
		// Read-only memory
		PermMemoryRead,
	}
}

// observerPermissions returns permissions for the observer role.
// Observers have strictly read-only access.
func observerPermissions() []Permission {
	return []Permission{
		PermAgentRead,
		PermTaskRead,
		PermMessageRead,
		PermEnvRead,
		PermFileRead,
		PermMemoryRead,
	}
}

// AllPermissions returns the complete list of all defined permissions.
func AllPermissions() []Permission {
	return []Permission{
		PermAgentCreate, PermAgentRead, PermAgentUpdate, PermAgentDelete, PermAgentControl,
		PermTaskCreate, PermTaskRead, PermTaskAssign, PermTaskCancel, PermTaskExecute,
		PermMessageSend, PermMessageRead, PermMessageBroadcast,
		PermEnvCreate, PermEnvRead, PermEnvDestroy, PermEnvExecute,
		PermFileRead, PermFileWrite, PermFileDelete,
		PermBrowserUse,
		PermMemoryRead, PermMemoryWrite,
		PermRoleAssign, PermRoleRevoke, PermRoleManage,
	}
}

// ValidatePermission checks if a permission string is a known permission.
func ValidatePermission(p Permission) bool {
	for _, known := range AllPermissions() {
		if p == known {
			return true
		}
	}
	return false
}
