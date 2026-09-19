package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryStore 内存记忆存储实现
type MemoryStore struct {
	mu      sync.RWMutex
	entries map[string]*MemoryEntry
}

// NewMemoryStore 创建内存记忆存储
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		entries: make(map[string]*MemoryEntry),
	}
}

// Create 创建记忆
func (s *MemoryStore) Create(ctx context.Context, entry *MemoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	now := time.Now()
	entry.CreatedAt = now
	entry.UpdatedAt = now

	// 设置过期时间
	if entry.TTL != nil {
		exp := now.Add(*entry.TTL)
		entry.ExpiresAt = &exp
	}

	s.entries[entry.ID] = entry
	return nil
}

// Get 获取记忆
func (s *MemoryStore) Get(ctx context.Context, id string) (*MemoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[id]
	if !ok {
		return nil, fmt.Errorf("memory entry not found: %s", id)
	}
	if entry.IsExpired() {
		delete(s.entries, id)
		return nil, fmt.Errorf("memory entry expired: %s", id)
	}
	entry.Touch()
	return entry, nil
}

// Update 更新记忆
func (s *MemoryStore) Update(ctx context.Context, entry *MemoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.entries[entry.ID]
	if !ok {
		return fmt.Errorf("memory entry not found: %s", entry.ID)
	}
	entry.CreatedAt = existing.CreatedAt
	entry.UpdatedAt = time.Now()
	entry.AccessCount = existing.AccessCount
	entry.LastAccessAt = existing.LastAccessAt
	s.entries[entry.ID] = entry
	return nil
}

// Delete 删除记忆
func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.entries[id]; !ok {
		return fmt.Errorf("memory entry not found: %s", id)
	}
	delete(s.entries, id)
	return nil
}

// Search 搜索记忆
func (s *MemoryStore) Search(ctx context.Context, req *SearchMemoryRequest) ([]*MemoryEntry, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*MemoryEntry
	for _, entry := range s.entries {
		if entry.IsExpired() {
			continue
		}
		if !matchSearch(entry, req) {
			continue
		}
		entry.Touch()
		results = append(results, entry)
	}

	// 按重要性降序排序
	sort.Slice(results, func(i, j int) bool {
		return results[i].Importance > results[j].Importance
	})

	total := len(results)

	// 分页
	offset := req.Offset
	if offset > len(results) {
		offset = len(results)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	end := offset + limit
	if end > len(results) {
		end = len(results)
	}

	return results[offset:end], total, nil
}

// GetByAgent 获取 Agent 的记忆
func (s *MemoryStore) GetByAgent(ctx context.Context, agentID string, limit, offset int) ([]*MemoryEntry, int, error) {
	return s.Search(ctx, &SearchMemoryRequest{
		AgentID: agentID,
		Limit:   limit,
		Offset:  offset,
	})
}

// GetByUser 获取用户的记忆
func (s *MemoryStore) GetByUser(ctx context.Context, userID string, limit, offset int) ([]*MemoryEntry, int, error) {
	return s.Search(ctx, &SearchMemoryRequest{
		UserID: userID,
		Limit:  limit,
		Offset: offset,
	})
}

// Cleanup 清理过期记忆
func (s *MemoryStore) Cleanup(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for id, entry := range s.entries {
		if entry.IsExpired() {
			delete(s.entries, id)
			count++
		}
	}
	return count, nil
}

// Stats 获取统计信息
func (s *MemoryStore) Stats(ctx context.Context) (*MemoryStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := &MemoryStats{
		ByType:  make(map[string]int),
		ByScope: make(map[string]int),
	}

	var totalImportance float64
	for _, entry := range s.entries {
		stats.TotalEntries++
		stats.ByType[string(entry.Type)]++
		stats.ByScope[string(entry.Scope)]++
		totalImportance += entry.Importance
		if entry.IsExpired() {
			stats.ExpiredCount++
		}
	}

	if stats.TotalEntries > 0 {
		stats.AvgImportance = totalImportance / float64(stats.TotalEntries)
	}

	return stats, nil
}

// matchSearch 检查记忆是否匹配搜索条件
func matchSearch(entry *MemoryEntry, req *SearchMemoryRequest) bool {
	if req.AgentID != "" && entry.AgentID != req.AgentID {
		return false
	}
	if req.UserID != "" && entry.UserID != req.UserID {
		return false
	}
	if req.Type != "" && entry.Type != req.Type {
		return false
	}
	if req.Scope != "" && entry.Scope != req.Scope {
		return false
	}
	if req.MinScore > 0 && entry.Importance < req.MinScore {
		return false
	}
	if req.Query != "" {
		query := strings.ToLower(req.Query)
		content := strings.ToLower(entry.Content)
		summary := strings.ToLower(entry.Summary)
		if !strings.Contains(content, query) && !strings.Contains(summary, query) {
			// 也搜索 tags
			found := false
			for _, tag := range entry.Tags {
				if strings.Contains(strings.ToLower(tag), query) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	if len(req.Tags) > 0 {
		tagSet := make(map[string]bool)
		for _, t := range entry.Tags {
			tagSet[strings.ToLower(t)] = true
		}
		for _, rt := range req.Tags {
			if !tagSet[strings.ToLower(rt)] {
				return false
			}
		}
	}
	return true
}
