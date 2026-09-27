package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestService() Service {
	return NewService(NewMemoryRepository())
}

func TestMemoryRepository_StatusCRUD(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	if _, err := repo.GetStatus(ctx, "missing"); err == nil {
		t.Fatal("expected error for missing status")
	}

	status := &AgentStatus{AgentID: "a1", State: "running", Progress: 42}
	if err := repo.SaveStatus(ctx, status); err != nil {
		t.Fatalf("save status: %v", err)
	}

	got, err := repo.GetStatus(ctx, "a1")
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	if got.State != "running" || got.Progress != 42 {
		t.Errorf("got state=%q progress=%v, want running/42", got.State, got.Progress)
	}

	// 保存的是副本：修改原对象不应影响仓库内容。
	status.State = "error"
	again, _ := repo.GetStatus(ctx, "a1")
	if again.State != "running" {
		t.Errorf("repository aliased caller state: got %q", again.State)
	}

	if err := repo.DeleteStatus(ctx, "a1"); err != nil {
		t.Fatalf("delete status: %v", err)
	}
	if _, err := repo.GetStatus(ctx, "a1"); err == nil {
		t.Error("expected error after delete")
	}
}

func TestMemoryRepository_ListStatusesSorted(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	for _, id := range []string{"c", "a", "b"} {
		if err := repo.SaveStatus(ctx, &AgentStatus{AgentID: id, State: "idle"}); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	list, err := repo.ListStatuses(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d statuses, want 3", len(list))
	}
	for i, want := range []string{"a", "b", "c"} {
		if list[i].AgentID != want {
			t.Errorf("index %d: got %q, want %q", i, list[i].AgentID, want)
		}
	}
}

func TestMemoryRepository_QuerySamplesWindow(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 10; i++ {
		s := &ResourceUsage{CPU: float64(i), Timestamp: base.Add(time.Duration(i) * time.Minute)}
		if err := repo.AppendSample(ctx, "a1", s); err != nil {
			t.Fatalf("append sample %d: %v", i, err)
		}
	}

	got, err := repo.QuerySamples(ctx, "a1", base.Add(2*time.Minute), base.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d samples, want 4 (inclusive bounds)", len(got))
	}
	// 结果按时间升序。
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.Before(got[i-1].Timestamp) {
			t.Error("samples not sorted ascending by timestamp")
		}
	}
}

func TestMemoryRepository_AlertLifecycle(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	alert := &Alert{ID: "al1", AgentID: "a1", Type: AlertCPUHigh, CreatedAt: time.Now().UTC()}
	if err := repo.CreateAlert(ctx, alert); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 重复 ID 应被拒绝。
	if err := repo.CreateAlert(ctx, alert); err == nil {
		t.Error("expected duplicate alert error")
	}

	alert.Resolved = true
	if err := repo.UpdateAlert(ctx, alert); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := repo.GetAlert(ctx, "al1")
	if !got.Resolved {
		t.Error("alert not marked resolved")
	}

	if _, err := repo.GetAlert(ctx, "missing"); err == nil {
		t.Error("expected error for missing alert")
	}
}

func TestHealthScore_PerfectWhenIdleHealthy(t *testing.T) {
	status := &AgentStatus{
		AgentID:    "a1",
		State:      "running",
		LastActive: time.Now().UTC(),
		Resources:  ResourceUsage{CPU: 10, Memory: 20, Disk: 30},
	}
	if got := HealthScore(status); got != 100 {
		t.Errorf("got %d, want 100", got)
	}
}

func TestHealthScore_ErrorStateIsZero(t *testing.T) {
	status := &AgentStatus{AgentID: "a1", State: "error", LastActive: time.Now().UTC()}
	if got := HealthScore(status); got != 0 {
		t.Errorf("got %d, want 0 for error state", got)
	}
}

