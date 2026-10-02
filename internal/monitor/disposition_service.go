package monitor

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// ===== 告警处置状态机实现（Day 24）=====

// DefaultDispositionRecentLimit 是处置摘要里默认返回的最近记录条数。
const DefaultDispositionRecentLimit = 10

// AcknowledgeAlert 认领告警：firing → acknowledged。
//
// 重复认领返回错误而不是静默成功 —— 运维需要知道「已经被别人认领了」，
// 静默成功会让两个人同时排查同一条告警。
func (s *service) AcknowledgeAlert(ctx context.Context, alertID string, req DispositionRequest) (*Alert, error) {
	req.Action = AlertActionAck
	return s.transition(ctx, alertID, req, AlertStatusFiring, AlertStatusAcknowledged)
}

// ResolveAlertWithDisposition 解决告警并记录处置人/备注。
//
// 允许从 firing 直接跳到 resolved（问题可能自愈或被快速修复），
// 但状态机仍会补一条 resolve 记录，不会丢处置轨迹。
func (s *service) ResolveAlertWithDisposition(ctx context.Context, alertID string, req DispositionRequest) (*Alert, error) {
	return s.transition(ctx, alertID, req, "", AlertStatusResolved)
}

// ReopenAlert 重新打开告警：resolved → firing。
//
// 用于「问题其实没修好」或「误关」的情形；重开后会重新计入未解决告警。
func (s *service) ReopenAlert(ctx context.Context, alertID string, req DispositionRequest) (*Alert, error) {
	return s.transition(ctx, alertID, req, AlertStatusResolved, AlertStatusFiring)
}

