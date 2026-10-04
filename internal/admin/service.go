package admin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// 概览与最近列表的上限，避免下游返回超量数据拖慢控制台。
const (
	// overviewPageSize 是聚合概览时依次拉取的单页大小。
	overviewPageSize = 500
	// maxOverviewAgents 是概览计数的采样上限，达到上限时标记截断。
	maxOverviewAgents = 5000
)

// service 是 Service 接口的实现。
//
// 配置与静态挂载状态保存在内存中（本模块不引入持久化依赖，
// 保证在无数据库环境下也能启动并自证可用）。
type service struct {
	sources   Sources
	version   string
	staticDir string
	staticPfx string

	mu         sync.RWMutex
	config     *Config
	staticInfo StaticMountStatus
}

// Option 用于覆盖默认装配。
type Option func(*service)

// WithStaticDir 指定将来的前端构建产物目录。
func WithStaticDir(dir string) Option {
	return func(s *service) { s.staticDir = dir }
}

// WithStaticPrefix 指定静态资源的 URL 前缀（默认 /admin）。
func WithStaticPrefix(prefix string) Option {
	return func(s *service) { s.staticPfx = prefix }
}

// NewService 创建管理后台服务。
func NewService(sources Sources, version string, opts ...Option) Service {
	if version == "" {
		version = "dev"
	}
	s := &service{
		sources:   sources,
		version:   version,
		staticDir: "web/admin/dist",
		staticPfx: "/admin",
		config:    NewConfig("AgentBot Admin", version),
	}
	for _, opt := range opts {
		opt(s)
	}
	// 挂载状态由装配层稍后回报，这里先按目录探测一次，
	// 让控制台在静态资源已存在时无需额外调用即可显示正确状态。
	s.staticInfo = probeStatic(s.staticDir, s.staticPfx)
	return s
}

func (s *service) Config(_ context.Context) (*Config, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.clone(), nil
}

func (s *service) UpdateConfig(_ context.Context, req UpdateConfigRequest) (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.config.clone()
	if req.Title != nil {
		title, err := ValidateTitle(*req.Title)
		if err != nil {
			return nil, err
		}
		next.Title = title
	}
	if req.Version != nil {
		next.Version = *req.Version
	}
	if req.FeatureFlags != nil {
		flags, err := normalizeFlags(req.FeatureFlags)
		if err != nil {
			return nil, err
		}
		next.FeatureFlags = flags
	}
	next.UpdatedAt = time.Now().UTC()
	s.config = next
	return next.clone(), nil
}

func (s *service) StaticStatus(_ context.Context) (*StaticMountStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	info := s.staticInfo
	return &info, nil
}