func TestHealthScore_ResourcePenaltyMonotonic(t *testing.T) {
	base := &AgentStatus{AgentID: "a1", State: "running", LastActive: time.Now().UTC()}
	low := *base
	low.Resources = ResourceUsage{CPU: 70}
	high := *base
	high.Resources = ResourceUsage{CPU: 85}

	sl, sh := HealthScore(&low), HealthScore(&high)
	if sl != 100 {
		t.Errorf("at warn threshold got %d, want 100 (no penalty yet)", sl)
	}
	if sh != 100-DefaultCPUThreshold+DefaultCPUThreshold-30 {
		// 明确断言：CPU 达 85 时扣满 30 分。
		if sh != 70 {
			t.Errorf("at max threshold got %d, want 70", sh)
		}
	}
	if sh >= sl {
		t.Errorf("penalty not monotonic: low=%d high=%d", sl, sh)
	}
}

func TestHealthScore_SeverityWeighting(t *testing.T) {
	status := &AgentStatus{
		AgentID:    "a1",
		State:      "running",
		LastActive: time.Now().UTC(),
		Errors: []Error{
			{Code: "e1", Severity: "critical"},
			{Code: "e2", Severity: "low"},
		},
	}
	// critical 扣 15，low 扣 5。
	if got := HealthScore(status); got != 80 {
		t.Errorf("got %d, want 80", got)
	}
}

func TestHealthScore_InactivityReducesScore(t *testing.T) {
	active := &AgentStatus{AgentID: "a1", State: "running", LastActive: time.Now().UTC()}
	stale := &AgentStatus{AgentID: "a1", State: "running",
		LastActive: time.Now().UTC().Add(-2 * DefaultInactiveAfter)}

	if HealthScore(stale) >= HealthScore(active) {
		t.Errorf("stale agent scored %d, active scored %d — expected stale lower",
			HealthScore(stale), HealthScore(active))
	}
}

func TestService_ReportStatusPersists(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	status := &AgentStatus{AgentID: "a1", State: "running"}
	if err := svc.ReportStatus(ctx, status); err != nil {
		t.Fatalf("report: %v", err)
	}
	got, err := svc.GetAgentStatus(ctx, "a1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LastActive.IsZero() {
		t.Error("LastActive should be defaulted on report")
	}
}

func TestService_ReportStatusValidation(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	if err := svc.ReportStatus(ctx, nil); err == nil {
		t.Error("expected error for nil status")
	}
	if err := svc.ReportStatus(ctx, &AgentStatus{}); err == nil {
		t.Error("expected error for empty agent id")
	}
	if _, err := svc.GetAgentStatus(ctx, ""); err == nil {
		t.Error("expected error for empty agent id on get")
	}
}

func TestService_ReportMetricsUpdatesStatus(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	usage := &ResourceUsage{CPU: 55, Memory: 60, Disk: 40}
	if err := svc.ReportMetrics(ctx, "a1", usage); err != nil {
		t.Fatalf("report metrics: %v", err)
	}

	status, err := svc.GetAgentStatus(ctx, "a1")
	if err != nil {
		t.Fatalf("status not auto-created: %v", err)
	}
	if status.Resources.CPU != 55 {
		t.Errorf("status resources not synced: got cpu=%v, want 55", status.Resources.CPU)
	}
	if status.State != "unknown" {
		t.Errorf("auto-created state got %q, want unknown", status.State)
	}
}

func TestService_ReportMetricsValidation(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	if err := svc.ReportMetrics(ctx, "", &ResourceUsage{}); err == nil {
		t.Error("expected error for empty agent id")
	}
	if err := svc.ReportMetrics(ctx, "a1", nil); err == nil {
		t.Error("expected error for nil usage")
	}
}

func TestService_GetAggregatedMetrics(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	for _, cpu := range []float64{10, 20, 30, 40} {
		if err := svc.ReportMetrics(ctx, "a1", &ResourceUsage{CPU: cpu, Memory: cpu * 2}); err != nil {
			t.Fatalf("report: %v", err)
		}
	}
	agg, err := svc.GetAggregatedMetrics(ctx, "a1", time.Hour)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	// 均值 (10+20+30+40)/4 = 25
	if diff := agg.CPU - 25; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("got cpu=%v, want 25", agg.CPU)
	}
	if diff := agg.Memory - 50; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("got memory=%v, want 50", agg.Memory)
	}
}

func TestService_GetAggregatedMetricsNoSamples(t *testing.T) {
	svc := newTestService()
	if _, err := svc.GetAggregatedMetrics(context.Background(), "ghost", time.Hour); err == nil {
		t.Error("expected error when no samples exist")
	}
}

