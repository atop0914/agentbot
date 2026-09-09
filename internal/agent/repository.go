package agent

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// MemoryRepository 基于内存的 Agent 存储实现
// 适合开发和测试阶段，后续可替换为 PostgreSQL 实现
type MemoryRepository struct {
	mu     sync.RWMutex
	agents map[string]*Agent
}

// NewMemoryRepository 创建内存存储仓库
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		agents: make(map[string]*Agent),
	}
}

// Create 创建新 Agent
func (r *MemoryRepository) Create(ctx context.Context, agent *Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.agents[agent.ID]; exists {
		return fmt.Errorf("agent %s already exists", agent.ID)
	}

	// 存储副本，避免外部修改影响内部状态
	copy := *agent
	r.agents[agent.ID] = &copy
	return nil
}

// GetByID 根据 ID 获取 Agent
func (r *MemoryRepository) GetByID(ctx context.Context, id string) (*Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agent, ok := r.agents[id]
	if !ok {
		return nil, fmt.Errorf("agent %s not found", id)
	}

	// 返回副本
	copy := *agent
	return &copy, nil
}

// Update 更新 Agent
func (r *MemoryRepository) Update(ctx context.Context, agent *Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.agents[agent.ID]; !exists {
		return fmt.Errorf("agent %s not found", agent.ID)
	}

	copy := *agent
	r.agents[agent.ID] = &copy
	return nil
}

// Delete 删除 Agent
func (r *MemoryRepository) Delete(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.agents[id]; !exists {
		return fmt.Errorf("agent %s not found", id)
	}

	delete(r.agents, id)
	return nil
}

// List 列出 Agent（分页，按创建时间降序）
func (r *MemoryRepository) List(ctx context.Context, offset, limit int) ([]*Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 收集所有 agent
	all := make([]*Agent, 0, len(r.agents))
	for _, a := range r.agents {
		copy := *a
		all = append(all, &copy)
	}

	// 按创建时间降序排序
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	// 分页
	if offset >= len(all) {
		return []*Agent{}, nil
	}

	end := offset + limit
	if end > len(all) {
		end = len(all)
	}

	return all[offset:end], nil
}

// UpdateState 仅更新 Agent 状态
func (r *MemoryRepository) UpdateState(ctx context.Context, id string, state State) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	agent, ok := r.agents[id]
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}

	agent.State = state
	return nil
}

// Count 返回 Agent 总数
func (r *MemoryRepository) Count(ctx context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.agents), nil
}

// ListByState 按状态过滤 Agent
func (r *MemoryRepository) ListByState(ctx context.Context, state State) ([]*Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Agent, 0)
	for _, a := range r.agents {
		if a.State == state {
			copy := *a
			result = append(result, &copy)
		}
	}
	return result, nil
}
