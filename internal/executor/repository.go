package executor

import (
	"context"
	"fmt"
	"sync"

	"github.com/atop0914/agentbot/internal/task"
)

// Repository 任务持久化接口
type Repository interface {
	Create(ctx context.Context, t *task.Task) error
	GetByID(ctx context.Context, id string) (*task.Task, error)
	Update(ctx context.Context, t *task.Task) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, filter TaskFilter, offset, limit int) ([]*task.Task, error)
	UpdateState(ctx context.Context, id string, state task.TaskState) error
}

// memoryRepository 内存任务仓库实现
type memoryRepository struct {
	mu    sync.RWMutex
	tasks map[string]*task.Task
}

// NewMemoryRepository 创建内存任务仓库
func NewMemoryRepository() Repository {
	return &memoryRepository{
		tasks: make(map[string]*task.Task),
	}
}

// Create 创建任务
func (r *memoryRepository) Create(ctx context.Context, t *task.Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tasks[t.ID]; exists {
		return fmt.Errorf("task already exists: %s", t.ID)
	}

	r.tasks[t.ID] = t
	return nil
}

// GetByID 根据 ID 获取任务（返回深拷贝，避免并发竞争）
func (r *memoryRepository) GetByID(ctx context.Context, id string) (*task.Task, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.tasks[id]
	if !ok {
		return nil, fmt.Errorf("task not found: %s", id)
	}
	return copyTask(t), nil
}

// Update 更新任务
func (r *memoryRepository) Update(ctx context.Context, t *task.Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tasks[t.ID]; !exists {
		return fmt.Errorf("task not found: %s", t.ID)
	}

	r.tasks[t.ID] = t
	return nil
}

// Delete 删除任务
func (r *memoryRepository) Delete(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tasks[id]; !exists {
		return fmt.Errorf("task not found: %s", id)
	}

	delete(r.tasks, id)
	return nil
}

// List 列出任务（支持过滤和分页）
func (r *memoryRepository) List(ctx context.Context, filter TaskFilter, offset, limit int) ([]*task.Task, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*task.Task
	for _, t := range r.tasks {
		if !matchFilter(t, filter) {
			continue
		}
		result = append(result, t)
	}

	// 分页
	if offset >= len(result) {
		return []*task.Task{}, nil
	}
	end := offset + limit
	if end > len(result) {
		end = len(result)
	}

	return result[offset:end], nil
}

// UpdateState 更新任务状态
func (r *memoryRepository) UpdateState(ctx context.Context, id string, state task.TaskState) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	t, ok := r.tasks[id]
	if !ok {
		return fmt.Errorf("task not found: %s", id)
	}

	t.State = state
	return nil
}

// matchFilter 检查任务是否匹配过滤条件
func matchFilter(t *task.Task, filter TaskFilter) bool {
	if filter.AgentID != "" && t.AgentID != filter.AgentID {
		return false
	}
	if filter.State != "" && t.State != filter.State {
		return false
	}
	return true
}

// copyTask 深拷贝 Task，避免并发读写同一指针
func copyTask(src *task.Task) *task.Task {
	dst := *src
	dst.Subtasks = make([]task.Subtask, len(src.Subtasks))
	for i, st := range src.Subtasks {
		dst.Subtasks[i] = copySubtask(st)
	}
	return &dst
}

// copySubtask 深拷贝 Subtask
func copySubtask(src task.Subtask) task.Subtask {
	dst := src
	if src.DependsOn != nil {
		dst.DependsOn = make([]string, len(src.DependsOn))
		copy(dst.DependsOn, src.DependsOn)
	}
	if src.Actions != nil {
		dst.Actions = make([]task.Action, len(src.Actions))
		for i, a := range src.Actions {
			dst.Actions[i] = copyAction(a)
		}
	}
	return dst
}

// copyAction 深拷贝 Action
func copyAction(src task.Action) task.Action {
	dst := src
	if src.Params != nil {
		dst.Params = make(map[string]string, len(src.Params))
		for k, v := range src.Params {
			dst.Params[k] = v
		}
	}
	return dst
}
