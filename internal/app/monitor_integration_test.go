package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

// ===== Day 24：监控仪表盘（时间序列 + 告警处置）的端到端联通验证 =====
//
// 这些测试走真实路由（NewRouter，含 authz 中间件），验证：
//  1. 新增路由已被权限表登记（否则会落进默认拒绝返回 403）；
//  2. 指标上报 → 时间序列 → 后台 agents 分区摘要，这条链路真的通；
//  3. 告警处置会落到审计日志（"谁改的"可追溯）。

// monitorSeriesResponse 是时间序列接口的最小反序列化目标。
type monitorSeriesResponse struct {
	AgentID     string  `json:"agent_id"`
	SampleCount int     `json:"sample_count"`
	CPUAvg      float64 `json:"cpu_avg"`
	TaskCount   int     `json:"task_count"`
	Points      []struct {
		BucketStart string  `json:"bucket_start"`
		Empty       bool    `json:"empty"`
		CPUAvg      float64 `json:"cpu_avg"`
	} `json:"points"`
}

func (e *testEnv) doJSONPost(path, body string) *http.Response {
	e.t.Helper()
	return e.do(http.MethodPost, path, body)
}

func TestMonitorSeriesEndpointReachableOverRouter(t *testing.T) {
	env := newTestEnv(t)

	// 1. 上报资源指标（写：需要 agent:control）。
	resp := env.doJSONPost("/api/v1/monitor/agents/a1/metrics", `{"cpu":42,"memory":55,"disk":10}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("report metrics status = %d, want 201", resp.StatusCode)
	}

	// 2. 上报任务结果（写）。
	resp = env.doJSONPost("/api/v1/monitor/task-outcomes", `{"agent_id":"a1","task_id":"t1","success":true}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("report task outcome status = %d, want 201", resp.StatusCode)
	}

	// 3. 读时间序列（读：agent:read）。
	resp = env.do(http.MethodGet, "/api/v1/monitor/series/a1?duration=10m", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("series status = %d, want 200 (403 means the route is not in the permission table)", resp.StatusCode)
	}
	var series monitorSeriesResponse
	if err := json.NewDecoder(resp.Body).Decode(&series); err != nil {
		t.Fatalf("decode series: %v", err)
	}
	if series.SampleCount != 1 {
		t.Errorf("sample_count = %d, want 1", series.SampleCount)
	}
	if series.CPUAvg != 42 {
		t.Errorf("cpu_avg = %v, want 42", series.CPUAvg)
	}
	if series.TaskCount != 1 {
		t.Errorf("task_count = %d, want 1 (outcome report must be observable here)", series.TaskCount)
	}
	if len(series.Points) == 0 {
		t.Error("expected the series to carry buckets")
	}
}

