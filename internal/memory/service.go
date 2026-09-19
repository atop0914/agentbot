package memory

import (
	"context"
	"fmt"
	"time"
)

// memoryService 记忆服务实现
type memoryService struct {
	store Store
}

// NewMemoryService 创建记忆服务
func NewMemoryService(store Store) Service {
	return &memoryService{store: store}
}

// Create 创建记忆
func (s *memoryService) Create(ctx context.Context, req *CreateMemoryRequest) (*MemoryEntry, error) {
	if req.Content == "" {
		return nil, fmt.Errorf("content is required")
	}
	if req.AgentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}

	entry := &MemoryEntry{
		AgentID:    req.AgentID,
		UserID:     req.UserID,
		Type:       req.Type,
		Scope:      req.Scope,
		Content:    req.Content,
		Summary:    req.Summary,
		Tags:       req.Tags,
		Metadata:   req.Metadata,
		Importance: req.Importance,
	}

	// 设置 TTL
	if req.TTLSeconds != nil {
		ttl := time.Duration(*req.TTLSeconds) * time.Second
		entry.TTL = &ttl
	}

	// 默认重要性
	if entry.Importance == 0 {
		entry.Importance = 0.5
	}

	// 默认作用域
	if entry.Scope == "" {
		entry.Scope = MemoryScopeAgent
	}

	if err := s.store.Create(ctx, entry); err != nil {
		return nil, fmt.Errorf("create memory: %w", err)
	}
	return entry, nil
}

// Get 获取记忆
func (s *memoryService) Get(ctx context.Context, id string) (*MemoryEntry, error) {
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}
	return s.store.Get(ctx, id)
}

// Update 更新记忆
func (s *memoryService) Update(ctx context.Context, id string, req *UpdateMemoryRequest) (*MemoryEntry, error) {
	entry, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if req.Content != nil {
		entry.Content = *req.Content
	}
	if req.Summary != nil {
		entry.Summary = *req.Summary
	}
	if req.Tags != nil {
		entry.Tags = req.Tags
	}
	if req.Metadata != nil {
		entry.Metadata = req.Metadata
	}
	if req.Importance != nil {
		entry.Importance = *req.Importance
	}

	if err := s.store.Update(ctx, entry); err != nil {
		return nil, fmt.Errorf("update memory: %w", err)
	}
	return entry, nil
}

// Delete 删除记忆
func (s *memoryService) Delete(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("id is required")
	}
	return s.store.Delete(ctx, id)
}

// Search 搜索记忆
func (s *memoryService) Search(ctx context.Context, req *SearchMemoryRequest) ([]*MemoryEntry, int, error) {
	return s.store.Search(ctx, req)
}

// GetByAgent 获取 Agent 记忆
func (s *memoryService) GetByAgent(ctx context.Context, agentID string, limit, offset int) ([]*MemoryEntry, int, error) {
	return s.store.GetByAgent(ctx, agentID, limit, offset)
}

// GetByUser 获取用户记忆
func (s *memoryService) GetByUser(ctx context.Context, userID string, limit, offset int) ([]*MemoryEntry, int, error) {
	return s.store.GetByUser(ctx, userID, limit, offset)
}

// Cleanup 清理过期记忆
func (s *memoryService) Cleanup(ctx context.Context) (int, error) {
	return s.store.Cleanup(ctx)
}

// Stats 获取统计信息
func (s *memoryService) Stats(ctx context.Context) (*MemoryStats, error) {
	return s.store.Stats(ctx)
}
