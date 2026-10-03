package audit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestService(t *testing.T) Service {
	t.Helper()
	return NewService(NewMemoryRepository())
}

// newTestServerWithMux 用调用方自备的 mux 起测试服务器（用于需要挂留存服务的场景）。
func newTestServerWithMux(t *testing.T, mux *http.ServeMux) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func mustLog(t *testing.T, svc Service, ev Event) *EventRecord {
	t.Helper()
	rec, err := svc.LogEvent(context.Background(), ev)
	if err != nil {
		t.Fatalf("LogEvent(%+v) error: %v", ev, err)
	}
	return rec
}

func TestLogEvent_AssignsIDAndTimestamp(t *testing.T) {
	svc := newTestService(t)

	rec := mustLog(t, svc, Event{Action: "user.login", Actor: "u1"})

	if rec.ID == "" {
		t.Error("expected non-empty event ID")
	}
	if rec.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
	if rec.Status != StatusSuccess {
		t.Errorf("status = %q, want %q", rec.Status, StatusSuccess)
	}
	if rec.ActorType != ActorTypeUser {
		t.Errorf("actor_type = %q, want %q", rec.ActorType, ActorTypeUser)
	}
	if rec.Seq == 0 {
		t.Error("expected non-zero sequence number")
	}
}

func TestLogEvent_RequiresAction(t *testing.T) {
	svc := newTestService(t)

	_, err := svc.LogEvent(context.Background(), Event{Actor: "u1"})
	if !errors.Is(err, ErrActionRequired) {
		t.Fatalf("err = %v, want ErrActionRequired", err)
	}

	// 纯空白也算空
	_, err = svc.LogEvent(context.Background(), Event{Action: "   "})
	if !errors.Is(err, ErrActionRequired) {
		t.Fatalf("err = %v, want ErrActionRequired for whitespace action", err)
	}
}

func TestLog_PersistsEvent(t *testing.T) {
	svc := newTestService(t)

	if err := svc.Log(context.Background(), Event{Action: "agent.created", Actor: "a1"}); err != nil {
		t.Fatalf("Log error: %v", err)
	}

	n, err := svc.Count(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("Count error: %v", err)
	}
	if n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
}

func TestQuery_FiltersByActorAndStatus(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	mustLog(t, svc, Event{Action: "agent.created", Actor: "a1", ActorType: ActorTypeAgent})
	mustLog(t, svc, Event{Action: "agent.failed", Actor: "a1", ActorType: ActorTypeAgent, Status: StatusFailure})
	mustLog(t, svc, Event{Action: "user.login", Actor: "u1"})

	cases := []struct {
		name   string
		filter Filter
		want   int
	}{
		{"by actor", Filter{Actor: "a1"}, 2},
		{"by actor and status", Filter{Actor: "a1", Status: StatusSuccess}, 1},
		{"by actor type", Filter{ActorType: ActorTypeAgent}, 2},
		{"by event type", Filter{EventType: "user.login"}, 1},
		{"no match", Filter{Actor: "nobody"}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.Query(ctx, tc.filter)
			if err != nil {
				t.Fatalf("Query error: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("len = %d, want %d", len(got), tc.want)
			}
		})
	}
}

func TestQuery_FiltersByTimeRange(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{0, time.Hour, 2 * time.Hour} {
		if _, err := repo.Append(ctx, Event{
			Action:    "tick",
			Timestamp: base.Add(offset),
			Status:    StatusSuccess,
		}); err != nil {
			t.Fatalf("Append error: %v", err)
		}
	}

	start := base.Add(30 * time.Minute)
	end := base.Add(90 * time.Minute)
	got, err := svc.Query(ctx, Filter{StartTime: &start, EndTime: &end})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if !got[0].Timestamp.Equal(base.Add(time.Hour)) {
		t.Errorf("timestamp = %v, want %v", got[0].Timestamp, base.Add(time.Hour))
	}
}