func TestService_GetMetricsSwapsReversedWindow(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	if err := svc.ReportMetrics(ctx, "a1", &ResourceUsage{CPU: 5}); err != nil {
		t.Fatalf("report: %v", err)
	}

	now := time.Now().UTC()
	// start 晚于 end：应自动交换而不是查空。
	samples, err := svc.GetMetrics(ctx, "a1", now.Add(time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	if len(samples) != 1 {
		t.Errorf("got %d samples, want 1", len(samples))
	}
}

func TestService_HealthCheck(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	if _, err := svc.HealthCheck(ctx, "ghost"); err == nil {
		t.Error("expected error for unknown agent")
	}

	if err := svc.ReportStatus(ctx, &AgentStatus{AgentID: "a1", State: "running"}); err != nil {
		t.Fatalf("report: %v", err)
	}
	ok, err := svc.HealthCheck(ctx, "a1")
	if err != nil || !ok {
		t.Errorf("healthy agent: ok=%v err=%v", ok, err)
	}

	// error 状态的 Agent 不健康。
	if err := svc.ReportStatus(ctx, &AgentStatus{AgentID: "a2", State: "error"}); err != nil {
		t.Fatalf("report: %v", err)
	}
	ok, _ = svc.HealthCheck(ctx, "a2")
	if ok {
		t.Error("error-state agent reported healthy")
	}
}

func TestService_HealthCheckStaleAgentUnhealthy(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	stale := &AgentStatus{AgentID: "a1", State: "running",
		LastActive: time.Now().UTC().Add(-2 * DefaultInactiveAfter)}
	if err := svc.ReportStatus(ctx, stale); err != nil {
		t.Fatalf("report: %v", err)
	}
	ok, err := svc.HealthCheck(ctx, "a1")
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	if ok {
		t.Error("stale agent reported healthy")
	}
}

func TestService_AlertRuleCRUD(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	// 阈值留空时套用默认值。
	rule, err := svc.CreateAlert(ctx, AlertRule{AgentID: "a1", Type: AlertCPUHigh})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	rules, err := svc.ListAlertRules(ctx, "a1")
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(rules))
	}
	if rules[0].Threshold != DefaultCPUThreshold {
		t.Errorf("got threshold %v, want %v", rules[0].Threshold, DefaultCPUThreshold)
	}
	if !rules[0].Enabled {
		t.Error("rule should be enabled by default")
	}
	_ = rule

	if err := svc.DeleteAlert(ctx, rules[0].ID); err != nil {
		t.Fatalf("delete rule: %v", err)
	}
	after, _ := svc.ListAlertRules(ctx, "a1")
	if len(after) != 0 {
		t.Errorf("got %d rules after delete, want 0", len(after))
	}
	if err := svc.DeleteAlert(ctx, "ghost"); err == nil {
		t.Error("expected error deleting unknown rule")
	}
}

func TestService_CreateAlertValidation(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	if _, err := svc.CreateAlert(ctx, AlertRule{Type: AlertCPUHigh}); err == nil {
		t.Error("expected error when agent id missing")
	}
	if _, err := svc.CreateAlert(ctx, AlertRule{AgentID: "a1"}); err == nil {
		t.Error("expected error when type missing")
	}
}

func TestService_EvaluateAlertsRaisesOnBreach(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	if _, err := svc.CreateAlert(ctx, AlertRule{AgentID: "a1", Type: AlertCPUHigh, Threshold: 80}); err != nil {
		t.Fatalf("create rule: %v", err)
	}

	// 未越界：无告警。
	breached := svc.EvaluateAlerts(ctx, &AgentStatus{AgentID: "a1", State: "running",
		LastActive: time.Now().UTC(), Resources: ResourceUsage{CPU: 50}})
	if len(breached) != 0 {
		t.Fatalf("got %d alerts below threshold, want 0", len(breached))
	}

	// 越界：产生一条告警，带实际值与阈值。
	breached = svc.EvaluateAlerts(ctx, &AgentStatus{AgentID: "a1", State: "running",
		LastActive: time.Now().UTC(), Resources: ResourceUsage{CPU: 90}})
	if len(breached) != 1 {
		t.Fatalf("got %d alerts above threshold, want 1", len(breached))
	}
	a := breached[0]
	if a.Type != AlertCPUHigh || a.Value != 90 || a.Threshold != 80 {
		t.Errorf("alert fields wrong: %+v", a)
	}
	if a.AgentID != "a1" || a.Resolved {
		t.Errorf("alert identity wrong: %+v", a)
	}
}

