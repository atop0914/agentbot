package monitor

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// 默认的告警阈值，可在创建告警规则时覆盖。
const (
	DefaultCPUThreshold       = 85.0
	DefaultMemoryThreshold    = 90.0
	DefaultDiskThreshold      = 90.0
	DefaultErrorRateThreshold = 0.2 // 20% 失败率
	DefaultInactiveAfter      = 5 * time.Minute
)

// service 是 Service 接口的实现，聚合状态、样本与告警。
type service struct {
	repo Repository

	mu    sync.RWMutex
	rules map[string]*AlertRule // key: rule ID
	byAge map[string][]*Alert   // key: agentID，已产生的告警历史
}

// NewService 创建监控服务。
func NewService(repo Repository) Service {
	return &service{
		repo:  repo,
		rules: make(map[string]*AlertRule),
		byAge: make(map[string][]*Alert),
	}
}

func (s *service) GetAgentStatus(ctx context.Context, agentID string) (*AgentStatus, error) {
	if agentID == "" {
		return nil, fmt.Errorf("monitor: agent id is required")
	}
	return s.repo.GetStatus(ctx, agentID)
}

func (s *service) ListAgentStatuses(ctx context.Context) ([]*AgentStatus, error) {
	return s.repo.ListStatuses(ctx)
}

// ReportStatus 记录一次上报的状态。这是唯一的状态写入入口。
func (s *service) ReportStatus(ctx context.Context, status *AgentStatus) error {
	if status == nil {
		return fmt.Errorf("monitor: status is required")
	}
	if status.AgentID == "" {
		return fmt.Errorf("monitor: agent id is required")
	}
	if status.LastActive.IsZero() {
		status.LastActive = time.Now().UTC()
	}
	if err := s.repo.SaveStatus(ctx, status); err != nil {
		return err
	}
	s.EvaluateAlerts(ctx, status)
	return nil
}

// ReportMetrics 记录一次资源采样，并同步刷新该 Agent 的最近状态。
func (s *service) ReportMetrics(ctx context.Context, agentID string, usage *ResourceUsage) error {
	if agentID == "" {
		return fmt.Errorf("monitor: agent id is required")
	}
	if usage == nil {
		return fmt.Errorf("monitor: usage is required")
	}
	if usage.Timestamp.IsZero() {
		usage.Timestamp = time.Now().UTC()
	}
	if err := s.repo.AppendSample(ctx, agentID, usage); err != nil {
		return err
	}
	// 状态可能尚未建立（Agent 只上报了指标没有心跳），此时补建一条。
	status, err := s.repo.GetStatus(ctx, agentID)
	if err != nil {
		status = &AgentStatus{AgentID: agentID, State: "unknown"}
	}
	status.Resources = *usage
	status.LastActive = usage.Timestamp
	return s.ReportStatus(ctx, status)
}

func (s *service) GetMetrics(ctx context.Context, agentID string, start, end time.Time) ([]*ResourceUsage, error) {
	if end.Before(start) {
		start, end = end, start
	}
	return s.repo.QuerySamples(ctx, agentID, start, end)
}

func (s *service) GetAggregatedMetrics(ctx context.Context, agentID string, duration time.Duration) (*ResourceUsage, error) {
	now := time.Now().UTC()
	samples, err := s.repo.QuerySamples(ctx, agentID, now.Add(-duration), now)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("monitor: no samples for agent %s in the last %s", agentID, duration)
	}
	var sum ResourceUsage
	for _, s := range samples {
		sum.CPU += s.CPU
		sum.Memory += s.Memory
		sum.Disk += s.Disk
		sum.NetworkIn += s.NetworkIn
		sum.NetworkOut += s.NetworkOut
	}
	n := float64(len(samples))
	// 百分比类指标取窗口内均值；网络字节数是累计量，同样取均值表示"每采样周期增量"。
	return &ResourceUsage{
		CPU:        sum.CPU / n,
		Memory:     sum.Memory / n,
		Disk:       sum.Disk / n,
		NetworkIn:  int64(float64(sum.NetworkIn) / n),
		NetworkOut: int64(float64(sum.NetworkOut) / n),
		Timestamp:  now,
	}, nil
}

// HealthScore 依据资源占用、错误率和活跃度给出 0-100 的健康分。
// 100 表示完全健康，分数越低越危险。State 为 error 时直接封顶 0 分。
func HealthScore(status *AgentStatus) int {
	if status == nil {
		return 0
	}
	if status.State == "error" {
		return 0
	}
	score := 100.0

	// 资源惩罚：超出 70% 预警线后线性扣分，达到阈值扣满 30 分。
	score -= resourcePenalty(status.Resources.CPU, 70, DefaultCPUThreshold, 30)
	score -= resourcePenalty(status.Resources.Memory, 70, DefaultMemoryThreshold, 30)
	score -= resourcePenalty(status.Resources.Disk, 70, DefaultDiskThreshold, 20)

	// 错误惩罚：每个未解决的严重错误扣 15 分，其余错误扣 5 分。
	for i := range status.Errors {
		switch status.Errors[i].Severity {
		case "critical", "high":
			score -= 15
		default:
			score -= 5
		}
	}

	// 活跃度惩罚：超过默认静默窗口未上报心跳，按超时倍数扣分。
	if !status.LastActive.IsZero() {
		idle := time.Since(status.LastActive)
		if idle > DefaultInactiveAfter {
			over := float64(idle) / float64(DefaultInactiveAfter)
			score -= (over - 1) * 20
		}
	}

	if score < 0 {
		return 0
	}
	return int(score)
}

