package app

import (
	"context"
	"time"

	"github.com/atop0914/agentbot/internal/admin"
	"github.com/atop0914/agentbot/internal/agent"
	"github.com/atop0914/agentbot/internal/audit"
	"github.com/atop0914/agentbot/internal/monitor"
	"github.com/atop0914/agentbot/internal/task"
)

// adminAgentSource 把 agent.Service 适配为 admin.AgentSource。
//
// 转换发生在这一层：admin 包只认自己的轻量视图类型，
// 不依赖 agent 包的具体模型，避免管理后台与业务模块耦合。
type adminAgentSource struct {
	svc agent.Service
}

func (s adminAgentSource) List(ctx context.Context, offset, limit int) ([]*admin.AgentView, error) {
	agents, err := s.svc.List(ctx, offset, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*admin.AgentView, 0, len(agents))
	for _, a := range agents {
		if a == nil {
			continue
		}
		out = append(out, &admin.AgentView{
			ID:        a.ID,
			Name:      a.Name,
			State:     string(a.State),
			UpdatedAt: a.UpdatedAt,
		})
	}
	return out, nil
}

// adminTaskSource 把 task.Manager 适配为 admin.TaskSource。
//
// Manager.List 只支持「按 agent + 状态」过滤，因此管理后台的分页在
// 这一层用切片模拟：先全量取回再做偏移，保证概览计数准确。
type adminTaskSource struct {
	mgr task.Manager
}

