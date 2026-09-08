package monitor

import (
	"context"
	"time"
)

// AgentStatus represents the real-time status of an agent
type AgentStatus struct {
	AgentID     string    `json:"agent_id"`
	State       string    `json:"state"`
	CurrentTask string    `json:"current_task,omitempty"`
	Progress    float64   `json:"progress"` // 0-100
	Uptime      time.Duration `json:"uptime"`
	LastActive  time.Time `json:"last_active"`
	Resources   ResourceUsage `json:"resources"`
	Errors      []Error   `json:"errors,omitempty"`
}

// ResourceUsage represents current resource usage
type ResourceUsage struct {
	CPU       float64 `json:"cpu"`       // percentage
	Memory    float64 `json:"memory"`    // percentage
	Disk      float64 `json:"disk"`      // percentage
	NetworkIn int64   `json:"network_in"`
	NetworkOut int64  `json:"network_out"`
}

// Error represents an error occurrence
type Error struct {
	Timestamp time.Time `json:"timestamp"`
	Code      string    `json:"code"`
	Message   string    `json:"message"`
	Severity  string    `json:"severity"` // "low", "medium", "high", "critical"
}

// Alert represents an alert
type Alert struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Type      AlertType `json:"type"`
	Severity  string    `json:"severity"`
	Message   string    `json:"message"`
	Value     float64   `json:"value,omitempty"`
	Threshold float64   `json:"threshold,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Resolved  bool      `json:"resolved"`
	ResolvedAt time.Time `json:"resolved_at,omitempty"`
}

// AlertType represents the type of alert
type AlertType string

const (
	AlertCPUHigh      AlertType = "cpu_high"
	AlertMemoryHigh   AlertType = "memory_high"
	AlertDiskHigh     AlertType = "disk_high"
	AlertErrorRate    AlertType = "error_rate"
	AlertInactive     AlertType = "inactive"
	AlertTaskFailed   AlertType = "task_failed"
	AlertUnresponsive AlertType = "unresponsive"
)

// Service defines the monitoring service interface
type Service interface {
	// GetAgentStatus returns real-time status of an agent
	GetAgentStatus(ctx context.Context, agentID string) (*AgentStatus, error)
	// ListAgentStatuses returns statuses of all agents
	ListAgentStatuses(ctx context.Context) ([]*AgentStatus, error)
	
	// GetMetrics returns historical metrics
	GetMetrics(ctx context.Context, agentID string, start, end time.Time) ([]*ResourceUsage, error)
	// GetAggregatedMetrics returns aggregated metrics
	GetAggregatedMetrics(ctx context.Context, agentID string, duration time.Duration) (*ResourceUsage, error)
	
	// CreateAlert creates an alert rule
	CreateAlert(ctx context.Context, alert AlertRule) (*Alert, error)
	// DeleteAlert deletes an alert rule
	DeleteAlert(ctx context.Context, id string) error
	// ListAlerts lists alerts for an agent
	ListAlerts(ctx context.Context, agentID string, resolved bool) ([]*Alert, error)
	// ResolveAlert resolves an alert
	ResolveAlert(ctx context.Context, alertID string) error
	
	// HealthCheck checks if an agent is responsive
	HealthCheck(ctx context.Context, agentID string) (bool, error)
	// GetDashboard returns dashboard data
	GetDashboard(ctx context.Context) (*Dashboard, error)
}

// AlertRule defines an alert rule
type AlertRule struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Type      AlertType `json:"type"`
	Threshold float64   `json:"threshold"`
	Duration  time.Duration `json:"duration"`
	Enabled   bool      `json:"enabled"`
}

// Dashboard represents dashboard data
type Dashboard struct {
	TotalAgents    int              `json:"total_agents"`
	ActiveAgents   int              `json:"active_agents"`
	FailedAgents   int              `json:"failed_agents"`
	TotalTasks     int              `json:"total_tasks"`
	RunningTasks   int              `json:"running_tasks"`
	CompletedTasks int              `json:"completed_tasks"`
	FailedTasks    int              `json:"failed_tasks"`
	Alerts         []*Alert         `json:"alerts"`
	TopAgents      []*AgentStatus   `json:"top_agents"`
}

// Collector collects metrics from environments
type Collector interface {
	// Start starts collecting metrics
	Start(ctx context.Context) error
	// Stop stops collecting metrics
	Stop() error
	// Collect collects metrics for an environment
	Collect(ctx context.Context, envID string) (*ResourceUsage, error)
}
