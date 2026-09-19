package memory

import "context"

// Store 记忆存储接口
type Store interface {
	// Create 创建记忆
	Create(ctx context.Context, entry *MemoryEntry) error
	// Get 获取记忆
	Get(ctx context.Context, id string) (*MemoryEntry, error)
	// Update 更新记忆
	Update(ctx context.Context, entry *MemoryEntry) error
	// Delete 删除记忆
	Delete(ctx context.Context, id string) error
	// Search 搜索记忆
	Search(ctx context.Context, req *SearchMemoryRequest) ([]*MemoryEntry, int, error)
	// GetByAgent 获取 Agent 的所有记忆
	GetByAgent(ctx context.Context, agentID string, limit, offset int) ([]*MemoryEntry, int, error)
	// GetByUser 获取用户的所有记忆
	GetByUser(ctx context.Context, userID string, limit, offset int) ([]*MemoryEntry, int, error)
	// Cleanup 清理过期记忆
	Cleanup(ctx context.Context) (int, error)
	// Stats 获取统计信息
	Stats(ctx context.Context) (*MemoryStats, error)
}

// Service 记忆服务接口
type Service interface {
	// Create 创建记忆
	Create(ctx context.Context, req *CreateMemoryRequest) (*MemoryEntry, error)
	// Get 获取记忆
	Get(ctx context.Context, id string) (*MemoryEntry, error)
	// Update 更新记忆
	Update(ctx context.Context, id string, req *UpdateMemoryRequest) (*MemoryEntry, error)
	// Delete 删除记忆
	Delete(ctx context.Context, id string) error
	// Search 搜索记忆
	Search(ctx context.Context, req *SearchMemoryRequest) ([]*MemoryEntry, int, error)
	// GetByAgent 获取 Agent 记忆
	GetByAgent(ctx context.Context, agentID string, limit, offset int) ([]*MemoryEntry, int, error)
	// GetByUser 获取用户记忆
	GetByUser(ctx context.Context, userID string, limit, offset int) ([]*MemoryEntry, int, error)
	// Cleanup 清理过期记忆
	Cleanup(ctx context.Context) (int, error)
	// Stats 获取统计信息
	Stats(ctx context.Context) (*MemoryStats, error)
}
