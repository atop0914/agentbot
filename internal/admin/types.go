// Package admin 提供管理后台（Admin Console）的服务端能力。
//
// 本仓库是纯 Go 后端，不含 Node/前端工具链，因此「管理后台」在服务端的职责是：
//  1. 把各业务模块（agent / task / monitor / audit / template / user ...）的统计聚合成
//     控制台首屏需要的单一视图，避免前端并发调用十几个接口；
//  2. 托管将来由 React 构建产物提供的静态资源（产物缺失时给出明确占位，而不是 panic）；
//  3. 管理控制台自身的展示配置与功能开关（FeatureFlags）。
package admin

import (
	"context"
	"time"
)

// Section 表示聚合视图中的一个分区。
//
// 控制台首屏由若干分区拼装而成（概览卡片、Agent 分布、任务分布、
// 告警列表、最近审计事件、用户与授权……）。每个分区独立可用性降级：某个下游模块
// 返回错误时，仅该分区标记 unavailable，不影响其余分区渲染。
type Section string

const (
	SectionOverview    Section = "overview" // 概览计数
	SectionAgents      Section = "agents"   // Agent 状态分布
	SectionTasks       Section = "tasks"    // 任务状态分布
	SectionAlerts      Section = "alerts"   // 未处理告警
	SectionAudit       Section = "audit"    // 最近审计事件
	SectionUsers       Section = "users"    // 用户与授权链（RBAC）
	SectionPlugins     Section = "plugins"  // 功能开关
	SectionStaticAsset Section = "static"   // 静态资源挂载状态
)

// AllSections 返回全部可选分区，顺序即默认渲染顺序。
func AllSections() []Section {
	return []Section{
		SectionOverview,
		SectionAgents,
		SectionTasks,
		SectionAlerts,
		SectionAudit,
		SectionUsers,
		SectionPlugins,
		SectionStaticAsset,
	}
}

// Valid 判断分区名是否合法。
func (s Section) Valid() bool {
	for _, candidate := range AllSections() {
		if s == candidate {
			return true
		}
	}
	return false
}

// Snapshot 是一次「聚合视图」请求的结果。
type Snapshot struct {
	Title       string             `json:"title"`
	Version     string             `json:"version"`
	GeneratedAt time.Time          `json:"generated_at"`
	Overview    *Overview          `json:"overview,omitempty"`
	Agents      *AgentBreakdown    `json:"agents,omitempty"`
	Tasks       *TaskBreakdown     `json:"tasks,omitempty"`
	Alerts      *AlertDigest       `json:"alerts,omitempty"`
	Audit       *AuditDigest       `json:"audit,omitempty"`
	Users       *UserDigest        `json:"users,omitempty"`
	Plugins     *PluginDigest      `json:"plugins,omitempty"`
	Static      *StaticMountStatus `json:"static,omitempty"`
	Errors      map[string]string  `json:"errors,omitempty"`
}

// Overview 是控制台首屏的概览计数。
type Overview struct {
	TotalAgents   int `json:"total_agents"`
	RunningAgents int `json:"running_agents"`
	FailedAgents  int `json:"failed_agents"`
	TotalTasks    int `json:"total_tasks"`
	RunningTasks  int `json:"running_tasks"`
	FailedTasks   int `json:"failed_tasks"`
	OpenAlerts    int `json:"open_alerts"`
	AuditEvents   int `json:"audit_events"`
	// Degraded 为 true 表示至少有一个分区聚合失败，计数为部分数据。
	Degraded bool `json:"degraded"`
}

// AgentBreakdown 是 Agent 状态分布。
type AgentBreakdown struct {
	Total    int            `json:"total"`
	ByState  map[string]int `json:"by_state"`
	Recently []AgentSummary `json:"recently,omitempty"`
	// ===== Day 24：监控时间序列摘要 + 告警处置进度 =====
	//
	// Monitoring 是集群级监控摘要（时间序列 + 处置进度）。
	// 独立于 Total/ByState：监控数据不可用时这里为 nil，
	// 而 Agent 状态分布仍照常渲染（分区内再降级）。
	Monitoring *MonitorSummary `json:"monitoring,omitempty"`
}

