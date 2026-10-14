// 本文件的每个字段都逐条对照 internal/admin/types.go 的 struct tag 抄写。
//
// 为什么不凭印象写：Go 的 json 编码对小写 snake_case tag 与 TS 的 camelCase
// 之间**没有任何编译期约束**，字段名写错不会报错，只会静默 undefined ——
// 页面看起来「渲染成功但全是 0 / 空」，是最难查的一类前端 bug。

/** 分区标识，对应 admin.Section 的常量值。 */
export type Section =
  | 'overview'
  | 'agents'
  | 'tasks'
  | 'alerts'
  | 'audit'
  | 'users'
  | 'network'
  | 'plugins'
  | 'static'

/** admin.Overview */
export interface Overview {
  total_agents: number
  running_agents: number
  failed_agents: number
  total_tasks: number
  running_tasks: number
  failed_tasks: number
  open_alerts: number
  audit_events: number
  /** 至少一个分区聚合失败时为 true —— 计数是部分数据，必须显著提示。 */
  degraded: boolean
}

/** admin.AgentSeriesSummary */
export interface AgentSeriesSummary {
  agent_id: string
  samples: number
  cpu_avg: number
  cpu_peak: number
  memory_avg: number
  memory_peak: number
  /** -1 表示窗口内没有任务数据（注意不是 0）。 */
  task_success_rate: number
  task_count: number
  /** 窗口内没有任何采样。 */
  empty: boolean
}

/** admin.DispositionBrief */
export interface DispositionBrief {
  alert_id: string
  agent_id: string
  action: string
  from: string
  to: string
  operator: string
  note?: string
  at: string
}

/** admin.DispositionProgress */
export interface DispositionProgress {
  open: number
  acknowledged: number
  resolved: number
  unacknowledged: number
  avg_time_to_ack_seconds: number
  avg_time_to_resolve_seconds: number
  by_severity: Record<string, number> | null
  recent?: DispositionBrief[]
}

/** admin.MonitorSummary */
export interface MonitorSummary {
  series?: AgentSeriesSummary[]
  dispositions?: DispositionProgress
  series_window: string
  generated_at: string
  degraded?: boolean
  errors?: Record<string, string>
}

/** admin.AgentSummary */
export interface AgentSummary {
  id: string
  name: string
  state: string
  updated_at: string
}

/** admin.AgentBreakdown */
export interface AgentBreakdown {
  total: number
  by_state: Record<string, number> | null
  recently?: AgentSummary[]
  monitoring?: MonitorSummary
}

/** admin.TaskBreakdown */
export interface TaskBreakdown {
  total: number
  by_state: Record<string, number> | null
}

/** admin.AlertBrief */
export interface AlertBrief {
  id: string
  agent_id: string
  type: string
  severity: string
  message: string
  created_at: string
}

/** admin.AlertDigest */
export interface AlertDigest {
  total: number
  by_severity: Record<string, number> | null
  items?: AlertBrief[]
}

/** admin.AuditBrief */
export interface AuditBrief {
  id: string
  timestamp: string
  actor: string
  actor_type: string
  action: string
  resource: string
  resource_id: string
  status: string
}

/** admin.AuditDigest */
export interface AuditDigest {
  total: number
  by_action: Record<string, number> | null
  recent?: AuditBrief[]
  truncated?: boolean
}

/** admin.UserSummary */
export interface UserSummary {
  id: string
  username: string
  email: string
  status: string
  role: string
  provider: string
  created_at: string
}

/** admin.AuthzStats */
export interface AuthzStats {
  total_assignments: number
  user_assignments: number
  agent_assignments: number
  expired: number
  by_role: Record<string, number> | null
  decisions: number
  denials: number
  degraded: number
}

/** admin.UserDigest */
export interface UserDigest {
  total: number
  by_status: Record<string, number> | null
  by_role: Record<string, number> | null
  by_provider: Record<string, number> | null
  recent?: UserSummary[]
  authz?: AuthzStats
}

/** admin.NetworkDomainView */
export interface NetworkDomainView {
  domain: string
  requests: number
  blocked: number
  bytes: number
  suspicious: boolean
  reasons?: string[]
}

/** admin.NetworkSummary */
export interface NetworkSummary {
  window: string
  generated_at: string
  total_rules: number
  allow_rules: number
  deny_rules: number
  total_requests: number
  allowed_requests: number
  blocked_requests: number
  failed_requests: number
  distinct_domains: number
  suspicious_requests: number
  total_bytes: number
  top_domains: NetworkDomainView[] | null
  suspicious_domains: NetworkDomainView[] | null
  degraded: boolean
  errors?: Record<string, string>
}

/** admin.PluginDigest */
export interface PluginDigest {
  enabled: string[] | null
  disabled: string[] | null
}

/** admin.StaticMountStatus */
export interface StaticMountStatus {
  prefix: string
  dir: string
  mounted: boolean
  index_exists: boolean
  message?: string
}

/**
 * admin.Snapshot —— 控制台首屏的单一聚合视图。
 *
 * 所有分区指针在 Go 侧都是 `omitempty`，因此这里**全部可选**：
 * 某分区聚合失败时键直接不出现，而不是出现一个 null。
 * 渲染代码必须对 undefined 与 null 都做处理。
 */
export interface Snapshot {
  title: string
  version: string
  generated_at: string
  overview?: Overview
  agents?: AgentBreakdown
  tasks?: TaskBreakdown
  alerts?: AlertDigest
  audit?: AuditDigest
  users?: UserDigest
  network?: NetworkSummary
  plugins?: PluginDigest
  static?: StaticMountStatus
  /** 分区名 → 失败原因。非空即说明该分区缺失。 */
  errors?: Record<string, string>
}

/** admin.Config 的对外视图（只取控制台需要的字段）。 */
export interface AdminConfig {
  title: string
  version: string
  feature_flags?: Record<string, boolean>
}

/** snapshot 查询参数，对应 admin.SnapshotQuery。 */
export interface SnapshotQuery {
  sections?: Section[]
  recent_limit?: number
  /** Go duration 语法，如 "24h"。 */
  audit_window?: string
}
