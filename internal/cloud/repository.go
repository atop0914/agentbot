package cloud

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Repository defines the interface for environment persistence
type Repository interface {
	Create(ctx context.Context, env *Environment) error
	GetByID(ctx context.Context, id string) (*Environment, error)
	Update(ctx context.Context, env *Environment) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, agentID string) ([]*Environment, error)
	ListAll(ctx context.Context, offset, limit int) ([]*Environment, error)
}

// MemoryRepository 基于内存的环境存储实现
type MemoryRepository struct {
	mu           sync.RWMutex
	environments map[string]*Environment
}

// NewMemoryRepository 创建内存存储仓库
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		environments: make(map[string]*Environment),
	}
}

// Create 创建新环境记录
func (r *MemoryRepository) Create(ctx context.Context, env *Environment) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.environments[env.ID]; exists {
		return fmt.Errorf("environment %s already exists", env.ID)
	}

	copy := *env
	r.environments[env.ID] = &copy
	return nil
}

// GetByID 根据 ID 获取环境
func (r *MemoryRepository) GetByID(ctx context.Context, id string) (*Environment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	env, ok := r.environments[id]
	if !ok {
		return nil, fmt.Errorf("environment %s not found", id)
	}

	copy := *env
	return &copy, nil
}

// Update 更新环境信息
func (r *MemoryRepository) Update(ctx context.Context, env *Environment) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.environments[env.ID]; !exists {
		return fmt.Errorf("environment %s not found", env.ID)
	}

	copy := *env
	r.environments[env.ID] = &copy
	return nil
}

// Delete 删除环境记录
func (r *MemoryRepository) Delete(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.environments[id]; !exists {
		return fmt.Errorf("environment %s not found", id)
	}

	delete(r.environments, id)
	return nil
}

// List 列出指定 Agent 的所有环境
func (r *MemoryRepository) List(ctx context.Context, agentID string) ([]*Environment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Environment, 0)
	for _, env := range r.environments {
		if env.AgentID == agentID {
			copy := *env
			result = append(result, &copy)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	return result, nil
}

// ListAll 列出所有环境（分页）
func (r *MemoryRepository) ListAll(ctx context.Context, offset, limit int) ([]*Environment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*Environment, 0, len(r.environments))
	for _, env := range r.environments {
		copy := *env
		all = append(all, &copy)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	if offset >= len(all) {
		return []*Environment{}, nil
	}

	end := offset + limit
	if end > len(all) {
		end = len(all)
	}

	return all[offset:end], nil
}

// Count 返回环境总数
func (r *MemoryRepository) Count(ctx context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.environments), nil
}
