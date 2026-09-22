package role

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSystemRoles(t *testing.T) {
	roles := SystemRoles()
	if len(roles) != 4 {
		t.Fatalf("expected 4 system roles, got %d", len(roles))
	}

	roleTypes := make(map[RoleType]bool)
	for _, r := range roles {
		roleTypes[r.Type] = true
		if !r.IsSystem {
			t.Errorf("system role %s should have IsSystem=true", r.Name)
		}
		if len(r.Permissions) == 0 {
			t.Errorf("system role %s should have permissions", r.Name)
		}
	}

	expectedTypes := []RoleType{RoleTypeCoordinator, RoleTypeWorker, RoleTypeReviewer, RoleTypeObserver}
	for _, rt := range expectedTypes {
		if !roleTypes[rt] {
			t.Errorf("missing system role type: %s", rt)
		}
	}
}

func TestCoordinatorHasAllPermissions(t *testing.T) {
	roles := SystemRoles()
	var coordinator *Role
	for _, r := range roles {
		if r.Type == RoleTypeCoordinator {
			coordinator = r
			break
		}
	}
	if coordinator == nil {
		t.Fatal("coordinator role not found")
	}

	allPerms := AllPermissions()
	permSet := make(map[Permission]bool)
	for _, p := range coordinator.Permissions {
		permSet[p] = true
	}
	for _, p := range allPerms {
		if !permSet[p] {
			t.Errorf("coordinator missing permission: %s", p)
		}
	}
}

func TestObserverHasReadOnlyPermissions(t *testing.T) {
	roles := SystemRoles()
	var observer *Role
	for _, r := range roles {
		if r.Type == RoleTypeObserver {
			observer = r
			break
		}
	}
	if observer == nil {
		t.Fatal("observer role not found")
	}

	writePerms := []Permission{
		PermAgentCreate, PermAgentUpdate, PermAgentDelete, PermAgentControl,
		PermTaskCreate, PermTaskAssign, PermTaskCancel, PermTaskExecute,
		PermMessageSend, PermMessageBroadcast,
		PermEnvCreate, PermEnvDestroy, PermEnvExecute,
		PermFileWrite, PermFileDelete,
		PermBrowserUse,
		PermMemoryWrite,
		PermRoleAssign, PermRoleRevoke, PermRoleManage,
	}

	permSet := make(map[Permission]bool)
	for _, p := range observer.Permissions {
		permSet[p] = true
	}
	for _, p := range writePerms {
		if permSet[p] {
			t.Errorf("observer should not have write permission: %s", p)
		}
	}
}