func TestQuery_SortOrderStable(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	// 同一毫秒内写入多条，验证 Seq 兜底排序而不依赖时间精度。
	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	for _, action := range []string{"a", "b", "c"} {
		if _, err := repo.Append(ctx, Event{Action: action, Timestamp: ts}); err != nil {
			t.Fatalf("Append error: %v", err)
		}
	}

	desc, err := svc.Query(ctx, Filter{SortOrder: SortDesc})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(desc) != 3 {
		t.Fatalf("len = %d, want 3", len(desc))
	}
	// 默认按写入顺序倒序：c, b, a
	if desc[0].Action != "c" || desc[1].Action != "b" || desc[2].Action != "a" {
		t.Errorf("desc order = %s,%s,%s want c,b,a", desc[0].Action, desc[1].Action, desc[2].Action)
	}

	asc, err := svc.Query(ctx, Filter{SortOrder: SortAsc})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if asc[0].Action != "a" || asc[2].Action != "c" {
		t.Errorf("asc order = %s,%s,%s want a,b,c", asc[0].Action, asc[1].Action, asc[2].Action)
	}
}

func TestQuery_SortByAction(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	mustLog(t, svc, Event{Action: "user.logout"})
	mustLog(t, svc, Event{Action: "agent.created"})
	mustLog(t, svc, Event{Action: "task.completed"})

	got, err := svc.Query(ctx, Filter{SortBy: SortByAction, SortOrder: SortAsc})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	want := []string{"agent.created", "task.completed", "user.logout"}
	for i, w := range want {
		if got[i].Action != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i].Action, w)
		}
	}
}

