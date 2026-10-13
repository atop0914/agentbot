package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/storage"
)

// agent_persistence_e2e_test.go 是 agent 仓库 Postgres 化的端到端证明。
//
// 断言纪律沿用 3-4 的结论：**断言副作用，不读标识**。
// 具体到 Agent，有两条 3-4 没有的关切：
//
//  1. Agent 是资源主体。用户丢了还能重新注册，Agent 的配置与状态丢了
//     就是丢了 —— 所以「重启后配置与状态原样还在」必须被直接断言，
//     而不是只断言「列表里还有一行」。
//  2. 建 Agent 走的是**带授权中间件的真实 router**。若只调 service，
//     就绕过了 authz 的 default-deny 与目标存在性校验，
//     而后者会去 Postgres 里查这个 Agent 是否存在 ——
//     那是一条只有全链路才会走到的读路径。

// agentAuthzSubject 复用 persistence_e2e_test.go 里的 authzSubject。
// 在同一个 App 实例上授权，否则授权层会把请求 403 掉。

// createAgent 经真实 HTTP 路由建一个 Agent，返回它的 ID。
// 需要带上已授权用户的 token：POST /api/v1/agents 需要 agent:create。
func createAgent(t *testing.T, baseURL, token, name string) string {
	t.Helper()

	body := fmt.Sprintf(
		`{"name":%q,"description":"e2e persistence probe","config":{"model":"gpt-4o","max_tokens":2048,"tools":["shell"]}}`,
		name)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/agents",
		bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("build create-agent request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("create agent status = %d, want 201; body = %s", resp.StatusCode, raw)
	}

	var created struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create-agent response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("create-agent response has no id")
	}
	// 状态机起点必须是 idle（NewAgent 的语义）。
	if created.State != "idle" {
		t.Errorf("created agent state = %q, want idle", created.State)
	}
	return created.ID
}

// adminWithToken 注册一个用户并授予 coordinator 角色，返回用户 ID 与 token。
// 必须在**同一个** App 上授权（authz 是进程内状态，另建实例无效）。
func adminWithToken(t *testing.T, ts *httptest.Server, app *App) (string, string) {
	t.Helper()

	email := fmt.Sprintf("agent-admin-%d@example.com", time.Now().UnixNano())
	userID, token := registerUser(t, ts.URL, email)
	if token == "" {
		t.Fatal("register response has no access token")
	}
	if _, err := app.AuthzSvc.Assign(context.Background(),
		authzSubject(userID), "role-coordinator", "test-bootstrap"); err != nil {
		t.Fatalf("assign coordinator role: %v", err)
	}
	return userID, token
}

func TestPersistence_AgentLandsInPostgres(t *testing.T) {
	db := requirePersistedDB(t)
	if _, err := db.Exec(`TRUNCATE users, audit_events, agents RESTART IDENTITY`); err != nil {
		t.Fatalf("failed to truncate tables: %v", err)
	}

	cfg := withDatabase(t)
	ts, app := newPersistentApp(t, cfg)
	_, token := adminWithToken(t, ts, app)

	agentID := createAgent(t, ts.URL, token, "persist-agent")

	// 核心断言 1：直接用 SQL 查那一行。
	var (
		gotName  string
		gotState string
	)
	err := db.QueryRow(
		`SELECT name, state FROM agents WHERE id = $1`, agentID).
		Scan(&gotName, &gotState)
	if err != nil {
		t.Fatalf("created agent %s is not in the agents table: %v", agentID, err)
	}
	if gotState != "idle" {
		t.Errorf("stored state = %q, want idle", gotState)
	}

	// 核心断言 2：配置要真的落进 JSONB，而不是只存了名字。
	// 若 config 列被写成空对象，Agent 重启后会以一个「没有任何配置」
	// 的形态复活 —— 不报错，只是行为全变。
	var model string
	var maxTokens int
	if err := db.QueryRow(
		`SELECT config->>'model', (config->>'max_tokens')::int FROM agents WHERE id = $1`,
		agentID).Scan(&model, &maxTokens); err != nil {
		t.Fatalf("failed to read the agent config JSONB: %v", err)
	}
	if model != "gpt-4o" {
		t.Errorf("stored config.model = %q, want gpt-4o", model)
	}
	if maxTokens != 2048 {
		t.Errorf("stored config.max_tokens = %d, want 2048", maxTokens)
	}
}

