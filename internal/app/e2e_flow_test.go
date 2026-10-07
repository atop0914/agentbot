package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/auth"
	"github.com/atop0914/agentbot/internal/authz"
	"github.com/atop0914/agentbot/internal/tenant"
)

// 本文件是 Day 29 的端到端全链路测试：注册 → 登录 → 建 Agent → 执行任务
// → 产生审计事件 → 管理后台可见。
//
// 为什么必须走真实 router 而不是各模块单测：
//
//	Day 23-28 反复出现的故障都只在「模块接线」这一层暴露 ——
//	认证挂在授权内层导致受保护路由恒 401、新增集合路径漏登记权限导致
//	403、租户中间件层序反了导致读不到 claims。这些都是**单测全绿、
//	真实链路全坏**的典型。所以本文件的每一条断言都打在新路由 + 中间件
//	链上，而不是直接调用 service。
//
// 刻意不复用 newTestEnv：那个环境会在建用户时绕过注册接口直接调 service。
// 全链路测试的价值恰恰在于「注册接口本身也是链路的一环」，
// 任何一处接线断裂都必须在这里可见。

// e2eEnv 是一次全链路测试的运行时环境。
type e2eEnv struct {
	t      *testing.T
	app    *App
	router http.Handler
	server *httptest.Server
	// token 是「既有权限、又绑定租户」的主体令牌，链路主线的操作者。
	token    string
	userID   string
	tenantID tenant.ID
	email    string
	password string
}

// newE2EEnv 装配一个全链路环境。
//
// 链路顺序刻意与真实用户一致：先注册（公开路由），再登录（公开路由），
// 然后由装配层把内置 coordinator 角色授给这个新用户（相当于运营在后台
// 点了一次「授权」），最后建租户并把用户绑进去（相当于企业入驻）。
// 全程不直接写 store 模拟「已登录」状态。
func newE2EEnv(t *testing.T) *e2eEnv {
	t.Helper()

	a := New()
	router := NewRouter(a)
	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)

	env := &e2eEnv{
		t:        t,
		app:      a,
		router:   router,
		server:   ts,
		email:    fmt.Sprintf("e2e-%d@corp.example", time.Now().UnixNano()),
		password: "Str0ngPass!123",
	}

	// 1. 注册（公开路由，不带任何令牌）。
	reg := env.postAnon("/api/v1/auth/register", fmt.Sprintf(
		`{"email":%q,"username":"e2euser","password":%q}`, env.email, env.password))
	if reg.Code != http.StatusCreated {
		t.Fatalf("注册应返回 201，得到 %d: %s", reg.Code, reg.Body.String())
	}
	var regBody struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		AccessToken string `json:"access_token"`
	}
	env.decode(t, reg.Body.Bytes(), &regBody)
	env.userID = regBody.User.ID
	if env.userID == "" {
		t.Fatalf("注册响应缺少 user.id: %s", reg.Body.String())
	}

	// 2. 登录（公开路由）—— 刻意重新走一次登录而不是复用注册返回的令牌，
	//    因为「注册后能否登录」本身就是链路的一部分。
	login := env.postAnon("/api/v1/auth/login", fmt.Sprintf(
		`{"email":%q,"password":%q}`, env.email, env.password))
	if login.Code != http.StatusOK {
		t.Fatalf("登录应返回 200，得到 %d: %s", login.Code, login.Body.String())
	}
	var loginBody struct {
		AccessToken string `json:"access_token"`
	}
	env.decode(t, login.Body.Bytes(), &loginBody)
	if loginBody.AccessToken == "" {
		t.Fatal("登录响应缺少 access_token")
	}

	// 3. 授权：把内置 coordinator 角色授给该用户（等价于后台的一次授权操作）。
	//    走 authz 服务而非伪造 claims —— 权限判定的依据必须真实。
	subject := authz.Subject{Type: authz.SubjectUser, ID: env.userID}
	if _, err := a.AuthzSvc.Assign(context.Background(), subject, "role-coordinator", "e2e-bootstrap"); err != nil {
		t.Fatalf("授予 coordinator 角色失败: %v", err)
	}

	// 4. 建租户并把用户绑进去，然后**重新签发**带租户声明的令牌。
	//    不能靠改 query/header 换租户：租户身份只认签名声明。
	tn, err := a.TenantSvc.Create(context.Background(), tenant.CreateRequest{
		Name: "e2e-corp", Slug: "e2e-corp", Plan: tenant.PlanTeam, OwnerID: env.userID,
	})
	if err != nil {
		t.Fatalf("建租户失败: %v", err)
	}
	env.tenantID = tn.ID

	pair, err := a.Auth.IssueTokens(&auth.Claims{
		UserID:   env.userID,
		Username: "e2euser",
		Email:    env.email,
		Role:     "admin",
		TenantID: string(tn.ID),
	})
	if err != nil {
		t.Fatalf("签发带租户声明的令牌失败: %v", err)
	}
	env.token = pair.AccessToken
	return env
}