func (s *service) SetStaticMount(_ context.Context, status StaticMountStatus) error {
	if status.Prefix == "" {
		status.Prefix = s.staticPfx
	}
	if status.Message == "" && !status.Mounted {
		status.Message = "admin console assets are not mounted"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.staticInfo = status
	return nil
}

// Snapshot 聚合各下游模块，产出控制台首屏视图。
//
// 单个分区失败不会中断整个请求：错误被记录到 Snapshot.Errors 并令
// Overview.Degraded 为 true，前端据此展示「部分数据」提示。
func (s *service) Snapshot(ctx context.Context, query SnapshotQuery) (*Snapshot, error) {
	q := query.Normalize()

	s.mu.RLock()
	cfg := s.config.clone()
	staticInfo := s.staticInfo
	s.mu.RUnlock()

	snap := &Snapshot{
		Title:       cfg.Title,
		Version:     cfg.Version,
		GeneratedAt: time.Now().UTC(),
		Errors:      make(map[string]string),
	}

	// 概览需要全部四个数据源，先各自取一次带缓存的快照。
	var agents []*AgentView
	var tasks []*TaskView
	var alerts []*AlertView
	var auditCount int
	var auditRecent []*AuditView

	needOverview := q.Includes(SectionOverview)

	if q.Includes(SectionAgents) || needOverview {
		list, err := s.fetchAgents(ctx)
		if err != nil {
			snap.Errors[string(SectionAgents)] = err.Error()
		} else {
			agents = list
		}
		if q.Includes(SectionAgents) {
			breakdown := buildAgentBreakdown(agents, q.RecentLimit)
			// 监控摘要是 agents 分区内的可选增强：取数失败时仅去掉这一段，
			// 不影响 Agent 状态分布的渲染（分区内二级降级）。
			//
			// 错误并入 agents 键而不是新开 agents.monitoring：Snapshot.Errors
			// 的契约是「每分区一个键」，多开子键会让控制台的分区降级判断失效。
			// 具体哪一段降级由 Monitoring.Errors 自己说明。
			monitoring, err := s.fetchMonitorSummary(ctx, q)
			if err != nil {
				breakdown.Monitoring = &MonitorSummary{
					SeriesWindow: monitorWindowFor(q).String(),
					GeneratedAt:  time.Now().UTC(),
					Degraded:     true,
					Errors:       map[string]string{"summary": err.Error()},
				}
				if _, already := snap.Errors[string(SectionAgents)]; !already {
					snap.Errors[string(SectionAgents)] = err.Error()
				}
			} else {
				breakdown.Monitoring = monitoring
			}
			snap.Agents = breakdown
		}
	}

	if q.Includes(SectionTasks) || needOverview {
		list, err := s.fetchTasks(ctx)
		if err != nil {
			snap.Errors[string(SectionTasks)] = err.Error()
		} else {
			tasks = list
		}
		if q.Includes(SectionTasks) {
			snap.Tasks = buildTaskBreakdown(tasks)
		}
	}

	if q.Includes(SectionAlerts) || needOverview {
		list, err := s.fetchAlerts(ctx)
		if err != nil {
			snap.Errors[string(SectionAlerts)] = err.Error()
		} else {
			alerts = list
		}
		if q.Includes(SectionAlerts) {
			snap.Alerts = buildAlertDigest(alerts, q.RecentLimit)
		}
	}

	if q.Includes(SectionAudit) || needOverview {
		var since *time.Time
		if q.AuditWindow > 0 {
			cutoff := time.Now().UTC().Add(-q.AuditWindow)
			since = &cutoff
		}
		count, err := s.fetchAuditCount(ctx, since)
		if err != nil {
			snap.Errors[string(SectionAudit)] = err.Error()
		} else {
			auditCount = count
		}
		if q.Includes(SectionAudit) {
			// 多取一条用于判断是否发生截断。
			recent, err := s.fetchAuditRecent(ctx, since, q.RecentLimit+1)
			if err != nil {
				snap.Errors[string(SectionAudit)] = err.Error()
			} else {
				auditRecent = recent
			}
			snap.Audit = buildAuditDigest(auditRecent, auditCount, q.RecentLimit)
		}
	}

	if q.Includes(SectionUsers) {
		digest, err := s.buildUserDigest(ctx, q.RecentLimit)
		if err != nil {
			snap.Errors[string(SectionUsers)] = err.Error()
		} else {
			snap.Users = digest
		}
	}

	if q.Includes(SectionNetwork) {
		// 网络分区的独立降级：出口摘要取不到时只让 network 分区标记不可用，
		// 其余分区照常渲染（与 agents/tasks 等分区一致）。
		summary, err := s.fetchNetworkSummary(ctx, q)
		if err != nil {
			snap.Errors[string(SectionNetwork)] = err.Error()
		} else {
			snap.Network = summary
		}
	}

	if q.Includes(SectionPlugins) {
		snap.Plugins = &PluginDigest{
			Enabled:  FlagNames(cfg.FeatureFlags, true),
			Disabled: FlagNames(cfg.FeatureFlags, false),
		}
	}

	if q.Includes(SectionStaticAsset) {
		info := staticInfo
		snap.Static = &info
	}

	if needOverview {
		ov := buildOverview(agents, tasks, alerts, auditCount)
		ov.Degraded = len(snap.Errors) > 0
		snap.Overview = ov
	}

	if len(snap.Errors) == 0 {
		snap.Errors = nil
	}
	return snap, nil
}

func (s *service) fetchAgents(ctx context.Context) ([]*AgentView, error) {
	if s.sources.Agents == nil {
		return nil, fmt.Errorf("admin: agent source is not configured")
	}
	out := make([]*AgentView, 0, overviewPageSize)
	for offset := 0; len(out) < maxOverviewAgents; offset += overviewPageSize {
		page, err := s.sources.Agents.List(ctx, offset, overviewPageSize)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < overviewPageSize {
			break
		}
	}
	return out, nil
}

func (s *service) fetchTasks(ctx context.Context) ([]*TaskView, error) {
	if s.sources.Tasks == nil {
		return nil, fmt.Errorf("admin: task source is not configured")
	}
	out := make([]*TaskView, 0, overviewPageSize)
	for offset := 0; len(out) < maxOverviewAgents; offset += overviewPageSize {
		page, err := s.sources.Tasks.List(ctx, "", "", overviewPageSize)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < overviewPageSize {
			break
		}
	}
	return out, nil
}

func (s *service) fetchAlerts(ctx context.Context) ([]*AlertView, error) {
	if s.sources.Monitor == nil {
		return nil, fmt.Errorf("admin: monitor source is not configured")
	}
	return s.sources.Monitor.ListAlerts(ctx, "", false)
}

// fetchMonitorSummary 取监控摘要（时间序列 + 处置进度）。
//
// 窗口沿用审计窗口的语义：未显式指定时用默认 30 分钟，
// 保证控制台首屏的「最近趋势」和其他分区的时间尺度一致。
func (s *service) fetchMonitorSummary(ctx context.Context, q SnapshotQuery) (*MonitorSummary, error) {
	if s.sources.Monitor == nil {
		return nil, fmt.Errorf("admin: monitor source is not configured")
	}
	return s.sources.Monitor.Summarize(ctx, monitorWindowFor(q), q.RecentLimit)
}

// monitorWindowFor 解析 agents 分区监控摘要的时间窗。
func monitorWindowFor(q SnapshotQuery) time.Duration {
	if q.AuditWindow > 0 {
		return q.AuditWindow
	}
	return DefaultMonitorWindow
}

// fetchNetworkSummary 取出口摘要（策略计数 + 出站流量）。
func (s *service) fetchNetworkSummary(ctx context.Context, q SnapshotQuery) (*NetworkSummary, error) {
	if s.sources.Network == nil {
		return nil, fmt.Errorf("admin: network source is not configured")
	}
	return s.sources.Network.Summarize(ctx, networkWindowFor(q))
}

// networkWindowFor 解析 network 分区的时间窗。
//
// 默认比监控窗口长得多（24h）：外联异常的特征是「低频但持续」，
// 半小时窗口会把已经发生的横向探测整段漏掉。
// 显式给出更长的 AuditWindow 时以调用方为准（控制台「看最近 7 天」的场景）。
func networkWindowFor(q SnapshotQuery) time.Duration {
	if q.AuditWindow > DefaultNetworkWindow {
		return q.AuditWindow
	}
	return DefaultNetworkWindow
}

func (s *service) fetchAuditCount(ctx context.Context, since *time.Time) (int, error) {
	if s.sources.Audit == nil {
		return 0, fmt.Errorf("admin: audit source is not configured")
	}
	return s.sources.Audit.Count(ctx, since)
}

func (s *service) fetchAuditRecent(ctx context.Context, since *time.Time, limit int) ([]*AuditView, error) {
	if s.sources.Audit == nil {
		return nil, fmt.Errorf("admin: audit source is not configured")
	}
	return s.sources.Audit.Query(ctx, since, limit)
}

// buildUserDigest 汇总用户分布与授权链统计。
//
// 用户列表按当前页拉取（默认取一页足够后台首屏展示），授权链统计则来自 authz，
// 两者互相独立：authz 不可用时 users 分区仍然能展示用户计数，只是 authz 字段缺失。
func (s *service) buildUserDigest(ctx context.Context, recentLimit int) (*UserDigest, error) {
	if s.sources.Users == nil {
		return nil, fmt.Errorf("admin: user source is not configured")
	}
	users, total, err := s.sources.Users.ListUsers(ctx, 0, overviewPageSize)
	if err != nil {
		return nil, err
	}

	digest := &UserDigest{
		Total:      total,
		ByStatus:   make(map[string]int),
		ByRole:     make(map[string]int),
		ByProvider: make(map[string]int),
		Recent:     make([]UserSummary, 0, recentLimit),
	}

	// 最近 = 创建时间倒序；时间相同按 ID 倒序，保证输出稳定。
	sorted := make([]*UserView, 0, len(users))
	for _, u := range users {
		if u == nil {
			continue
		}
		status := u.Status
		if status == "" {
			status = "unknown"
		}
		digest.ByStatus[status]++

		roleName := u.Role
		if roleName == "" {
			roleName = "unknown"
		}
		digest.ByRole[roleName]++

		provider := u.Provider
		if provider == "" {
			provider = "unknown"
		}
		digest.ByProvider[provider]++

		sorted = append(sorted, u)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
		}
		return sorted[i].ID > sorted[j].ID
	})
	for i, u := range sorted {
		if i >= recentLimit {
			break
		}
		digest.Recent = append(digest.Recent, UserSummary{
			ID:        u.ID,
			Username:  u.Username,
			Email:     u.Email,
			Status:    u.Status,
			Role:      u.Role,
			Provider:  u.Provider,
			CreatedAt: u.CreatedAt,
		})
	}

	if s.sources.Authz != nil {
		stats, err := s.sources.Authz.Stats(ctx)
		if err != nil {
			// 授权链统计失败不影响用户分布，但要把原因暴露出来。
			return digest, fmt.Errorf("admin: authorization stats unavailable: %w", err)
		}
		digest.Authz = stats
	}

	return digest, nil
}