// resourcePenalty 计算单项资源超出 warnAt 后向 maxAt 逼近时的线性扣分值。
func resourcePenalty(value, warnAt, maxAt, maxPenalty float64) float64 {
	if value <= warnAt || maxAt <= warnAt {
		return 0
	}
	if value >= maxAt {
		return maxPenalty
	}
	return (value - warnAt) / (maxAt - warnAt) * maxPenalty
}

func (s *service) HealthCheck(ctx context.Context, agentID string) (bool, error) {
	status, err := s.repo.GetStatus(ctx, agentID)
	if err != nil {
		return false, err
	}
	// 判据三条：非 error 状态、心跳未超时、健康分及格。
	if status.State == "error" {
		return false, nil
	}
	if !status.LastActive.IsZero() && time.Since(status.LastActive) > DefaultInactiveAfter {
		return false, nil
	}
	return HealthScore(status) >= 60, nil
}

func (s *service) CreateAlert(ctx context.Context, rule AlertRule) (*Alert, error) {
	if rule.AgentID == "" {
		return nil, fmt.Errorf("monitor: agent id is required")
	}
	if rule.Type == "" {
		return nil, fmt.Errorf("monitor: alert type is required")
	}
	if rule.ID == "" {
		rule.ID = uuid.NewString()
	}
	if rule.Threshold == 0 {
		rule.Threshold = defaultThreshold(rule.Type)
	}
	rule.Enabled = true

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.rules[rule.ID]; exists {
		return nil, fmt.Errorf("monitor: alert rule %s already exists", rule.ID)
	}
	cp := rule
	s.rules[rule.ID] = &cp

	// 规则本身立即回读一次，使已处于异常状态的 Agent 马上产生告警。
	created := []*Alert{}
	if status, err := s.repo.GetStatus(ctx, rule.AgentID); err == nil {
		created = s.evaluateLocked(rule.AgentID, status)
	}
	if len(created) > 0 {
		return created[0], nil
	}
	return nil, nil
}

func (s *service) DeleteAlert(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.rules[id]; !exists {
		return fmt.Errorf("monitor: alert rule %s not found", id)
	}
	delete(s.rules, id)
	return nil
}

