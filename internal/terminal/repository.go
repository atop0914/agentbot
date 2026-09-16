package terminal

import (
	"fmt"
	"sync"
)

// MemoryRepository 内存终端会话仓库
type MemoryRepository struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewMemoryRepository 创建内存仓库
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		sessions: make(map[string]*Session),
	}
}

// Save 保存会话
func (r *MemoryRepository) Save(session *Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.sessions[session.ID]; exists {
		return fmt.Errorf("session %s already exists", session.ID)
	}

	copy := *session
	r.sessions[session.ID] = &copy
	return nil
}

// Get 获取会话
func (r *MemoryRepository) Get(id string) (*Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	session, ok := r.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %s not found", id)
	}

	copy := *session
	return &copy, nil
}

// Delete 删除会话
func (r *MemoryRepository) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.sessions[id]; !ok {
		return fmt.Errorf("session %s not found", id)
	}

	delete(r.sessions, id)
	return nil
}

// List 列出会话
func (r *MemoryRepository) List(filter SessionFilter) ([]*Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Session, 0)
	for _, s := range r.sessions {
		if filter.AgentID != "" && s.AgentID != filter.AgentID {
			continue
		}
		if filter.State != "" && s.State != filter.State {
			continue
		}
		copy := *s
		result = append(result, &copy)
	}
	return result, nil
}

// Update 更新会话
func (r *MemoryRepository) Update(session *Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.sessions[session.ID]; !ok {
		return fmt.Errorf("session %s not found", session.ID)
	}

	copy := *session
	r.sessions[session.ID] = &copy
	return nil
}
