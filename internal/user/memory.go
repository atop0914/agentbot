package user

import (
	"fmt"
	"sort"
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

	// 检查邮箱唯一性。
	// 返回与 Postgres 实现**同一个**哨兵错误：两套后端的错误类型
	// 一旦分叉，handler 的 409 映射就会只在其中一种后端上生效。
	for _, u := range r.users {
		if u.Email == user.Email {
			return ErrEmailExists
		}
		if u.Username == user.Username {
			return ErrUsernameExists
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
	// 两个字段都为空时必须直接返回 nil，不能进循环。
	//
	// 未绑定 OAuth 的用户在存储里是两列空串，而「多条空串共存」是
	// 合法形态（Postgres 侧靠部分索引实现）。若这里拿空串去匹配，
	// 会因为 map 遍历顺序随机而**随机命中**其中一个未绑定账号 ——
	// 症状是第三方登录把用户登成另一个人：不报错、不失败，
	// 只是身份变成了别人的。契约测试抓到的正是这一条。
	if provider == "" || oauthID == "" {
		return nil, nil
	}

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

	// 必须排序：Go 的 map 迭代顺序是随机的，直接返回会让
	// 「带 offset 的分页」每次拿到不同的切片 —— 表现为某一页重复出现
	// 上一页的行、另一页永远看不到，且同一参数的两次查询结果不同。
	// 排序键用 (CreatedAt DESC, ID DESC) 与 Postgres 实现保持一致，
	// 保证两套后端下分页顺序也相同（否则换后端会让前端翻页错乱）。
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID > all[j].ID
	})

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