func TestService_EvaluateAlertsDeduplicates(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	if _, err := svc.CreateAlert(ctx, AlertRule{AgentID: "a1", Type: AlertCPUHigh, Threshold: 80}); err != nil {
		t.Fatalf("create rule: %v", err)
	}

	hot := &AgentStatus{AgentID: "a1", State: "running", LastActive: time.Now().UTC(),
		Resources: ResourceUsage{CPU: 95}}

	first := svc.EvaluateAlerts(ctx, hot)
	second := svc.EvaluateAlerts(ctx, hot)
	if len(first) != 1 {
		t.Fatalf("first evaluation: got %d, want 1", len(first))
	}
	if len(second) != 0 {
		t.Errorf("second evaluation re-raised %d alerts, want 0 (dedupe)", len(second))
	}
}

func TestService_EvaluateAlertsIgnoresOtherAgents(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	if _, err := svc.CreateAlert(ctx, AlertRule{AgentID: "a1", Type: AlertCPUHigh, Threshold: 80}); err != nil {
		t.Fatalf("create rule: %v", err)
	}
	// 规则属于 a1，a2 越界不应产生告警。
	got := svc.EvaluateAlerts(ctx, &AgentStatus{AgentID: "a2", State: "running",
		LastActive: time.Now().UTC(), Resources: ResourceUsage{CPU: 99}})
	if len(got) != 0 {
		t.Errorf("got %d alerts for unrelated agent, want 0", len(got))
	}
}

func TestService_ResolveAlert(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	if _, err := svc.CreateAlert(ctx, AlertRule{AgentID: "a1", Type: AlertCPUHigh, Threshold: 80}); err != nil {
		t.Fatalf("create rule: %v", err)
	}
	raised := svc.EvaluateAlerts(ctx, &AgentStatus{AgentID: "a1", State: "running",
		LastActive: time.Now().UTC(), Resources: ResourceUsage{CPU: 95}})
	if len(raised) != 1 {
		t.Fatalf("setup failed: got %d alerts", len(raised))
	}

	unresolved, _ := svc.ListAlerts(ctx, "a1", false)
	if len(unresolved) != 1 {
		t.Fatalf("got %d unresolved, want 1", len(unresolved))
	}

	if err := svc.ResolveAlert(ctx, raised[0].ID); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	unresolved, _ = svc.ListAlerts(ctx, "a1", false)
	if len(unresolved) != 0 {
		t.Errorf("got %d unresolved after resolve, want 0", len(unresolved))
	}
	resolved, _ := svc.ListAlerts(ctx, "a1", true)
	if len(resolved) != 1 {
		t.Errorf("got %d resolved, want 1", len(resolved))
	}
	if resolved[0].ResolvedAt.IsZero() {
		t.Error("ResolvedAt not stamped")
	}

	// 重复 resolve 应报错。
	if err := svc.ResolveAlert(ctx, raised[0].ID); err == nil {
		t.Error("expected error on double resolve")
	}
	if err := svc.ResolveAlert(ctx, "ghost"); err == nil {
		t.Error("expected error resolving unknown alert")
	}
}

func TestService_ResolveThenReRaise(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	if _, err := svc.CreateAlert(ctx, AlertRule{AgentID: "a1", Type: AlertCPUHigh, Threshold: 80}); err != nil {
		t.Fatalf("create rule: %v", err)
	}
	hot := &AgentStatus{AgentID: "a1", State: "running", LastActive: time.Now().UTC(),
		Resources: ResourceUsage{CPU: 95}}

	raised := svc.EvaluateAlerts(ctx, hot)
	if err := svc.ResolveAlert(ctx, raised[0].ID); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// 解决后再次越界应重新产生告警（去重只针对未解决项）。
	again := svc.EvaluateAlerts(ctx, hot)
	if len(again) != 1 {
		t.Errorf("got %d alerts after resolve, want 1 re-raise", len(again))
	}
}

