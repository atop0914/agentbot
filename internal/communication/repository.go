package communication

import (
	"context"
	"sync"
	"time"
)

// MemoryRepository stores messages and groups in memory.
type MemoryRepository struct {
	mu       sync.RWMutex
	messages []Message
	groups   map[string]*Group
	channels map[string]*Channel
}

// NewMemoryRepository creates a new in-memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		groups:   make(map[string]*Group),
		channels: make(map[string]*Channel),
	}
}

// SaveMessage stores a message.
func (r *MemoryRepository) SaveMessage(_ context.Context, msg Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	r.messages = append(r.messages, msg)
	return nil
}

// GetMessages returns messages for an agent, most recent first.
func (r *MemoryRepository) GetMessages(_ context.Context, agentID string, limit int) ([]Message, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Message
	// Iterate backwards for most recent
	for i := len(r.messages) - 1; i >= 0; i-- {
		msg := r.messages[i]
		if msg.To == agentID || msg.From == agentID {
			result = append(result, msg)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

// GetConversation returns messages between two agents.
func (r *MemoryRepository) GetConversation(_ context.Context, agent1, agent2 string, limit int) ([]Message, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Message
	for i := len(r.messages) - 1; i >= 0; i-- {
		msg := r.messages[i]
		if (msg.From == agent1 && msg.To == agent2) || (msg.From == agent2 && msg.To == agent1) {
			result = append(result, msg)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

// SaveGroup stores a group.
func (r *MemoryRepository) SaveGroup(_ context.Context, group *Group) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groups[group.ID] = group
	return nil
}

// GetGroup returns a group by ID.
func (r *MemoryRepository) GetGroup(_ context.Context, id string) (*Group, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	g, ok := r.groups[id]
	if !ok {
		return nil, nil
	}
	return g, nil
}

// ListGroups returns groups for an agent.
func (r *MemoryRepository) ListGroups(_ context.Context, agentID string) ([]Group, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Group
	for _, g := range r.groups {
		for _, m := range g.Members {
			if m == agentID {
				result = append(result, *g)
				break
			}
		}
	}
	return result, nil
}

// SaveChannel stores a channel.
func (r *MemoryRepository) SaveChannel(_ context.Context, ch *Channel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.channels[ch.ID] = ch
	return nil
}

// GetChannel returns a channel by ID.
func (r *MemoryRepository) GetChannel(_ context.Context, id string) (*Channel, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ch, ok := r.channels[id]
	if !ok {
		return nil, nil
	}
	return ch, nil
}

// ListChannels returns channels for an agent.
func (r *MemoryRepository) ListChannels(_ context.Context, agentID string) ([]Channel, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Channel
	for _, ch := range r.channels {
		for _, m := range ch.Members {
			if m == agentID {
				result = append(result, *ch)
				break
			}
		}
	}
	return result, nil
}