func TestQuery_Pagination(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	repo := NewMemoryRepository()
	svc = NewService(repo)
	for i := 0; i < 10; i++ {
		if _, err := repo.Append(ctx, Event{
			Action:    "tick",
			Timestamp: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("Append error: %v", err)
		}
	}

	page1, err := svc.Query(ctx, Filter{SortOrder: SortAsc, Limit: 4, Offset: 0})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(page1) != 4 {
		t.Fatalf("page1 len = %d, want 4", len(page1))
	}
	if !page1[0].Timestamp.Equal(base) {
		t.Errorf("page1[0] = %v, want %v", page1[0].Timestamp, base)
	}

	page2, err := svc.Query(ctx, Filter{SortOrder: SortAsc, Limit: 4, Offset: 4})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(page2) != 4 {
		t.Fatalf("page2 len = %d, want 4", len(page2))
	}
	if !page2[0].Timestamp.Equal(base.Add(4 * time.Second)) {
		t.Errorf("page2[0] = %v, want %v", page2[0].Timestamp, base.Add(4*time.Second))
	}

	// 越界 offset 返回空切片而不是 nil，保证 JSON 输出为 []
	empty, err := svc.Query(ctx, Filter{Offset: 100})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if empty == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(empty) != 0 {
		t.Fatalf("len = %d, want 0", len(empty))
	}
}

func TestQuery_ClampsLimit(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	mustLog(t, svc, Event{Action: "tick"})

	// 超出 maxLimit 时被裁剪，不应报错
	got, err := svc.Query(ctx, Filter{Limit: 99999})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
}

func TestCount_IgnoresPagination(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	for i := 0; i < 7; i++ {
		mustLog(t, svc, Event{Action: "tick", Actor: "a1"})
	}

	n, err := svc.Count(ctx, Filter{Actor: "a1", Limit: 2, Offset: 3})
	if err != nil {
		t.Fatalf("Count error: %v", err)
	}
	if n != 7 {
		t.Fatalf("count = %d, want 7 (pagination must not affect count)", n)
	}
}

func TestGetEvent(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	rec := mustLog(t, svc, Event{Action: "agent.created", Actor: "a1"})

	got, err := svc.GetEvent(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetEvent error: %v", err)
	}
	if got.ID != rec.ID || got.Action != rec.Action {
		t.Errorf("got %+v, want %+v", got, rec)
	}

	if _, err := svc.GetEvent(ctx, "missing"); !errors.Is(err, ErrEventNotFound) {
		t.Errorf("err = %v, want ErrEventNotFound", err)
	}
	if _, err := svc.GetEvent(ctx, ""); !errors.Is(err, ErrIDRequired) {
		t.Errorf("err = %v, want ErrIDRequired", err)
	}
}

func TestStoreCopiesDetails(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	details := map[string]interface{}{"key": "original"}
	rec := mustLog(t, svc, Event{Action: "x", Details: details})

	// 调用方后续修改不应污染已存数据
	details["key"] = "mutated"

	got, err := svc.GetEvent(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetEvent error: %v", err)
	}
	if got.Details["key"] != "original" {
		t.Errorf("details[key] = %v, want original", got.Details["key"])
	}
}

func TestStats(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	mustLog(t, svc, Event{Action: "agent.created", Actor: "a1", ActorType: ActorTypeAgent})
	mustLog(t, svc, Event{Action: "agent.created", Actor: "a2", ActorType: ActorTypeAgent})
	mustLog(t, svc, Event{Action: "user.login", Actor: "u1"})

	stats, err := svc.Stats(ctx, Filter{}, DistinctAction)
	if err != nil {
		t.Fatalf("Stats error: %v", err)
	}
	if stats["agent.created"] != 2 {
		t.Errorf("agent.created = %d, want 2", stats["agent.created"])
	}
	if stats["user.login"] != 1 {
		t.Errorf("user.login = %d, want 1", stats["user.login"])
	}

	actorStats, err := svc.Stats(ctx, Filter{}, DistinctActor)
	if err != nil {
		t.Fatalf("Stats error: %v", err)
	}
	if len(actorStats) != 3 {
		t.Errorf("actor buckets = %d, want 3", len(actorStats))
	}

	if _, err := svc.Stats(ctx, Filter{}, "nope"); !errors.Is(err, ErrUnknownDistinctField) {
		t.Errorf("err = %v, want ErrUnknownDistinctField", err)
	}
}

func TestStats_UnknownBucketForEmptyField(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// ip_address 为空的事件归入 unknown，保证聚合不丢数据
	mustLog(t, svc, Event{Action: "tick"})

	stats, err := svc.Stats(ctx, Filter{}, DistinctIPAddress)
	if err != nil {
		t.Fatalf("Stats error: %v", err)
	}
	if stats["unknown"] != 1 {
		t.Errorf("unknown = %d, want 1", stats["unknown"])
	}
}

func TestDistinct(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	mustLog(t, svc, Event{Action: "agent.created", Actor: "a2", ActorType: ActorTypeAgent})
	mustLog(t, svc, Event{Action: "user.login", Actor: "a1"})
	mustLog(t, svc, Event{Action: "user.login", Actor: "a1"})

	values, err := svc.Distinct(ctx, DistinctAction)
	if err != nil {
		t.Fatalf("Distinct error: %v", err)
	}
	want := []string{"agent.created", "user.login"}
	if len(values) != len(want) {
		t.Fatalf("values = %v, want %v", values, want)
	}
	for i := range want {
		if values[i] != want[i] {
			t.Errorf("values[%d] = %q, want %q", i, values[i], want[i])
		}
	}

	actors, err := svc.Distinct(ctx, DistinctActor)
	if err != nil {
		t.Fatalf("Distinct error: %v", err)
	}
	if len(actors) != 2 || actors[0] != "a1" || actors[1] != "a2" {
		t.Errorf("actors = %v, want [a1 a2] (sorted)", actors)
	}

	if _, err := svc.Distinct(ctx, "bogus"); !errors.Is(err, ErrUnknownDistinctField) {
		t.Errorf("err = %v, want ErrUnknownDistinctField", err)
	}
}

func TestPurge(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if _, err := repo.Append(ctx, Event{
			Action:    "tick",
			Timestamp: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("Append error: %v", err)
		}
	}

	removed, err := svc.Purge(ctx, base.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("Purge error: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}

	n, err := svc.Count(ctx, Filter{})
	if err != nil {
		t.Fatalf("Count error: %v", err)
	}
	if n != 3 {
		t.Fatalf("remaining = %d, want 3", n)
	}

	if _, err := svc.Purge(ctx, time.Time{}); !errors.Is(err, ErrCutoffRequired) {
		t.Errorf("err = %v, want ErrCutoffRequired", err)
	}
}

func TestExportJSON(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	mustLog(t, svc, Event{Action: "agent.created", Actor: "a1", Resource: ResourceAgent})

	res, err := svc.Export(ctx, Filter{}, FormatJSON)
	if err != nil {
		t.Fatalf("Export error: %v", err)
	}

	// 新格式：manifest + records，且 manifest 自带条数与摘要。
	var payload struct {
		Manifest ExportManifest `json:"manifest"`
		Records  []*EventRecord `json:"records"`
	}
	if err := json.Unmarshal(res.Data, &payload); err != nil {
		t.Fatalf("invalid JSON export: %v\n%s", err, res.Data)
	}
	if len(payload.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(payload.Records))
	}
	if payload.Manifest.Count != 1 {
		t.Errorf("manifest.count = %d, want 1", payload.Manifest.Count)
	}
	if payload.Manifest.SHA256 == "" {
		t.Error("manifest.sha256 is empty")
	}
	if payload.Manifest.Algo != ExportHashAlgo {
		t.Errorf("manifest.algo = %q, want %q", payload.Manifest.Algo, ExportHashAlgo)
	}
	if payload.Manifest.WindowStart == "" || payload.Manifest.WindowEnd == "" {
		t.Error("manifest window is empty for non-empty export")
	}
	if payload.Records[0].Action != "agent.created" {
		t.Errorf("action = %q, want agent.created", payload.Records[0].Action)
	}
}

func TestExportJSON_EmptyIsArray(t *testing.T) {
	svc := newTestService(t)

	res, err := svc.Export(context.Background(), Filter{}, FormatJSON)
	if err != nil {
		t.Fatalf("Export error: %v", err)
	}
	// 空结果必须导出为 []，而不是 null，避免下游解析崩溃
	if !strings.Contains(string(res.Data), `"records": []`) {
		t.Errorf("empty export = %s, want records: []", res.Data)
	}
	if res.Manifest.Count != 0 {
		t.Errorf("manifest.count = %d, want 0", res.Manifest.Count)
	}
	if res.Manifest.Truncated {
		t.Error("empty export marked truncated")
	}
}

func TestExportCSV(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	mustLog(t, svc, Event{
		Action:     "agent.created",
		Actor:      "a1",
		ActorType:  ActorTypeAgent,
		Resource:   ResourceAgent,
		ResourceID: "agent-1",
		IPAddress:  "10.0.0.1",
	})

	res, err := svc.Export(ctx, Filter{}, FormatCSV)
	if err != nil {
		t.Fatalf("Export error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(res.Data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("csv lines = %d, want 2 (header + 1 row)\n%s", len(lines), res.Data)
	}
	if !strings.HasPrefix(lines[0], "id,timestamp,actor") {
		t.Errorf("header = %q, want id,timestamp,actor prefix", lines[0])
	}
	if !strings.Contains(lines[1], "agent.created") {
		t.Errorf("row = %q, want to contain agent.created", lines[1])
	}
	if !strings.Contains(lines[1], "10.0.0.1") {
		t.Errorf("row = %q, want to contain ip", lines[1])
	}
	if res.Manifest.Format != FormatCSV {
		t.Errorf("manifest.format = %q, want csv", res.Manifest.Format)
	}
	if res.Manifest.Count != 1 {
		t.Errorf("manifest.count = %d, want 1", res.Manifest.Count)
	}
	// CSV 摘要覆盖正文，必须能被独立重算出来。
	if ComputeDigest(res.Data) != res.Manifest.SHA256 {
		t.Error("csv manifest digest does not match content")
	}
}

func TestExport_UnsupportedFormat(t *testing.T) {
	svc := newTestService(t)

	_, err := svc.Export(context.Background(), Filter{}, "xml")
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
	}

	// 空 format 默认 json
	if _, err := svc.Export(context.Background(), Filter{}, ""); err != nil {
		t.Fatalf("default format error: %v", err)
	}
}

func TestRecorder_RecordsAgentEvent(t *testing.T) {
	svc := newTestService(t)
	rec := NewRecorder(svc)
	ctx := context.Background()

	if err := rec.RecordAgentEvent(ctx, EventAgentCreated, "agent-1", map[string]interface{}{"name": "demo"}); err != nil {
		t.Fatalf("RecordAgentEvent error: %v", err)
	}

	events, err := svc.Query(ctx, Filter{Resource: ResourceAgent})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len = %d, want 1", len(events))
	}
	if events[0].Action != string(EventAgentCreated) {
		t.Errorf("action = %q, want %q", events[0].Action, EventAgentCreated)
	}
	if events[0].ActorType != ActorTypeAgent {
		t.Errorf("actor_type = %q, want %q", events[0].ActorType, ActorTypeAgent)
	}
	if events[0].ResourceID != "agent-1" {
		t.Errorf("resource_id = %q, want agent-1", events[0].ResourceID)
	}
}

func TestRecorder_TaskFailureStatus(t *testing.T) {
	svc := newTestService(t)
	rec := NewRecorder(svc)
	ctx := context.Background()

	if err := rec.RecordTaskEvent(ctx, EventTaskFailed, "task-1", nil); err != nil {
		t.Fatalf("RecordTaskEvent error: %v", err)
	}
	if err := rec.RecordTaskEvent(ctx, EventTaskCompleted, "task-2", nil); err != nil {
		t.Fatalf("RecordTaskEvent error: %v", err)
	}

	failed, err := svc.Query(ctx, Filter{ResourceID: "task-1"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if failed[0].Status != StatusFailure {
		t.Errorf("status = %q, want %q", failed[0].Status, StatusFailure)
	}

	done, err := svc.Query(ctx, Filter{ResourceID: "task-2"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if done[0].Status != StatusSuccess {
		t.Errorf("status = %q, want %q", done[0].Status, StatusSuccess)
	}
}

func TestRecorder_SystemEventFailure(t *testing.T) {
	svc := newTestService(t)
	rec := NewRecorder(svc)
	ctx := context.Background()

	if err := rec.RecordSystemEvent(ctx, EventSystemError, map[string]interface{}{"detail": "boom"}); err != nil {
		t.Fatalf("RecordSystemEvent error: %v", err)
	}

	events, err := svc.Query(ctx, Filter{Resource: ResourceSystem, Status: StatusFailure})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len = %d, want 1", len(events))
	}
	if events[0].ActorType != ActorTypeSystem {
		t.Errorf("actor_type = %q, want %q", events[0].ActorType, ActorTypeSystem)
	}
}

func TestBusinessEvent_ToEvent(t *testing.T) {
	err := errors.New("boom")
	ev := BusinessEvent{
		Action:     "user.login",
		Resource:   ResourceUser,
		ResourceID: "u1",
		Actor:      "u1",
		Err:        err,
	}.ToEvent()

	if ev.Status != StatusFailure {
		t.Errorf("status = %q, want %q", ev.Status, StatusFailure)
	}
	if ev.Error != "boom" {
		t.Errorf("error = %q, want boom", ev.Error)
	}
	if ev.Details["error"] != "boom" {
		t.Errorf("details[error] = %v, want boom", ev.Details["error"])
	}
	if ev.Timestamp.IsZero() {
		t.Error("expected timestamp to be set")
	}

	ok := BusinessEvent{Action: "user.login"}.ToEvent()
	if ok.Status != StatusSuccess {
		t.Errorf("status = %q, want %q", ok.Status, StatusSuccess)
	}
	if ok.Error != "" {
		t.Errorf("error = %q, want empty", ok.Error)
	}
}

func TestMiddleware_WritesEventAndPropagatesError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	audited := Middleware(svc, BusinessEvent{Action: "user.login", Resource: ResourceUser, Actor: "u1"})

	// 成功路径
	if err := audited(ctx, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 失败路径：原始错误必须原样返回，同时记录失败事件
	wantErr := errors.New("invalid credentials")
	gotErr := audited(ctx, func(context.Context) error { return wantErr })
	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("err = %v, want %v", gotErr, wantErr)
	}

	events, err := svc.Query(ctx, Filter{Resource: ResourceUser})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len = %d, want 2", len(events))
	}
	failures, err := svc.Query(ctx, Filter{Status: StatusFailure})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(failures))
	}
}