func (s adminTaskSource) List(ctx context.Context, _ string, _ string, limit int) ([]*admin.TaskView, error) {
	all, err := s.mgr.List(ctx, "", "")
	if err != nil {
		return nil, err
	}
	out := make([]*admin.TaskView, 0, len(all))
	for _, t := range all {
		if t == nil {
			continue
		}
		out = append(out, &admin.TaskView{
			ID:        t.ID,
			AgentID:   t.AgentID,
			State:     string(t.State),
			CreatedAt: t.CreatedAt,
		})
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// adminMonitorSource 把 monitor.Service 适配为 admin.MonitorSource。
type adminMonitorSource struct {
	svc monitor.Service
}

func (s adminMonitorSource) ListAlerts(ctx context.Context, agentID string, resolved bool) ([]*admin.AlertView, error) {
	alerts, err := s.svc.ListAlerts(ctx, agentID, resolved)
	if err != nil {
		return nil, err
	}
	out := make([]*admin.AlertView, 0, len(alerts))
	for _, a := range alerts {
		if a == nil {
			continue
		}
		out = append(out, &admin.AlertView{
			ID:        a.ID,
			AgentID:   a.AgentID,
			Type:      string(a.Type),
			Severity:  a.Severity,
			Message:   a.Message,
			CreatedAt: a.CreatedAt,
		})
	}
	return out, nil
}

// Summarize 把 monitor 的时间序列与处置进度转成 admin 的轻量视图。
//
// 两个子项独立降级：时间序列取数失败不影响处置进度，反之亦然，
// 失败原因写入 summary.Errors 并把 Degraded 置真。
func (s adminMonitorSource) Summarize(ctx context.Context, window time.Duration, recentLimit int) (*admin.MonitorSummary, error) {
	if window <= 0 {
		window = admin.DefaultMonitorWindow
	}
	if recentLimit <= 0 {
		recentLimit = admin.DefaultRecentLimit
	}

	summary := &admin.MonitorSummary{
		SeriesWindow: window.String(),
		GeneratedAt:  time.Now().UTC(),
	}

	// 时间序列摘要：枚举所有上报过状态的 Agent，逐个取窗口摘要。
	statuses, err := s.svc.ListAgentStatuses(ctx)
	if err != nil {
		summary.Degraded = true
		summary.Errors = map[string]string{"series": err.Error()}
	} else {
		series := make([]admin.AgentSeriesSummary, 0, len(statuses))
		for _, st := range statuses {
			if st == nil || st.AgentID == "" {
				continue
			}
			ts, err := s.svc.GetTimeSeriesByDuration(ctx, st.AgentID, window)
			if err != nil {
				summary.Degraded = true
				if summary.Errors == nil {
					summary.Errors = make(map[string]string)
				}
				summary.Errors["series."+st.AgentID] = err.Error()
				continue
			}
			series = append(series, admin.AgentSeriesSummary{
				AgentID:         ts.AgentID,
				Samples:         ts.SampleCount,
				CPUAvg:          ts.CPUAvg,
				CPUPeak:         ts.CPUPeak,
				MemoryAvg:       ts.MemoryAvg,
				MemoryPeak:      ts.MemoryPeak,
				TaskSuccessRate: ts.TaskSuccessRate,
				TaskCount:       ts.TaskCount,
				Empty:           ts.Empty,
			})
		}
		summary.Series = series
	}

	// 处置进度。
	progress, err := s.svc.DispositionSummary(ctx, "", recentLimit)
	if err != nil {
		summary.Degraded = true
		if summary.Errors == nil {
			summary.Errors = make(map[string]string)
		}
		summary.Errors["dispositions"] = err.Error()
	} else if progress != nil {
		dp := &admin.DispositionProgress{
			Open:                    progress.Open,
			Acknowledged:            progress.Acknowledged,
			Resolved:                progress.Resolved,
			Unacknowledged:          progress.Unacknowledged,
			AvgTimeToAckSeconds:     progress.AvgTimeToAckSeconds,
			AvgTimeToResolveSeconds: progress.AvgTimeToResolveSeconds,
			BySeverity:              progress.BySeverity,
		}
		for _, d := range progress.Recent {
			dp.Recent = append(dp.Recent, admin.DispositionBrief{
				AlertID:  d.AlertID,
				AgentID:  d.AgentID,
				Action:   string(d.Action),
				From:     string(d.From),
				To:       string(d.To),
				Operator: d.Operator,
				Note:     d.Note,
				At:       d.At,
			})
		}
		summary.Dispositions = dp
	}

	return summary, nil
}

// adminAuditSource 把 audit.Service 适配为 admin.AuditSource。
type adminAuditSource struct {
	svc audit.Service
}

func (s adminAuditSource) Query(ctx context.Context, since *time.Time, limit int) ([]*admin.AuditView, error) {
	filter := audit.Filter{Limit: limit, SortBy: "timestamp", SortOrder: "desc"}
	if since != nil {
		filter.StartTime = since
	}
	events, err := s.svc.Query(ctx, filter)
	if err != nil {
		return nil, err
	}
	// audit.Query 在未显式排序时按写入顺序返回，这里再排一次，
	// 保证「最近事件」在时间相同时也有稳定顺序。
	sortAuditDesc(events)
	out := make([]*admin.AuditView, 0, len(events))
	for _, e := range events {
		if e == nil {
			continue
		}
		out = append(out, &admin.AuditView{
			ID:         e.ID,
			Timestamp:  e.Timestamp,
			Actor:      e.Actor,
			ActorType:  e.ActorType,
			Action:     e.Action,
			Resource:   e.Resource,
			ResourceID: e.ResourceID,
			Status:     e.Status,
		})
	}
	return out, nil
}

func (s adminAuditSource) Count(ctx context.Context, since *time.Time) (int, error) {
	filter := audit.Filter{}
	if since != nil {
		filter.StartTime = since
	}
	return s.svc.Count(ctx, filter)
}

// sortAuditDesc 按时间倒序排列审计事件，时间相同时按 ID 倒序。
func sortAuditDesc(events []*audit.EventRecord) {
	for i := 1; i < len(events); i++ {
		for j := i; j > 0; j-- {
			if !auditLess(events[j], events[j-1]) {
				break
			}
			events[j], events[j-1] = events[j-1], events[j]
		}
	}
}

func auditLess(a, b *audit.EventRecord) bool {
	if a == nil || b == nil {
		return a == nil && b != nil
	}
	if !a.Timestamp.Equal(b.Timestamp) {
		return a.Timestamp.After(b.Timestamp)
	}
	return a.ID > b.ID
}