// buildOverview 汇总各模块计数。
func buildOverview(agents []*AgentView, tasks []*TaskView, alerts []*AlertView, auditCount int) *Overview {
	ov := &Overview{AuditEvents: auditCount}
	for _, a := range agents {
		if a == nil {
			continue
		}
		ov.TotalAgents++
		switch a.State {
		case "running":
			ov.RunningAgents++
		case "error":
			ov.FailedAgents++
		}
	}
	for _, t := range tasks {
		if t == nil {
			continue
		}
		ov.TotalTasks++
		switch t.State {
		case "in_progress":
			ov.RunningTasks++
		case "failed":
			ov.FailedTasks++
		}
	}
	for _, al := range alerts {
		if al != nil {
			ov.OpenAlerts++
		}
	}
	return ov
}

// buildAgentBreakdown 统计 Agent 状态分布并给出最近的若干条。
//
// 返回的 ByState 一定非 nil（无数据时为空 map），保证 JSON 输出形状稳定。
func buildAgentBreakdown(agents []*AgentView, recentLimit int) *AgentBreakdown {
	out := &AgentBreakdown{
		Total:   len(agents),
		ByState: make(map[string]int),
	}
	sorted := make([]*AgentView, 0, len(agents))
	for _, a := range agents {
		if a == nil {
			continue
		}
		state := a.State
		if state == "" {
			state = "unknown"
		}
		out.ByState[state]++
		sorted = append(sorted, a)
	}
	out.Total = len(sorted)
	// 最近 = UpdatedAt 倒序；时间相同时按 ID 倒序，保证顺序稳定。
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].UpdatedAt.Equal(sorted[j].UpdatedAt) {
			return sorted[i].UpdatedAt.After(sorted[j].UpdatedAt)
		}
		return sorted[i].ID > sorted[j].ID
	})
	for i, a := range sorted {
		if i >= recentLimit {
			break
		}
		out.Recently = append(out.Recently, AgentSummary{
			ID:        a.ID,
			Name:      a.Name,
			State:     a.State,
			UpdatedAt: a.UpdatedAt,
		})
	}
	return out
}