func TestMonitorDispositionReachableOverRouter(t *testing.T) {
	env := newTestEnv(t)

	// 造一条告警：超阈值心跳 + 建规则。
	resp := env.doJSONPost("/api/v1/monitor/agents", `{"agent_id":"a1","state":"running","resources":{"cpu":95}}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("report status = %d, want 201", resp.StatusCode)
	}
	resp = env.doJSONPost("/api/v1/monitor/alerts", `{"agent_id":"a1","type":"cpu_high","threshold":80}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create alert rule status = %d, want 201", resp.StatusCode)
	}
	var created struct {
		Alert *struct {
			ID string `json:"id"`
		} `json:"alert"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create rule: %v", err)
	}
	resp.Body.Close()
	if created.Alert == nil || created.Alert.ID == "" {
		t.Fatal("expected the rule to raise an alert immediately")
	}
	alertID := created.Alert.ID

	// 汇总端点可读。
	resp = env.do(http.MethodGet, "/api/v1/monitor/alert-dispositions", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disposition summary status = %d, want 200", resp.StatusCode)
	}
	var summary struct {
		Unacknowledged int `json:"unacknowledged"`
	}
	json.NewDecoder(resp.Body).Decode(&summary)
	resp.Body.Close()
	if summary.Unacknowledged != 1 {
		t.Errorf("unacknowledged = %d, want 1", summary.Unacknowledged)
	}

	// 认领。
	resp = env.doJSONPost("/api/v1/monitor/alerts/"+alertID+"/ack", `{"operator":"ops-1","note":"排查中"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ack status = %d, want 200", resp.StatusCode)
	}

	// 解决。
	resp = env.doJSONPost("/api/v1/monitor/alerts/"+alertID+"/resolve", `{"operator":"ops-1","note":"已扩容"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve status = %d, want 200", resp.StatusCode)
	}

	// 处置记录可读，且轨迹完整。
	resp = env.do(http.MethodGet, "/api/v1/monitor/alerts/"+alertID+"/dispositions", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dispositions status = %d, want 200", resp.StatusCode)
	}
	var disp struct {
		Total        int `json:"total"`
		Dispositions []struct {
			Action   string `json:"action"`
			From     string `json:"from"`
			To       string `json:"to"`
			Operator string `json:"operator"`
			Note     string `json:"note"`
		} `json:"dispositions"`
	}
	json.NewDecoder(resp.Body).Decode(&disp)
	resp.Body.Close()
	if disp.Total != 2 {
		t.Fatalf("dispositions = %d, want 2 (ack + resolve)", disp.Total)
	}
	if disp.Dispositions[0].From != "firing" || disp.Dispositions[0].To != "acknowledged" {
		t.Errorf("first transition = %s -> %s, want firing -> acknowledged",
			disp.Dispositions[0].From, disp.Dispositions[0].To)
	}
	if disp.Dispositions[1].To != "resolved" || disp.Dispositions[1].Operator != "ops-1" {
		t.Errorf("second disposition = %+v, want resolved by ops-1", disp.Dispositions[1])
	}
}

// 告警处置必须落到审计日志：「谁在什么时候认领/解决了什么」是事故复盘的根。
func TestMonitorDispositionLandsInAudit(t *testing.T) {
	env := newTestEnv(t)

	resp := env.doJSONPost("/api/v1/monitor/agents", `{"agent_id":"a1","state":"running","resources":{"cpu":95}}`)
	resp.Body.Close()
	resp = env.doJSONPost("/api/v1/monitor/alerts", `{"agent_id":"a1","type":"cpu_high","threshold":80}`)
	var created struct {
		Alert *struct {
			ID string `json:"id"`
		} `json:"alert"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.Alert == nil {
		t.Fatal("expected an alert")
	}

	resp = env.doJSONPost("/api/v1/monitor/alerts/"+created.Alert.ID+"/ack", `{"operator":"ops-1","note":"排查中"}`)
	resp.Body.Close()

	// 查审计：处置动作必须以 alert.acknowledged 落库，且带处置人与 Agent。
	resp = env.do(http.MethodGet, "/api/v1/audit/events?event_type=alert.acknowledged&limit=50", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit query status = %d, want 200", resp.StatusCode)
	}
	var events struct {
		Events []struct {
			Actor      string                 `json:"actor"`
			Action     string                 `json:"action"`
			ResourceID string                 `json:"resource_id"`
			Details    map[string]interface{} `json:"details"`
		} `json:"events"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		t.Fatalf("decode audit events: %v", err)
	}
	if events.Total == 0 {
		t.Fatal("acknowledging an alert must leave an audit trail")
	}
	ev := events.Events[0]
	if ev.Action != "alert.acknowledged" {
		t.Errorf("audit action = %q, want alert.acknowledged", ev.Action)
	}
	if ev.ResourceID != "a1" {
		t.Errorf("audit resource_id = %q, want the agent id a1", ev.ResourceID)
	}
	if got, _ := ev.Details["operator"].(string); got != "ops-1" {
		t.Errorf("audit detail operator = %q, want ops-1", got)
	}
}

// 后台 agents 分区必须带上监控摘要（时间序列摘要 + 处置进度），
// 且监控不可用时不能拖垮 Agent 状态分布。
func TestAdminAgentsSectionCarriesMonitoringSummary(t *testing.T) {
	env := newTestEnv(t)

	// 造数据：一个真实 Agent（admin 的 agents 分区读的是 agent 服务），
	// 再上报监控状态、指标与告警规则。
	resp := env.doJSONPost("/api/v1/agents", `{"name":"worker-1","description":"测试 Agent"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create agent status = %d, want 201", resp.StatusCode)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create agent: %v", err)
	}
	resp.Body.Close()
	if created.ID == "" {
		t.Fatal("create agent returned no id")
	}

	resp = env.doJSONPost("/api/v1/monitor/agents",
		`{"agent_id":"`+created.ID+`","state":"running","resources":{"cpu":70}}`)
	resp.Body.Close()
	resp = env.doJSONPost("/api/v1/monitor/agents/"+created.ID+"/metrics", `{"cpu":70,"memory":40,"disk":20}`)
	resp.Body.Close()
	resp = env.doJSONPost("/api/v1/monitor/alerts",
		`{"agent_id":"`+created.ID+`","type":"cpu_high","threshold":95}`)
	resp.Body.Close()

	resp = env.do(http.MethodGet, "/api/v1/admin/snapshot?sections=agents&recent_limit=5", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d, want 200", resp.StatusCode)
	}
	var snap struct {
		Agents *struct {
			Total      int            `json:"total"`
			ByState    map[string]int `json:"by_state"`
			Monitoring *struct {
				SeriesWindow string `json:"series_window"`
				Series       []struct {
					AgentID string  `json:"agent_id"`
					Samples int     `json:"samples"`
					CPUAvg  float64 `json:"cpu_avg"`
				} `json:"series"`
				Dispositions *struct {
					Open int `json:"open"`
				} `json:"dispositions"`
			} `json:"monitoring"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.Agents == nil {
		t.Fatal("agents section missing from the snapshot")
	}
	if snap.Agents.Total != 1 {
		t.Errorf("agents total = %d, want 1", snap.Agents.Total)
	}
	if snap.Agents.Monitoring == nil {
		t.Fatal("agents section must carry the monitoring summary")
	}
	if snap.Agents.Monitoring.SeriesWindow == "" {
		t.Error("series_window must be echoed back to the console")
	}
	if len(snap.Agents.Monitoring.Series) != 1 {
		t.Fatalf("monitoring series = %d entries, want 1", len(snap.Agents.Monitoring.Series))
	}
	if got := snap.Agents.Monitoring.Series[0].CPUAvg; got != 70 {
		t.Errorf("series cpu_avg = %v, want 70", got)
	}
	if snap.Agents.Monitoring.Dispositions == nil {
		t.Error("disposition progress must be part of the monitoring summary")
	}
}
