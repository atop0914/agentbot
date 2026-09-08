package audit

import (
	"context"
	"time"
)

// Event represents an audit event
type Event struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Actor     string    `json:"actor"`     // user ID or agent ID
	ActorType string    `json:"actor_type"` // "user" or "agent"
	Action    string    `json:"action"`
	Resource  string    `json:"resource"`
	ResourceID string   `json:"resource_id"`
	Details   map[string]interface{} `json:"details,omitempty"`
	IPAddress string    `json:"ip_address,omitempty"`
	UserAgent string    `json:"user_agent,omitempty"`
	Status    string    `json:"status"` // "success", "failure"
	Error     string    `json:"error,omitempty"`
}

// EventType represents the type of audit event
type EventType string

const (
	// Agent events
	EventAgentCreated   EventType = "agent.created"
	EventAgentUpdated   EventType = "agent.updated"
	EventAgentDeleted   EventType = "agent.deleted"
	EventAgentStarted   EventType = "agent.started"
	EventAgentStopped   EventType = "agent.stopped"
	EventAgentFailed    EventType = "agent.failed"
	
	// Task events
	EventTaskCreated    EventType = "task.created"
	EventTaskStarted    EventType = "task.started"
	EventTaskCompleted  EventType = "task.completed"
	EventTaskFailed     EventType = "task.failed"
	EventTaskCancelled  EventType = "task.cancelled"
	
	// Message events
	EventMessageSent    EventType = "message.sent"
	EventMessageReceived EventType = "message.received"
	
	// User events
	EventUserLogin      EventType = "user.login"
	EventUserLogout     EventType = "user.logout"
	EventUserCreated    EventType = "user.created"
	EventUserUpdated    EventType = "user.updated"
	EventUserDeleted    EventType = "user.deleted"
	
	// Permission events
	EventPermissionGranted EventType = "permission.granted"
	EventPermissionRevoked EventType = "permission.revoked"
	
	// Template events
	EventTemplateCreated EventType = "template.created"
	EventTemplateShared  EventType = "template.shared"
	EventTemplateUsed    EventType = "template.used"
	
	// System events
	EventSystemStartup   EventType = "system.startup"
	EventSystemShutdown  EventType = "system.shutdown"
	EventSystemError     EventType = "system.error"
)

// Service defines the audit service interface
type Service interface {
	// Log logs an audit event
	Log(ctx context.Context, event Event) error
	// Query queries audit events
	Query(ctx context.Context, filter Filter) ([]*Event, error)
	// GetEvent returns an event by ID
	GetEvent(ctx context.Context, id string) (*Event, error)
	// Count counts events matching filter
	Count(ctx context.Context, filter Filter) (int, error)
	// Export exports audit events
	Export(ctx context.Context, filter Filter, format string) ([]byte, error)
}

// Filter defines audit event filter
type Filter struct {
	StartTime  *time.Time `json:"start_time,omitempty"`
	EndTime    *time.Time `json:"end_time,omitempty"`
	Actor      string     `json:"actor,omitempty"`
	ActorType  string     `json:"actor_type,omitempty"`
	EventType  string     `json:"event_type,omitempty"`
	Resource   string     `json:"resource,omitempty"`
	ResourceID string     `json:"resource_id,omitempty"`
	Status     string     `json:"status,omitempty"`
	IPAddress  string     `json:"ip_address,omitempty"`
	Limit      int        `json:"limit,omitempty"`
	Offset     int        `json:"offset,omitempty"`
	SortBy     string     `json:"sort_by,omitempty"` // "timestamp", "action"
	SortOrder  string     `json:"sort_order,omitempty"` // "asc", "desc"
}

// Recorder records audit events
type Recorder interface {
	// RecordAgentEvent records an agent-related event
	RecordAgentEvent(ctx context.Context, eventType EventType, agentID string, details map[string]interface{}) error
	// RecordTaskEvent records a task-related event
	RecordTaskEvent(ctx context.Context, eventType EventType, taskID string, details map[string]interface{}) error
	// RecordUserEvent records a user-related event
	RecordUserEvent(ctx context.Context, eventType EventType, userID string, details map[string]interface{}) error
	// RecordMessageEvent records a message event
	RecordMessageEvent(ctx context.Context, eventType EventType, messageID string, details map[string]interface{}) error
	// RecordPermissionEvent records a permission event
	RecordPermissionEvent(ctx context.Context, eventType EventType, userID string, details map[string]interface{}) error
	// RecordSystemEvent records a system event
	RecordSystemEvent(ctx context.Context, eventType EventType, details map[string]interface{}) error
}

// Logger provides structured audit logging
type Logger interface {
	// Info logs an info-level audit event
	Info(ctx context.Context, action string, details map[string]interface{})
	// Warn logs a warning-level audit event
	Warn(ctx context.Context, action string, details map[string]interface{})
	// Error logs an error-level audit event
	Error(ctx context.Context, action string, err error, details map[string]interface{})
	// Fatal logs a fatal-level audit event
	Fatal(ctx context.Context, action string, err error, details map[string]interface{})
}
