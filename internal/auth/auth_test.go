package auth_test

import (
	"testing"

	"github.com/atop0914/agentbot/internal/auth"
)

func TestPasswordManager_HashPassword(t *testing.T) {
	pm := auth.NewPasswordManager()

	tests := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{"valid password", "StrongPass123!", false},
		{"too short", "abc", true},
		{"no uppercase", "lowercase123!", true},
		{"no digit", "NoDigitsHere!", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash, err := pm.HashPassword(tt.password)
			if (err != nil) != tt.wantErr {
				t.Errorf("HashPassword() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && hash == "" {
				t.Error("HashPassword() returned empty hash")
			}
		})
	}
}

func TestPasswordManager_VerifyPassword(t *testing.T) {
	pm := auth.NewPasswordManager()

	password := "StrongPass123!"
	hash, err := pm.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	// 验证正确密码
	if !pm.VerifyPassword(password, hash) {
		t.Error("VerifyPassword() failed for correct password")
	}

	// 验证错误密码
	if pm.VerifyPassword("WrongPassword123!", hash) {
		t.Error("VerifyPassword() succeeded for wrong password")
	}
}

func TestJWTManager_GenerateAndValidate(t *testing.T) {
	config := auth.TokenConfig{
		Secret:       "test-secret-key-for-jwt-signing",
		AccessExpiry: 3600000000000, // 1 hour in nanoseconds
		Issuer:       "test",
	}

	manager := auth.NewJWTManager(config)

	claims := &auth.Claims{
		UserID:   "user-123",
		Username: "testuser",
		Email:    "test@example.com",
		Role:     "user",
	}

	// 生成 token pair
	tokenPair, err := manager.GenerateTokenPair(claims)
	if err != nil {
		t.Fatalf("GenerateTokenPair() error = %v", err)
	}

	// 验证 access token
	parsedClaims, err := manager.ValidateAccessToken(tokenPair.AccessToken)
	if err != nil {
		t.Fatalf("ValidateAccessToken() error = %v", err)
	}

	if parsedClaims.UserID != claims.UserID {
		t.Errorf("UserID = %v, want %v", parsedClaims.UserID, claims.UserID)
	}
	if parsedClaims.Username != claims.Username {
		t.Errorf("Username = %v, want %v", parsedClaims.Username, claims.Username)
	}
	if parsedClaims.Email != claims.Email {
		t.Errorf("Email = %v, want %v", parsedClaims.Email, claims.Email)
	}

	// 验证 refresh token
	userID, err := manager.ValidateRefreshToken(tokenPair.RefreshToken)
	if err != nil {
		t.Fatalf("ValidateRefreshToken() error = %v", err)
	}
	if userID != claims.UserID {
		t.Errorf("UserID = %v, want %v", userID, claims.UserID)
	}
}

func TestJWTManager_InvalidToken(t *testing.T) {
	config := auth.TokenConfig{
		Secret: "test-secret",
		Issuer: "test",
	}

	manager := auth.NewJWTManager(config)

	// 测试无效 token
	_, err := manager.ValidateAccessToken("invalid-token")
	if err == nil {
		t.Error("ValidateAccessToken() should fail for invalid token")
	}

	// 测试空 token
	_, err = manager.ValidateAccessToken("")
	if err == nil {
		t.Error("ValidateAccessToken() should fail for empty token")
	}
}

func TestGenerateState(t *testing.T) {
	state1, err := auth.GenerateState()
	if err != nil {
		t.Fatalf("GenerateState() error = %v", err)
	}

	state2, err := auth.GenerateState()
	if err != nil {
		t.Fatalf("GenerateState() error = %v", err)
	}

	if state1 == state2 {
		t.Error("GenerateState() should return unique states")
	}

	if len(state1) != 32 { // 16 bytes = 32 hex chars
		t.Errorf("GenerateState() length = %v, want 32", len(state1))
	}
}