func TestValidatePermission(t *testing.T) {
	tests := []struct {
		perm    Permission
		isValid bool
	}{
		{PermAgentRead, true},
		{PermTaskExecute, true},
		{"invalid:perm", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ValidatePermission(tt.perm); got != tt.isValid {
			t.Errorf("ValidatePermission(%q) = %v, want %v", tt.perm, got, tt.isValid)
		}
	}
}

func TestMemoryRepository_CRUD(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	// List system roles
	roles, err := repo.ListRoles(ctx)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if len(roles) != 4 {
		t.Fatalf("expected 4 system roles, got %d", len(roles))
	}

	// Create custom role
	custom := &Role{
		ID:          "role-custom-1",
		Name:        "Custom Role",
		Type:        RoleTypeCustom,
		Description: "A test custom role",
		Permissions: []Permission{PermAgentRead, PermTaskRead},
		IsSystem:    false,
	}
	if err := repo.CreateRole(ctx, custom); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	// Get role
	got, err := repo.GetRole(ctx, "role-custom-1")
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if got.Name != "Custom Role" {
		t.Errorf("got name %q, want %q", got.Name, "Custom Role")
	}

	// Update role
	got.Name = "Updated Role"
	if err := repo.UpdateRole(ctx, got); err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	got2, _ := repo.GetRole(ctx, "role-custom-1")
	if got2.Name != "Updated Role" {
		t.Errorf("name not updated: %q", got2.Name)
	}

	// Delete custom role
	if err := repo.DeleteRole(ctx, "role-custom-1"); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
	_, err = repo.GetRole(ctx, "role-custom-1")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestMemoryRepository_CannotDeleteSystemRole(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	err := repo.DeleteRole(ctx, "role-coordinator")
	if err == nil {
		t.Error("expected error when deleting system role")
	}
}

func TestMemoryRepository_CannotCreateDuplicate(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	r := &Role{ID: "role-coordinator", Name: "Dup"}
	err := repo.CreateRole(ctx, r)
	if err == nil {
		t.Error("expected error when creating duplicate role")
	}
}

func TestMemoryRepository_AssignRevokeRole(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	assignment := &AgentRole{
		ID:      "ar-1",
		AgentID: "agent-1",
		RoleID:  "role-worker",
	}

	// Assign
	if err := repo.AssignRole(ctx, assignment); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}

	// Duplicate assign should fail
	err := repo.AssignRole(ctx, assignment)
	if err == nil {
		t.Error("expected error on duplicate assignment")
	}

	// Get agent roles
	roles, err := repo.GetAgentRoles(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgentRoles: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("expected 1 role, got %d", len(roles))
	}

	// Get agents by role
	agents, err := repo.GetAgentsByRole(ctx, "role-worker")
	if err != nil {
		t.Fatalf("GetAgentsByRole: %v", err)
	}
	if len(agents) != 1 || agents[0] != "agent-1" {
		t.Errorf("unexpected agents: %v", agents)
	}

	// Revoke
	if err := repo.RevokeRole(ctx, "agent-1", "role-worker"); err != nil {
		t.Fatalf("RevokeRole: %v", err)
	}

	// Revoking again should fail
	err = repo.RevokeRole(ctx, "agent-1", "role-worker")
	if err == nil {
		t.Error("expected error when revoking non-assigned role")
	}
}

func TestService_CreateRole(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	// Valid create
	role, err := svc.CreateRole(ctx, CreateRoleRequest{
		Name:        "Test Role",
		Description: "For testing",
		Permissions: []Permission{PermAgentRead, PermTaskRead},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if role.Type != RoleTypeCustom {
		t.Errorf("expected custom type, got %s", role.Type)
	}
	if role.IsSystem {
		t.Error("custom role should not be system")
	}

	// Missing name
	_, err = svc.CreateRole(ctx, CreateRoleRequest{
		Permissions: []Permission{PermAgentRead},
	})
	if err == nil {
		t.Error("expected error for missing name")
	}

	// Missing permissions
	_, err = svc.CreateRole(ctx, CreateRoleRequest{
		Name: "No Perms",
	})
	if err == nil {
		t.Error("expected error for missing permissions")
	}

	// Invalid permission
	_, err = svc.CreateRole(ctx, CreateRoleRequest{
		Name:        "Bad",
		Permissions: []Permission{"fake:perm"},
	})
	if err == nil {
		t.Error("expected error for invalid permission")
	}
}

func TestService_AssignAndCheckPermission(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	// Assign worker role to agent-1
	_, err := svc.AssignRole(ctx, AssignRoleRequest{
		AgentID: "agent-1",
		RoleID:  "role-worker",
	})
	if err != nil {
		t.Fatalf("AssignRole: %v", err)
	}

	// Worker should have task:execute
	allowed, err := svc.CheckPermission(ctx, "agent-1", PermTaskExecute)
	if err != nil {
		t.Fatalf("CheckPermission: %v", err)
	}
	if !allowed {
		t.Error("worker should have task:execute permission")
	}

	// Worker should NOT have task:assign
	allowed, err = svc.CheckPermission(ctx, "agent-1", PermTaskAssign)
	if err != nil {
		t.Fatalf("CheckPermission: %v", err)
	}
	if allowed {
		t.Error("worker should NOT have task:assign permission")
	}

	// Get all permissions for agent-1
	perms, err := svc.GetAgentPermissions(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgentPermissions: %v", err)
	}
	if len(perms) == 0 {
		t.Error("expected some permissions")
	}

	// Agent with no roles should have no permissions
	allowed, err = svc.CheckPermission(ctx, "agent-nobody", PermAgentRead)
	if err != nil {
		t.Fatalf("CheckPermission: %v", err)
	}
	if allowed {
		t.Error("unassigned agent should have no permissions")
	}
}

func TestService_CannotModifySystemRole(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	name := "Hacked"
	_, err := svc.UpdateRole(ctx, "role-coordinator", UpdateRoleRequest{
		Name: &name,
	})
	if err == nil {
		t.Error("expected error when modifying system role")
	}
}

func TestHandler_ListRoles(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	h := NewHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/roles", nil)
	w := httptest.NewRecorder()
	h.listRoles(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	roles := resp["roles"].([]interface{})
	if len(roles) < 4 {
		t.Errorf("expected at least 4 roles, got %d", len(roles))
	}
}

func TestHandler_CreateAndGetRole(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	h := NewHandler(svc)

	// Create
	body, _ := json.Marshal(CreateRoleRequest{
		Name:        "API Test Role",
		Description: "Created via API",
		Permissions: []Permission{PermAgentRead},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/roles", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.createRole(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}

	var created RoleResponse
	json.NewDecoder(w.Body).Decode(&created)
	if created.Name != "API Test Role" {
		t.Errorf("got name %q", created.Name)
	}

	// Get
	req = httptest.NewRequest(http.MethodGet, "/api/v1/roles/"+created.ID, nil)
	w = httptest.NewRecorder()
	h.getRole(w, req, created.ID)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandler_AssignAndGetAgentRoles(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	h := NewHandler(svc)

	// Assign
	body, _ := json.Marshal(AssignRoleRequest{
		RoleID: "role-worker",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/role-assignments/agent-1", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.assignRole(w, req, "agent-1")

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}

	// Get agent roles
	req = httptest.NewRequest(http.MethodGet, "/api/v1/role-assignments/agent-1", nil)
	w = httptest.NewRecorder()
	h.getAgentRoles(w, req, "agent-1")

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	roles := resp["roles"].([]interface{})
	if len(roles) != 1 {
		t.Errorf("expected 1 role, got %d", len(roles))
	}
}

func TestHandler_PermissionCheck(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	h := NewHandler(svc)

	// Assign coordinator role
	svc.AssignRole(context.Background(), AssignRoleRequest{
		AgentID: "agent-1",
		RoleID:  "role-coordinator",
	})

	// Check allowed
	body, _ := json.Marshal(PermissionCheckRequest{
		AgentID:    "agent-1",
		Permission: PermRoleManage,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/permissions/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.handlePermissionCheck(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp PermissionCheckResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !resp.Allowed {
		t.Error("coordinator should have role:manage")
	}
}

func TestHandler_ListPermissions(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	h := NewHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/permissions", nil)
	w := httptest.NewRecorder()
	h.handleListPermissions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	total := int(resp["total"].(float64))
	if total != len(AllPermissions()) {
		t.Errorf("expected %d permissions, got %d", len(AllPermissions()), total)
	}
}

func TestNewCustomRole(t *testing.T) {
	role := NewCustomRole(CreateRoleRequest{
		Name:        "Test",
		Description: "desc",
		Permissions: []Permission{PermAgentRead},
	})
	if role.ID == "" {
		t.Error("expected generated ID")
	}
	if role.Type != RoleTypeCustom {
		t.Errorf("expected custom type, got %s", role.Type)
	}
	if role.IsSystem {
		t.Error("custom role should not be system")
	}
}