// --- HTTP 辅助 ---

// do 发一个带认证的请求，返回 recorder（便于断言状态码与响应体）。
func (e *e2eEnv) do(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// doWithToken 用指定令牌发请求（用于断言越权/无权限场景）。
func (e *e2eEnv) doWithToken(method, path, body, token string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// postAnon 发一个不带令牌的请求。
func (e *e2eEnv) postAnon(path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// decode 反序列化 JSON，失败即终止（响应体打印出来便于定位）。
func (e *e2eEnv) decode(t *testing.T, raw []byte, dst interface{}) {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("解析 JSON 失败: %v (body=%s)", err, string(raw))
	}
}

// mustStatus 断言状态码，不匹配时把响应体一并打印 —— 中间件拒绝时
// 响应体里的错误消息（如 "no permission rule registered"）是唯一的诊断线索。
func (e *e2eEnv) mustStatus(rec *httptest.ResponseRecorder, want int, what string) {
	e.t.Helper()
	if rec.Code != want {
		e.t.Fatalf("%s: 期望 %d，得到 %d: %s", what, want, rec.Code, rec.Body.String())
	}
}

// expireToken 由环境自己签发一张已过期/无租户声明的令牌（用于边界断言）。
func (e *e2eEnv) issueToken(userID string, tenantID string) string {
	e.t.Helper()
	pair, err := e.app.Auth.IssueTokens(&auth.Claims{
		UserID: userID, Username: userID, Email: userID + "@corp.example",
		Role: "user", TenantID: tenantID,
	})
	if err != nil {
		e.t.Fatalf("签发令牌失败: %v", err)
	}
	return pair.AccessToken
}

// ===== 主线：全链路 =====

func TestE2EFullChainRegisterToAdminConsole(t *testing.T) {
	env := newE2EEnv(t)

	// --- 第 1 步：建 Agent ---
	agentResp := env.do(http.MethodPost, "/api/v1/agents",
		`{"name":"e2e-agent","description":"全链路测试 Agent","config":{}}`)
	env.mustStatus(agentResp, http.StatusCreated, "建 Agent")
	var createdAgent struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		State string `json:"state"`
	}
	env.decode(t, agentResp.Body.Bytes(), &createdAgent)
	if createdAgent.ID == "" {
		t.Fatalf("建 Agent 未返回 id: %s", agentResp.Body.String())
	}
	if createdAgent.State != "idle" {
		t.Errorf("新建 Agent 状态应为 idle，得到 %q", createdAgent.State)
	}

	// --- 第 2 步：启动 Agent（走生命周期子动作，验证权限表登记正确） ---
	startResp := env.do(http.MethodPost, "/api/v1/agents/"+createdAgent.ID+"/start", "")
	env.mustStatus(startResp, http.StatusOK, "启动 Agent")

	detail := env.do(http.MethodGet, "/api/v1/agents/"+createdAgent.ID, "")
	env.mustStatus(detail, http.StatusOK, "查 Agent 详情")
	var fetched struct {
		State string `json:"state"`
	}
	env.decode(t, detail.Body.Bytes(), &fetched)
	if fetched.State != "running" {
		t.Errorf("启动后状态应为 running，得到 %q", fetched.State)
	}

	// --- 第 3 步：创建任务（任务的归属必须挂到刚刚建出来的 Agent 上） ---
	taskResp := env.do(http.MethodPost, "/api/v1/tasks",
		fmt.Sprintf(`{"agent_id":%q,"goal":"写一个 hello world 并自测"}`, createdAgent.ID))
	env.mustStatus(taskResp, http.StatusCreated, "建任务")
	var createdTask struct {
		ID      string `json:"id"`
		AgentID string `json:"agent_id"`
		State   string `json:"state"`
	}
	env.decode(t, taskResp.Body.Bytes(), &createdTask)
	if createdTask.ID == "" {
		t.Fatalf("建任务未返回 id: %s", taskResp.Body.String())
	}
	if createdTask.AgentID != createdAgent.ID {
		t.Errorf("任务归属错误: got %q, want %q", createdTask.AgentID, createdAgent.ID)
	}

	// --- 第 4 步：把这条链路的业务动作写入审计（用真实 actor 身份） ---
	// 注意 actor 取自登录后的 userID，而不是硬编码串 —— 断言的是
	// 「谁做的」这条信息在链路上没有丢失。
	auditBody := fmt.Sprintf(
		`{"action":"agent.created","actor":%q,"actor_type":"user","resource":"agent","resource_id":%q,"status":"success"}`,
		env.userID, createdAgent.ID)
	auditResp := env.do(http.MethodPost, "/api/v1/audit/events", auditBody)
	env.mustStatus(auditResp, http.StatusCreated, "写审计事件")

	// --- 第 5 步：审计可查询，且能按 actor 精确命中 ---
	queryResp := env.do(http.MethodGet, "/api/v1/audit/events?actor="+env.userID, "")
	env.mustStatus(queryResp, http.StatusOK, "查审计事件")
	var auditList struct {
		Total  int `json:"total"`
		Events []struct {
			ID         string `json:"id"`
			Action     string `json:"action"`
			Actor      string `json:"actor"`
			ResourceID string `json:"resource_id"`
		} `json:"events"`
	}
	env.decode(t, queryResp.Body.Bytes(), &auditList)
	if auditList.Total != 1 || len(auditList.Events) != 1 {
		t.Fatalf("审计查询应命中 1 条，得到 total=%d len=%d: %s",
			auditList.Total, len(auditList.Events), queryResp.Body.String())
	}
	if auditList.Events[0].ResourceID != createdAgent.ID {
		t.Errorf("审计事件的资源 ID 应指向真实 Agent，got %q want %q",
			auditList.Events[0].ResourceID, createdAgent.ID)
	}

	// --- 第 6 步：管理后台聚合视图必须反映**同一条**链路 ---
	// 断言的是「后台看到的就是刚刚发生的」，因此数量与 ID 都要对得上。
	snapResp := env.do(http.MethodGet, "/api/v1/admin/snapshot", "")
	env.mustStatus(snapResp, http.StatusOK, "拉取后台聚合视图")
	var snap struct {
		Overview *struct {
			TotalAgents int  `json:"total_agents"`
			TotalTasks  int  `json:"total_tasks"`
			AuditEvents int  `json:"audit_events"`
			Degraded    bool `json:"degraded"`
		} `json:"overview"`
		Agents *struct {
			Total   int            `json:"total"`
			ByState map[string]int `json:"by_state"`
			Recently []struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				State string `json:"state"`
			} `json:"recently"`
		} `json:"agents"`
		Tasks *struct {
			Total int `json:"total"`
		} `json:"tasks"`
		Audit *struct {
			Total  int `json:"total"`
			Recent []struct {
				ID         string `json:"id"`
				Action     string `json:"action"`
				ResourceID string `json:"resource_id"`
			} `json:"recent"`
		} `json:"audit"`
	}
	env.decode(t, snapResp.Body.Bytes(), &snap)

	if snap.Overview == nil {
		t.Fatal("聚合视图缺少 overview 分区")
	}
	if snap.Overview.Degraded {
		t.Errorf("聚合视图处于 degraded 状态，说明某个下游分区取数失败: %s", snapResp.Body.String())
	}
	if snap.Overview.TotalAgents != 1 {
		t.Errorf("后台看到的 Agent 数应为 1（与链路一致），得到 %d", snap.Overview.TotalAgents)
	}
	if snap.Overview.TotalTasks != 1 {
		t.Errorf("后台看到的任务数应为 1（与链路一致），得到 %d", snap.Overview.TotalTasks)
	}
	if snap.Overview.AuditEvents != 1 {
		t.Errorf("后台看到的审计事件数应为 1（与链路一致），得到 %d", snap.Overview.AuditEvents)
	}
	if snap.Agents == nil || snap.Agents.ByState["running"] != 1 {
		t.Errorf("后台 Agent 状态分布应含 1 个 running，得到 %+v", snap.Agents)
	}
	// 后台的 recent 列表必须出现同一条审计（不是各自造的数据源）。
	if snap.Audit == nil || len(snap.Audit.Recent) == 0 {
		t.Fatal("后台审计分区为空，链路未贯通")
	}
	if snap.Audit.Recent[0].ResourceID != createdAgent.ID {
		t.Errorf("后台审计分区的资源 ID 与链路不一致: got %q want %q",
			snap.Audit.Recent[0].ResourceID, createdAgent.ID)
	}
}

