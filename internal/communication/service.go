package communication

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// CommService implements the Service interface.
type CommService struct {
	repo *MemoryRepository
	bus  *MemoryBus
}

// NewCommService creates a new communication service.
func NewCommService(repo *MemoryRepository, bus *MemoryBus) *CommService {
	return &CommService{
		repo: repo,
		bus:  bus,
	}
}

func (s *CommService) SendMessage(ctx context.Context, msg Message) error {
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}

	// Store message
	if err := s.repo.SaveMessage(ctx, msg); err != nil {
		return fmt.Errorf("save message: %w", err)
	}

	// Publish to bus for real-time delivery
	topic := "direct.message"
	if msg.GroupID != "" {
		topic = "group.message"
	}
	if err := s.bus.Publish(ctx, topic, msg); err != nil {
		return fmt.Errorf("publish message: %w", err)
	}

	return nil
}

func (s *CommService) GetMessages(ctx context.Context, agentID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	return s.repo.GetMessages(ctx, agentID, limit)
}

func (s *CommService) GetConversation(ctx context.Context, agent1, agent2 string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	return s.repo.GetConversation(ctx, agent1, agent2, limit)
}

func (s *CommService) CreateGroup(ctx context.Context, group Group) (*Group, error) {
	if group.ID == "" {
		group.ID = uuid.New().String()
	}
	group.CreatedAt = time.Now()
	if err := s.repo.SaveGroup(ctx, &group); err != nil {
		return nil, fmt.Errorf("save group: %w", err)
	}
	return &group, nil
}

func (s *CommService) GetGroup(ctx context.Context, id string) (*Group, error) {
	return s.repo.GetGroup(ctx, id)
}

func (s *CommService) ListGroups(ctx context.Context, agentID string) ([]Group, error) {
	return s.repo.ListGroups(ctx, agentID)
}

func (s *CommService) AddToGroup(ctx context.Context, groupID, agentID string) error {
	g, err := s.repo.GetGroup(ctx, groupID)
	if err != nil {
		return err
	}
	if g == nil {
		return fmt.Errorf("group %s not found", groupID)
	}

	// Check if already a member
	for _, m := range g.Members {
		if m == agentID {
			return nil // already in group
		}
	}

	g.Members = append(g.Members, agentID)
	return s.repo.SaveGroup(ctx, g)
}

func (s *CommService) RemoveFromGroup(ctx context.Context, groupID, agentID string) error {
	g, err := s.repo.GetGroup(ctx, groupID)
	if err != nil {
		return err
	}
	if g == nil {
		return fmt.Errorf("group %s not found", groupID)
	}

	for i, m := range g.Members {
		if m == agentID {
			g.Members = append(g.Members[:i], g.Members[i+1:]...)
			return s.repo.SaveGroup(ctx, g)
		}
	}
	return nil // not in group, nothing to do
}

func (s *CommService) CreateChannel(ctx context.Context, channel Channel) (*Channel, error) {
	if channel.ID == "" {
		channel.ID = uuid.New().String()
	}
	channel.CreatedAt = time.Now()
	if err := s.repo.SaveChannel(ctx, &channel); err != nil {
		return nil, fmt.Errorf("save channel: %w", err)
	}
	return &channel, nil
}

func (s *CommService) GetChannel(ctx context.Context, id string) (*Channel, error) {
	return s.repo.GetChannel(ctx, id)
}

func (s *CommService) ListChannels(ctx context.Context, agentID string) ([]Channel, error) {
	return s.repo.ListChannels(ctx, agentID)
}