func buildTaskBreakdown(tasks []*TaskView) *TaskBreakdown {
	out := &TaskBreakdown{ByState: make(map[string]int)}
	for _, t := range tasks {
		if t == nil {
			continue
		}
		state := t.State
		if state == "" {
			state = "unknown"
		}
		out.ByState[state]++
		out.Total++
	}
	return out
}

// buildAlertDigest 汇总未处理告警，按时间倒序。
func buildAlertDigest(alerts []*AlertView, recentLimit int) *AlertDigest {
	out := &AlertDigest{
		BySeverity: make(map[string]int),
	}
	sorted := make([]*AlertView, 0, len(alerts))
	for _, a := range alerts {
		if a == nil {
			continue
		}
		sev := a.Severity
		if sev == "" {
			sev = "unknown"
		}
		out.BySeverity[sev]++
		sorted = append(sorted, a)
	}
	out.Total = len(sorted)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
		}
		return sorted[i].ID > sorted[j].ID
	})
	for i, a := range sorted {
		if i >= recentLimit {
			break
		}
		out.Items = append(out.Items, AlertBrief{
			ID:        a.ID,
			AgentID:   a.AgentID,
			Type:      a.Type,
			Severity:  a.Severity,
			Message:   a.Message,
			CreatedAt: a.CreatedAt,
		})
	}
	return out
}

// buildAuditDigest 汇总审计事件。
//
// recent 允许比 limit 多一条，多出的那一条仅用于判断 Truncated，
// 不会出现在输出里。
func buildAuditDigest(recent []*AuditView, total, limit int) *AuditDigest {
	out := &AuditDigest{
		Total:    total,
		ByAction: make(map[string]int),
	}
	if total < len(recent) {
		// 下游 Count 与 Query 的口径可能不同，取可观测到的较大值。
		out.Total = len(recent)
	}
	for _, e := range recent {
		if e == nil {
			continue
		}
		action := e.Action
		if action == "" {
			action = "unknown"
		}
		out.ByAction[action]++
	}
	if len(recent) > limit {
		out.Truncated = true
	}
	for i, e := range recent {
		if i >= limit {
			break
		}
		out.Recent = append(out.Recent, AuditBrief{
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
	return out
}

// probeStatic 探测静态资源目录，返回挂载状态。
// 目录不存在时返回明确的占位状态而不是让调用方崩溃。
func probeStatic(dir, prefix string) StaticMountStatus {
	status := StaticMountStatus{
		Prefix: prefix,
		Dir:    dir,
	}
	if dir == "" {
		status.Message = "admin console assets are not mounted"
		return status
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		status.Message = fmt.Sprintf("resolve static dir: %v", err)
		return status
	}
	status.Dir = abs
	info, err := os.Stat(abs)
	if err != nil {
		status.Message = fmt.Sprintf("admin console assets are not built yet (%s)", abs)
		return status
	}
	if !info.IsDir() {
		status.Message = fmt.Sprintf("%s is not a directory", abs)
		return status
	}
	status.Mounted = true
	if _, err := os.Stat(filepath.Join(abs, "index.html")); err == nil {
		status.IndexExists = true
	} else {
		status.Message = "index.html not found in admin console assets"
	}
	return status
}
