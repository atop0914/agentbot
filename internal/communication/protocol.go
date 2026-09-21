package communication

import (
	"time"
)

// Priority represents message priority levels.
type Priority int

const (
	PriorityLow    Priority = 0
	PriorityNormal Priority = 1
	PriorityHigh   Priority = 2
	PriorityUrgent Priority = 3
)

// DeliveryStatus tracks the delivery state of a message.
type DeliveryStatus string

const (
	StatusPending   DeliveryStatus = "pending"
	StatusSent      DeliveryStatus = "sent"
	StatusDelivered DeliveryStatus = "delivered"
	StatusRead      DeliveryStatus = "read"
	StatusFailed    DeliveryStatus = "failed"
	StatusExpired   DeliveryStatus = "expired"
)

// Envelope wraps a Message with protocol-level metadata for routing,
// delivery tracking, and priority handling.
type Envelope struct {
	MessageID    string         `json:"message_id"`
	From         string         `json:"from"`
	To           string         `json:"to"`
	GroupID      string         `json:"group_id,omitempty"`
	Priority     Priority       `json:"priority"`
	TTL          time.Duration  `json:"ttl,omitempty"`          // time-to-live, 0 = no expiry
	RequireAck   bool           `json:"require_ack,omitempty"`  // request delivery acknowledgment
	ReplyTo      string         `json:"reply_to,omitempty"`     // for request-response pattern
	CorrelationID string        `json:"correlation_id,omitempty"` // groups related messages
	CreatedAt    time.Time      `json:"created_at"`
	Status       DeliveryStatus `json:"status"`
}

// Ack represents a delivery/read acknowledgment.
type Ack struct {
	MessageID string         `json:"message_id"`
	AgentID   string         `json:"agent_id"`
	Status    DeliveryStatus `json:"status"`
	Timestamp time.Time      `json:"timestamp"`
}

// RequestEnvelope is a structured request from one agent to another.
type RequestEnvelope struct {
	RequestID    string            `json:"request_id"`
	From         string            `json:"from"`
	To           string            `json:"to"`
	Action       string            `json:"action"`
	Params       map[string]string `json:"params,omitempty"`
	Priority     Priority          `json:"priority"`
	Timeout      time.Duration     `json:"timeout,omitempty"`
	CorrelationID string           `json:"correlation_id,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

// ResponseEnvelope is the reply to a RequestEnvelope.
type ResponseEnvelope struct {
	RequestID string            `json:"request_id"`
	From      string            `json:"from"`
	To        string            `json:"to"`
	Success   bool              `json:"success"`
	Result    map[string]string `json:"result,omitempty"`
	Error     string            `json:"error,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

// StatusReport represents an agent's status broadcast.
type StatusReport struct {
	AgentID   string            `json:"agent_id"`
	Status    AgentStatus       `json:"status"`
	Message   string            `json:"message,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

// AgentStatus represents the operational status of an agent.
type AgentStatus string

const (
	AgentStatusOnline  AgentStatus = "online"
	AgentStatusBusy    AgentStatus = "busy"
	AgentStatusAway    AgentStatus = "away"
	AgentStatusOffline AgentStatus = "offline"
	AgentStatusError   AgentStatus = "error"
)

// PendingRequest tracks an in-flight request awaiting a response.
type PendingRequest struct {
	Request   RequestEnvelope
	StartedAt time.Time
	ReplyCh   chan ResponseEnvelope
}