func TestService_InactivityAlert(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	if _, err := svc.CreateAlert(ctx, AlertRule{AgentID: "a1", Type: AlertInactive, Duration: time.Minute}); err != nil {
		t.Fatalf("create rule: %v", err)
	}

	fresh := svc.EvaluateAlerts(ctx, &AgentStatus{AgentID: "a1", State: "running", LastActive: time.Now().UTC()})
	if len(fresh) != 0 {
		t.Errorf("fresh agent raised %d alerts, want 0", len(fresh))
	}

	stale := svc.EvaluateAlerts(ctx, &AgentStatus{AgentID: "a1", State: "running",
		LastActive: time.Now().UTC().Add(-5 * time.Minute)})
	if len(stale) != 1 {
		t.Fatalf("stale agent raised %d alerts, want 1", len(stale))
	}
	if stale[0].Severity != "medium" {
		t.Errorf("got severity %q, want medium", stale[0].Severity)
	}
}

func TestService_GetDashboard(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	agents := []*AgentStatus{
		{AgentID: "a1", State: "running", CurrentTask: "t1"},
		{AgentID: "a2", State: "running"},
		{AgentID: "a3", State: "error"},
		{AgentID: "a4", State: "idle"},
	}
	for _, st := range agents {
		if err := svc.ReportStatus(ctx, st); err != nil {
			t.Fatalf("report %s: %v", st.AgentID, err)
		}
	}

	dash, err := svc.GetDashboard(ctx)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if dash.TotalAgents != 4 {
		t.Errorf("got total=%d, want 4", dash.TotalAgents)
	}
	if dash.ActiveAgents != 2 {
		t.Errorf("got active=%d, want 2", dash.ActiveAgents)
	}
	if dash.FailedAgents != 1 {
		t.Errorf("got failed=%d, want 1", dash.FailedAgents)
	}
	if dash.RunningTasks != 1 {
		t.Errorf("got running tasks=%d, want 1", dash.RunningTasks)
	}
	if len(dash.TopAgents) != 4 {
		t.Errorf("got %d top agents, want 4 (below the cap of 5)", len(dash.TopAgents))
	}
}

func TestService_DashboardTopAgentsCappedAndRanked(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	// 7 个 Agent：a0 最健康（无告警），a6 状态 error 分数最低。
	for i := 0; i < 6; i++ {
		st := &AgentStatus{AgentID: string(rune('a' + i)), State: "running", LastActive: time.Now().UTC()}
		if err := svc.ReportStatus(ctx, st); err != nil {
			t.Fatalf("report: %v", err)
		}
	}
	if err := svc.ReportStatus(ctx, &AgentStatus{AgentID: "z-bad", State: "error"}); err != nil {
		t.Fatalf("report: %v", err)
	}

	dash, err := svc.GetDashboard(ctx)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if len(dash.TopAgents) != 5 {
		t.Fatalf("got %d top agents, want 5 (capped)", len(dash.TopAgents))
	}
	if dash.TopAgents[len(dash.TopAgents)-1].AgentID == "z-bad" {
		t.Error("unhealthy agent ranked inside the top list")
	}
}

func TestService_DashboardIncludesUnresolvedAlerts(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	if _, err := svc.CreateAlert(ctx, AlertRule{AgentID: "a1", Type: AlertCPUHigh, Threshold: 80}); err != nil {
		t.Fatalf("create rule: %v", err)
	}
	if err := svc.ReportStatus(ctx, &AgentStatus{AgentID: "a1", State: "running",
		LastActive: time.Now().UTC(), Resources: ResourceUsage{CPU: 99}}); err != nil {
		t.Fatalf("report: %v", err)
	}

	dash, err := svc.GetDashboard(ctx)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if len(dash.Alerts) != 1 {
		t.Errorf("got %d dashboard alerts, want 1", len(dash.Alerts))
	}
}

// ---------- HTTP handler ----------

