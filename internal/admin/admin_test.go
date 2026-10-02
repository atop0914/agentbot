package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- 测试替身：全部用内存实现，不依赖 Docker/数据库 ----

type fakeAgentSource struct {
	items []*AgentView
	err   error
	calls int
}

func (f *fakeAgentSource) List(_ context.Context, offset, limit int) ([]*AgentView, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if offset >= len(f.items) {
		return nil, nil
	}
	end := offset + limit
	if end > len(f.items) {
		end = len(f.items)
	}
	return f.items[offset:end], nil
}

type fakeTaskSource struct {
	items []*TaskView
	err   error
}

func (f *fakeTaskSource) List(_ context.Context, _, _ string, _ int) ([]*TaskView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

type fakeMonitorSource struct {
	items   []*AlertView
	err     error
	summary *MonitorSummary
	// summaryErr 用于测试「监控摘要失败但告警列表可用」的分区内降级。
	summaryErr error
}

func (f *fakeMonitorSource) ListAlerts(_ context.Context, _ string, _ bool) ([]*AlertView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func (f *fakeMonitorSource) Summarize(_ context.Context, window time.Duration, _ int) (*MonitorSummary, error) {
	if f.summaryErr != nil {
		return nil, f.summaryErr
	}
	if f.summary != nil {
		return f.summary, nil
	}
	return &MonitorSummary{SeriesWindow: window, GeneratedAt: time.Now().UTC()}, nil
}

type fakeAuditSource struct {
	items []*AuditView
	err   error
}

func (f *fakeAuditSource) Query(_ context.Context, _ *time.Time, limit int) ([]*AuditView, error) {
	if f.err != nil {
		return nil, f.err
	}
	if limit > 0 && len(f.items) > limit {
		return f.items[:limit], nil
	}
	return f.items, nil
}

func (f *fakeAuditSource) Count(_ context.Context, _ *time.Time) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return len(f.items), nil
}

// fakeUserSource 是 UserSource 的测试替身。
type fakeUserSource struct {
	items []*UserView
	total int
	err   error
}

func (f *fakeUserSource) ListUsers(_ context.Context, offset, limit int) ([]*UserView, int, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	total := f.total
	if total == 0 {
		total = len(f.items)
	}
	if offset >= len(f.items) {
		return []*UserView{}, total, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(f.items) {
		end = len(f.items)
	}
	return f.items[offset:end], total, nil
}

// fakeAuthzSource 是 AuthzSource 的测试替身。
type fakeAuthzSource struct {
	stats *AuthzStats
	err   error
}

func (f *fakeAuthzSource) Stats(_ context.Context) (*AuthzStats, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.stats, nil
}

func baseTime() time.Time {
	return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
}

func newTestService(t *testing.T, src Sources, opts ...Option) Service {
	t.Helper()
	return NewService(src, "v9.9.9", opts...)
}

// ---- 类型与查询默认值 ----

func TestSectionValid(t *testing.T) {
	for _, s := range AllSections() {
		if !s.Valid() {
			t.Errorf("section %q from AllSections() should be valid", s)
		}
	}
	if Section("nope").Valid() {
		t.Error("unknown section should be invalid")
	}
}

func TestSnapshotQueryNormalizeDefaults(t *testing.T) {
	q := SnapshotQuery{}.Normalize()
	if q.RecentLimit != DefaultRecentLimit {
		t.Errorf("RecentLimit = %d, want %d", q.RecentLimit, DefaultRecentLimit)
	}
	if len(q.Sections) != len(AllSections()) {
		t.Errorf("Sections = %v, want all sections", q.Sections)
	}
}

func TestSnapshotQueryNormalizeClampsAndDedupes(t *testing.T) {
	q := SnapshotQuery{
		Sections:    []Section{SectionTasks, SectionAgents, SectionTasks, "bogus"},
		RecentLimit: 99999,
	}.Normalize()

	if q.RecentLimit != MaxRecentLimit {
		t.Errorf("RecentLimit = %d, want %d", q.RecentLimit, MaxRecentLimit)
	}
	// 去重 + 非法丢弃 + 按固定顺序排列
	want := []Section{SectionAgents, SectionTasks}
	if len(q.Sections) != len(want) {
		t.Fatalf("Sections = %v, want %v", q.Sections, want)
	}
	for i := range want {
		if q.Sections[i] != want[i] {
			t.Errorf("Sections[%d] = %q, want %q", i, q.Sections[i], want[i])
		}
	}
	if !q.Includes(SectionAgents) || q.Includes(SectionAlerts) {
		t.Error("Includes() disagrees with the normalized sections")
	}
}

// ---- 配置 ----

func TestConfigDefaults(t *testing.T) {
	svc := newTestService(t, Sources{})
	cfg, err := svc.Config(context.Background())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if cfg.Title != "AgentBot Admin" {
		t.Errorf("Title = %q", cfg.Title)
	}
	if cfg.Version != "v9.9.9" {
		t.Errorf("Version = %q, want v9.9.9", cfg.Version)
	}
	// 默认必须关闭会改变运行行为的开关
	if cfg.FeatureFlags["agent_control"] {
		t.Error("agent_control should default to disabled")
	}
	if !cfg.FeatureFlags["show_audit"] {
		t.Error("show_audit should default to enabled")
	}
}

func TestUpdateConfigPartial(t *testing.T) {
	svc := newTestService(t, Sources{})
	orig, _ := svc.Config(context.Background())

	title := "  运维控制台  "
	cfg, err := svc.UpdateConfig(context.Background(), UpdateConfigRequest{Title: &title})
	if err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if cfg.Title != "运维控制台" {
		t.Errorf("Title = %q, want trimmed 运维控制台", cfg.Title)
	}
	// 未提供的字段保持原值
	if cfg.Version != orig.Version {
		t.Errorf("Version = %q, want unchanged %q", cfg.Version, orig.Version)
	}
	if len(cfg.FeatureFlags) != len(orig.FeatureFlags) {
		t.Error("FeatureFlags should be unchanged when not supplied")
	}
	if !cfg.UpdatedAt.After(orig.UpdatedAt) && !cfg.UpdatedAt.Equal(orig.UpdatedAt) {
		t.Error("UpdatedAt should be refreshed")
	}
}

func TestUpdateConfigRejectsBadTitleAndFlags(t *testing.T) {
	svc := newTestService(t, Sources{})
	ctx := context.Background()

	empty := "   "
	if _, err := svc.UpdateConfig(ctx, UpdateConfigRequest{Title: &empty}); err == nil {
		t.Error("empty title should be rejected")
	}

	long := make([]rune, MaxTitleLen+1)
	for i := range long {
		long[i] = 'x'
	}
	longStr := string(long)
	if _, err := svc.UpdateConfig(ctx, UpdateConfigRequest{Title: &longStr}); err == nil {
		t.Error("over-long title should be rejected")
	}

	if _, err := svc.UpdateConfig(ctx, UpdateConfigRequest{
		FeatureFlags: map[string]bool{"bad flag": true},
	}); err == nil {
		t.Error("whitespace in flag name should be rejected")
	}

	// 失败的更新不得污染已有配置
	cfg, _ := svc.Config(ctx)
	if cfg.Title != "AgentBot Admin" {
		t.Errorf("Title = %q, want unchanged after failed updates", cfg.Title)
	}
}

func TestUpdateConfigReplacesFlagsWholesale(t *testing.T) {
	svc := newTestService(t, Sources{})
	cfg, err := svc.UpdateConfig(context.Background(), UpdateConfigRequest{
		FeatureFlags: map[string]bool{"only_flag": true},
	})
	if err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if len(cfg.FeatureFlags) != 1 || !cfg.FeatureFlags["only_flag"] {
		t.Errorf("FeatureFlags = %v, want exactly {only_flag:true}", cfg.FeatureFlags)
	}
}

func TestFlagNamesSorted(t *testing.T) {
	flags := map[string]bool{"z": true, "a": true, "m": false}
	enabled := FlagNames(flags, true)
	if len(enabled) != 2 || enabled[0] != "a" || enabled[1] != "z" {
		t.Errorf("enabled = %v, want [a z]", enabled)
	}
	disabled := FlagNames(flags, false)
	if len(disabled) != 1 || disabled[0] != "m" {
		t.Errorf("disabled = %v, want [m]", disabled)
	}
}

// ---- 聚合视图 ----

func TestSnapshotOverviewAggregatesAllSources(t *testing.T) {
	now := baseTime()
	src := Sources{
		Agents: &fakeAgentSource{items: []*AgentView{
			{ID: "a1", Name: "one", State: "running", UpdatedAt: now},
			{ID: "a2", Name: "two", State: "running", UpdatedAt: now.Add(-time.Minute)},
			{ID: "a3", Name: "three", State: "error", UpdatedAt: now.Add(-2 * time.Minute)},
		}},
		Tasks: &fakeTaskSource{items: []*TaskView{
			{ID: "t1", State: "in_progress", CreatedAt: now},
			{ID: "t2", State: "failed", CreatedAt: now},
			{ID: "t3", State: "completed", CreatedAt: now},
		}},
		Monitor: &fakeMonitorSource{items: []*AlertView{
			{ID: "al1", Severity: "high", CreatedAt: now},
			{ID: "al2", Severity: "high", CreatedAt: now},
		}},
		Audit: &fakeAuditSource{items: []*AuditView{
			{ID: "e1", Action: "agent.created", Timestamp: now},
		}},
		Users: &fakeUserSource{items: []*UserView{
			{ID: "u1", Username: "alice", Email: "alice@example.com", Status: "active", Role: "admin", Provider: "password", CreatedAt: now},
			{ID: "u2", Username: "bob", Email: "bob@example.com", Status: "inactive", Role: "user", Provider: "github", CreatedAt: now.Add(-time.Minute)},
		}, total: 2},
		Authz: &fakeAuthzSource{stats: &AuthzStats{TotalAssignments: 3, UserAssignments: 2, AgentAssignments: 1, ByRole: map[string]int{"role-worker": 1}}},
	}
	svc := newTestService(t, src)

	snap, err := svc.Snapshot(context.Background(), SnapshotQuery{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	ov := snap.Overview
	if ov == nil {
		t.Fatal("Overview is nil")
	}
	if ov.TotalAgents != 3 || ov.RunningAgents != 2 || ov.FailedAgents != 1 {
		t.Errorf("agent counts = %d/%d/%d, want 3/2/1", ov.TotalAgents, ov.RunningAgents, ov.FailedAgents)
	}
	if ov.TotalTasks != 3 || ov.RunningTasks != 1 || ov.FailedTasks != 1 {
		t.Errorf("task counts = %d/%d/%d, want 3/1/1", ov.TotalTasks, ov.RunningTasks, ov.FailedTasks)
	}
	if ov.OpenAlerts != 2 {
		t.Errorf("OpenAlerts = %d, want 2", ov.OpenAlerts)
	}
	if ov.AuditEvents != 1 {
		t.Errorf("AuditEvents = %d, want 1", ov.AuditEvents)
	}
	if ov.Degraded {
		t.Error("Degraded should be false when every source is healthy")
	}
	if snap.Errors != nil {
		t.Errorf("Errors = %v, want nil", snap.Errors)
	}
}

// TestSnapshotUserDigestAggregates 验证 users 分区把用户分布与授权链统计一起聚合。
func TestSnapshotUserDigestAggregates(t *testing.T) {
	now := baseTime()
	src := Sources{
		Users: &fakeUserSource{items: []*UserView{
			{ID: "u1", Username: "alice", Status: "active", Role: "admin", Provider: "password", CreatedAt: now},
			{ID: "u2", Username: "bob", Status: "inactive", Role: "user", Provider: "github", CreatedAt: now.Add(-time.Minute)},
			{ID: "u3", Username: "carol", Status: "active", Role: "user", Provider: "github", CreatedAt: now.Add(-2 * time.Minute)},
		}},
		Authz: &fakeAuthzSource{stats: &AuthzStats{
			TotalAssignments: 4, UserAssignments: 3, AgentAssignments: 1,
			ByRole: map[string]int{"role-worker": 2}, Decisions: 9, Denials: 2,
		}},
	}
	svc := newTestService(t, src)

	snap, err := svc.Snapshot(context.Background(), SnapshotQuery{Sections: []Section{SectionUsers}})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Users == nil {
		t.Fatal("Users digest is nil")
	}
	if snap.Users.Total != 3 {
		t.Errorf("Total = %d, want 3", snap.Users.Total)
	}
	if snap.Users.ByStatus["active"] != 2 || snap.Users.ByStatus["inactive"] != 1 {
		t.Errorf("ByStatus = %v, want active:2 inactive:1", snap.Users.ByStatus)
	}
	if snap.Users.ByProvider["github"] != 2 {
		t.Errorf("ByProvider = %v, want github:2", snap.Users.ByProvider)
	}
	if snap.Users.Authz == nil || snap.Users.Authz.Denials != 2 {
		t.Errorf("Authz stats not aggregated: %+v", snap.Users.Authz)
	}
	// 最近列表必须按创建时间倒序且稳定。
	if len(snap.Users.Recent) != 3 {
		t.Fatalf("Recent has %d entries, want 3", len(snap.Users.Recent))
	}
	if snap.Users.Recent[0].ID != "u1" || snap.Users.Recent[2].ID != "u3" {
		t.Errorf("Recent order = %s..%s, want u1..u3", snap.Users.Recent[0].ID, snap.Users.Recent[2].ID)
	}
}

// TestSnapshotUserDigestDegradesIndependently users 分区失败不能拖垮其他分区。
func TestSnapshotUserDigestDegradesIndependently(t *testing.T) {
	src := Sources{
		Agents: &fakeAgentSource{items: []*AgentView{{ID: "a1", State: "running", UpdatedAt: baseTime()}}},
		Users:  &fakeUserSource{err: fmt.Errorf("user backend down")},
		Authz:  &fakeAuthzSource{stats: &AuthzStats{TotalAssignments: 1}},
	}
	svc := newTestService(t, src)

	snap, err := svc.Snapshot(context.Background(), SnapshotQuery{})
	if err != nil {
		t.Fatalf("Snapshot must not fail when one source degrades: %v", err)
	}
	if snap.Users != nil {
		t.Error("Users digest should be nil when its source fails")
	}
	if _, ok := snap.Errors[string(SectionUsers)]; !ok {
		t.Errorf("Errors missing users entry: %v", snap.Errors)
	}
	if snap.Agents == nil || snap.Agents.Total != 1 {
		t.Error("agents section should still render")
	}
}

func TestSnapshotSectionSelection(t *testing.T) {
	src := Sources{
		Agents:  &fakeAgentSource{items: []*AgentView{{ID: "a1", State: "idle", UpdatedAt: baseTime()}}},
		Tasks:   &fakeTaskSource{items: []*TaskView{{ID: "t1", State: "pending"}}},
		Monitor: &fakeMonitorSource{items: []*AlertView{{ID: "al1", Severity: "low"}}},
		Audit:   &fakeAuditSource{items: []*AuditView{{ID: "e1", Action: "x"}}},
	}
	agentSrc := src.Agents.(*fakeAgentSource)
	svc := newTestService(t, src)

	snap, err := svc.Snapshot(context.Background(), SnapshotQuery{Sections: []Section{SectionPlugins}})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Overview != nil {
		t.Error("Overview should be omitted when not requested")
	}
	if snap.Agents != nil || snap.Tasks != nil || snap.Alerts != nil || snap.Audit != nil {
		t.Error("data sections should be omitted when not requested")
	}
	if snap.Plugins == nil {
		t.Fatal("Plugins should be present")
	}
	if agentSrc.calls != 0 {
		t.Errorf("agent source called %d times, want 0 for a plugins-only snapshot", agentSrc.calls)
	}
}

func TestSnapshotDegradesOnSingleSourceFailure(t *testing.T) {
	src := Sources{
		Agents:  &fakeAgentSource{err: os.ErrPermission},
		Tasks:   &fakeTaskSource{items: []*TaskView{{ID: "t1", State: "pending"}}},
		Monitor: &fakeMonitorSource{items: nil},
		Audit:   &fakeAuditSource{items: nil},
	}
	svc := newTestService(t, src)

	snap, err := svc.Snapshot(context.Background(), SnapshotQuery{})
	if err != nil {
		t.Fatalf("Snapshot should not fail when a single source fails: %v", err)
	}
	if snap.Errors[string(SectionAgents)] == "" {
		t.Error("agent failure should be recorded in Errors")
	}
	if !snap.Overview.Degraded {
		t.Error("Degraded should be true when a source fails")
	}
	if snap.Tasks == nil || snap.Tasks.Total != 1 {
		t.Error("healthy sections should still be populated")
	}
}

func TestSnapshotDoesNotPanicWithNoSources(t *testing.T) {
	svc := newTestService(t, Sources{})
	snap, err := svc.Snapshot(context.Background(), SnapshotQuery{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// 未装配的数据源：agents / tasks / alerts / audit / users 五个分区降级。
	// plugins 与 static 不依赖外部数据源，因此仍能正常渲染。
	if len(snap.Errors) != 5 {
		t.Errorf("Errors has %d entries, want 5 (one per unconfigured source): %v", len(snap.Errors), snap.Errors)
	}
	if snap.Overview == nil || !snap.Overview.Degraded {
		t.Error("Overview should be present and marked degraded")
	}
	if snap.Plugins == nil || snap.Static == nil {
		t.Error("sections without an external source should still render")
	}
}

func TestBuildAgentBreakdownSortsAndLimits(t *testing.T) {
	now := baseTime()
	agents := []*AgentView{
		{ID: "a1", Name: "oldest", State: "idle", UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "a3", Name: "newest", State: "running", UpdatedAt: now},
		{ID: "a2", Name: "middle", State: "idle", UpdatedAt: now.Add(-time.Hour)},
	}
	out := buildAgentBreakdown(agents, 2)

	if out.Total != 3 {
		t.Errorf("Total = %d, want 3", out.Total)
	}
	if out.ByState["idle"] != 2 || out.ByState["running"] != 1 {
		t.Errorf("ByState = %v, want idle:2 running:1", out.ByState)
	}
	if len(out.Recently) != 2 {
		t.Fatalf("Recently has %d items, want 2", len(out.Recently))
	}
	// 必须按 UpdatedAt 倒序，且最近列表要截断
	if out.Recently[0].ID != "a3" || out.Recently[1].ID != "a2" {
		t.Errorf("Recently order = %s,%s want a3,a2", out.Recently[0].ID, out.Recently[1].ID)
	}
}

func TestBuildAgentBreakdownStableOnEqualTimestamps(t *testing.T) {
	ts := baseTime()
	agents := []*AgentView{
		{ID: "a1", State: "idle", UpdatedAt: ts},
		{ID: "a3", State: "idle", UpdatedAt: ts},
		{ID: "a2", State: "idle", UpdatedAt: ts},
	}
	first := buildAgentBreakdown(agents, 3)
	second := buildAgentBreakdown([]*AgentView{agents[2], agents[0], agents[1]}, 3)
	for i := range first.Recently {
		if first.Recently[i].ID != second.Recently[i].ID {
			t.Fatalf("order differs between runs: %v vs %v", first.Recently, second.Recently)
		}
	}
	// 时间相同则按 ID 倒序
	if first.Recently[0].ID != "a3" {
		t.Errorf("first item = %s, want a3", first.Recently[0].ID)
	}
}

func TestBuildAgentBreakdownEmptyStateIsUnknown(t *testing.T) {
	out := buildAgentBreakdown([]*AgentView{{ID: "a1"}}, 5)
	if out.ByState["unknown"] != 1 {
		t.Errorf("ByState = %v, want unknown:1", out.ByState)
	}
	if out.ByState == nil {
		t.Error("ByState must never be nil")
	}
}

func TestBuildTaskBreakdownNilStateKept(t *testing.T) {
	out := buildTaskBreakdown([]*TaskView{{ID: "t1", State: "pending"}, nil, {ID: "t2"}})
	if out.Total != 2 {
		t.Errorf("Total = %d, want 2 (nil entries skipped)", out.Total)
	}
	if out.ByState["pending"] != 1 || out.ByState["unknown"] != 1 {
		t.Errorf("ByState = %v", out.ByState)
	}
}

func TestBuildAlertDigestSeverityAndOrder(t *testing.T) {
	now := baseTime()
	alerts := []*AlertView{
		{ID: "al1", Severity: "low", CreatedAt: now.Add(-time.Minute)},
		{ID: "al2", CreatedAt: now},
	}
	out := buildAlertDigest(alerts, 10)
	if out.Total != 2 {
		t.Errorf("Total = %d, want 2", out.Total)
	}
	if out.BySeverity["low"] != 1 || out.BySeverity["unknown"] != 1 {
		t.Errorf("BySeverity = %v", out.BySeverity)
	}
	if out.Items[0].ID != "al2" {
		t.Errorf("Items[0] = %s, want al2 (newest first)", out.Items[0].ID)
	}
}

func TestBuildAuditDigestTruncation(t *testing.T) {
	now := baseTime()
	recent := []*AuditView{
		{ID: "e3", Action: "agent.created", Timestamp: now},
		{ID: "e2", Action: "agent.created", Timestamp: now},
		{ID: "e1", Action: "task.started", Timestamp: now},
	}
	out := buildAuditDigest(recent, 10, 2)
	if !out.Truncated {
		t.Error("Truncated should be true when more than limit events are available")
	}
	if len(out.Recent) != 2 {
		t.Errorf("Recent has %d items, want 2", len(out.Recent))
	}
	if out.Total != 10 {
		t.Errorf("Total = %d, want the authoritative count 10", out.Total)
	}
	if out.ByAction["agent.created"] != 2 || out.ByAction["task.started"] != 1 {
		t.Errorf("ByAction = %v", out.ByAction)
	}
}

func TestBuildAuditDigestTotalFallsBackToObserved(t *testing.T) {
	out := buildAuditDigest([]*AuditView{{ID: "e1"}}, 0, 5)
	if out.Total != 1 {
		t.Errorf("Total = %d, want 1 when the reported count is lower than observed", out.Total)
	}
	if out.Truncated {
		t.Error("Truncated should be false when fewer events than limit are available")
	}
}

func TestSnapshotAuditWindowFilters(t *testing.T) {
	src := &fakeAuditSource{items: []*AuditView{{ID: "e1", Action: "x", Timestamp: baseTime()}}}
	svc := newTestService(t, Sources{Audit: src})

	// fake 不真正按窗口过滤，这里只验证窗口被解析并传递（不 panic、不报错）。
	if _, err := svc.Snapshot(context.Background(), SnapshotQuery{AuditWindow: time.Hour}); err != nil {
		t.Fatalf("Snapshot with audit window: %v", err)
	}
}

// ---- 静态资源 ----

func TestProbeStaticMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	status := probeStatic(dir, "/admin")
	if status.Mounted {
		t.Error("Mounted should be false for a missing directory")
	}
	if status.Message == "" {
		t.Error("Message should explain why the console is not mounted")
	}
}

func TestProbeStaticWithIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	status := probeStatic(dir, "/admin")
	if !status.Mounted {
		t.Fatalf("Mounted = false, message = %q", status.Message)
	}
	if !status.IndexExists {
		t.Error("IndexExists should be true")
	}
	if status.Message != "" {
		t.Errorf("Message = %q, want empty for a fully mounted console", status.Message)
	}
}

func TestProbeStaticMissingIndex(t *testing.T) {
	dir := t.TempDir()
	status := probeStatic(dir, "/admin")
	if !status.Mounted {
		t.Error("Mounted should be true for an existing directory")
	}
	if status.IndexExists {
		t.Error("IndexExists should be false without index.html")
	}
	if status.Message == "" {
		t.Error("Message should explain the missing index.html")
	}
}

func TestSetStaticMountAndStatus(t *testing.T) {
	svc := newTestService(t, Sources{})
	ctx := context.Background()

	if err := svc.SetStaticMount(ctx, StaticMountStatus{Mounted: true, IndexExists: true, Dir: "/srv/admin"}); err != nil {
		t.Fatalf("SetStaticMount: %v", err)
	}
	status, err := svc.StaticStatus(ctx)
	if err != nil {
		t.Fatalf("StaticStatus: %v", err)
	}
	if !status.Mounted || status.Dir != "/srv/admin" {
		t.Errorf("status = %+v", status)
	}
	if status.Prefix != "/admin" {
		t.Errorf("Prefix = %q, want the default /admin", status.Prefix)
	}
}

func TestSetStaticMountAddsDefaultMessage(t *testing.T) {
	svc := newTestService(t, Sources{})
	ctx := context.Background()
	if err := svc.SetStaticMount(ctx, StaticMountStatus{Mounted: false}); err != nil {
		t.Fatalf("SetStaticMount: %v", err)
	}
	status, _ := svc.StaticStatus(ctx)
	if status.Message == "" {
		t.Error("an unmounted console should carry an explanatory message")
	}
}

// ---- HTTP 处理器 ----

func newTestHandler(t *testing.T, src Sources, staticDir string) *Handler {
	t.Helper()
	svc := newTestService(t, src, WithStaticDir(staticDir))
	return NewHandler(svc, staticDir)
}

func TestHandlerSnapshotEndpoint(t *testing.T) {
	h := newTestHandler(t, Sources{
		Agents: &fakeAgentSource{items: []*AgentView{{ID: "a1", State: "running", UpdatedAt: baseTime()}}},
	}, "")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/snapshot?sections=agents&recent_limit=5", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var snap Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.Agents == nil || snap.Agents.Total != 1 {
		t.Errorf("Agents = %+v, want a single agent", snap.Agents)
	}
	if snap.Overview != nil {
		t.Error("Overview should not be rendered for an agents-only request")
	}
}

func TestHandlerSnapshotRejectsBadQuery(t *testing.T) {
	h := newTestHandler(t, Sources{}, "")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	for _, target := range []string{
		"/api/v1/admin/snapshot?sections=bogus",
		"/api/v1/admin/snapshot?recent_limit=abc",
		"/api/v1/admin/snapshot?recent_limit=-1",
		"/api/v1/admin/snapshot?audit_window=soon",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, rec.Code)
		}
	}
}

func TestHandlerSnapshotRejectsPost(t *testing.T) {
	h := newTestHandler(t, Sources{}, "")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/snapshot", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestHandlerConfigGetAndPut(t *testing.T) {
	h := newTestHandler(t, Sources{}, "")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}

	body := `{"title":"控制台","feature_flags":{"show_audit":true}}`
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodPut, "/api/v1/admin/config", strings.NewReader(body)))
	if rec2.Code != http.StatusOK {
		t.Fatalf("PUT status = %d (body %s)", rec2.Code, rec2.Body.String())
	}
	var cfg Config
	if err := json.Unmarshal(rec2.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Title != "控制台" {
		t.Errorf("Title = %q", cfg.Title)
	}
	if len(cfg.FeatureFlags) != 1 {
		t.Errorf("FeatureFlags = %v, want the wholesale replacement", cfg.FeatureFlags)
	}
}

