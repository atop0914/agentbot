package user

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// UserService 用户服务实现
type UserService struct {
	repo Repository
}

// NewUserService 创建用户服务
func NewUserService(repo Repository) *UserService {
	return &UserService{repo: repo}
}

// Create 创建用户
func (s *UserService) Create(req *CreateUserRequest) (*User, error) {
	// 检查邮箱是否已存在
	existing, err := s.repo.GetByEmail(req.Email)
	if err != nil {
		return nil, fmt.Errorf("failed to check email: %w", err)
	}
	if existing != nil {
		return nil, fmt.Errorf("email already exists")
	}

	// 检查用户名是否已存在
	existing, err = s.repo.GetByUsername(req.Username)
	if err != nil {
		return nil, fmt.Errorf("failed to check username: %w", err)
	}
	if existing != nil {
		return nil, fmt.Errorf("username already exists")
	}

	// 哈希密码
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// 生成 ID
	id, err := generateID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate ID: %w", err)
	}

	user := &User{
		ID:           id,
		Email:        req.Email,
		Username:     req.Username,
		PasswordHash: string(hash),
		Role:         RoleUser,
		Status:       StatusActive,
	}

	if err := s.repo.Create(user); err != nil {
		return nil, err
	}

	return user, nil
}

// GetByID 根据 ID 获取用户
func (s *UserService) GetByID(id string) (*User, error) {
	return s.repo.GetByID(id)
}

// GetByEmail 根据邮箱获取用户
func (s *UserService) GetByEmail(email string) (*User, error) {
	return s.repo.GetByEmail(email)
}

// Update 更新用户
func (s *UserService) Update(id string, req *UpdateUserRequest) (*User, error) {
	user, err := s.repo.GetByID(id)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, fmt.Errorf("user not found")
	}

	if req.Email != nil {
		// 检查邮箱是否已被其他用户使用
		existing, err := s.repo.GetByEmail(*req.Email)
		if err != nil {
			return nil, err
		}
		if existing != nil && existing.ID != id {
			return nil, fmt.Errorf("email already exists")
		}
		user.Email = *req.Email
	}

	if req.Username != nil {
		// 检查用户名是否已被其他用户使用
		existing, err := s.repo.GetByUsername(*req.Username)
		if err != nil {
			return nil, err
		}
		if existing != nil && existing.ID != id {
			return nil, fmt.Errorf("username already exists")
		}
		user.Username = *req.Username
	}

	if req.Avatar != nil {
		user.Avatar = *req.Avatar
	}

	if err := s.repo.Update(user); err != nil {
		return nil, err
	}

	return user, nil
}

// Delete 删除用户
func (s *UserService) Delete(id string) error {
	return s.repo.Delete(id)
}

// List 列出用户
func (s *UserService) List(offset, limit int) ([]*User, int, error) {
	return s.repo.List(offset, limit)
}

// ValidatePassword 验证密码
func (s *UserService) ValidatePassword(email, password string) (*User, error) {
	user, err := s.repo.GetByEmail(email)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, fmt.Errorf("invalid email or password")
	}

	if user.PasswordHash == "" {
		return nil, fmt.Errorf("password login not available for this account")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, fmt.Errorf("invalid email or password")
	}

	return user, nil
}

// UpdatePassword 更新密码
func (s *UserService) UpdatePassword(id, oldPassword, newPassword string) error {
	user, err := s.repo.GetByID(id)
	if err != nil {
		return err
	}
	if user == nil {
		return fmt.Errorf("user not found")
	}

	// 验证旧密码
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(oldPassword)); err != nil {
		return fmt.Errorf("invalid old password")
	}

	// 哈希新密码
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	user.PasswordHash = string(hash)
	return s.repo.Update(user)
}

// LinkOAuth 关联 OAuth 账号
func (s *UserService) LinkOAuth(userID, provider, oauthID string) error {
	user, err := s.repo.GetByID(userID)
	if err != nil {
		return err
	}
	if user == nil {
		return fmt.Errorf("user not found")
	}

	user.OAuthProvider = provider
	user.OAuthID = oauthID
	return s.repo.Update(user)
}

// generateID 生成唯一 ID
func generateID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