// MonitorSummary 是控制台 agents 分区需要的监控摘要。
type MonitorSummary struct {
	// Series 是按 Agent 的最近窗口指标摘要（含均值/峰值与样本量）。
	Series []AgentSeriesSummary `json:"series,omitempty"`
	// Dispositions 是告警处置进度。
	Dispositions *DispositionProgress `json:"dispositions,omitempty"`
	// SeriesWindow 是时间序列摘要所用的窗口（回显给前端）。
	SeriesWindow time.Duration `json:"series_window"`
	// GeneratedAt 是摘要生成时刻。
	GeneratedAt time.Time `json:"generated_at"`
	// Degraded 为 true 表示时间序列或处置进度至少有一项取数失败。
	Degraded bool `json:"degraded,omitempty"`
	// Errors 记录降级原因（按子项名索引）。
	Errors map[string]string `json:"errors,omitempty"`
}

// AgentSeriesSummary 是单个 Agent 的时间序列摘要。
type AgentSeriesSummary struct {
	AgentID    string  `json:"agent_id"`
	Samples    int     `json:"samples"`
	CPUAvg     float64 `json:"cpu_avg"`
	CPUPeak    float64 `json:"cpu_peak"`
	MemoryAvg  float64 `json:"memory_avg"`
	MemoryPeak float64 `json:"memory_peak"`
	// TaskSuccessRate 为 -1 表示窗口内没有任务数据。
	TaskSuccessRate float64 `json:"task_success_rate"`
	TaskCount       int     `json:"task_count"`
	// Empty 为 true 表示窗口内没有任何采样。
	Empty bool `json:"empty"`
}

// DispositionProgress 是告警处置进度摘要（admin 视图）。
type DispositionProgress struct {
	Open           int `json:"open"`
	Acknowledged   int `json:"acknowledged"`
	Resolved       int `json:"resolved"`
	Unacknowledged int `json:"unacknowledged"`
	// AvgTimeToAckSeconds / AvgTimeToResolveSeconds 为运维响应时长指标。
	AvgTimeToAckSeconds     float64            `json:"avg_time_to_ack_seconds"`
	AvgTimeToResolveSeconds float64            `json:"avg_time_to_resolve_seconds"`
	BySeverity              map[string]int     `json:"by_severity"`
	Recent                  []DispositionBrief `json:"recent,omitempty"`
}

// DispositionBrief 是处置记录的轻量视图。
type DispositionBrief struct {
	AlertID  string    `json:"alert_id"`
	AgentID  string    `json:"agent_id"`
	Action   string    `json:"action"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	Operator string    `json:"operator"`
	Note     string    `json:"note,omitempty"`
	At       time.Time `json:"at"`
}

// AgentSummary 是 Agent 的轻量视图，只暴露控制台列表需要的字段。
type AgentSummary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TaskBreakdown 是任务状态分布。
type TaskBreakdown struct {
	Total   int            `json:"total"`
	ByState map[string]int `json:"by_state"`
}

// AlertDigest 是未处理告警摘要。
type AlertDigest struct {
	Total      int            `json:"total"`
	BySeverity map[string]int `json:"by_severity"`
	Items      []AlertBrief   `json:"items,omitempty"`
}

// AlertBrief 是告警的轻量视图。
type AlertBrief struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Type      string    `json:"type"`
	Severity  string    `json:"severity"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// AuditDigest 是最近审计事件摘要。
type AuditDigest struct {
	Total     int            `json:"total"`
	ByAction  map[string]int `json:"by_action"`
	Recent    []AuditBrief   `json:"recent,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
}

// AuditBrief 是审计事件的轻量视图。
type AuditBrief struct {
	ID         string    `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	Actor      string    `json:"actor"`
	ActorType  string    `json:"actor_type"`
	Action     string    `json:"action"`
	Resource   string    `json:"resource"`
	ResourceID string    `json:"resource_id"`
	Status     string    `json:"status"`
}

// UserDigest 是用户与授权链（RBAC）的摘要。
type UserDigest struct {
	Total      int            `json:"total"`
	ByStatus   map[string]int `json:"by_status"`
	ByRole     map[string]int `json:"by_role"`
	ByProvider map[string]int `json:"by_provider"`
	Recent     []UserSummary  `json:"recent,omitempty"`
	Authz      *AuthzStats    `json:"authz,omitempty"`
}

// UserSummary 是用户的轻量视图（不含任何凭据字段）。
type UserSummary struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	Status    string    `json:"status"`
	Role      string    `json:"role"`
	Provider  string    `json:"provider"`
	CreatedAt time.Time `json:"created_at"`
}

