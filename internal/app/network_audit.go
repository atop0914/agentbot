package app

import (
	"context"

	"github.com/atop0914/agentbot/internal/audit"
	"github.com/atop0914/agentbot/internal/network"
)

// egressAuditRecorder 把出口网关的流量记录翻译成审计事件。
//
// 翻译发生在装配层：network 只管「哪台 Agent 往哪个域名发了什么」，
// 审计所需的资源类型、事件名与明细结构由这里决定，两边都不需要知道对方的模型
// （与 monitorDispositionRecorder 同一套理由）。
//
// 为什么**阻断**也必须落审计：阻断记录是「Agent 试图外联到未授权目标」的证据链。
// 只记放行等于把最有价值的那部分信号丢掉 —— 攻击尝试恰恰发生在被拦下的请求里。
type egressAuditRecorder struct {
	rec audit.Recorder
}

// 出口相关的事件名。
//
// 用 egress.* 前缀而不是复用 agent.* 事件：出口是独立的控制面，
// 混进 Agent 生命周期事件会让「查这台 Agent 所有外联」变得不可过滤。
const (
	auditActionEgressAllowed = "egress.allowed"
	auditActionEgressBlocked = "egress.blocked"
	auditActionEgressFailed  = "egress.failed"
)

func (r egressAuditRecorder) RecordEgress(ctx context.Context, rec *network.TrafficRecord) error {
	if r.rec == nil || rec == nil {
		return nil
	}

	details := map[string]interface{}{
		"domain":      rec.Domain,
		"method":      rec.Method,
		"url":         rec.URL,
		"status_code": rec.StatusCode,
		"bytes":       rec.Bytes,
		"duration_ms": rec.DurationMS,
		"allowed":     rec.Allowed,
	}
	if rec.RuleID != "" {
		details["rule_id"] = rec.RuleID
	}
	if rec.Reason != "" {
		details["reason"] = rec.Reason
	}
	if rec.Error != "" {
		details["error"] = rec.Error
	}
	if rec.Suspicious {
		details["suspicious"] = true
		details["suspicious_reasons"] = rec.SuspiciousReasons
	}

	// 以 Agent 为主体落审计：审计查询可以按 agent_id 过滤出
	// 「这台机器往哪些域名发过流量」。
	return r.rec.RecordAgentEvent(ctx, egressEventType(rec), rec.AgentID, details)
}

// egressEventType 决定一条流量记录对应的事件名。
//
// 优先级：转发失败 > 被阻断 > 放行。一条既被阻断又「失败」的记录不存在
// （阻断根本不发起连接），所以这里的顺序只是保证语义清晰。
func egressEventType(rec *network.TrafficRecord) audit.EventType {
	switch {
	case rec.Error != "":
		return audit.EventType(auditActionEgressFailed)
	case !rec.Allowed:
		return audit.EventType(auditActionEgressBlocked)
	default:
		return audit.EventType(auditActionEgressAllowed)
	}
}