func newTestServer(t *testing.T) (*httptest.Server, Service) {
	t.Helper()
	svc := newTestService()
	mux := http.NewServeMux()
	NewHandler(svc).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, svc
}

func doJSON(t *testing.T, method, url, body string) (int, map[string]interface{}) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()

	out := map[string]interface{}{}
	if resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode %s %s: %v", method, url, err)
		}
	}
	return resp.StatusCode, out
}

func TestHandler_ReportAndGetStatus(t *testing.T) {
	srv, _ := newTestServer(t)

	code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/agents",
		`{"agent_id":"a1","state":"running","progress":30}`)
	if code != http.StatusCreated {
		t.Fatalf("post status: got %d, want 201", code)
	}

	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/agents/a1", "")
	if code != http.StatusOK {
		t.Fatalf("get status: got %d, want 200", code)
	}
	if body["state"] != "running" {
		t.Errorf("got state %v, want running", body["state"])
	}

	code, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/agents", "")
	if code != http.StatusOK {
		t.Fatalf("list: got %d, want 200", code)
	}
	if body["total"].(float64) != 1 {
		t.Errorf("got total %v, want 1", body["total"])
	}
}

func TestHandler_ReportStatusRejectsBadInput(t *testing.T) {
	srv, _ := newTestServer(t)

	code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/agents", `{"state":"running"}`)
	if code != http.StatusBadRequest {
		t.Errorf("missing agent_id: got %d, want 400", code)
	}

	code, _ = doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/agents", `not-json`)
	if code != http.StatusBadRequest {
		t.Errorf("malformed json: got %d, want 400", code)
	}

	code, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/agents/ghost", "")
	if code != http.StatusNotFound {
		t.Errorf("unknown agent: got %d, want 404", code)
	}
}

func TestHandler_HealthEndpoint(t *testing.T) {
	srv, svc := newTestServer(t)
	if err := svc.ReportStatus(context.Background(), &AgentStatus{AgentID: "a1", State: "running"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/agents/a1/health", "")
	if code != http.StatusOK {
		t.Fatalf("health: got %d, want 200", code)
	}
	if body["healthy"] != true {
		t.Errorf("got healthy=%v, want true", body["healthy"])
	}
	if body["health_score"].(float64) != 100 {
		t.Errorf("got score %v, want 100", body["health_score"])
	}
}

func TestHandler_MetricsRoundTrip(t *testing.T) {
	srv, _ := newTestServer(t)

	code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/agents/a1/metrics",
		`{"cpu":42.5,"memory":60,"disk":30}`)
	if code != http.StatusCreated {
		t.Fatalf("post metrics: got %d, want 201", code)
	}

	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/metrics/a1?duration=1h", "")
	if code != http.StatusOK {
		t.Fatalf("get metrics: got %d, want 200", code)
	}
	if body["total"].(float64) != 1 {
		t.Fatalf("got total %v, want 1", body["total"])
	}

	code, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/metrics/a1?duration=1h&aggregate=1", "")
	if code != http.StatusOK {
		t.Fatalf("aggregate: got %d, want 200", code)
	}
	if body["cpu"].(float64) != 42.5 {
		t.Errorf("got aggregated cpu %v, want 42.5", body["cpu"])
	}
}

func TestHandler_MetricsRejectsBadWindow(t *testing.T) {
	srv, _ := newTestServer(t)
	code, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/metrics/a1?start=nonsense", "")
	if code != http.StatusBadRequest {
		t.Errorf("bad start: got %d, want 400", code)
	}
	code, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/metrics/a1?duration=abc", "")
	if code != http.StatusBadRequest {
		t.Errorf("bad duration: got %d, want 400", code)
	}
}

func TestHandler_AlertRuleAndResolution(t *testing.T) {
	srv, svc := newTestServer(t)
	ctx := context.Background()

	code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/alerts",
		`{"agent_id":"a1","type":"cpu_high","threshold":80}`)
	if code != http.StatusCreated {
		t.Fatalf("create rule: got %d, want 201", code)
	}
	if body["rule"] == nil {
		t.Fatal("response missing rule")
	}

	// 规则创建后上报越界状态 -> 产生告警。
	if err := svc.ReportStatus(ctx, &AgentStatus{AgentID: "a1", State: "running",
		LastActive: time.Now().UTC(), Resources: ResourceUsage{CPU: 99}}); err != nil {
		t.Fatalf("report: %v", err)
	}

	code, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/alerts?agent_id=a1", "")
	if code != http.StatusOK {
		t.Fatalf("list alerts: got %d, want 200", code)
	}
	if body["total"].(float64) != 1 {
		t.Fatalf("got %v alerts, want 1", body["total"])
	}
	alerts := body["alerts"].([]interface{})
	alertID := alerts[0].(map[string]interface{})["id"].(string)

	code, _ = doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/alerts/"+alertID+"/resolve", "")
	if code != http.StatusOK {
		t.Fatalf("resolve: got %d, want 200", code)
	}

	code, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/alerts?agent_id=a1", "")
	if body["total"].(float64) != 0 {
		t.Errorf("got %v unresolved after resolve, want 0", body["total"])
	}

	// rules=1 列出规则，DELETE 删除。
	code, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/alerts?agent_id=a1&rules=1", "")
	if code != http.StatusOK || body["total"].(float64) != 1 {
		t.Fatalf("list rules: code=%d body=%v", code, body)
	}
	ruleID := body["rules"].([]interface{})[0].(map[string]interface{})["id"].(string)

	code, _ = doJSON(t, http.MethodDelete, srv.URL+"/api/v1/monitor/alerts/"+ruleID, "")
	if code != http.StatusOK {
		t.Fatalf("delete rule: got %d, want 200", code)
	}
	code, _ = doJSON(t, http.MethodDelete, srv.URL+"/api/v1/monitor/alerts/"+ruleID, "")
	if code != http.StatusNotFound {
		t.Errorf("re-delete: got %d, want 404", code)
	}
}