// AuthzStats 是授权链（用户/Agent → 角色 → 权限）的统计。
type AuthzStats struct {
	TotalAssignments int            `json:"total_assignments"`
	UserAssignments  int            `json:"user_assignments"`
	AgentAssignments int            `json:"agent_assignments"`
	Expired          int            `json:"expired"`
	ByRole           map[string]int `json:"by_role"`
	Decisions        int            `json:"decisions"`
	Denials          int            `json:"denials"`
	Degraded         int            `json:"degraded"`
}

// UserView 是 admin 需要的用户字段子集（避免 admin 依赖 user 的完整模型）。
type UserView struct {
	ID        string
	Username  string
	Email     string
	Status    string
	Role      string
	Provider  string
	CreatedAt time.Time
}

// UserSource 由 internal/user.Service 适配而来。
type UserSource interface {
	ListUsers(ctx context.Context, offset, limit int) ([]*UserView, int, error)
}

// AuthzSource 由 internal/authz.Service 适配而来。
type AuthzSource interface {
	Stats(ctx context.Context) (*AuthzStats, error)
}

// PluginDigest 反映功能开关的当前取值。
type PluginDigest struct {
	Enabled  []string `json:"enabled"`
	Disabled []string `json:"disabled"`
}

// StaticMountStatus 描述静态资源的挂载情况。
type StaticMountStatus struct {
	Prefix      string `json:"prefix"`
	Dir         string `json:"dir"`
	Mounted     bool   `json:"mounted"`
	IndexExists bool   `json:"index_exists"`
	// Message 在未挂载时给出可读的原因（例如「前端构建产物尚未生成」）。
	Message string `json:"message,omitempty"`
}

// SnapshotQuery 描述一次聚合请求的范围。
type SnapshotQuery struct {
	// Sections 为空表示取全部合法分区。
	Sections []Section `json:"sections,omitempty"`
	// RecentLimit 控制「最近」类列表的长度（Agent / 告警 / 审计事件）。
	RecentLimit int `json:"recent_limit,omitempty"`
	// AuditWindow 只统计该时间窗内的审计事件；为零值表示不限制。
	AuditWindow time.Duration `json:"audit_window,omitempty"`
}

// Defaults 适用于未显式指定的查询字段。
const (
	DefaultRecentLimit = 10
	MaxRecentLimit     = 200
	// DefaultMonitorWindow 是 agents 分区监控摘要的默认时间窗。
	DefaultMonitorWindow = 30 * time.Minute
)

// Normalize 填充默认值并把 RecentLimit 收敛到合法区间。
// 返回的 Sections 一定是有序去重且全部合法的。
func (q SnapshotQuery) Normalize() SnapshotQuery {
	out := q
	if out.RecentLimit <= 0 {
		out.RecentLimit = DefaultRecentLimit
	}
	if out.RecentLimit > MaxRecentLimit {
		out.RecentLimit = MaxRecentLimit
	}
	if out.AuditWindow < 0 {
		out.AuditWindow = 0
	}

	seen := make(map[Section]bool, len(out.Sections))
	normalized := make([]Section, 0, len(out.Sections))
	for _, requested := range out.Sections {
		if !requested.Valid() || seen[requested] {
			continue
		}
		seen[requested] = true
		normalized = append(normalized, requested)
	}
	if len(normalized) == 0 {
		out.Sections = AllSections()
		return out
	}
	// 按 AllSections 的固定顺序重排，保证输出稳定。
	ordered := make([]Section, 0, len(normalized))
	for _, candidate := range AllSections() {
		if seen[candidate] {
			ordered = append(ordered, candidate)
		}
	}
	out.Sections = ordered
	return out
}

