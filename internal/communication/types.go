package communication

import (
	"context"
	"time"
)

// Message represents a message in the system
type Message struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`      // agent ID or user ID
	To        string    `json:"to"`        // agent ID or group ID
	GroupID   string    `json:"group_id,omitempty"`
	Type      MessageType `json:"type"`
	Content   string    `json:"content"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// MessageType represents the type of message
type MessageType string

const (
	MessageText     MessageType = "text"
	MessageTask     MessageType = "task"
	MessageStatus   MessageType = "status"
	MessageFile     MessageType = "file"
	MessageAction   MessageType = "action"
	MessageRequest  MessageType = "request"
	MessageResponse MessageType = "response"
)

// Group represents a group of agents
type Group struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Members     []string  `json:"members"` // agent IDs
	CreatedAt   time.Time `json:"created_at"`
}

// Channel represents a communication channel
type Channel struct {
	ID        string    `json:"id"`
	Type      ChannelType `json:"type"`
	Name      string    `json:"name"`
	Members   []string  `json:"members"`
	CreatedAt time.Time `json:"created_at"`
}

// ChannelType represents the type of channel
type ChannelType string

const (
	ChannelDirect ChannelType = "direct" // 1-on-1
	ChannelGroup  ChannelType = "group"  // group chat
	ChannelBroadcast ChannelType = "broadcast" // one-to-many
)

// Bus defines the message bus interface
type Bus interface {
	// Publish publishes a message to a topic
	Publish(ctx context.Context, topic string, msg Message) error
	// Subscribe subscribes to a topic
	Subscribe(ctx context.Context, topic string, handler func(Message)) error
	// Unsubscribe unsubscribes from a topic
	Unsubscribe(ctx context.Context, topic string) error
}

// Service defines the communication service interface
type Service interface {
	// SendMessage sends a message to an agent or group
	SendMessage(ctx context.Context, msg Message) error
	// GetMessages retrieves messages for an agent or group
	GetMessages(ctx context.Context, agentID string, limit int) ([]Message, error)
	// GetConversation retrieves conversation between two agents
	GetConversation(ctx context.Context, agent1, agent2 string, limit int) ([]Message, error)
	
	// CreateGroup creates a new group
	CreateGroup(ctx context.Context, group Group) (*Group, error)
	// GetGroup returns a group by ID
	GetGroup(ctx context.Context, id string) (*Group, error)
	// ListGroups lists groups for an agent
	ListGroups(ctx context.Context, agentID string) ([]Group, error)
	// AddToGroup adds an agent to a group
	AddToGroup(ctx context.Context, groupID, agentID string) error
	// RemoveFromGroup removes an agent from a group
	RemoveFromGroup(ctx context.Context, groupID, agentID string) error
	
	// CreateChannel creates a new channel
	CreateChannel(ctx context.Context, channel Channel) (*Channel, error)
	// GetChannel returns a channel by ID
	GetChannel(ctx context.Context, id string) (*Channel, error)
	// ListChannels lists channels
	ListChannels(ctx context.Context, agentID string) ([]Channel, error)
}

// AgentMessenger defines the interface for agent-to-agent communication
type AgentMessenger interface {
	// SendToAgent sends a message to a specific agent
	SendToAgent(ctx context.Context, from, to string, content string) error
	// BroadcastToGroup sends a message to all agents in a group
	BroadcastToGroup(ctx context.Context, from, groupID string, content string) error
	// RequestAction requests another agent to perform an action
	RequestAction(ctx context.Context, from, to string, action string, params map[string]string) (string, error)
	// ReportStatus reports status to a supervisor or group
	ReportStatus(ctx context.Context, from string, status string) error
}
