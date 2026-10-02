package app

import (
	"context"

	"github.com/atop0914/agentbot/internal/audit"
	"github.com/atop0914/agentbot/internal/monitor"
)

// monitorDispositionRecorder 把监控模块的告警处置动作翻译成审计事件。
//
// 翻译发生在装配层：monitor 只管「谁对哪条告警做了什么」，审计所需的
// 资源类型、事件名与明细结构由这里决定，两边都不需要知道对方的模型。
type monitorDispositionRecorder struct {
	rec audit.Recorder
}

// DispositionActionAliases 把处置动作映射为审计动作名。
//
// 用 alert.* 前缀而不是复用已有的 agent.* 事件：告警处置是观测面上的动作，
// 与 Agent 生命周期事件混在一起会让审计查询难以按语义过滤。
const (
	auditActionAlertAcked    = "alert.acknowledged"
	auditActionAlertResolved = "alert.resolved"
	auditActionAlertReopened = "alert.reopened"
)

func (r monitorDispositionRecorder) RecordDisposition(ctx context.Context, alert *monitor.Alert, d *monitor.AlertDisposition) error {
	if r.rec == nil || alert == nil || d == nil {
		return nil
	}
	details := map[string]interface{}{
		"alert_id":   d.AlertID,
		"alert_type": string(alert.Type),
		"severity":   alert.Severity,
		"from":       string(d.From),
		"to":         string(d.To),
		"operator":   d.Operator,
		"message":    alert.Message,
	}
	if d.Note != "" {
		details["note"] = d.Note
	}

	// 处置记录以 Agent 为主体落审计：审计查询可以按 agent_id 过滤出
	// 「这台机器上发生过哪些运维动作」。
	return r.rec.RecordAgentEvent(ctx, auditedActionFor(d.Action), alert.AgentID, details)
}

// auditedActionFor 把处置动作类型映射为审计事件名。
func auditedActionFor(action monitor.AlertAction) audit.EventType {
	switch action {
	case monitor.AlertActionAck:
		return audit.EventType(auditActionAlertAcked)
	case monitor.AlertActionResolve:
		return audit.EventType(auditActionAlertResolved)
	case monitor.AlertActionReopen:
		return audit.EventType(auditActionAlertReopened)
	default:
		// 未知动作仍然记录，避免因为枚举扩展而静默丢审计。
		return audit.EventType("alert." + string(action))
	}
}
