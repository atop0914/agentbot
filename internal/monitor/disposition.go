package monitor

import "time"

// ===== 告警处置状态机（Day 24）=====
//
// 第 1 轮的告警只有 resolved 一个布尔位，无法表达「有人认领了但还没修好」这个
// 生产环境里最常见的中间态。运维的真实流程是：
//
//	firing ──ack──▶ acknowledged ──resolve──▶ resolved
//	   ▲                    │                    │
//	   └──── re-fire ───────┴──── re-fire ───────┘
//	（同类型告警重新触发时回到 firing）
//
// 每次状态流转都会留下一条处置记录（Action），并同步写入审计（由装配层注入
// 的 Recorder 完成），从而回答「谁在什么时候认领/解决了哪条告警」。

// AlertStatus 是告警的处置状态。
type AlertStatus string

const (
	// AlertStatusFiring 表示告警已触发、尚无人认领。
	AlertStatusFiring AlertStatus = "firing"
	// AlertStatusAcknowledged 表示有人认领，正在处理中。
	AlertStatusAcknowledged AlertStatus = "acknowledged"
	// AlertStatusResolved 表示问题已解决（或已被人工关闭）。
	AlertStatusResolved AlertStatus = "resolved"
)

// Valid 判断状态取值是否合法。
func (s AlertStatus) Valid() bool {
	switch s {
	case AlertStatusFiring, AlertStatusAcknowledged, AlertStatusResolved:
		return true
	}
	return false
}

// AlertAction 是一次处置动作的类型。
type AlertAction string

const (
	AlertActionAck     AlertAction = "ack"
	AlertActionResolve AlertAction = "resolve"
	AlertActionReopen  AlertAction = "reopen"
	AlertActionComment AlertAction = "comment"
)

// AlertDisposition 是一条告警处置记录（不可变追加，只增不改）。
type AlertDisposition struct {
	ID      string      `json:"id"`
	AlertID string      `json:"alert_id"`
	AgentID string      `json:"agent_id"`
	Action  AlertAction `json:"action"`
	// From / To 记录状态机的迁移边界；comment 动作二者相同。
	From AlertStatus `json:"from"`
	To   AlertStatus `json:"to"`
	// Operator 是处置人（通常为 JWT 中的 user_id）。
	Operator string `json:"operator"`
	// Note 是处置备注。
	Note string `json:"note,omitempty"`
	// At 是处置时刻。
	At time.Time `json:"at"`
}

// DispositionRequest 描述一次处置请求。
type DispositionRequest struct {
	// Action 为 ack / resolve / reopen。
	Action AlertAction `json:"action"`
	// Operator 是处置人；服务层不接受空值，避免出现「无名处置」。
	Operator string `json:"operator"`
	// Note 是可选备注。
	Note string `json:"note,omitempty"`
}

// AlertDispositionSummary 是告警处置进度摘要，供管理后台 agents 分区使用。
type AlertDispositionSummary struct {
	// Open / Acknowledged / Resolved 是三种状态的告警数。
	Open         int `json:"open"`
	Acknowledged int `json:"acknowledged"`
	Resolved     int `json:"resolved"`
	// Unacknowledged 是「已触发但无人认领」的数量 —— 运维最先要看的指标。
	Unacknowledged int `json:"unacknowledged"`
	// AvgTimeToAck / AvgTimeToResolve 是平均认领时长与平均解决时长（秒）。
	// 没有样本时为 0。
	AvgTimeToAckSeconds     float64 `json:"avg_time_to_ack_seconds"`
	AvgTimeToResolveSeconds float64 `json:"avg_time_to_resolve_seconds"`
	// BySeverity 是未解决告警按严重级别的分布。
	BySeverity map[string]int `json:"by_severity"`
	// Recent 是最近的处置记录（时间倒序）。
	Recent []AlertDisposition `json:"recent,omitempty"`
}
