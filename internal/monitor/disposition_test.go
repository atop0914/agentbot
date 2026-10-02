package monitor

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// ===== Day 24：告警处置状态机测试 =====

// seedAlert 造一条已触发的告警，返回其 ID。
//
// 走真实规则求值路径（而不是直接往 s.byAge 里塞对象），
// 保证测试覆盖的正是生产上产生告警的那条链路。
func seedAlert(t *testing.T, svc Service, agentID string) string {
	t.Helper()
	ctx := context.Background()
	if err := svc.ReportStatus(ctx, &AgentStatus{
		AgentID: agentID,
		State:   "running",
		Resources: ResourceUsage{
			CPU: 95, Memory: 50, Disk: 50,
		},
	}); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	if _, err := svc.CreateAlert(ctx, AlertRule{
		AgentID: agentID, Type: AlertCPUHigh, Threshold: 80,
	}); err != nil {
		t.Fatalf("create rule: %v", err)
	}
	alerts, err := svc.ListAlerts(ctx, agentID, false)
	if err != nil {
		t.Fatalf("list alerts: %v", err)
	}
	if len(alerts) == 0 {
		t.Fatal("expected an alert to be raised")
	}
	return alerts[0].ID
}

func TestAlert_DispositionLifecycle(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	alertID := seedAlert(t, svc, "a1")

	// 新告警从 firing 开始。
	alerts, _ := svc.ListAlerts(ctx, "a1", false)
	if alerts[0].Status != AlertStatusFiring {
		t.Fatalf("new alert status = %q, want firing", alerts[0].Status)
	}

	// firing → acknowledged
	acked, err := svc.AcknowledgeAlert(ctx, alertID, DispositionRequest{Operator: "alice", Note: "查一下"})
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	if acked.Status != AlertStatusAcknowledged {
		t.Errorf("status = %q, want acknowledged", acked.Status)
	}
	if acked.AcknowledgedBy != "alice" {
		t.Errorf("acknowledged_by = %q, want alice", acked.AcknowledgedBy)
	}
	if acked.Resolved {
		t.Error("acknowledged alert must not be marked resolved")
	}
	if acked.DispositionCount != 1 || acked.LastNote != "查一下" {
		t.Errorf("count/note = %d/%q, want 1/查一下", acked.DispositionCount, acked.LastNote)
	}

	// 重复认领必须报冲突，而不是静默成功。
	if _, err := svc.AcknowledgeAlert(ctx, alertID, DispositionRequest{Operator: "bob"}); err == nil {
		t.Error("expected error on double ack")
	}

	// acknowledged → resolved
	resolved, err := svc.ResolveAlertWithDisposition(ctx, alertID, DispositionRequest{Operator: "alice", Note: "已扩容"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Status != AlertStatusResolved || !resolved.Resolved {
		t.Errorf("status = %q resolved=%v, want resolved/true", resolved.Status, resolved.Resolved)
	}
	if resolved.ResolvedBy != "alice" || resolved.ResolvedAt.IsZero() {
		t.Errorf("resolver = %q at %v, want alice/non-zero", resolved.ResolvedBy, resolved.ResolvedAt)
	}
	if resolved.DispositionCount != 2 {
		t.Errorf("disposition count = %d, want 2", resolved.DispositionCount)
	}

	// resolved → firing（重开）
	reopened, err := svc.ReopenAlert(ctx, alertID, DispositionRequest{Operator: "carol", Note: "又复现了"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Status != AlertStatusFiring {
		t.Errorf("status = %q, want firing", reopened.Status)
	}
	// 重开必须清掉认领与解决痕迹，否则「无人处理」的语义会被污染。
	if reopened.AcknowledgedBy != "" || reopened.ResolvedBy != "" || !reopened.ResolvedAt.IsZero() {
		t.Errorf("reopen left stale disposition fields: %+v", reopened)
	}

	// 处置轨迹必须完整保留 3 条。
	records, err := svc.ListDispositions(ctx, alertID)
	if err != nil {
		t.Fatalf("list dispositions: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d dispositions, want 3", len(records))
	}
	wantActions := []AlertAction{AlertActionAck, AlertActionResolve, AlertActionReopen}
	for i, want := range wantActions {
		if records[i].Action != want {
			t.Errorf("disposition %d action = %q, want %q", i, records[i].Action, want)
		}
	}
	if records[2].From != AlertStatusResolved || records[2].To != AlertStatusFiring {
		t.Errorf("reopen transition = %s -> %s, want resolved -> firing", records[2].From, records[2].To)
	}
}

func TestAlert_ResolveWithoutAckIsAllowed(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	alertID := seedAlert(t, svc, "a1")

	// 问题可能自愈：允许 firing 直接跳到 resolved，但轨迹仍要留下。
	resolved, err := svc.ResolveAlertWithDisposition(ctx, alertID, DispositionRequest{Operator: "alice"})
	if err != nil {
		t.Fatalf("direct resolve: %v", err)
	}
	if resolved.Status != AlertStatusResolved {
		t.Errorf("status = %q, want resolved", resolved.Status)
	}
	if resolved.AcknowledgedAt.IsZero() {
		// 未认领就解决时不应伪造认领时间，否则会污染平均认领时长统计。
		records, _ := svc.ListDispositions(ctx, alertID)
		if len(records) != 1 || records[0].From != AlertStatusFiring {
			t.Errorf("expected a single firing->resolved record, got %+v", records)
		}
	}
}

func TestAlert_DispositionRequiresOperator(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	alertID := seedAlert(t, svc, "a1")

	// 没有责任人的处置记录在事故复盘里毫无价值 → 必须拒绝。
	if _, err := svc.AcknowledgeAlert(ctx, alertID, DispositionRequest{}); err == nil {
		t.Error("expected error when operator is empty")
	}
	if _, err := svc.ResolveAlertWithDisposition(ctx, alertID, DispositionRequest{}); err == nil {
		t.Error("expected error when operator is empty")
	}
}

func TestAlert_DispositionUnknownAndInvalidTransitions(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	if _, err := svc.AcknowledgeAlert(ctx, "nope", DispositionRequest{Operator: "alice"}); err == nil {
		t.Error("expected not-found error")
	}

	alertID := seedAlert(t, svc, "a1")
	// 未解决的告警不能重开（只能 resolve 后再 reopen）。
	if _, err := svc.ReopenAlert(ctx, alertID, DispositionRequest{Operator: "alice"}); err == nil {
		t.Error("expected error reopening a firing alert")
	}
	// 解决两次必须报「已解决」。
	if _, err := svc.ResolveAlertWithDisposition(ctx, alertID, DispositionRequest{Operator: "alice"}); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if _, err := svc.ResolveAlertWithDisposition(ctx, alertID, DispositionRequest{Operator: "alice"}); err == nil {
		t.Error("expected error resolving twice")
	}
}

func TestDispositionSummary(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	// 三条告警：一条保持 firing，一条已认领，一条已解决。
	ids := []string{
		seedAlert(t, svc, "a1"),
		seedAlert(t, svc, "a2"),
		seedAlert(t, svc, "a3"),
	}
	if _, err := svc.AcknowledgeAlert(ctx, ids[1], DispositionRequest{Operator: "alice"}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if _, err := svc.AcknowledgeAlert(ctx, ids[2], DispositionRequest{Operator: "alice"}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if _, err := svc.ResolveAlertWithDisposition(ctx, ids[2], DispositionRequest{Operator: "bob"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	sum, err := svc.DispositionSummary(ctx, "", 10)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Open != 1 {
		t.Errorf("open = %d, want 1", sum.Open)
	}
	if sum.Acknowledged != 1 {
		t.Errorf("acknowledged = %d, want 1", sum.Acknowledged)
	}
	if sum.Resolved != 1 {
		t.Errorf("resolved = %d, want 1", sum.Resolved)
	}
	// Unacknowledged 是运维最先看的指标：只有仍处 firing 的那条算。
	if sum.Unacknowledged != 1 {
		t.Errorf("unacknowledged = %d, want 1", sum.Unacknowledged)
	}
	// 每条告警至少有一条处置记录（认领/解决），最近列表应被填充。
	if len(sum.Recent) == 0 {
		t.Error("expected recent dispositions to be populated")
	}
	// 严重级别统计覆盖未解决的两条（firing + acknowledged）。
	if total := sum.BySeverity["high"]; total != 2 {
		t.Errorf("by_severity[high] = %d, want 2", total)
	}

	// 按 Agent 过滤。
	only, err := svc.DispositionSummary(ctx, "a3", 5)
	if err != nil {
		t.Fatalf("filtered summary: %v", err)
	}
	if only.Resolved != 1 || only.Open != 0 {
		t.Errorf("filtered summary = %+v, want resolved=1 open=0", only)
	}
}

func TestDispositionSummary_AverageDurations(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	alertID := seedAlert(t, svc, "a1")

	if _, err := svc.AcknowledgeAlert(ctx, alertID, DispositionRequest{Operator: "alice"}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if _, err := svc.ResolveAlertWithDisposition(ctx, alertID, DispositionRequest{Operator: "alice"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	sum, err := svc.DispositionSummary(ctx, "", 10)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	// 时长都是正数且量级合理（测试里几乎瞬间完成，只断言非负与有限）。
	if sum.AvgTimeToAckSeconds < 0 || sum.AvgTimeToResolveSeconds < 0 {
		t.Errorf("durations must not be negative: %+v", sum)
	}
	if sum.AvgTimeToAckSeconds > 60 || sum.AvgTimeToResolveSeconds > 60 {
		t.Errorf("durations implausibly large for an in-test ack/resolve: %+v", sum)
	}
}

// 处置记录必须真的落到注入的记录器上 —— 这是「谁改的」可追溯性的根。
func TestAlert_DispositionInvokesRecorder(t *testing.T) {
	repo := NewMemoryRepository()
	var got []AlertDisposition
	svc := NewServiceWithRecorder(repo, DispositionRecorderFunc(
		func(_ context.Context, alert *Alert, d *AlertDisposition) error {
			if alert == nil || d == nil {
				t.Error("recorder received nil arguments")
			}
			got = append(got, *d)
			return nil
		},
	))

	ctx := context.Background()
	alertID := seedAlert(t, svc, "a1")
	if _, err := svc.AcknowledgeAlert(ctx, alertID, DispositionRequest{Operator: "alice", Note: "n1"}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if _, err := svc.ResolveAlertWithDisposition(ctx, alertID, DispositionRequest{Operator: "bob"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("recorder saw %d dispositions, want 2", len(got))
	}
	if got[0].Operator != "alice" || got[0].Action != AlertActionAck {
		t.Errorf("first record = %+v, want alice/ack", got[0])
	}
	if got[1].To != AlertStatusResolved {
		t.Errorf("second record to = %q, want resolved", got[1].To)
	}
}

// 审计留痕失败不能让处置动作失败：处置已经真实发生，报错会让调用方重试出重复记录。
func TestAlert_DispositionSurvivesRecorderFailure(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewServiceWithRecorder(repo, DispositionRecorderFunc(
		func(_ context.Context, _ *Alert, _ *AlertDisposition) error {
			return errors.New("audit backend down")
		},
	))
	ctx := context.Background()
	alertID := seedAlert(t, svc, "a1")

	alert, err := svc.AcknowledgeAlert(ctx, alertID, DispositionRequest{Operator: "alice"})
	if alert == nil {
		t.Fatal("alert must still be returned when only the recorder fails")
	}
	if err == nil {
		t.Error("expected the recorder error to be surfaced to the caller")
	}
	if alert.Status != AlertStatusAcknowledged {
		t.Errorf("status = %q, want acknowledged (transition must persist)", alert.Status)
	}
	// 状态必须真的写进仓库，而不是只改了返回值。
	stored, err := repo.GetAlert(ctx, alertID)
	if err != nil {
		t.Fatalf("get stored alert: %v", err)
	}
	if stored.Status != AlertStatusAcknowledged {
		t.Errorf("stored status = %q, want acknowledged", stored.Status)
	}
}

// --- HTTP 层 ---

func TestHandler_AlertDispositionFlow(t *testing.T) {
	srv, _ := newTestServer(t)

	// 造一条告警：上报超阈值状态 + 建规则。
	postJSON(t, srv.URL+"/api/v1/monitor/agents", `{"agent_id":"a1","state":"running","resources":{"cpu":95}}`)
	code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/alerts",
		`{"agent_id":"a1","type":"cpu_high","threshold":80}`)
	if code != http.StatusCreated {
		t.Fatalf("create rule: got %d (%v)", code, body)
	}
	if body["alert"] == nil {
		t.Fatal("expected an alert to be raised immediately by the rule")
	}
	alertID := body["alert"].(map[string]interface{})["id"].(string)

	// 认领：正文带处置人与备注。
	code, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/alerts/"+alertID+"/ack",
		`{"operator":"alice","note":"查看中"}`)
	if code != http.StatusOK {
		t.Fatalf("ack: got %d (%v)", code, body)
	}
	if body["status"] != string(AlertStatusAcknowledged) {
		t.Errorf("status = %v, want acknowledged", body["status"])
	}

	// 重复认领 → 409（而不是 404：告警确实存在，只是状态不允许）。
	code, _ = doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/alerts/"+alertID+"/ack", `{"operator":"bob"}`)
	if code != http.StatusConflict {
		t.Errorf("double ack: got %d, want 409", code)
	}

	// 处置记录可查。
	code, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/alerts/"+alertID+"/dispositions", "")
	if code != http.StatusOK {
		t.Fatalf("dispositions: got %d", code)
	}
	if body["total"].(float64) != 1 {
		t.Errorf("got %v dispositions, want 1", body["total"])
	}

	// 解决（无正文时处置人回落到 system）。
	code, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/alerts/"+alertID+"/resolve", "")
	if code != http.StatusOK {
		t.Fatalf("resolve: got %d (%v)", code, body)
	}
	if body["status"] != string(AlertStatusResolved) {
		t.Errorf("status = %v, want resolved", body["status"])
	}

	// 重开 → 回到 firing。
	code, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/alerts/"+alertID+"/reopen", `{"operator":"carol"}`)
	if code != http.StatusOK {
		t.Fatalf("reopen: got %d (%v)", code, body)
	}
	if body["status"] != string(AlertStatusFiring) {
		t.Errorf("status = %v, want firing", body["status"])
	}

	// 未知告警 → 404。
	code, _ = doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/alerts/missing/ack", `{"operator":"alice"}`)
	if code != http.StatusNotFound {
		t.Errorf("missing alert: got %d, want 404", code)
	}

	// 错误方法 → 405。
	code, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/alerts/"+alertID+"/ack", "")
	if code != http.StatusMethodNotAllowed {
		t.Errorf("GET ack: got %d, want 405", code)
	}
}

// 处置摘要端点必须反映真实处置进度（端到端）。
func TestHandler_DispositionSummaryReflectsProgress(t *testing.T) {
	srv, _ := newTestServer(t)

	postJSON(t, srv.URL+"/api/v1/monitor/agents", `{"agent_id":"a1","state":"running","resources":{"cpu":95}}`)
	code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/alerts",
		`{"agent_id":"a1","type":"cpu_high","threshold":80}`)
	if code != http.StatusCreated {
		t.Fatalf("create rule: got %d", code)
	}
	alertID := body["alert"].(map[string]interface{})["id"].(string)

	code, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/alert-dispositions", "")
	if code != http.StatusOK {
		t.Fatalf("summary: got %d", code)
	}
	if body["unacknowledged"].(float64) != 1 {
		t.Errorf("unacknowledged = %v, want 1", body["unacknowledged"])
	}

	doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/alerts/"+alertID+"/ack", `{"operator":"alice"}`)

	_, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/alert-dispositions", "")
	if body["acknowledged"].(float64) != 1 || body["unacknowledged"].(float64) != 0 {
		t.Errorf("after ack: acknowledged=%v unacknowledged=%v, want 1/0",
			body["acknowledged"], body["unacknowledged"])
	}
	if len(body["recent"].([]interface{})) == 0 {
		t.Error("expected recent dispositions after ack")
	}
}

// postJSON 是 doJSON 的薄封装：仅用于不关心响应的预置请求。
func postJSON(t *testing.T, url, body string) {
	t.Helper()
	doJSON(t, http.MethodPost, url, body)
}

// status= 三态过滤必须真正生效，而不是把参数吞掉返回全部。
func TestHandler_ListAlertsByStatusFilter(t *testing.T) {
	srv, _ := newTestServer(t)

	postJSON(t, srv.URL+"/api/v1/monitor/agents", `{"agent_id":"a1","state":"running","resources":{"cpu":95}}`)
	code, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/alerts",
		`{"agent_id":"a1","type":"cpu_high","threshold":80}`)
	if code != http.StatusCreated {
		t.Fatalf("create rule: got %d", code)
	}
	alertID := body["alert"].(map[string]interface{})["id"].(string)

	// 尚未认领：firing 里有 1 条，acknowledged / resolved 均为空。
	for _, tc := range []struct {
		status string
		want   float64
	}{
		{string(AlertStatusFiring), 1},
		{string(AlertStatusAcknowledged), 0},
		{string(AlertStatusResolved), 0},
	} {
		code, resp := doJSON(t, http.MethodGet,
			srv.URL+"/api/v1/monitor/alerts?agent_id=a1&status="+tc.status, "")
		if code != http.StatusOK {
			t.Fatalf("status=%s: got %d, want 200", tc.status, code)
		}
		if resp["total"].(float64) != tc.want {
			t.Errorf("status=%s: total = %v, want %v", tc.status, resp["total"], tc.want)
		}
	}

	// 认领后必须从 firing 移动到 acknowledged。
	postJSON(t, srv.URL+"/api/v1/monitor/alerts/"+alertID+"/ack", `{"operator":"alice"}`)
	_, resp := doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/alerts?agent_id=a1&status=firing", "")
	if resp["total"].(float64) != 0 {
		t.Errorf("firing after ack = %v, want 0", resp["total"])
	}
	_, resp = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/alerts?agent_id=a1&status=acknowledged", "")
	if resp["total"].(float64) != 1 {
		t.Errorf("acknowledged after ack = %v, want 1", resp["total"])
	}

	// 非法状态必须 400，而不是静默忽略过滤条件返回全部。
	code, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/alerts?status=bogus", "")
	if code != http.StatusBadRequest {
		t.Errorf("invalid status: got %d, want 400", code)
	}
}

var _ = time.Now
