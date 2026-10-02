package monitor

import (
	"context"
	"time"
)

// AgentStatus represents the real-time status of an agent
type AgentStatus struct {
	AgentID     string        `json:"agent_id"`
	State       string        `json:"state"`
	CurrentTask string        `json:"current_task,omitempty"`
	Progress    float64       `json:"progress"` // 0-100
	Uptime      time.Duration `json:"uptime"`
	LastActive  time.Time     `json:"last_active"`
	Resources   ResourceUsage `json:"resources"`
	Errors      []Error       `json:"errors,omitempty"`
}

// ResourceUsage represents current resource usage
type ResourceUsage struct {
	CPU        float64   `json:"cpu"`    // percentage
	Memory     float64   `json:"memory"` // percentage
	Disk       float64   `json:"disk"`   // percentage
	NetworkIn  int64     `json:"network_in"`
	NetworkOut int64     `json:"network_out"`
	Timestamp  time.Time `json:"timestamp"`
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
	ID         string    `json:"id"`
	AgentID    string    `json:"agent_id"`
	Type       AlertType `json:"type"`
	Severity   string    `json:"severity"`
	Message    string    `json:"message"`
	Value      float64   `json:"value,omitempty"`
	Threshold  float64   `json:"threshold,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Resolved   bool      `json:"resolved"`
	ResolvedAt time.Time `json:"resolved_at,omitempty"`

	// ===== Day 24 新增：处置状态机 =====
	//
	// Status 是处置状态（firing / acknowledged / resolved）。
	// 保留 Resolved 字段是为了兼容已上线的接口契约：二者始终同步，
	// Resolved == (Status == AlertStatusResolved)。
	Status AlertStatus `json:"status"`
	// AcknowledgedBy / AcknowledgedAt 记录认领人与认领时刻。
	AcknowledgedBy string    `json:"acknowledged_by,omitempty"`
	AcknowledgedAt time.Time `json:"acknowledged_at,omitempty"`
	// ResolvedBy 记录解决人。
	ResolvedBy string `json:"resolved_by,omitempty"`
	// DispositionCount 是该告警的处置记录条数。
	DispositionCount int `json:"disposition_count"`
	// LastNote 是最后一条处置记录的备注，便于列表页直接展示。
	LastNote string `json:"last_note,omitempty"`
}

// NormalizeStatus 补齐告警的状态字段，使「旧数据（只有 Resolved 布尔位）」
// 与「新数据（有 Status）」在同一套读取路径下语义一致。
func (a *Alert) NormalizeStatus() {
	if a == nil {
		return
	}
	if !a.Status.Valid() {
		if a.Resolved {
			a.Status = AlertStatusResolved
		} else {
			a.Status = AlertStatusFiring
		}
	}
	// 两个表示必须同步，否则列表与详情会给出矛盾结论。
	a.Resolved = a.Status == AlertStatusResolved
	if a.Resolved && a.ResolvedAt.IsZero() {
		a.ResolvedAt = a.CreatedAt
	}
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

	// ReportStatus records a status heartbeat reported by an agent, and
	// evaluates the registered alert rules against it.
	ReportStatus(ctx context.Context, status *AgentStatus) error
	// ReportMetrics records a resource sample for an agent
	ReportMetrics(ctx context.Context, agentID string, usage *ResourceUsage) error

	// GetMetrics returns historical metrics
	GetMetrics(ctx context.Context, agentID string, start, end time.Time) ([]*ResourceUsage, error)
	// GetAggregatedMetrics returns aggregated metrics
	GetAggregatedMetrics(ctx context.Context, agentID string, duration time.Duration) (*ResourceUsage, error)

	// ===== Day 24：监控时间序列 =====

	// GetTimeSeries 返回指定 Agent 在窗口内按 BucketInterval 归并的时间序列。
	// 窗口内没有采样的桶会以 Empty 点补齐，保证时间轴连续。
	GetTimeSeries(ctx context.Context, agentID string, start, end time.Time) (*TimeSeries, error)
	// GetTimeSeriesByDuration 是 GetTimeSeries 的便捷形式（窗口 = [now-duration, now]）。
	GetTimeSeriesByDuration(ctx context.Context, agentID string, duration time.Duration) (*TimeSeries, error)
	// ReportTaskOutcome 记录一次任务结果，用于计算时间序列里的任务成功率。
	ReportTaskOutcome(ctx context.Context, outcome TaskOutcome) error

	// ===== Day 24：告警处置状态机 =====

	// AcknowledgeAlert 认领告警（firing → acknowledged）。
	AcknowledgeAlert(ctx context.Context, alertID string, req DispositionRequest) (*Alert, error)
	// ResolveAlertWithDisposition 解决告警并记录处置人与备注。
	ResolveAlertWithDisposition(ctx context.Context, alertID string, req DispositionRequest) (*Alert, error)
	// ReopenAlert 重新打开已解决的告警（resolved → firing）。
	ReopenAlert(ctx context.Context, alertID string, req DispositionRequest) (*Alert, error)
	// ListDispositions 返回某条告警的处置记录（时间正序）。
	ListDispositions(ctx context.Context, alertID string) ([]AlertDisposition, error)
	// DispositionSummary 汇总告警处置进度，供管理后台 agents 分区使用。
	// recentLimit <= 0 时取默认条数。
	DispositionSummary(ctx context.Context, agentID string, recentLimit int) (*AlertDispositionSummary, error)

	// CreateAlert creates an alert rule
	CreateAlert(ctx context.Context, alert AlertRule) (*Alert, error)
	// DeleteAlert deletes an alert rule
	DeleteAlert(ctx context.Context, id string) error
	// ListAlertRules lists the configured alert rules for an agent
	ListAlertRules(ctx context.Context, agentID string) ([]*AlertRule, error)
	// ListAlerts lists alerts for an agent
	ListAlerts(ctx context.Context, agentID string, resolved bool) ([]*Alert, error)
	// ListAlertsByStatus 按三态处置状态过滤告警（status 为空表示不过滤）。
	ListAlertsByStatus(ctx context.Context, agentID string, status AlertStatus) ([]*Alert, error)
	// ResolveAlert resolves an alert
	ResolveAlert(ctx context.Context, alertID string) error
	// EvaluateAlerts evaluates the alert rules against a status snapshot
	// and returns the alerts newly raised by this evaluation.
	EvaluateAlerts(ctx context.Context, status *AgentStatus) []*Alert

	// HealthCheck checks if an agent is responsive
	HealthCheck(ctx context.Context, agentID string) (bool, error)
	// GetDashboard returns dashboard data
	GetDashboard(ctx context.Context) (*Dashboard, error)
}

// AlertRule defines an alert rule
type AlertRule struct {
	ID        string        `json:"id"`
	AgentID   string        `json:"agent_id"`
	Type      AlertType     `json:"type"`
	Threshold float64       `json:"threshold"`
	Duration  time.Duration `json:"duration"`
	Enabled   bool          `json:"enabled"`
}

// Dashboard represents dashboard data
type Dashboard struct {
	TotalAgents    int            `json:"total_agents"`
	ActiveAgents   int            `json:"active_agents"`
	FailedAgents   int            `json:"failed_agents"`
	TotalTasks     int            `json:"total_tasks"`
	RunningTasks   int            `json:"running_tasks"`
	CompletedTasks int            `json:"completed_tasks"`
	FailedTasks    int            `json:"failed_tasks"`
	Alerts         []*Alert       `json:"alerts"`
	TopAgents      []*AgentStatus `json:"top_agents"`
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