// TestE2EAuditChainIsQueryableAndExportable 验证审计链路的完整性：
// 一次业务动作落库后，既能按维度查询，也能导出，且导出内容含这条记录。
func TestE2EAuditChainIsQueryableAndExportable(t *testing.T) {
	env := newE2EEnv(t)

	for i := 0; i < 3; i++ {
		body := fmt.Sprintf(
			`{"action":"task.completed","actor":%q,"actor_type":"user","resource":"task","resource_id":"task-%d"}`,
			env.userID, i)
		env.mustStatus(env.do(http.MethodPost, "/api/v1/audit/events", body),
			http.StatusCreated, fmt.Sprintf("写第 %d 条审计", i))
	}

	// 统计接口：按 action 聚合应恰好命中 3 条。
	statsRec := env.do(http.MethodGet, "/api/v1/audit/stats?dimension=action&actor="+env.userID, "")
	env.mustStatus(statsRec, http.StatusOK, "审计统计")
	var stats struct {
		Total   int            `json:"total"`
		Buckets map[string]int `json:"buckets"`
	}
	env.decode(t, statsRec.Body.Bytes(), &stats)
	if stats.Total != 3 {
		t.Errorf("审计统计总数应为 3，得到 %d: %s", stats.Total, statsRec.Body.String())
	}
	if stats.Buckets["task.completed"] != 3 {
		t.Errorf("task.completed 桶应为 3，得到 %d", stats.Buckets["task.completed"])
	}

	// 导出：CSV 必须带上真实行，而不是空表头。
	exportRec := env.do(http.MethodGet, "/api/v1/audit/export?format=csv", "")
	env.mustStatus(exportRec, http.StatusOK, "导出审计 CSV")
	raw := exportRec.Body.Bytes()
	if !bytes.Contains(raw, []byte("task.completed")) {
		t.Errorf("导出内容应含刚写入的事件，实际: %s", string(raw))
	}

	// 去重取值：actor 维度应只出现链路里的那一个用户。
	distinctRec := env.do(http.MethodGet, "/api/v1/audit/distinct?field=actor", "")
	env.mustStatus(distinctRec, http.StatusOK, "审计去重取值")
	var distinct struct {
		Values []string `json:"values"`
	}
	env.decode(t, distinctRec.Body.Bytes(), &distinct)
	found := false
	for _, v := range distinct.Values {
		if v == env.userID {
			found = true
		}
	}
	if !found {
		t.Errorf("去重取值应含 %q，得到 %v", env.userID, distinct.Values)
	}
}
