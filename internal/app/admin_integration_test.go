package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// --- 管理后台（Day 22 新增模块的端到端联通验证） ---
//
// 这些测试走真实路由（NewRouter），确保管理后台的分区聚合确实能
// 从 agent / task / monitor / audit 模块取到数据，而不是只在本包内自证。

// adminSnapshot 是聚合视图的最小反序列化目标。
type adminSnapshot struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	GeneratedAt string `json:"generated_at"`
	Overview    *struct {
		TotalAgents int  `json:"total_agents"`
		TotalTasks  int  `json:"total_tasks"`
		AuditEvents int  `json:"audit_events"`
		OpenAlerts  int  `json:"open_alerts"`
		Degraded    bool `json:"degraded"`
	} `json:"overview"`
	Agents *struct {
		Total   int            `json:"total"`
		ByState map[string]int `json:"by_state"`
	} `json:"agents"`
	Tasks *struct {
		Total   int            `json:"total"`
		ByState map[string]int `json:"by_state"`
	} `json:"tasks"`
	Audit *struct {
		Total  int `json:"total"`
		Recent []struct {
			ID     string `json:"id"`
			Action string `json:"action"`
		} `json:"recent"`
	} `json:"audit"`
}

func TestAdminConfigEndpoint(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/admin/config")
	if err != nil {
		t.Fatalf("GET /api/v1/admin/config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var cfg struct {
		Title        string          `json:"title"`
		Version      string          `json:"version"`
		FeatureFlags map[string]bool `json:"feature_flags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Title == "" {
		t.Error("console title should not be empty")
	}
	if len(cfg.FeatureFlags) == 0 {
		t.Error("default feature flags should be exposed")
	}
	// 危险开关默认关闭
	if cfg.FeatureFlags["agent_control"] {
		t.Error("agent_control should default to disabled")
	}
}

func TestAdminConfigUpdateRoundTrips(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	body := `{"title":"AgentBot 运维台","feature_flags":{"show_audit":true,"user_admin":true}}`
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/admin/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/v1/admin/config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// 读回确认真正落库（而非只回显请求体）
	resp2, err := http.Get(ts.URL + "/api/v1/admin/config")
	if err != nil {
		t.Fatalf("GET after update: %v", err)
	}
	defer resp2.Body.Close()
	var cfg struct {
		Title        string          `json:"title"`
		FeatureFlags map[string]bool `json:"feature_flags"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Title != "AgentBot 运维台" {
		t.Errorf("Title = %q, want the updated value", cfg.Title)
	}
	if !cfg.FeatureFlags["user_admin"] || len(cfg.FeatureFlags) != 2 {
		t.Errorf("FeatureFlags = %v, want wholesale replacement {show_audit,user_admin}", cfg.FeatureFlags)
	}
}

func TestAdminConfigRejectsEmptyTitle(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/admin/config", bytes.NewBufferString(`{"title":"   "}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/v1/admin/config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAdminStaticStatusPlaceholder(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 前端构建产物在本仓库中并不存在，接口仍须可用。
	resp, err := http.Get(ts.URL + "/api/v1/admin/static")
	if err != nil {
		t.Fatalf("GET /api/v1/admin/static: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var status struct {
		Prefix  string `json:"prefix"`
		Dir     string `json:"dir"`
		Mounted bool   `json:"mounted"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.Prefix != "/admin" {
		t.Errorf("Prefix = %q, want /admin", status.Prefix)
	}
	if !status.Mounted || status.Message != "" {
		// 若产物目录存在（例如本地已构建），状态应为已挂载且无告警信息。
		t.Logf("static status: %+v", status)
	}

	// 控制台入口必须始终可达：产物缺失时返回占位页而非 404。
	consoleResp, err := http.Get(ts.URL + "/admin/")
	if err != nil {
		t.Fatalf("GET /admin/: %v", err)
	}
	defer consoleResp.Body.Close()
	if consoleResp.StatusCode != http.StatusOK {
		t.Errorf("GET /admin/ status = %d, want 200", consoleResp.StatusCode)
	}
}

func TestAdminSnapshotAggregatesLiveModules(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 1. 创建两个 Agent
	agentIDs := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		body := fmt.Sprintf(`{"name":"admin-agent-%d","description":"d"}`, i)
		resp, err := http.Post(ts.URL+"/api/v1/agents", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatalf("POST /api/v1/agents: %v", err)
		}
		var created struct {
			ID string `json:"id"`
		}
		json.NewDecoder(resp.Body).Decode(&created)
		resp.Body.Close()
		if created.ID == "" {
			t.Fatal("agent creation returned no id")
		}
		agentIDs = append(agentIDs, created.ID)
	}

	// 2. 启动其中一个，制造 running/idle 的状态分布
	startResp, err := http.Post(ts.URL+"/api/v1/agents/"+agentIDs[0]+"/start", "application/json", nil)
	if err != nil {
		t.Fatalf("POST start: %v", err)
	}
	startResp.Body.Close()
	if startResp.StatusCode != http.StatusOK && startResp.StatusCode != http.StatusNoContent {
		t.Fatalf("start status = %d", startResp.StatusCode)
	}

	// 3. 创建一个任务
	taskBody := fmt.Sprintf(`{"agent_id":%q,"goal":"admin snapshot task"}`, agentIDs[0])
	taskResp, err := http.Post(ts.URL+"/api/v1/tasks", "application/json", bytes.NewBufferString(taskBody))
	if err != nil {
		t.Fatalf("POST /api/v1/tasks: %v", err)
	}
	taskResp.Body.Close()

	// 4. 写一条审计事件
	auditBody := `{"action":"agent.created","actor":"admin","actor_type":"user","resource":"agent"}`
	auditResp, err := http.Post(ts.URL+"/api/v1/audit/events", "application/json", bytes.NewBufferString(auditBody))
	if err != nil {
		t.Fatalf("POST /api/v1/audit/events: %v", err)
	}
	auditResp.Body.Close()

	// 5. 拉取聚合视图
	resp, err := http.Get(ts.URL + "/api/v1/admin/snapshot")
	if err != nil {
		t.Fatalf("GET /api/v1/admin/snapshot: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var snap adminSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.Overview == nil {
		t.Fatal("Overview should be present in a full snapshot")
	}
	if snap.Overview.TotalAgents != 2 {
		t.Errorf("TotalAgents = %d, want 2 (live agent module)", snap.Overview.TotalAgents)
	}
	if snap.Overview.TotalTasks != 1 {
		t.Errorf("TotalTasks = %d, want 1 (live task module)", snap.Overview.TotalTasks)
	}
	if snap.Overview.AuditEvents < 1 {
		t.Errorf("AuditEvents = %d, want >= 1 (live audit module)", snap.Overview.AuditEvents)
	}
	if snap.Agents == nil || snap.Agents.ByState["running"] != 1 || snap.Agents.ByState["idle"] != 1 {
		t.Errorf("agent breakdown = %+v, want one running and one idle", snap.Agents)
	}
	if snap.Tasks == nil || snap.Tasks.Total != 1 {
		t.Errorf("task breakdown = %+v, want 1 task", snap.Tasks)
	}
	if snap.Audit == nil || len(snap.Audit.Recent) < 1 {
		t.Fatalf("audit digest = %+v, want at least one recent event", snap.Audit)
	}
	if snap.Audit.Recent[0].Action != "agent.created" {
		t.Errorf("recent audit action = %q, want agent.created", snap.Audit.Recent[0].Action)
	}
}

func TestAdminSnapshotSectionFilterAndValidation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 只取 plugins 分区：不应包含概览或数据分区
	resp, err := http.Get(ts.URL + "/api/v1/admin/snapshot?sections=plugins")
	if err != nil {
		t.Fatalf("GET snapshot?sections=plugins: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	raw := make(map[string]json.RawMessage)
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["plugins"]; !ok {
		t.Error("plugins section should be present")
	}
	for _, absent := range []string{"overview", "agents", "tasks", "audit"} {
		if _, ok := raw[absent]; ok {
			t.Errorf("section %q should be omitted when not requested", absent)
		}
	}

	// 未知分区 → 400
	badResp, err := http.Get(ts.URL + "/api/v1/admin/snapshot?sections=nope")
	if err != nil {
		t.Fatalf("GET snapshot?sections=nope: %v", err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown section status = %d, want 400", badResp.StatusCode)
	}

	// 非法 recent_limit → 400
	badLimit, err := http.Get(ts.URL + "/api/v1/admin/snapshot?recent_limit=abc")
	if err != nil {
		t.Fatalf("GET snapshot?recent_limit=abc: %v", err)
	}
	defer badLimit.Body.Close()
	if badLimit.StatusCode != http.StatusBadRequest {
		t.Errorf("bad recent_limit status = %d, want 400", badLimit.StatusCode)
	}
}

func TestAdminSnapshotRecentLimitTruncates(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	for i := 0; i < 3; i++ {
		body := fmt.Sprintf(`{"action":"agent.created","actor":"u%d","actor_type":"user","resource":"agent"}`, i)
		resp, err := http.Post(ts.URL+"/api/v1/audit/events", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatalf("POST audit event: %v", err)
		}
		resp.Body.Close()
	}

	resp, err := http.Get(ts.URL + "/api/v1/admin/snapshot?sections=audit&recent_limit=2")
	if err != nil {
		t.Fatalf("GET snapshot: %v", err)
	}
	defer resp.Body.Close()

	var snap struct {
		Audit *struct {
			Total     int  `json:"total"`
			Truncated bool `json:"truncated"`
			Recent    []struct {
				ID string `json:"id"`
			} `json:"recent"`
		} `json:"audit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.Audit == nil {
		t.Fatal("audit section missing")
	}
	if len(snap.Audit.Recent) != 2 {
		t.Errorf("Recent has %d items, want 2 (recent_limit)", len(snap.Audit.Recent))
	}
	if !snap.Audit.Truncated {
		t.Error("Truncated should be true when more events exist than recent_limit")
	}
	if snap.Audit.Total != 3 {
		t.Errorf("Total = %d, want 3", snap.Audit.Total)
	}
}

func TestAdminStaticConsoleIsReachableWithoutBuildArtifacts(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 构建产物不存在时，控制台入口返回占位页而不是 500/404。
	resp, err := http.Get(ts.URL + "/admin/anything")
	if err != nil {
		t.Fatalf("GET /admin/anything: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 placeholder", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html placeholder", ct)
	}
}