func TestHandlerConfigRejectsInvalidAndWrongMethod(t *testing.T) {
	h := newTestHandler(t, Sources{}, "")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/admin/config", strings.NewReader(`{"title":"  "}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty title: status = %d, want 400", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/config", nil))
	if rec2.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: status = %d, want 405", rec2.Code)
	}
}

func TestHandlerStaticStatusEndpoint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	h := newTestHandler(t, Sources{}, dir)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/static", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var status StaticMountStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !status.Mounted || !status.IndexExists {
		t.Errorf("status = %+v, want mounted with index", status)
	}
}

func TestHandlerServesStaticAsset(t *testing.T) {
	dir := t.TempDir()
	assets := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assets, "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	h := newTestHandler(t, Sources{}, dir)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/assets/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "console.log(1)" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestHandlerStaticPlaceholderWhenAssetsMissing(t *testing.T) {
	h := newTestHandler(t, Sources{}, filepath.Join(t.TempDir(), "missing"))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 placeholder", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Error("placeholder should set a Content-Type")
	}
	if body := rec.Body.String(); !strings.Contains(body, "AgentBot Admin") {
		t.Errorf("placeholder body = %q", body)
	}
}

func TestHandlerStaticRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	// 在 root 之外放一个文件，越界访问必须失败
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("top secret"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	h := newTestHandler(t, Sources{}, root)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.URL.Path = "/admin/../../secret.txt"
	mux.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "top secret") {
		t.Fatal("path traversal escaped the static root")
	}
}

func TestHandlerStaticRejectsWriteMethods(t *testing.T) {
	h := newTestHandler(t, Sources{}, t.TempDir())
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestHandlerStaticPrefixOverride(t *testing.T) {
	h := newTestHandler(t, Sources{}, t.TempDir())
	h.SetStaticPrefix("console")
	if h.staticPrefix != "/console" {
		t.Errorf("staticPrefix = %q, want /console", h.staticPrefix)
	}
	h.SetStaticPrefix("")
	if h.staticPrefix != "/console" {
		t.Errorf("empty prefix should be ignored, got %q", h.staticPrefix)
	}
}

func TestParseSnapshotQueryDefaults(t *testing.T) {
	q, err := ParseSnapshotQuery(httptest.NewRequest(http.MethodGet, "/api/v1/admin/snapshot", nil))
	if err != nil {
		t.Fatalf("ParseSnapshotQuery: %v", err)
	}
	if q.RecentLimit != DefaultRecentLimit {
		t.Errorf("RecentLimit = %d, want %d", q.RecentLimit, DefaultRecentLimit)
	}
	if len(q.Sections) != len(AllSections()) {
		t.Errorf("Sections = %v, want all", q.Sections)
	}
}
