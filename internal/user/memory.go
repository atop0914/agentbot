package user

import (
	"fmt"
	"sync"
	"time"
)

// MemoryRepository 基于内存的用户存储实现
type MemoryRepository struct {
	mu    sync.RWMutex
	users map[string]*User // id -> user
}

// NewMemoryRepository 创建内存存储仓库
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		users: make(map[string]*User),
	}
}

// Create 创建用户
func (r *MemoryRepository) Create(user *User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 检查 ID 唯一性
	if _, exists := r.users[user.ID]; exists {
		return fmt.Errorf("user %s already exists", user.ID)
	}

	// 检查邮箱唯一性
	for _, u := range r.users {
		if u.Email == user.Email {
			return fmt.Errorf("email %s already exists", user.Email)
		}
		if u.Username == user.Username {
			return fmt.Errorf("username %s already exists", user.Username)
		}
	}

	now := time.Now()
	user.CreatedAt = now
	user.UpdatedAt = now
	if user.Role == "" {
		user.Role = RoleUser
	}
	if user.Status == "" {
		user.Status = StatusActive
	}

	copy := *user
	r.users[user.ID] = &copy
	return nil
}

// GetByID 根据 ID 获取用户
func (r *MemoryRepository) GetByID(id string) (*User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	u, ok := r.users[id]
	if !ok {
		return nil, nil
	}
	copy := *u
	return &copy, nil
}

// GetByEmail 根据邮箱获取用户
func (r *MemoryRepository) GetByEmail(email string) (*User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, u := range r.users {
		if u.Email == email {
			copy := *u
			return &copy, nil
		}
	}
	return nil, nil
}

// GetByUsername 根据用户名获取用户
func (r *MemoryRepository) GetByUsername(username string) (*User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, u := range r.users {
		if u.Username == username {
			copy := *u
			return &copy, nil
		}
	}
	return nil, nil
}

// GetByOAuth 根据 OAuth 信息获取用户
func (r *MemoryRepository) GetByOAuth(provider, oauthID string) (*User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, u := range r.users {
		if u.OAuthProvider == provider && u.OAuthID == oauthID {
			copy := *u
			return &copy, nil
		}
	}
	return nil, nil
}

// Update 更新用户
func (r *MemoryRepository) Update(user *User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.users[user.ID]; !exists {
		return fmt.Errorf("user %s not found", user.ID)
	}
	user.UpdatedAt = time.Now()
	copy := *user
	r.users[user.ID] = &copy
	return nil
}

// Delete 删除用户
func (r *MemoryRepository) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.users[id]; !exists {
		return fmt.Errorf("user %s not found", id)
	}
	delete(r.users, id)
	return nil
}

// List 列出用户
func (r *MemoryRepository) List(offset, limit int) ([]*User, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	total := len(r.users)
	all := make([]*User, 0, total)
	for _, u := range r.users {
		copy := *u
		all = append(all, &copy)
	}

	if offset >= total {
		return []*User{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return all[offset:end], total, nil
}

// UpdateLastLogin 更新最后登录时间
func (r *MemoryRepository) UpdateLastLogin(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	u, ok := r.users[id]
	if !ok {
		return fmt.Errorf("user %s not found", id)
	}
	now := time.Now()
	u.LastLoginAt = &now
	return nil
}
