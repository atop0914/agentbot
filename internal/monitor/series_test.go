package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// ===== Day 24：监控时间序列测试 =====

func TestGetTimeSeries_BucketsAggregate(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	// 用一个固定基准时刻，避免测试跨越桶边界时抖动。
	base := truncateToBucket(time.Now().UTC().Add(-10 * time.Minute))

	// 同一分钟内两个采样：均值应被平均，峰值取最大。
	for _, s := range []struct{ cpu, mem float64 }{{20, 40}, {60, 80}} {
		if err := svc.ReportMetrics(ctx, "a1", &ResourceUsage{
			CPU: s.cpu, Memory: s.mem, Disk: 30,
			Timestamp: base.Add(5 * time.Second),
		}); err != nil {
			t.Fatalf("report metrics: %v", err)
		}
	}
	series, err := svc.GetTimeSeries(ctx, "a1", base, base.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("get time series: %v", err)
	}

	// 窗口两端都向上含入所在桶，因此 [base, base+3m] 覆盖 4 个完整桶 + 1 个边界桶。
	if len(series.Points) != 5 {
		t.Fatalf("got %d buckets, want 5 (inclusive window)", len(series.Points))
	}
	first := series.Points[0]
	if first.Empty {
		t.Fatal("first bucket should not be empty")
	}
	if first.Samples != 2 {
		t.Errorf("got %d samples, want 2", first.Samples)
	}
	if first.CPUAvg != 40 {
		t.Errorf("got cpu avg %v, want 40 (mean of 20 and 60)", first.CPUAvg)
	}
	// 峰值必须保留尖刺：均值 40 会掩盖 60。
	if first.CPUPeak != 60 {
		t.Errorf("got cpu peak %v, want 60", first.CPUPeak)
	}
	if first.MemoryAvg != 60 || first.MemoryPeak != 80 {
		t.Errorf("memory avg/peak = %v/%v, want 60/80", first.MemoryAvg, first.MemoryPeak)
	}

	// 空桶必须被补齐并标记，前端才不用自己对齐时间轴。
	for i := 1; i < len(series.Points); i++ {
		if !series.Points[i].Empty {
			t.Errorf("bucket %d should be empty", i)
		}
		if series.Points[i].Samples != 0 {
			t.Errorf("bucket %d samples = %d, want 0", i, series.Points[i].Samples)
		}
	}
	// 桶起点必须按桶宽对齐且递增。
	for i := 1; i < len(series.Points); i++ {
		gap := series.Points[i].BucketStart.Sub(series.Points[i-1].BucketStart)
		if gap != BucketInterval {
			t.Errorf("bucket gap = %v, want %v", gap, BucketInterval)
		}
	}
}

func TestGetTimeSeries_WindowMeanIgnoresEmptyBuckets(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	base := truncateToBucket(time.Now().UTC().Add(-20 * time.Minute))

	// 只在窗口最左端上报一次采样，窗口其余部分为空。
	if err := svc.ReportMetrics(ctx, "a1", &ResourceUsage{CPU: 90, Timestamp: base.Add(time.Second)}); err != nil {
		t.Fatalf("report: %v", err)
	}
	series, err := svc.GetTimeSeries(ctx, "a1", base, base.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("get series: %v", err)
	}
	// 若均值按「全部桶数」求平均，这里会得到 90/5=18；正确做法是按非空桶平均。
	if series.CPUAvg != 90 {
		t.Errorf("got window cpu avg %v, want 90", series.CPUAvg)
	}
	if series.SampleCount != 1 {
		t.Errorf("got %d samples, want 1", series.SampleCount)
	}
	if series.TaskSuccessRate != -1 {
		t.Errorf("task success rate = %v, want -1 when no tasks reported", series.TaskSuccessRate)
	}
}