func (s *service) ListAlertRules(_ context.Context, agentID string) ([]*AlertRule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*AlertRule, 0, len(s.rules))
	for _, r := range s.rules {
		if agentID != "" && r.AgentID != agentID {
			continue
		}
		cp := *r
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *service) ListAlerts(ctx context.Context, agentID string, resolved bool) ([]*Alert, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Alert
	for id, alerts := range s.byAge {
		if agentID != "" && id != agentID {
			continue
		}
		for _, a := range alerts {
			if a.Resolved != resolved {
				continue
			}
			cp := *a
			// 旧数据可能只有 Resolved 布尔位，这里统一补齐 Status，
			// 保证列表接口返回的每条告警都带合法状态。
			cp.NormalizeStatus()
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ListAlertsByStatus 按处置状态过滤告警。status 为空时等价于 ListAlerts(_, false)。
//
// 之所以不改造 ListAlerts 的签名：它已被既有接口与测试依赖，
// 而 Resolved 布尔过滤与三态过滤是两套语义，混在一起容易出错。
func (s *service) ListAlertsByStatus(ctx context.Context, agentID string, status AlertStatus) ([]*Alert, error) {
	if status != "" && !status.Valid() {
		return nil, fmt.Errorf("monitor: invalid alert status %q", status)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Alert
	for id, alerts := range s.byAge {
		if agentID != "" && id != agentID {
			continue
		}
		for _, a := range alerts {
			cp := *a
			cp.NormalizeStatus()
			if status != "" && cp.Status != status {
				continue
			}
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (s *service) ResolveAlert(ctx context.Context, alertID string) error {
	if alertID == "" {
		return fmt.Errorf("monitor: alert id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, alerts := range s.byAge {
		for _, a := range alerts {
			if a.ID != alertID {
				continue
			}
			a.NormalizeStatus()
			if a.Resolved {
				return fmt.Errorf("monitor: alert %s already resolved", alertID)
			}
			a.Status = AlertStatusResolved
			a.Resolved = true
			if a.ResolvedAt.IsZero() {
				a.ResolvedAt = time.Now().UTC()
			}
			return s.repo.UpdateAlert(ctx, a)
		}
	}
	return fmt.Errorf("monitor: alert %s not found", alertID)
}

func (s *service) GetDashboard(ctx context.Context) (*Dashboard, error) {
	statuses, err := s.repo.ListStatuses(ctx)
	if err != nil {
		return nil, err
	}

	dash := &Dashboard{TotalAgents: len(statuses)}
	for _, st := range statuses {
		switch st.State {
		case "running":
			dash.ActiveAgents++
		case "error":
			dash.FailedAgents++
		}
		if st.CurrentTask != "" {
			dash.RunningTasks++
		}
	}

	// 未解决的告警全部展示，按创建时间倒序。
	unresolved, err := s.ListAlerts(ctx, "", false)
	if err != nil {
		return nil, err
	}
	dash.Alerts = unresolved

	// TopAgents 按健康分倒序取前 5 个。
	ranked := make([]*AgentStatus, len(statuses))
	copy(ranked, statuses)
	sort.Slice(ranked, func(i, j int) bool {
		si, sj := HealthScore(ranked[i]), HealthScore(ranked[j])
		if si != sj {
			return si > sj
		}
		return ranked[i].AgentID < ranked[j].AgentID
	})
	if len(ranked) > 5 {
		ranked = ranked[:5]
	}
	dash.TopAgents = ranked
	return dash, nil
}

// EvaluateAlerts 针对单个状态求值所有启用规则，返回本次新产生的告警。
func (s *service) EvaluateAlerts(_ context.Context, status *AgentStatus) []*Alert {
	if status == nil || status.AgentID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evaluateLocked(status.AgentID, status)
}

// evaluateLocked 要求在持有 s.mu 的情况下调用。
func (s *service) evaluateLocked(agentID string, status *AgentStatus) []*Alert {
	var created []*Alert
	for _, rule := range s.rules {
		if !rule.Enabled || rule.AgentID != agentID {
			continue
		}
		value, breached := ruleBreach(rule, status)
		if !breached {
			continue
		}
		// 同一规则的同类告警已在未解决状态时不重复产生。
		if s.hasUnresolvedLocked(agentID, rule.Type) {
			continue
		}
		alert := &Alert{
			ID:        uuid.NewString(),
			AgentID:   agentID,
			Type:      rule.Type,
			Severity:  severityFor(rule.Type),
			Message:   buildMessage(rule, value),
			Value:     value,
			Threshold: rule.Threshold,
			CreatedAt: time.Now().UTC(),
			// 新告警一律从 firing 开始，等待人工认领。
			Status: AlertStatusFiring,
		}
		s.byAge[agentID] = append(s.byAge[agentID], alert)
		_ = s.repo.CreateAlert(context.Background(), alert)
		created = append(created, alert)
	}
	return created
}

func (s *service) hasUnresolvedLocked(agentID string, t AlertType) bool {
	for _, a := range s.byAge[agentID] {
		if a.Type == t && !a.Resolved {
			return true
		}
	}
	return false
}

// ruleBreach 判定一条规则是否被当前状态触发，并返回触发的实际值。
func ruleBreach(rule *AlertRule, status *AgentStatus) (float64, bool) {
	switch rule.Type {
	case AlertCPUHigh:
		return status.Resources.CPU, status.Resources.CPU >= rule.Threshold
	case AlertMemoryHigh:
		return status.Resources.Memory, status.Resources.Memory >= rule.Threshold
	case AlertDiskHigh:
		return status.Resources.Disk, status.Resources.Disk >= rule.Threshold
	case AlertInactive, AlertUnresponsive:
		if status.LastActive.IsZero() {
			return 0, false
		}
		idle := time.Since(status.LastActive)
		limit := DefaultInactiveAfter
		if rule.Duration > 0 {
			limit = rule.Duration
		}
		return idle.Seconds(), idle >= limit
	case AlertErrorRate:
		total := 0.0
		for i := range status.Errors {
			switch status.Errors[i].Severity {
			case "critical", "high":
				total++
			}
		}
		// 用严重错误数 / 10 近似错误率，避免依赖尚未统计的调用总量。
		rate := total / 10
		return rate, rate >= rule.Threshold
	case AlertTaskFailed:
		for i := range status.Errors {
			if status.Errors[i].Code == "task_failed" {
				return 1, true
			}
		}
		return 0, false
	}
	return 0, false
}

func defaultThreshold(t AlertType) float64 {
	switch t {
	case AlertCPUHigh:
		return DefaultCPUThreshold
	case AlertMemoryHigh:
		return DefaultMemoryThreshold
	case AlertDiskHigh:
		return DefaultDiskThreshold
	case AlertErrorRate:
		return DefaultErrorRateThreshold
	case AlertInactive, AlertUnresponsive:
		return DefaultInactiveAfter.Seconds()
	}
	return 0
}

func severityFor(t AlertType) string {
	switch t {
	case AlertUnresponsive, AlertTaskFailed:
		return "critical"
	case AlertCPUHigh, AlertMemoryHigh, AlertErrorRate:
		return "high"
	default:
		return "medium"
	}
}

func buildMessage(rule *AlertRule, value float64) string {
	switch rule.Type {
	case AlertInactive, AlertUnresponsive:
		return fmt.Sprintf("agent inactive for %.0fs (threshold %.0fs)", value, rule.Threshold)
	default:
		return fmt.Sprintf("%s reached %.2f (threshold %.2f)", rule.Type, value, rule.Threshold)
	}
}
