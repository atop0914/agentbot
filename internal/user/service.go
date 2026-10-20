package user

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	apperrors "github.com/atop0914/agentbot/internal/pkg/errors"
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
		return nil, ErrEmailExists
	}

	// 检查用户名是否已存在
	existing, err = s.repo.GetByUsername(req.Username)
	if err != nil {
		return nil, fmt.Errorf("failed to check username: %w", err)
	}
	if existing != nil {
		return nil, ErrUsernameExists
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
		return nil, ErrNotFound
	}

	if req.Email != nil {
		// 检查邮箱是否已被其他用户使用
		existing, err := s.repo.GetByEmail(*req.Email)
		if err != nil {
			return nil, err
		}
		if existing != nil && existing.ID != id {
			return nil, ErrEmailExists
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
			return nil, ErrUsernameExists
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
//
// ⚠️ 所有「凭据无效」的错误都必须 %w 包装 apperrors.ErrInvalidPassword。
//
// 此前这里返回的是裸 fmt.Errorf，而 handler 用 errors.FromError 做分类：
// 裸错误落进 default 分支 → **500 而不是 401**。
// 实测确认：`POST /api/v1/auth/login` 用错密码返回 500。
//
// 为什么这个 bug 能长期存在：既有的集成测试只断言 `status != 200`
// （见 integration_test.go 的 TestAuthLoginInvalidCredentials），
// 而 500 也满足「不是 200」。断言过弱让一个真实的错误分类缺陷
// 一直绿着 —— 这也是本轮把它一起修掉的原因（否则限流测试
// 会一直看到 500 而无法判断 429 是否生效）。
//
// 「账号不存在」与「密码错误」共用同一个对外的错误与文案是**有意**的：
// 若两者可区分，登录接口就变成了账号枚举工具
// （攻击者据此判断某个邮箱是否已注册）。
func (s *UserService) ValidatePassword(email, password string) (*User, error) {
	user, err := s.repo.GetByEmail(email)
	if err != nil {
		// 查库失败不是「凭据无效」：保持原样返回，让它落进 500。
		// 把它伪装成 401 会让一次数据库故障看起来像用户输错了密码。
		return nil, err
	}
	if user == nil {
		return nil, fmt.Errorf("%w: invalid email or password", apperrors.ErrInvalidPassword)
	}

	// 状态检查在密码校验之前：停用/封禁账号即使密码正确也不得登录。
	if !user.Status.IsUsable() {
		return nil, fmt.Errorf("account is %s", user.Status)
	}

	if user.PasswordHash == "" {
		// 无密码的账号（例如仅 OAuth）：这不是「密码错了」，
		// 而是「这个账号不能用密码登录」，属于不可用而非未授权。
		return nil, fmt.Errorf("password login not available for this account")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, fmt.Errorf("%w: invalid email or password", apperrors.ErrInvalidPassword)
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

// SetStatus 启用 / 停用 / 封禁用户。
//
// 这是管理后台的写操作，因此：
//  1. 状态必须显式合法，不接受空字符串（避免"清空即启用"这类隐式升级）；
//  2. 停用/封禁必须立刻影响登录 —— ValidatePassword 会在校验密码前先看状态。
func (s *UserService) SetStatus(id string, status Status) (*User, error) {
	if !status.Valid() {
		return nil, fmt.Errorf("invalid status: %s", status)
	}

	user, err := s.repo.GetByID(id)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrNotFound
	}
	if user.Status == status {
		// 幂等：重复设置同一状态直接返回，避免产生无意义的 updated_at 变更。
		return user, nil
	}

	user.Status = status
	if err := s.repo.Update(user); err != nil {
		return nil, err
	}
	return user, nil
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