func TestPersistence_AgentSurvivesRestart(t *testing.T) {
	// 本轮最关键的一条：重启后 Agent 的**配置与状态**原样还在。
	//
	// 只断言「列表里还有一行」是不够的 —— 一个把 id/name 落了库但
	// 把状态与配置丢掉的实现同样能通过那条断言，而那种 Agent
	// 在生产上等于不可用（不知道它该跑什么、也不知道它上次跑到哪）。
	db := requirePersistedDB(t)
	if _, err := db.Exec(`TRUNCATE users, audit_events, agents RESTART IDENTITY`); err != nil {
		t.Fatalf("failed to truncate tables: %v", err)
	}

	cfg := withDatabase(t)

	// 第一次装配：建 Agent，把它推进到 running。
	ts1, app1 := newPersistentApp(t, cfg)
	_, token1 := adminWithToken(t, ts1, app1)
	agentID := createAgent(t, ts1.URL, token1, "restart-agent")

	// 经真实路由把 Agent 启动到 running：这一步同时验证了
	// 「状态变更走的是 Update（整行覆盖）而不是只改内存里的结构体」。
	startReq, err := http.NewRequest(http.MethodPost,
		ts1.URL+"/api/v1/agents/"+agentID+"/start", nil)
	if err != nil {
		t.Fatalf("build start request: %v", err)
	}
	startReq.Header.Set("Authorization", "Bearer "+token1)
	startResp, err := http.DefaultClient.Do(startReq)
	if err != nil {
		t.Fatalf("start agent: %v", err)
	}
	_ = startResp.Body.Close()
	if startResp.StatusCode != http.StatusOK {
		t.Fatalf("start agent status = %d, want 200", startResp.StatusCode)
	}

	ts1.Close() // 关掉服务器与 App：内存状态随之消失

	// 第二次装配：全新的 App 与连接池，直接查 Agent。
	ts2, app2 := newPersistentApp(t, cfg)
	_, token2 := adminWithToken(t, ts2, app2)

	getReq, err := http.NewRequest(http.MethodGet, ts2.URL+"/api/v1/agents/"+agentID, nil)
	if err != nil {
		t.Fatalf("build get request: %v", err)
	}
	getReq.Header.Set("Authorization", "Bearer "+token2)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("get agent after restart: %v", err)
	}
	defer func() { _ = getResp.Body.Close() }()

	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get agent after restart status = %d, want 200 "+
			"(the agent did not survive the restart: it only ever lived in memory)",
			getResp.StatusCode)
	}

	var got struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		State  string `json:"state"`
		Config struct {
			Model     string   `json:"model"`
			MaxTokens int      `json:"max_tokens"`
			Tools     []string `json:"tools"`
		} `json:"config"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decode get-agent response: %v", err)
	}
	if got.ID != agentID {
		t.Errorf("agent id after restart = %q, want %q", got.ID, agentID)
	}
	// 状态必须还是 running —— 这正是「Agent 是资源主体」的核心：
	// 重启后状态退回 idle 会让调度器以为它闲置，从而重复派活。
	if got.State != "running" {
		t.Errorf("agent state after restart = %q, want running "+
			"(the state machine position did not persist)", got.State)
	}
	if got.Config.Model != "gpt-4o" {
		t.Errorf("agent config.model after restart = %q, want gpt-4o", got.Config.Model)
	}
	if got.Config.MaxTokens != 2048 {
		t.Errorf("agent config.max_tokens after restart = %d, want 2048", got.Config.MaxTokens)
	}
	if len(got.Config.Tools) != 1 || got.Config.Tools[0] != "shell" {
		t.Errorf("agent config.tools after restart = %v, want [shell]", got.Config.Tools)
	}

	// 交叉验证：授权层的目标存在性校验走的是 SQL 读路径
	// （见 authz_sources.go 的 "agent" 注册项）。若上面那次 get
	// 成功只是命中了某个内存缓存，带 TargetType 的 PUT 会失败。
	putReq, err := http.NewRequest(http.MethodPut, ts2.URL+"/api/v1/agents/"+agentID,
		bytes.NewBufferString(`{"description":"updated after restart"}`))
	if err != nil {
		t.Fatalf("build put request: %v", err)
	}
	putReq.Header.Set("Content-Type", "application/json")
	putReq.Header.Set("Authorization", "Bearer "+token2)
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("update agent after restart: %v", err)
	}
	defer func() { _ = putResp.Body.Close() }()
	// running 状态的 Agent 不允许改配置（service 的业务规则），
	// 所以这里期望 400 而不是 200 —— 但**绝不能是 404**：
	// 404 意味着授权层的存在性校验在库里没找到这个 Agent，
	// 那才是「重启后读不到」的信号。
	if putResp.StatusCode == http.StatusNotFound {
		t.Fatal("authorization target lookup could not find the agent in PostgreSQL " +
			"after a restart: the read path is not going through the database")
	}
}

func TestPersistence_MemoryBackendWritesNoAgentsToPostgres(t *testing.T) {
	// 反向断言：没有配置数据库时，建 Agent 绝不能悄悄写进 PG。
	// 防的是「装配层无论配置如何都连库」的回归 —— 那会让
	// 「我用内存后端」这个明确的部署选择失效。
	db := requirePersistedDB(t)
	if _, err := db.Exec(`TRUNCATE users, audit_events, agents RESTART IDENTITY`); err != nil {
		t.Fatalf("failed to truncate tables: %v", err)
	}

	cfg := withoutDatabase()
	a, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig() error = %v", err)
	}
	t.Cleanup(func() {
		if a.Storage != nil {
			_ = a.Storage.Close()
		}
	})

	if a.StorageBackend != storage.BackendMemory {
		t.Fatalf("StorageBackend = %q, want %q for a database-less config",
			a.StorageBackend, storage.BackendMemory)
	}

	ts := httptest.NewServer(NewRouter(a))
	t.Cleanup(ts.Close)
	_, token := adminWithToken(t, ts, a)
	createAgent(t, ts.URL, token, "memory-agent")

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&count); err != nil {
		t.Fatalf("failed to count agents: %v", err)
	}
	if count != 0 {
		t.Errorf("agents table has %d rows after a memory-backed create; "+
			"the memory backend must not touch PostgreSQL", count)
	}
}
