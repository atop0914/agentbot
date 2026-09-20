package user

import (
	"fmt"
	"testing"
)

// --- MemoryRepository Tests ---

func TestMemoryRepository_Create(t *testing.T) {
	repo := NewMemoryRepository()

	user := &User{
		ID:       "user-1",
		Email:    "test@example.com",
		Username: "testuser",
	}
	if err := repo.Create(user); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if user.Role != RoleUser {
		t.Errorf("default role = %q, want %q", user.Role, RoleUser)
	}
	if user.Status != StatusActive {
		t.Errorf("default status = %q, want %q", user.Status, StatusActive)
	}
	if user.CreatedAt.IsZero() {
		t.Error("CreatedAt not set")
	}
}

func TestMemoryRepository_CreateDuplicate(t *testing.T) {
	repo := NewMemoryRepository()

	user := &User{ID: "user-1", Email: "test@example.com", Username: "testuser"}
	repo.Create(user)

	// Duplicate ID
	if err := repo.Create(&User{ID: "user-1", Email: "other@example.com", Username: "other"}); err == nil {
		t.Error("expected error for duplicate ID")
	}

	// Duplicate email
	if err := repo.Create(&User{ID: "user-2", Email: "test@example.com", Username: "other"}); err == nil {
		t.Error("expected error for duplicate email")
	}

	// Duplicate username
	if err := repo.Create(&User{ID: "user-2", Email: "other@example.com", Username: "testuser"}); err == nil {
		t.Error("expected error for duplicate username")
	}
}