func TestHandler_CreateRuleValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	code, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/alerts", `{"type":"cpu_high"}`)
	if code != http.StatusBadRequest {
		t.Errorf("missing agent_id: got %d, want 400", code)
	}
	code, _ = doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/alerts", `{"agent_id":"a1"}`)
	if code != http.StatusBadRequest {
		t.Errorf("missing type: got %d, want 400", code)
	}
}

func TestHandler_Dashboard(t *testing.T) {
	srv, svc := newTestServer(t)
	ctx := context.Background()
	for _, st := range []*AgentStatus{
		{AgentID: "a1", State: "running"},
		{AgentID: "a2", State: "error"},
	} {
		if err := svc.ReportStatus(ctx, st); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/dashboard", "")
	if code != http.StatusOK {
		t.Fatalf("dashboard: got %d, want 200", code)
	}
	if body["total_agents"].(float64) != 2 {
		t.Errorf("got total_agents %v, want 2", body["total_agents"])
	}
	if body["failed_agents"].(float64) != 1 {
		t.Errorf("got failed_agents %v, want 1", body["failed_agents"])
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer(t)
	code, _ := doJSON(t, http.MethodDelete, srv.URL+"/api/v1/monitor/dashboard", "")
	if code != http.StatusMethodNotAllowed {
		t.Errorf("got %d, want 405", code)
	}
	code, _ = doJSON(t, http.MethodDelete, srv.URL+"/api/v1/monitor/agents", "")
	if code != http.StatusMethodNotAllowed {
		t.Errorf("got %d, want 405", code)
	}
}

func TestRepository_ConcurrentAccess(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	done := make(chan struct{})

	// 并发读写验证无数据竞争（配合 -race 运行）。
	for i := 0; i < 8; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			id := string(rune('a' + n%4))
			for j := 0; j < 50; j++ {
				_ = repo.SaveStatus(ctx, &AgentStatus{AgentID: id, State: "running"})
				_ = repo.AppendSample(ctx, id, &ResourceUsage{CPU: float64(j), Timestamp: time.Now().UTC()})
				_, _ = repo.ListStatuses(ctx)
				_, _ = repo.QuerySamples(ctx, id, time.Now().Add(-time.Hour), time.Now())
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}

	list, _ := repo.ListStatuses(ctx)
	if len(list) != 4 {
		t.Errorf("got %d statuses, want 4", len(list))
	}
}
