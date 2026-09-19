package memory

import "time"

// MemoryType 记忆类型
type MemoryType string

const (
	// MemoryTypeConversation 对话上下文（短期）
	MemoryTypeConversation MemoryType = "conversation"
	// MemoryTypeFact 事实知识（长期）
	MemoryTypeFact MemoryType = "fact"
	// MemoryTypePreference 用户偏好（长期）
	MemoryTypePreference MemoryType = "preference"
	// MemoryTypeTaskContext 任务上下文（短期）
	MemoryTypeTaskContext MemoryType = "task_context"
	// MemoryTypeSkill 技能经验（长期）
	MemoryTypeSkill MemoryType = "skill"
)

// MemoryScope 记忆作用域
type MemoryScope string

const (
	// MemoryScopeAgent Agent 私有记忆
	MemoryScopeAgent MemoryScope = "agent"
	// MemoryScopeUser 用户级记忆（跨 Agent 共享）
	MemoryScopeUser MemoryScope = "user"
	// MemoryScopeGlobal 全局记忆（所有 Agent 共享）
	MemoryScopeGlobal MemoryScope = "global"
)

// MemoryEntry 记忆条目
type MemoryEntry struct {
	ID        string            `json:"id"`
	AgentID   string            `json:"agent_id"`
	UserID    string            `json:"user_id,omitempty"`
	Type      MemoryType        `json:"type"`
	Scope     MemoryScope       `json:"scope"`
	Content   string            `json:"content"`
	Summary   string            `json:"summary,omitempty"`
	Tags      []string          `json:"tags,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Importance float64          `json:"importance"` // 0.0-1.0 重要性评分
	TTL       *time.Duration    `json:"ttl,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
	AccessCount int             `json:"access_count"`
	LastAccessAt *time.Time     `json:"last_access_at,omitempty"`
}

// IsExpired 检查记忆是否已过期
func (m *MemoryEntry) IsExpired() bool {
	if m.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*m.ExpiresAt)
}

// Touch 更新访问时间和次数
func (m *MemoryEntry) Touch() {
	now := time.Now()
	m.LastAccessAt = &now
	m.AccessCount++
}

// CreateMemoryRequest 创建记忆请求
type CreateMemoryRequest struct {
	AgentID    string            `json:"agent_id" binding:"required"`
	UserID     string            `json:"user_id,omitempty"`
	Type       MemoryType        `json:"type" binding:"required"`
	Scope      MemoryScope       `json:"scope" binding:"required"`
	Content    string            `json:"content" binding:"required"`
	Summary    string            `json:"summary,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Importance float64           `json:"importance"`
	TTLSeconds *int              `json:"ttl_seconds,omitempty"`
}

// UpdateMemoryRequest 更新记忆请求
type UpdateMemoryRequest struct {
	Content    *string           `json:"content,omitempty"`
	Summary    *string           `json:"summary,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Importance *float64          `json:"importance,omitempty"`
}

// SearchMemoryRequest 搜索记忆请求
type SearchMemoryRequest struct {
	AgentID  string       `json:"agent_id,omitempty"`
	UserID   string       `json:"user_id,omitempty"`
	Type     MemoryType   `json:"type,omitempty"`
	Scope    MemoryScope  `json:"scope,omitempty"`
	Query    string       `json:"query,omitempty"`
	Tags     []string     `json:"tags,omitempty"`
	MinScore float64      `json:"min_score,omitempty"`
	Limit    int          `json:"limit,omitempty"`
	Offset   int          `json:"offset,omitempty"`
}

// MemoryStats 记忆统计
type MemoryStats struct {
	TotalEntries  int            `json:"total_entries"`
	ByType        map[string]int `json:"by_type"`
	ByScope       map[string]int `json:"by_scope"`
	ExpiredCount  int            `json:"expired_count"`
	AvgImportance float64        `json:"avg_importance"`
}