func TestGetTimeSeries_TaskSuccessRate(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	base := truncateToBucket(time.Now().UTC().Add(-5 * time.Minute))

	// 3 成功 1 失败 → 75%。
	for i, ok := range []bool{true, true, false, true} {
		if err := svc.ReportTaskOutcome(ctx, TaskOutcome{
			AgentID: "a1", TaskID: "t" + string(rune('a'+i)), Success: ok,
			Timestamp: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("report outcome: %v", err)
		}
	}
	series, err := svc.GetTimeSeries(ctx, "a1", base, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("get series: %v", err)
	}
	if series.TaskCount != 4 {
		t.Fatalf("got %d tasks, want 4", series.TaskCount)
	}
	if series.TaskSuccessRate != 0.75 {
		t.Errorf("got success rate %v, want 0.75", series.TaskSuccessRate)
	}
	if p := series.Points[0]; p.Tasks != 4 || p.TaskSuccessRate != 0.75 {
		t.Errorf("bucket tasks=%d rate=%v, want 4/0.75", p.Tasks, p.TaskSuccessRate)
	}
}

func TestGetTimeSeries_ClampsHugeWindow(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	end := time.Now().UTC()
	// 请求 30 天窗口：必须被裁剪到 MaxSeriesBuckets，避免把内存打满。
	series, err := svc.GetTimeSeries(ctx, "a1", end.Add(-30*24*time.Hour), end)
	if err != nil {
		t.Fatalf("get series: %v", err)
	}
	if len(series.Points) != MaxSeriesBuckets {
		t.Errorf("got %d buckets, want %d (clamped)", len(series.Points), MaxSeriesBuckets)
	}
	// Start 必须反映实际起点，不能让调用方以为拿到了完整 30 天。
	if series.Start.Before(end.Add(-time.Duration(MaxSeriesBuckets) * BucketInterval)) {
		t.Errorf("series start %v is older than the clamp window", series.Start)
	}
}

func TestGetTimeSeries_RejectsEmptyAgentID(t *testing.T) {
	svc := newTestService()
	if _, err := svc.GetTimeSeries(context.Background(), "", time.Now(), time.Now()); err == nil {
		t.Error("expected error for empty agent id")
	}
}

func TestReportTaskOutcome_RequiresAgentID(t *testing.T) {
	svc := newTestService()
	if err := svc.ReportTaskOutcome(context.Background(), TaskOutcome{Success: true}); err == nil {
		t.Error("expected error for missing agent id")
	}
}

// --- HTTP 层 ---

func TestHandler_SeriesEndpoint(t *testing.T) {
	srv, svc := newTestServer(t)
	ctx := context.Background()

	if err := svc.ReportMetrics(ctx, "a1", &ResourceUsage{CPU: 55, Memory: 65, Disk: 10}); err != nil {
		t.Fatalf("seed metrics: %v", err)
	}

	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/series/a1?duration=10m", "")
	if code != http.StatusOK {
		t.Fatalf("series: got %d, want 200", code)
	}
	if body["agent_id"] != "a1" {
		t.Errorf("got agent_id %v, want a1", body["agent_id"])
	}
	points, ok := body["points"].([]interface{})
	if !ok || len(points) == 0 {
		t.Fatalf("points missing or empty: %v", body["points"])
	}
	if body["sample_count"].(float64) != 1 {
		t.Errorf("got sample_count %v, want 1", body["sample_count"])
	}

	// 非法 duration 必须是 400，而不是静默回落到默认窗口。
	code, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/series/a1?duration=bogus", "")
	if code != http.StatusBadRequest {
		t.Errorf("bad duration: got %d, want 400", code)
	}

	// 缺少 agent id。
	code, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/series/", "")
	if code != http.StatusBadRequest {
		t.Errorf("missing agent id: got %d, want 400", code)
	}
}

func TestHandler_TaskOutcomeEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)

	body := `{"agent_id":"a1","task_id":"t1","success":false}`
	code, resp := doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/task-outcomes", body)
	if code != http.StatusCreated {
		t.Fatalf("post outcome: got %d, want 201 (%v)", code, resp)
	}

	// 缺 agent_id 必须 400。
	code, _ = doJSON(t, http.MethodPost, srv.URL+"/api/v1/monitor/task-outcomes", `{"success":true}`)
	if code != http.StatusBadRequest {
		t.Errorf("missing agent id: got %d, want 400", code)
	}

	// GET 不允许。
	code, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/task-outcomes", "")
	if code != http.StatusMethodNotAllowed {
		t.Errorf("GET task-outcomes: got %d, want 405", code)
	}

	// 上报的结果必须能在时间序列里被观测到（端到端联通）。
	code, series := doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/series/a1?duration=10m", "")
	if code != http.StatusOK {
		t.Fatalf("series: got %d", code)
	}
	if series["task_count"].(float64) != 1 {
		t.Errorf("got task_count %v, want 1", series["task_count"])
	}
	if series["task_success_rate"].(float64) != 0 {
		t.Errorf("got success rate %v, want 0", series["task_success_rate"])
	}
}

func TestHandler_DispositionSummaryEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)

	code, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/alert-dispositions", "")
	if code != http.StatusOK {
		t.Fatalf("summary: got %d, want 200", code)
	}
	if body["open"].(float64) != 0 || body["unacknowledged"].(float64) != 0 {
		t.Errorf("empty summary should be all zeros, got %v", body)
	}

	// limit 非法必须 400。
	code, _ = doJSON(t, http.MethodGet, srv.URL+"/api/v1/monitor/alert-dispositions?limit=abc", "")
	if code != http.StatusBadRequest {
		t.Errorf("bad limit: got %d, want 400", code)
	}
}

// 保证 JSON 契约里 status 字段在列表接口上一定存在（旧数据也要补齐）。
func TestAlertJSON_CarriesStatus(t *testing.T) {
	alert := &Alert{ID: "x", AgentID: "a", Resolved: false}
	alert.NormalizeStatus()
	raw, err := json.Marshal(alert)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["status"] != string(AlertStatusFiring) {
		t.Errorf("got status %v, want %q", decoded["status"], AlertStatusFiring)
	}
}
