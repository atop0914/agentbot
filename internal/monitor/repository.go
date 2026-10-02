package monitor

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Repository 定义监控数据的持久化接口。
type Repository interface {
	SaveStatus(ctx context.Context, status *AgentStatus) error
	GetStatus(ctx context.Context, agentID string) (*AgentStatus, error)
	ListStatuses(ctx context.Context) ([]*AgentStatus, error)
	DeleteStatus(ctx context.Context, agentID string) error

	AppendSample(ctx context.Context, agentID string, sample *ResourceUsage) error
	QuerySamples(ctx context.Context, agentID string, start, end time.Time) ([]*ResourceUsage, error)

	// ===== Day 24：任务结果与告警处置记录 =====

	// AppendTaskOutcome 追加一条任务结果，用于时间序列的成功率计算。
	AppendTaskOutcome(ctx context.Context, outcome *TaskOutcome) error
	// QueryTaskOutcomes 返回窗口内的任务结果（时间正序）。
	QueryTaskOutcomes(ctx context.Context, agentID string, start, end time.Time) ([]*TaskOutcome, error)

	// AppendDisposition 追加一条不可变的告警处置记录。
	AppendDisposition(ctx context.Context, d *AlertDisposition) error
	// ListDispositions 返回处置记录；alertID 为空表示取全部（时间正序）。
	ListDispositions(ctx context.Context, alertID string) ([]*AlertDisposition, error)

	CreateAlert(ctx context.Context, alert *Alert) error
	GetAlert(ctx context.Context, id string) (*Alert, error)
	UpdateAlert(ctx context.Context, alert *Alert) error
	ListAlerts(ctx context.Context, agentID string) ([]*Alert, error)
}

// memoryRepository 是基于内存的 Repository 实现，所有方法线程安全。
type memoryRepository struct {
	mu      sync.RWMutex
	status  map[string]*AgentStatus
	samples map[string][]*ResourceUsage
	alerts  map[string]*Alert
	// outcomes / dispositions 是 Day 24 新增的两类追加型数据。
	outcomes     map[string][]*TaskOutcome
	dispositions map[string][]*AlertDisposition // key: alertID
}

// NewMemoryRepository 创建一个内存监控仓库。
func NewMemoryRepository() Repository {
	return &memoryRepository{
		status:       make(map[string]*AgentStatus),
		samples:      make(map[string][]*ResourceUsage),
		alerts:       make(map[string]*Alert),
		outcomes:     make(map[string][]*TaskOutcome),
		dispositions: make(map[string][]*AlertDisposition),
	}
}

func (r *memoryRepository) SaveStatus(_ context.Context, status *AgentStatus) error {
	if status == nil || status.AgentID == "" {
		return fmt.Errorf("monitor: status requires an agent id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// 存副本，避免调用方后续修改影响已保存的数据。
	cp := *status
	r.status[cp.AgentID] = &cp
	return nil
}

func (r *memoryRepository) GetStatus(_ context.Context, agentID string) (*AgentStatus, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.status[agentID]
	if !ok {
		return nil, fmt.Errorf("monitor: status for agent %s not found", agentID)
	}
	cp := *st
	return &cp, nil
}

func (r *memoryRepository) ListStatuses(_ context.Context) ([]*AgentStatus, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*AgentStatus, 0, len(r.status))
	for _, st := range r.status {
		cp := *st
		out = append(out, &cp)
	}
	// 按 AgentID 排序，保证列表接口输出稳定。
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out, nil
}

func (r *memoryRepository) DeleteStatus(_ context.Context, agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.status, agentID)
	delete(r.samples, agentID)
	return nil
}

func (r *memoryRepository) AppendSample(_ context.Context, agentID string, sample *ResourceUsage) error {
	if agentID == "" || sample == nil {
		return fmt.Errorf("monitor: sample requires an agent id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *sample
	r.samples[agentID] = append(r.samples[agentID], &cp)
	return nil
}

func (r *memoryRepository) QuerySamples(_ context.Context, agentID string, start, end time.Time) ([]*ResourceUsage, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*ResourceUsage
	for _, s := range r.samples[agentID] {
		if s.Timestamp.Before(start) || s.Timestamp.After(end) {
			continue
		}
		cp := *s
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	return out, nil
}

func (r *memoryRepository) CreateAlert(_ context.Context, alert *Alert) error {
	if alert == nil || alert.ID == "" {
		return fmt.Errorf("monitor: alert requires an id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.alerts[alert.ID]; exists {
		return fmt.Errorf("monitor: alert %s already exists", alert.ID)
	}
	cp := *alert
	r.alerts[cp.ID] = &cp
	return nil
}

func (r *memoryRepository) GetAlert(_ context.Context, id string) (*Alert, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.alerts[id]
	if !ok {
		return nil, fmt.Errorf("monitor: alert %s not found", id)
	}
	cp := *a
	return &cp, nil
}

func (r *memoryRepository) UpdateAlert(_ context.Context, alert *Alert) error {
	if alert == nil || alert.ID == "" {
		return fmt.Errorf("monitor: alert requires an id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.alerts[alert.ID]; !exists {
		return fmt.Errorf("monitor: alert %s not found", alert.ID)
	}
	cp := *alert
	r.alerts[cp.ID] = &cp
	return nil
}

func (r *memoryRepository) ListAlerts(_ context.Context, agentID string) ([]*Alert, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Alert
	for _, a := range r.alerts {
		if agentID != "" && a.AgentID != agentID {
			continue
		}
		cp := *a
		out = append(out, &cp)
	}
	// 最近创建的告警排在前面。
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ===== Day 24：追加型数据的仓库实现 =====

func (r *memoryRepository) AppendTaskOutcome(_ context.Context, outcome *TaskOutcome) error {
	if outcome == nil || outcome.AgentID == "" {
		return fmt.Errorf("monitor: task outcome requires an agent id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *outcome
	// 与采样一致：存副本，避免调用方后续修改污染已存数据。
	r.outcomes[cp.AgentID] = append(r.outcomes[cp.AgentID], &cp)
	return nil
}

func (r *memoryRepository) QueryTaskOutcomes(_ context.Context, agentID string, start, end time.Time) ([]*TaskOutcome, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*TaskOutcome
	for _, o := range r.outcomes[agentID] {
		if o.Timestamp.Before(start) || o.Timestamp.After(end) {
			continue
		}
		cp := *o
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	return out, nil
}

func (r *memoryRepository) AppendDisposition(_ context.Context, d *AlertDisposition) error {
	if d == nil || d.AlertID == "" {
		return fmt.Errorf("monitor: disposition requires an alert id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *d
	r.dispositions[cp.AlertID] = append(r.dispositions[cp.AlertID], &cp)
	return nil
}

func (r *memoryRepository) ListDispositions(_ context.Context, alertID string) ([]*AlertDisposition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// 处置记录是审计性数据，必须按「发生顺序」正序返回，便于还原处置过程。
	var out []*AlertDisposition
	if alertID != "" {
		for _, d := range r.dispositions[alertID] {
			cp := *d
			out = append(out, &cp)
		}
	} else {
		for _, list := range r.dispositions {
			for _, d := range list {
				cp := *d
				out = append(out, &cp)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		// 时间相同时按 ID 排序，保证输出稳定（ID 为 UUID，唯一）。
		return out[i].ID < out[j].ID
	})
	return out, nil
}