func TestMemoryRepository_GetByID(t *testing.T) {
	repo := NewMemoryRepository()

	user := &User{ID: "user-1", Email: "test@example.com", Username: "testuser"}
	repo.Create(user)

	got, err := repo.GetByID("user-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.Email != "test@example.com" {
		t.Errorf("email = %q, want %q", got.Email, "test@example.com")
	}

	// Not found
	got, err = repo.GetByID("nonexistent")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestMemoryRepository_GetByEmail(t *testing.T) {
	repo := NewMemoryRepository()

	user := &User{ID: "user-1", Email: "test@example.com", Username: "testuser"}
	repo.Create(user)

	got, err := repo.GetByEmail("test@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.ID != "user-1" {
		t.Errorf("id = %q, want %q", got.ID, "user-1")
	}

	// Not found
	got, err = repo.GetByEmail("nonexistent@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestMemoryRepository_GetByUsername(t *testing.T) {
	repo := NewMemoryRepository()

	user := &User{ID: "user-1", Email: "test@example.com", Username: "testuser"}
	repo.Create(user)

	got, err := repo.GetByUsername("testuser")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.ID != "user-1" {
		t.Errorf("id = %q, want %q", got.ID, "user-1")
	}
}

func TestMemoryRepository_GetByOAuth(t *testing.T) {
	repo := NewMemoryRepository()

	user := &User{ID: "user-1", Email: "test@example.com", Username: "testuser", OAuthProvider: "github", OAuthID: "gh-123"}
	repo.Create(user)

	got, err := repo.GetByOAuth("github", "gh-123")
	if err != nil {
		t.Fatalf("GetByOAuth: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.ID != "user-1" {
		t.Errorf("id = %q, want %q", got.ID, "user-1")
	}

	// Not found
	got, err = repo.GetByOAuth("google", "nonexistent")
	if err != nil {
		t.Fatalf("GetByOAuth: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestMemoryRepository_Update(t *testing.T) {
	repo := NewMemoryRepository()

	user := &User{ID: "user-1", Email: "test@example.com", Username: "testuser"}
	repo.Create(user)

	user.Email = "updated@example.com"
	if err := repo.Update(user); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := repo.GetByID("user-1")
	if got.Email != "updated@example.com" {
		t.Errorf("email = %q, want %q", got.Email, "updated@example.com")
	}

	// Update non-existent
	if err := repo.Update(&User{ID: "nonexistent"}); err == nil {
		t.Error("expected error for non-existent user")
	}
}

func TestMemoryRepository_Delete(t *testing.T) {
	repo := NewMemoryRepository()

	user := &User{ID: "user-1", Email: "test@example.com", Username: "testuser"}
	repo.Create(user)

	if err := repo.Delete("user-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ := repo.GetByID("user-1")
	if got != nil {
		t.Error("expected nil after delete")
	}

	// Delete non-existent
	if err := repo.Delete("nonexistent"); err == nil {
		t.Error("expected error for non-existent user")
	}
}

func TestMemoryRepository_List(t *testing.T) {
	repo := NewMemoryRepository()

	for i := 0; i < 5; i++ {
		repo.Create(&User{
			ID:       fmt.Sprintf("user-%d", i),
			Email:    fmt.Sprintf("user%d@example.com", i),
			Username: fmt.Sprintf("user%d", i),
		})
	}

	users, total, err := repo.List(0, 3)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(users) != 3 {
		t.Errorf("len = %d, want 3", len(users))
	}

	// Second page
	users, total, err = repo.List(3, 3)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("len = %d, want 2", len(users))
	}

	// Offset beyond total
	users, _, err = repo.List(10, 3)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("len = %d, want 0", len(users))
	}
}

func TestMemoryRepository_UpdateLastLogin(t *testing.T) {
	repo := NewMemoryRepository()

	user := &User{ID: "user-1", Email: "test@example.com", Username: "testuser"}
	repo.Create(user)

	if err := repo.UpdateLastLogin("user-1"); err != nil {
		t.Fatalf("UpdateLastLogin: %v", err)
	}

	got, _ := repo.GetByID("user-1")
	if got.LastLoginAt == nil {
		t.Error("LastLoginAt not set")
	}

	// Non-existent
	if err := repo.UpdateLastLogin("nonexistent"); err == nil {
		t.Error("expected error for non-existent user")
	}
}

// --- UserService Tests ---

func TestUserService_Create(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewUserService(repo)

	user, err := svc.Create(&CreateUserRequest{
		Email:    "test@example.com",
		Username: "testuser",
		Password: "StrongPass123!",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if user.ID == "" {
		t.Error("user ID not generated")
	}
	if user.Email != "test@example.com" {
		t.Errorf("email = %q, want %q", user.Email, "test@example.com")
	}
	if user.PasswordHash == "" || user.PasswordHash == "StrongPass123!" {
		t.Error("password not hashed")
	}
}

func TestUserService_CreateDuplicate(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewUserService(repo)

	svc.Create(&CreateUserRequest{Email: "test@example.com", Username: "testuser", Password: "pass"})

	// Duplicate email
	_, err := svc.Create(&CreateUserRequest{Email: "test@example.com", Username: "other", Password: "pass"})
	if err == nil {
		t.Error("expected error for duplicate email")
	}

	// Duplicate username
	_, err = svc.Create(&CreateUserRequest{Email: "other@example.com", Username: "testuser", Password: "pass"})
	if err == nil {
		t.Error("expected error for duplicate username")
	}
}

func TestUserService_ValidatePassword(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewUserService(repo)

	svc.Create(&CreateUserRequest{Email: "test@example.com", Username: "testuser", Password: "StrongPass123!"})

	// Valid
	user, err := svc.ValidatePassword("test@example.com", "StrongPass123!")
	if err != nil {
		t.Fatalf("ValidatePassword: %v", err)
	}
	if user == nil {
		t.Fatal("expected user, got nil")
	}

	// Invalid password
	_, err = svc.ValidatePassword("test@example.com", "wrong")
	if err == nil {
		t.Error("expected error for wrong password")
	}

	// Invalid email
	_, err = svc.ValidatePassword("nonexistent@example.com", "StrongPass123!")
	if err == nil {
		t.Error("expected error for non-existent email")
	}
}

func TestUserService_Update(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewUserService(repo)

	user, _ := svc.Create(&CreateUserRequest{Email: "test@example.com", Username: "testuser", Password: "pass"})

	newEmail := "updated@example.com"
	updated, err := svc.Update(user.ID, &UpdateUserRequest{Email: &newEmail})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Email != "updated@example.com" {
		t.Errorf("email = %q, want %q", updated.Email, "updated@example.com")
	}

	// Update non-existent
	_, err = svc.Update("nonexistent", &UpdateUserRequest{Email: &newEmail})
	if err == nil {
		t.Error("expected error for non-existent user")
	}
}

func TestUserService_UpdatePassword(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewUserService(repo)

	user, _ := svc.Create(&CreateUserRequest{Email: "test@example.com", Username: "testuser", Password: "OldPass123!"})

	// Wrong old password
	if err := svc.UpdatePassword(user.ID, "wrong", "NewPass123!"); err == nil {
		t.Error("expected error for wrong old password")
	}

	// Valid update
	if err := svc.UpdatePassword(user.ID, "OldPass123!", "NewPass123!"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}

	// Verify new password works
	_, err := svc.ValidatePassword("test@example.com", "NewPass123!")
	if err != nil {
		t.Fatalf("ValidatePassword with new password: %v", err)
	}
}

func TestUserService_LinkOAuth(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewUserService(repo)

	user, _ := svc.Create(&CreateUserRequest{Email: "test@example.com", Username: "testuser", Password: "pass"})

	if err := svc.LinkOAuth(user.ID, "github", "gh-123"); err != nil {
		t.Fatalf("LinkOAuth: %v", err)
	}

	updated, _ := repo.GetByID(user.ID)
	if updated.OAuthProvider != "github" || updated.OAuthID != "gh-123" {
		t.Errorf("OAuth not linked: provider=%q, id=%q", updated.OAuthProvider, updated.OAuthID)
	}

	// Non-existent user
	if err := svc.LinkOAuth("nonexistent", "github", "gh-456"); err == nil {
		t.Error("expected error for non-existent user")
	}
}