func TestMultiRecorder_FanOut(t *testing.T) {
	ctx := context.Background()
	repoA := NewMemoryRepository()
	repoB := NewMemoryRepository()
	mr := NewMultiRecorder(NewRecorder(NewService(repoA)), NewRecorder(NewService(repoB)))

	if err := mr.RecordUserEvent(ctx, EventUserCreated, "u1", nil); err != nil {
		t.Fatalf("RecordUserEvent error: %v", err)
	}

	for i, repo := range []Repository{repoA, repoB} {
		events, err := repo.List(ctx, Filter{})
		if err != nil {
			t.Fatalf("repo %d List error: %v", i, err)
		}
		if len(events) != 1 {
			t.Fatalf("repo %d len = %d, want 1", i, len(events))
		}
	}
}

func TestMultiRecorder_IgnoresNil(t *testing.T) {
	mr := NewMultiRecorder(nil, nil)
	if err := mr.RecordSystemEvent(context.Background(), EventSystemStartup, nil); err != nil {
		t.Fatalf("unexpected error with no downstream recorders: %v", err)
	}
}

func TestRepository_ConcurrentAppend(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	const goroutines = 16
	const perGoroutine = 25

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if _, err := svc.LogEvent(ctx, Event{
					Action: "concurrent.write",
					Actor:  "writer",
					Details: map[string]interface{}{
						"goroutine": g,
						"index":     i,
					},
				}); err != nil {
					t.Errorf("LogEvent error: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	n, err := svc.Count(ctx, Filter{})
	if err != nil {
		t.Fatalf("Count error: %v", err)
	}
	if want := goroutines * perGoroutine; n != want {
		t.Fatalf("count = %d, want %d", n, want)
	}

	// 并发写入后 ID 与 Seq 必须唯一
	events, err := svc.Query(ctx, Filter{Limit: maxLimit})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	ids := make(map[string]struct{}, len(events))
	seqs := make(map[int64]struct{}, len(events))
	for _, ev := range events {
		if _, dup := ids[ev.ID]; dup {
			t.Fatalf("duplicate event ID %q", ev.ID)
		}
		ids[ev.ID] = struct{}{}
		if _, dup := seqs[ev.Seq]; dup {
			t.Fatalf("duplicate seq %d", ev.Seq)
		}
		seqs[ev.Seq] = struct{}{}
	}
}

func TestServiceWithNilRepository(t *testing.T) {
	svc := NewService(nil)
	ctx := context.Background()

	if _, err := svc.Query(ctx, Filter{}); err == nil {
		t.Error("expected error for nil repository")
	}
	if _, err := svc.LogEvent(ctx, Event{Action: "x"}); err == nil {
		t.Error("expected error for nil repository")
	}
	if _, err := svc.Export(ctx, Filter{}, FormatJSON); err == nil {
		t.Error("expected error for nil repository")
	}
	if _, err := svc.Stats(ctx, Filter{}, DistinctAction); err == nil {
		t.Error("expected error for nil repository")
	}
	if _, err := svc.Distinct(ctx, DistinctActor); err == nil {
		t.Error("expected error for nil repository")
	}
	if _, err := svc.Purge(ctx, time.Now()); err == nil {
		t.Error("expected error for nil repository")
	}
}

func TestNewEventID_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 200)
	for i := 0; i < 200; i++ {
		id := NewEventID()
		if id == "" {
			t.Fatal("empty event id")
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate event id %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestSortActions(t *testing.T) {
	got := SortActions([]string{"z", "a", "m"})
	want := []string{"a", "m", "z"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got = %v, want %v", got, want)
		}
	}
	// 不得修改入参
	orig := []string{"z", "a"}
	_ = SortActions(orig)
	if orig[0] != "z" {
		t.Errorf("input slice was mutated: %v", orig)
	}
}

func TestParseLimitOffset(t *testing.T) {
	cases := []struct {
		raw      string
		fallback int
		want     int
	}{
		{"", 50, 50},
		{"10", 50, 10},
		{"0", 50, 50},
		{"-3", 50, 50},
		{"abc", 50, 50},
		{"99999", 50, maxLimit},
	}
	for _, tc := range cases {
		if got := parseLimit(tc.raw, tc.fallback); got != tc.want {
			t.Errorf("parseLimit(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}

	if got := parseOffset("-5"); got != 0 {
		t.Errorf("parseOffset(-5) = %d, want 0", got)
	}
	if got := parseOffset("x"); got != 0 {
		t.Errorf("parseOffset(x) = %d, want 0", got)
	}
	if got := parseOffset("12"); got != 12 {
		t.Errorf("parseOffset(12) = %d, want 12", got)
	}
}