// transition 是三个处置入口共用的状态迁移实现。
//
// from 为空表示「接受任意源状态」（用于 resolve —— 无论是否已认领都能解决）。
func (s *service) transition(ctx context.Context, alertID string, req DispositionRequest, from, to AlertStatus) (*Alert, error) {
	if alertID == "" {
		return nil, fmt.Errorf("monitor: alert id is required")
	}
	if req.Action == "" {
		return nil, fmt.Errorf("monitor: action is required")
	}
	if req.Operator == "" {
		// 不接受无名处置：没有责任人的处置记录在事故复盘中毫无价值。
		return nil, fmt.Errorf("monitor: operator is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	alert, agentID := s.findAlertLocked(alertID)
	if alert == nil {
		return nil, fmt.Errorf("monitor: alert %s not found", alertID)
	}
	alert.NormalizeStatus()
	if from != "" && alert.Status != from {
		return nil, fmt.Errorf("monitor: alert %s is %s, cannot %s (expected %s)",
			alertID, alert.Status, req.Action, from)
	}
	if alert.Status == to {
		return nil, fmt.Errorf("monitor: alert %s is already %s", alertID, to)
	}

	prev := alert.Status
	now := time.Now().UTC()
	switch to {
	case AlertStatusAcknowledged:
		alert.AcknowledgedBy = req.Operator
		alert.AcknowledgedAt = now
	case AlertStatusResolved:
		alert.ResolvedBy = req.Operator
		alert.ResolvedAt = now
	case AlertStatusFiring:
		// 重开：清掉认领与解决的痕迹，让告警回到「无人处理」的初始语义。
		alert.AcknowledgedBy = ""
		alert.AcknowledgedAt = time.Time{}
		alert.ResolvedBy = ""
		alert.ResolvedAt = time.Time{}
	}
	alert.Status = to
	// 兼容字段与状态机保持同步。
	alert.Resolved = to == AlertStatusResolved

	disposition := &AlertDisposition{
		ID:       uuid.NewString(),
		AlertID:  alertID,
		AgentID:  agentID,
		Action:   req.Action,
		From:     prev,
		To:       to,
		Operator: req.Operator,
		Note:     req.Note,
		At:       now,
	}
	if err := s.repo.AppendDisposition(ctx, disposition); err != nil {
		// 处置记录是审计资产：记录失败就不能让状态悄悄变更，否则出现
		// 「状态变了但没人知道是谁改的」。这里直接把错误抛给调用方。
		return nil, fmt.Errorf("monitor: record disposition for alert %s: %w", alertID, err)
	}
	alert.DispositionCount++
	if disposition.Note != "" {
		alert.LastNote = disposition.Note
	}

	if err := s.repo.UpdateAlert(ctx, alert); err != nil {
		return nil, err
	}
	// UpdateAlert 存的是副本；s.byAge 里的对象必须同步替换，否则
	// ListAlerts / DispositionSummary 仍会读到过渡前的状态。
	s.updateInPlaceLocked(alert)
	cp := *alert
	return &cp, nil
}

// findAlertLocked 在持有 s.mu 时按 ID 查告警，返回副本指针与所属 Agent ID。
func (s *service) findAlertLocked(alertID string) (*Alert, string) {
	for agentID, alerts := range s.byAge {
		for _, a := range alerts {
			if a.ID != alertID {
				continue
			}
			cp := *a
			return &cp, agentID
		}
	}
	return nil, ""
}

// updateInPlaceLocked 把过渡后的告警写回 s.byAge 中（复用原对象地址）。
func (s *service) updateInPlaceLocked(updated *Alert) {
	for _, alerts := range s.byAge {
		for i, a := range alerts {
			if a.ID == updated.ID {
				alerts[i] = updated
				return
			}
		}
	}
}

// ListDispositions 返回某条告警的处置记录（时间正序）。
func (s *service) ListDispositions(ctx context.Context, alertID string) ([]AlertDisposition, error) {
	if alertID == "" {
		return nil, fmt.Errorf("monitor: alert id is required")
	}
	records, err := s.repo.ListDispositions(ctx, alertID)
	if err != nil {
		return nil, err
	}
	out := make([]AlertDisposition, 0, len(records))
	for _, r := range records {
		if r == nil {
			continue
		}
		out = append(out, *r)
	}
	return out, nil
}

// DispositionSummary 汇总告警处置进度。
//
// agentID 为空时汇总全部 Agent。统计口径以告警的**当前状态**为准，
// 时长类指标来自处置记录中的时间戳（认领时长 = AcknowledgedAt - CreatedAt，
// 解决时长 = ResolvedAt - CreatedAt）。
func (s *service) DispositionSummary(ctx context.Context, agentID string, recentLimit int) (*AlertDispositionSummary, error) {
	if recentLimit <= 0 {
		recentLimit = DefaultDispositionRecentLimit
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	summary := &AlertDispositionSummary{BySeverity: make(map[string]int)}
	var ackDurations, resolveDurations []time.Duration

	for id, alerts := range s.byAge {
		if agentID != "" && id != agentID {
			continue
		}
		for _, a := range alerts {
			if a == nil {
				continue
			}
			cp := *a
			cp.NormalizeStatus()
			switch cp.Status {
			case AlertStatusResolved:
				summary.Resolved++
			case AlertStatusAcknowledged:
				summary.Acknowledged++
				summary.BySeverity[severityOrUnknown(cp.Severity)]++
			default:
				summary.Open++
				summary.Unacknowledged++
				summary.BySeverity[severityOrUnknown(cp.Severity)]++
			}
			if !cp.AcknowledgedAt.IsZero() && !cp.CreatedAt.IsZero() && cp.AcknowledgedAt.After(cp.CreatedAt) {
				ackDurations = append(ackDurations, cp.AcknowledgedAt.Sub(cp.CreatedAt))
			}
			if !cp.ResolvedAt.IsZero() && !cp.CreatedAt.IsZero() && cp.ResolvedAt.After(cp.CreatedAt) {
				resolveDurations = append(resolveDurations, cp.ResolvedAt.Sub(cp.CreatedAt))
			}
		}
	}
	summary.AvgTimeToAckSeconds = averageSeconds(ackDurations)
	summary.AvgTimeToResolveSeconds = averageSeconds(resolveDurations)

	// 最近处置记录：全量取回后按时间倒序截断。
	records, err := s.repo.ListDispositions(ctx, "")
	if err != nil {
		return nil, err
	}
	sorted := make([]AlertDisposition, 0, len(records))
	for _, r := range records {
		if r == nil {
			continue
		}
		if agentID != "" && r.AgentID != agentID {
			continue
		}
		sorted = append(sorted, *r)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].At.Equal(sorted[j].At) {
			return sorted[i].At.After(sorted[j].At)
		}
		return sorted[i].ID > sorted[j].ID
	})
	for i := range sorted {
		if i >= recentLimit {
			break
		}
		summary.Recent = append(summary.Recent, sorted[i])
	}
	return summary, nil
}

func severityOrUnknown(sev string) string {
	if sev == "" {
		return "unknown"
	}
	return sev
}

func averageSeconds(durations []time.Duration) float64 {
	if len(durations) == 0 {
		return 0
	}
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	return total.Seconds() / float64(len(durations))
}
