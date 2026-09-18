package adapter

import "time"

// AdapterType represents the category of an external application adapter.
type AdapterType string

const (
	AdapterTypeEmail    AdapterType = "email"
	AdapterTypeCalendar AdapterType = "calendar"
	AdapterTypeDocument AdapterType = "document"
	AdapterTypeBrowser  AdapterType = "browser"
	AdapterTypeCustom   AdapterType = "custom"
)

// AdapterStatus represents the connection status of an adapter.
type AdapterStatus string

const (
	StatusDisconnected AdapterStatus = "disconnected"
	StatusConnected    AdapterStatus = "connected"
	StatusError        AdapterStatus = "error"
)

// Config holds configuration for an adapter instance.
type Config struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Type        AdapterType       `json:"type"`
	Description string            `json:"description,omitempty"`
	AgentID     string            `json:"agent_id"`
	Settings    map[string]string `json:"settings,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// Action represents a single operation that an adapter can perform.
type Action struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Parameters  []ActionParam     `json:"parameters,omitempty"`
}

// ActionParam defines a parameter for an adapter action.
type ActionParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // string, number, boolean, object
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
}

// ActionRequest is the input for executing an adapter action.
type ActionRequest struct {
	Action     string            `json:"action"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

// ActionResult is the output from executing an adapter action.
type ActionResult struct {
	Success  bool              `json:"success"`
	Data     map[string]string `json:"data,omitempty"`
	Error    string            `json:"error,omitempty"`
	Duration time.Duration     `json:"duration"`
}

// AdapterInfo is a summary view of an adapter for listing.
type AdapterInfo struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Type        AdapterType   `json:"type"`
	Description string        `json:"description,omitempty"`
	AgentID     string        `json:"agent_id"`
	Status      AdapterStatus `json:"status"`
	Actions     int           `json:"actions"`
	CreatedAt   time.Time     `json:"created_at"`
}