// Includes 判断归一化后的查询是否包含某个分区。
func (q SnapshotQuery) Includes(s Section) bool {
	for _, candidate := range q.Sections {
		if candidate == s {
			return true
		}
	}
	return false
}

// Sources 是聚合视图依赖的下游模块。
//
// 全部字段可为 nil：对应分区会被标记为 unavailable 而不是让整个请求失败，
// 这样控制台在部分后端模块未装配时依然可用。
type Sources struct {
	Agents  AgentSource
	Tasks   TaskSource
	Monitor MonitorSource
	Audit   AuditSource
	Users   UserSource
	Authz   AuthzSource
}

// AgentSource 由 internal/agent.Service 满足。
type AgentSource interface {
	List(ctx context.Context, offset, limit int) ([]*AgentView, error)
}

// AgentView 是 admin 需要的 Agent 字段子集。
type AgentView struct {
	ID        string
	Name      string
	State     string
	UpdatedAt time.Time
}

// TaskSource 由 internal/task.Manager 适配而来。
type TaskSource interface {
	List(ctx context.Context, agentID string, state string, limit int) ([]*TaskView, error)
}

// TaskView 是 admin 需要的任务字段子集。
type TaskView struct {
	ID        string
	AgentID   string
	State     string
	CreatedAt time.Time
}

// MonitorSource 提供告警查询与监控摘要，由 internal/monitor.Service 适配。
type MonitorSource interface {
	ListAlerts(ctx context.Context, agentID string, resolved bool) ([]*AlertView, error)
	// Summarize 返回控制台 agents 分区需要的监控摘要：
	// 各 Agent 的时间序列摘要 + 告警处置进度。
	//
	// 与 ListAlerts 分开定义：告警列表即使可用，时间序列也可能不可用，
	// 二者需要独立降级。
	Summarize(ctx context.Context, window time.Duration, recentLimit int) (*MonitorSummary, error)
}

// AlertView 是 admin 需要的告警字段子集。
type AlertView struct {
	ID        string
	AgentID   string
	Type      string
	Severity  string
	Message   string
	CreatedAt time.Time
}

// AuditSource 提供审计事件查询，由 internal/audit.Service 适配。
type AuditSource interface {
	Query(ctx context.Context, since *time.Time, limit int) ([]*AuditView, error)
	Count(ctx context.Context, since *time.Time) (int, error)
}

// AuditView 是 admin 需要的审计事件字段子集。
type AuditView struct {
	ID         string
	Timestamp  time.Time
	Actor      string
	ActorType  string
	Action     string
	Resource   string
	ResourceID string
	Status     string
}

// Service 是管理后台的服务端接口。
type Service interface {
	// Config 返回控制台展示配置。
	Config(ctx context.Context) (*Config, error)
	// UpdateConfig 更新控制台展示配置（标题 / 版本 / 功能开关）。
	UpdateConfig(ctx context.Context, req UpdateConfigRequest) (*Config, error)
	// Snapshot 聚合各业务模块的统计，产出控制台首屏视图。
	Snapshot(ctx context.Context, query SnapshotQuery) (*Snapshot, error)
	// StaticStatus 返回静态资源的挂载状态。
	StaticStatus(ctx context.Context) (*StaticMountStatus, error)
	// SetStaticMount 由装配层在完成静态资源挂载后回报实际状态。
	SetStaticMount(ctx context.Context, status StaticMountStatus) error
}
