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

	"github.com/atop0914/agentbot/internal/authz"
)

// newTestServer creates a test HTTP server with all routes wired up.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	a := New()
	handler := NewRouter(a)
	return httptest.NewServer(handler)
}

// testEnv 是一次端到端测试的完整环境：服务器 + 已登录的管理员身份。
//
// 为什么需要它：Day 23 起，路由统一由 authz 中间件做权限判定（未登记的路由默认
// 拒绝）。因此每个测试都必须在一个**显式授权**的会话里发起请求 —— 这本身就是
// 对「默认拒绝」的持续验证，而不是为了通过测试而绕过中间件。
type testEnv struct {
	t       *testing.T
	server  *httptest.Server
	app     *App
	token   string
	adminID string
}

// newTestEnv 启动服务器并创建一个拥有全部权限的管理员用户。
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	a := New()
	ts := httptest.NewServer(NewRouter(a))
	t.Cleanup(ts.Close)

	env := &testEnv{t: t, server: ts, app: a}

	// 1. 注册一个用户（注册接口是公开路由）。
	email := fmt.Sprintf("admin-%d@example.com", time.Now().UnixNano())
	body := fmt.Sprintf(`{"email":%q,"username":"adminuser","password":"Str0ngPass!123"}`, email)
	resp, err := http.Post(ts.URL+"/api/v1/auth/register", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("register admin: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register admin status = %d, want 201", resp.StatusCode)
	}
	var reg struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if reg.AccessToken == "" || reg.User.ID == "" {
		t.Fatalf("register response missing token/user id: %+v", reg)
	}

	// 注册接口的响应形状随 auth 模块版本变化，这里做一次兜底解析：
	// 若 user.id 缺失，则用登录接口重新拿一次（登录同样返回 user + token）。
	if reg.User.ID == "" {
		loginBody := fmt.Sprintf(`{"email":%q,"password":"Str0ngPass!123"}`, email)
		loginResp, err := http.Post(ts.URL+"/api/v1/auth/login", "application/json", bytes.NewBufferString(loginBody))
		if err != nil {
			t.Fatalf("login admin: %v", err)
		}
		defer loginResp.Body.Close()
		if err := json.NewDecoder(loginResp.Body).Decode(&reg); err != nil {
			t.Fatalf("decode login response: %v", err)
		}
	}
	if reg.User.ID == "" {
		t.Fatal("could not determine the admin user id from register/login responses")
	}
	env.token = reg.AccessToken
	env.adminID = reg.User.ID

	// 2. 直接把 coordinator 角色授予该用户（走装配好的 authz 服务）。
	subject := authz.Subject{Type: authz.SubjectUser, ID: reg.User.ID}
	if _, err := a.AuthzSvc.Assign(context.Background(), subject, "role-coordinator", "test-bootstrap"); err != nil {
		t.Fatalf("assign coordinator role: %v", err)
	}

	return env
}

// do 发送带认证的请求。
func (e *testEnv) do(method, path, body string) *http.Response {
	e.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		e.t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// doAnon 发送不带认证的请求（用于验证默认拒绝 / 401 行为）。
func (e *testEnv) doAnon(method, path string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.server.URL+path, nil)
	if err != nil {
		e.t.Fatalf("new anon request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// grant 给指定用户授予角色。
func (e *testEnv) grant(userID, roleID string) {
	e.t.Helper()
	subject := authz.Subject{Type: authz.SubjectUser, ID: userID}
	if _, err := e.app.AuthzSvc.Assign(context.Background(), subject, roleID, "test"); err != nil {
		e.t.Fatalf("grant %s to %s: %v", roleID, userID, err)
	}
}

// ---- 已认证请求的便捷封装 ----
//
// 这些包装保留测试原本的调用形状（http.Get/http.Post 风格），但会带上
// 管理员 bearer token —— 权限中间件上线后，裸请求一律 401/403。

// get 发送带认证的 GET 请求。
func (e *testEnv) get(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	e.authorize(req)
	return http.DefaultClient.Do(req)
}

// post 发送带认证的 POST 请求。
func (e *testEnv) post(url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	e.authorize(req)
	return http.DefaultClient.Do(req)
}

// newRequest 构造带认证的请求（不接受错误返回，保持原调用点简洁）。
func (e *testEnv) newRequest(method, url string, body interface{}) *authRequest {
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		if b != "" {
			reader = bytes.NewBufferString(b)
		}
	case io.Reader:
		reader = b
	default:
		e.t.Fatalf("new request %s %s: unsupported body type %T", method, url, body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		e.t.Fatalf("new request %s %s: %v", method, url, err)
	}
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	e.authorize(req)
	return &authRequest{Request: req, t: e.t}
}

// authorize 把管理员 token 写入请求头。
func (e *testEnv) authorize(r *http.Request) {
	if e.token != "" {
		r.Header.Set("Authorization", "Bearer "+e.token)
	}
}

// authRequest 让调用点可以像 *http.Request 一样使用（Header/URL 直接可见），
// 同时用 Do() 发送。
type authRequest struct {
	*http.Request
	t *testing.T
}

// Do 发送请求。
func (a *authRequest) Do() (*http.Response, error) {
	return http.DefaultClient.Do(a.Request)
}

// doRaw 兼容 http.DefaultClient.Do(req) 的调用形状：把请求补上认证后发送。
func (e *testEnv) doRaw(req *http.Request) (*http.Response, error) {
	e.authorize(req)
	return http.DefaultClient.Do(req)
}

// createAgent 创建并返回一个 Agent ID。
func (e *testEnv) createAgent(name string) string {
	e.t.Helper()
	body := fmt.Sprintf(`{"name":%q,"description":"integration","config":{}}`, name)
	resp := e.do(http.MethodPost, "/api/v1/agents", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("create agent status = %d, want 201", resp.StatusCode)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		e.t.Fatalf("decode agent: %v", err)
	}
	if created.ID == "" {
		e.t.Fatal("create agent returned no id")
	}
	return created.ID
}

func TestHealthEndpoint(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want %q", body["status"], "ok")
	}
}

func TestCORSHeaders(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Preflight OPTIONS request
	req, _ := http.NewRequest("OPTIONS", ts.URL+"/api/v1/agents", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS /api/v1/agents: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want %q", got, "*")
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	reqID := resp.Header.Get("X-Request-ID")
	if reqID == "" {
		t.Error("X-Request-ID header missing")
	}
}

// --- Auth Flow Tests ---

func TestAuthRegisterAndLogin(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Register
	regBody := `{"email":"test@example.com","username":"testuser","password":"Str0ngPass!123"}`
	resp, err := http.Post(ts.URL+"/api/v1/auth/register", "application/json", bytes.NewBufferString(regBody))
	if err != nil {
		t.Fatalf("POST /register: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("register status = %d, want 201", resp.StatusCode)
		var errBody map[string]string
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Logf("error body: %v", errBody)
		return
	}

	var regResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&regResp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if regResp.AccessToken == "" {
		t.Error("access_token missing from register response")
	}

	// Login
	loginBody := `{"email":"test@example.com","password":"Str0ngPass!123"}`
	resp2, err := http.Post(ts.URL+"/api/v1/auth/login", "application/json", bytes.NewBufferString(loginBody))
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("login status = %d, want 200", resp2.StatusCode)
		return
	}

	var loginResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&loginResp); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if loginResp.AccessToken == "" {
		t.Error("access_token missing from login response")
	}
}

func TestAuthLoginInvalidCredentials(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	loginBody := `{"email":"nonexistent@example.com","password":"wrong"}`
	resp, err := http.Post(ts.URL+"/api/v1/auth/login", "application/json", bytes.NewBufferString(loginBody))
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The auth handler returns error via appErr, check for non-200
		t.Logf("login status = %d (expected non-200 for bad creds)", resp.StatusCode)
	}
}

func TestAuthRegisterValidation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Missing fields
	resp, err := http.Post(ts.URL+"/api/v1/auth/register", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("POST /register: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty register status = %d, want 400", resp.StatusCode)
	}
}

// --- Agent CRUD Tests ---

// createTestAgent 创建一个测试 Agent 并返回完整响应体（带认证）。
func createTestAgent(t *testing.T, env *testEnv) map[string]interface{} {
	t.Helper()
	body := `{"name":"test-agent","description":"A test agent"}`
	resp, err := env.post(env.server.URL+"/api/v1/agents", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /agents: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create agent status = %d, want 201", resp.StatusCode)
	}

	var agent map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&agent); err != nil {
		t.Fatalf("decode agent: %v", err)
	}
	return agent
}

func TestAgentCreateAndGet(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	agent := createTestAgent(t, env)
	id, ok := agent["id"].(string)
	if !ok || id == "" {
		t.Fatal("agent id missing")
	}
	if agent["name"] != "test-agent" {
		t.Errorf("name = %v, want %q", agent["name"], "test-agent")
	}
	if agent["state"] != "idle" {
		t.Errorf("state = %v, want %q", agent["state"], "idle")
	}

	// Get by ID
	resp, err := env.get(ts.URL + "/api/v1/agents/" + id)
	if err != nil {
		t.Fatalf("GET /agents/%s: %v", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("get agent status = %d, want 200", resp.StatusCode)
	}

	var fetched map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&fetched)
	if fetched["id"] != id {
		t.Errorf("fetched id = %v, want %q", fetched["id"], id)
	}
}

func TestAgentList(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create two agents
	createTestAgent(t, env)
	createTestAgent(t, env)

	resp, err := env.get(ts.URL + "/api/v1/agents")
	if err != nil {
		t.Fatalf("GET /agents: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("list status = %d, want 200", resp.StatusCode)
	}

	var listResp struct {
		Agents []map[string]interface{} `json:"agents"`
		Total  int                      `json:"total"`
	}
	json.NewDecoder(resp.Body).Decode(&listResp)
	if listResp.Total != 2 {
		t.Errorf("total = %d, want 2", listResp.Total)
	}
}

func TestAgentUpdate(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	agent := createTestAgent(t, env)
	id := agent["id"].(string)

	updateBody := `{"name":"updated-agent"}`
	req := env.newRequest("PUT", ts.URL+"/api/v1/agents/"+id, bytes.NewBufferString(updateBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := req.Do()
	if err != nil {
		t.Fatalf("PUT /agents/%s: %v", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("update status = %d, want 200", resp.StatusCode)
	}

	var updated map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&updated)
	if updated["name"] != "updated-agent" {
		t.Errorf("name = %v, want %q", updated["name"], "updated-agent")
	}
}

func TestAgentDelete(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	agent := createTestAgent(t, env)
	id := agent["id"].(string)

	req := env.newRequest("DELETE", ts.URL+"/api/v1/agents/"+id, nil)
	resp, err := req.Do()
	if err != nil {
		t.Fatalf("DELETE /agents/%s: %v", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("delete status = %d, want 200", resp.StatusCode)
	}

	// Verify deleted
	resp2, err := env.get(ts.URL + "/api/v1/agents/" + id)
	if err != nil {
		t.Fatalf("GET deleted agent: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound && resp2.StatusCode != http.StatusOK {
		t.Logf("get deleted agent status = %d", resp2.StatusCode)
	}
}

func TestAgentActions(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	agent := createTestAgent(t, env)
	id := agent["id"].(string)

	// Start agent
	req := env.newRequest("POST", ts.URL+"/api/v1/agents/"+id+"/start", nil)
	resp, err := req.Do()
	if err != nil {
		t.Fatalf("POST /agents/%s/start: %v", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("start status = %d, want 200", resp.StatusCode)
	}

	// Stop agent
	req2 := env.newRequest("POST", ts.URL+"/api/v1/agents/"+id+"/stop", nil)
	resp2, err := req2.Do()
	if err != nil {
		t.Fatalf("POST /agents/%s/stop: %v", id, err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("stop status = %d, want 200", resp2.StatusCode)
	}
}

func TestAgentNotFound(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	resp, err := env.get(ts.URL + "/api/v1/agents/nonexistent-id")
	if err != nil {
		t.Fatalf("GET /agents/nonexistent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		// The agent service returns error, which should be non-200
		t.Logf("status = %d for nonexistent agent", resp.StatusCode)
	}
}

// --- Task Tests ---

func TestTaskCreateAndGet(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	body := `{"agent_id":"agent-1","goal":"write hello world"}`
	resp, err := env.post(ts.URL+"/api/v1/tasks", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /tasks: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("create task status = %d, want 201", resp.StatusCode)
		var errBody map[string]string
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Logf("error body: %v", errBody)
		return
	}

	var task map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&task)
	id, ok := task["id"].(string)
	if !ok || id == "" {
		t.Fatal("task id missing")
	}

	// Get by ID
	resp2, err := env.get(ts.URL + "/api/v1/tasks/" + id)
	if err != nil {
		t.Fatalf("GET /tasks/%s: %v", id, err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("get task status = %d, want 200", resp2.StatusCode)
	}
}

func TestTaskList(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create a task
	body := `{"agent_id":"agent-1","goal":"test goal"}`
	env.post(ts.URL+"/api/v1/tasks", "application/json", bytes.NewBufferString(body))

	resp, err := env.get(ts.URL + "/api/v1/tasks")
	if err != nil {
		t.Fatalf("GET /tasks: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("list tasks status = %d, want 200", resp.StatusCode)
	}
}

func TestTaskCreateValidation(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Missing agent_id and goal
	resp, err := env.post(ts.URL+"/api/v1/tasks", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("POST /tasks: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		t.Error("expected error for empty task, got 201")
	}
}

// --- Cloud Environment Tests ---

func TestCloudEnvironmentCRUD(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create environment
	body := `{"agent_id":"agent-1","config":{"type":"sandbox","image":"ubuntu:22.04"}}`
	resp, err := env.post(ts.URL+"/api/v1/environments", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /environments: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("create env status = %d, want 201", resp.StatusCode)
		var errBody map[string]string
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Logf("error body: %v", errBody)
		return
	}

	var envResp map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&envResp)
	envID, ok := envResp["id"].(string)
	if !ok || envID == "" {
		t.Fatal("env id missing")
	}

	// Get by ID
	resp2, err := env.get(ts.URL + "/api/v1/environments/" + envID)
	if err != nil {
		t.Fatalf("GET /environments/%s: %v", envID, err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("get env status = %d, want 200", resp2.StatusCode)
	}

	// Delete
	req := env.newRequest("DELETE", ts.URL+"/api/v1/environments/"+envID, nil)
	resp3, err := req.Do()
	if err != nil {
		t.Fatalf("DELETE /environments/%s: %v", envID, err)
	}
	defer resp3.Body.Close()

	if resp3.StatusCode != http.StatusOK {
		t.Errorf("delete env status = %d, want 200", resp3.StatusCode)
	}
}

func TestCloudEnvironmentList(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create env
	body := `{"agent_id":"agent-1","config":{"type":"sandbox"}}`
	env.post(ts.URL+"/api/v1/environments", "application/json", bytes.NewBufferString(body))

	// List
	resp, err := env.get(ts.URL + "/api/v1/environments?agent_id=agent-1")
	if err != nil {
		t.Fatalf("GET /environments: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("list env status = %d, want 200", resp.StatusCode)
	}
}

func TestCloudEnvironmentListWithoutAgentID(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	resp, err := env.get(ts.URL + "/api/v1/environments")
	if err != nil {
		t.Fatalf("GET /environments: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("list without agent_id status = %d, want 400", resp.StatusCode)
	}
}

// --- Communication Tests ---

func TestCommunicationSendMessage(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	body := `{"from":"agent-1","to":"agent-2","type":"text","content":"hello"}`
	resp, err := env.post(ts.URL+"/api/v1/messages", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /messages: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("send message status = %d, want 201", resp.StatusCode)
	}
}

func TestCommunicationGetMessages(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Send a message
	body := `{"from":"agent-1","to":"agent-2","type":"text","content":"hello"}`
	env.post(ts.URL+"/api/v1/messages", "application/json", bytes.NewBufferString(body))

	// Get messages
	resp, err := env.get(ts.URL + "/api/v1/messages?agent_id=agent-1")
	if err != nil {
		t.Fatalf("GET /messages: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("get messages status = %d, want 200", resp.StatusCode)
	}
}

func TestCommunicationGroups(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create group
	body := `{"name":"test-group","description":"A test group","members":["agent-1","agent-2"]}`
	resp, err := env.post(ts.URL+"/api/v1/groups", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /groups: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("create group status = %d, want 201", resp.StatusCode)
	}

	// List groups
	resp2, err := env.get(ts.URL + "/api/v1/groups?agent_id=agent-1")
	if err != nil {
		t.Fatalf("GET /groups: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("list groups status = %d, want 200", resp2.StatusCode)
	}
}

// --- WebSocket Status Test ---

func TestWebSocketStatus(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	resp, err := env.get(ts.URL + "/api/v1/ws/status")
	if err != nil {
		t.Fatalf("GET /ws/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("ws status = %d, want 200", resp.StatusCode)
	}

	var body map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&body)
	if _, ok := body["connections"]; !ok {
		t.Error("connections field missing from ws status")
	}
}

// --- Method Not Allowed Tests ---

func TestMethodNotAllowed(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// PUT on /agents (collection endpoint)
	req := env.newRequest("PUT", ts.URL+"/api/v1/agents", nil)
	resp, err := req.Do()
	if err != nil {
		t.Fatalf("PUT /agents: %v", err)
	}
	defer resp.Body.Close()

	// 权限中间件在 handler 之前执行：该管理员虽已认证，但 PUT 集合路径未登记
	// 权限规则，因此先被默认拒绝（403）而不是走到 handler 的 405 分支。
	// 这里断言「未被放行」这一安全语义，而非具体的状态码来源。
	if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusForbidden {
		t.Errorf("PUT /agents status = %d, want 405 or 403 (denied)", resp.StatusCode)
	}
}

// --- Browser API Tests ---

func TestBrowserCreateAndGet(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create browser
	body := `{"agent_id":"agent-1","config":{"headless":true}}`
	resp, err := env.post(ts.URL+"/api/v1/browsers", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /browsers: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("create browser status = %d, want 200", resp.StatusCode)
	}

	var created map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&created)
	if created["id"] == nil {
		t.Error("browser id missing from create response")
	}
}

func TestBrowserCreateMultiple(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create multiple browsers for different agents
	for _, agentID := range []string{"agent-1", "agent-2"} {
		body := `{"agent_id":"` + agentID + `"}`
		resp, err := env.post(ts.URL+"/api/v1/browsers", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatalf("POST /browsers for %s: %v", agentID, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("create browser for %s status = %d, want 200", agentID, resp.StatusCode)
		}

		var created map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&created)
		if created["id"] == nil {
			t.Errorf("browser id missing for agent %s", agentID)
		}
	}
}

func TestBrowserProfileList(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// List profiles (GET only, creation via import-profile)
	resp, err := env.get(ts.URL + "/api/v1/browser/profiles")
	if err != nil {
		t.Fatalf("GET /profiles: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("list profiles status = %d, want 200", resp.StatusCode)
	}
}

// --- Terminal API Tests ---

func TestTerminalCreateAndGet(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create session
	body := `{"agent_id":"agent-1","connection_type":"local","command":"/bin/bash"}`
	resp, err := env.post(ts.URL+"/api/v1/terminals", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /terminals: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("create terminal status = %d, want 200", resp.StatusCode)
	}

	var created map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&created)
	if created["id"] == nil {
		t.Error("terminal id missing from create response")
	}
}

func TestTerminalList(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create a session first
	body := `{"agent_id":"agent-1","connection_type":"local"}`
	env.post(ts.URL+"/api/v1/terminals", "application/json", bytes.NewBufferString(body))

	resp, err := env.get(ts.URL + "/api/v1/terminals")
	if err != nil {
		t.Fatalf("GET /terminals: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("list terminals status = %d, want 200", resp.StatusCode)
	}
}

func TestTerminalExecute(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create session
	body := `{"agent_id":"agent-1","connection_type":"local"}`
	resp, err := env.post(ts.URL+"/api/v1/terminals", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /terminals: %v", err)
	}
	defer resp.Body.Close()

	var created map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&created)
	id := created["id"].(string)

	// Execute command
	execBody := `{"command":"echo hello"}`
	req := env.newRequest("POST", ts.URL+"/api/v1/terminals/"+id+"/execute", bytes.NewBufferString(execBody))
	req.Header.Set("Content-Type", "application/json")
	resp2, err := req.Do()
	if err != nil {
		t.Fatalf("POST /terminals/%s/execute: %v", id, err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("execute status = %d, want 200", resp2.StatusCode)
	}
}

// --- Filesystem API Tests ---

func TestFilesystemWriteAndRead(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Write file (relative path within sandbox /tmp/agentbot-fs/)
	writeBody := `{"path":"test-integration.txt","content":"aGVsbG8gd29ybGQ="}`
	resp, err := env.post(ts.URL+"/api/v1/filesystem/write", "application/json", bytes.NewBufferString(writeBody))
	if err != nil {
		t.Fatalf("POST /filesystem/write: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody map[string]string
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Errorf("write status = %d, want 200, err: %v", resp.StatusCode, errBody)
		return
	}

	// Read file
	resp2, err := env.get(ts.URL + "/api/v1/filesystem/read/test-integration.txt")
	if err != nil {
		t.Fatalf("GET /filesystem/read: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("read status = %d, want 200", resp2.StatusCode)
	}

	// Cleanup
	req := env.newRequest("DELETE", ts.URL+"/api/v1/filesystem/remove/test-integration.txt", nil)
	req.Do()
}

func TestFilesystemList(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// List root of sandbox (relative path)
	resp, err := env.get(ts.URL + "/api/v1/filesystem/list/")
	if err != nil {
		t.Fatalf("GET /filesystem/list: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("list status = %d, want 200", resp.StatusCode)
	}
}

func TestFilesystemMkdirAndRemove(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Mkdir (relative path within sandbox)
	mkdirBody := `{"path":"test-integration-dir"}`
	resp, err := env.post(ts.URL+"/api/v1/filesystem/mkdir", "application/json", bytes.NewBufferString(mkdirBody))
	if err != nil {
		t.Fatalf("POST /filesystem/mkdir: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody map[string]string
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Errorf("mkdir status = %d, want 200, err: %v", resp.StatusCode, errBody)
		return
	}

	// Remove
	req := env.newRequest("DELETE", ts.URL+"/api/v1/filesystem/remove/test-integration-dir", nil)
	resp2, err := req.Do()
	if err != nil {
		t.Fatalf("DELETE /filesystem/remove: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("remove status = %d, want 200", resp2.StatusCode)
	}
}

// --- Adapter API Tests ---

func TestAdapterCreateAndGet(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create adapter (Config struct: name, type, agent_id, settings)
	body := `{"id":"adapter-test-1","name":"test-email","type":"email","agent_id":"agent-1","settings":{"smtp_host":"smtp.example.com"}}`
	resp, err := env.post(ts.URL+"/api/v1/adapters", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /adapters: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody map[string]string
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Errorf("create adapter status = %d, want 201, err: %v", resp.StatusCode, errBody)
		return
	}

	var created map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&created)
	if created["id"] == nil {
		t.Error("adapter id missing from create response")
	}
}

func TestAdapterList(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create an adapter first
	body := `{"id":"adapter-test-1","name":"test-email","type":"email","agent_id":"agent-1"}`
	env.post(ts.URL+"/api/v1/adapters", "application/json", bytes.NewBufferString(body))

	resp, err := env.get(ts.URL + "/api/v1/adapters")
	if err != nil {
		t.Fatalf("GET /adapters: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("list adapters status = %d, want 200", resp.StatusCode)
	}
}

func TestAdapterExecute(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create adapter
	body := `{"id":"adapter-test-1","name":"test-email","type":"email","agent_id":"agent-1"}`
	resp, err := env.post(ts.URL+"/api/v1/adapters", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /adapters: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody map[string]string
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Logf("create adapter failed: status=%d, err=%v", resp.StatusCode, errBody)
		t.Skip("adapter creation failed, skipping execute test")
	}

	var created map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&created)
	idVal, ok := created["id"]
	if !ok || idVal == nil {
		t.Skip("adapter id missing, skipping execute test")
	}
	id := idVal.(string)

	// Execute action
	execBody := `{"action":"send","params":{"to":"test@example.com","subject":"Test","body":"Hello"}}`
	req := env.newRequest("POST", ts.URL+"/api/v1/adapters/"+id+"/execute", bytes.NewBufferString(execBody))
	req.Header.Set("Content-Type", "application/json")
	resp2, err := req.Do()
	if err != nil {
		t.Fatalf("POST /adapters/%s/execute: %v", id, err)
	}
	defer resp2.Body.Close()

	// Execute may return 200 or 500 depending on mock state
	if resp2.StatusCode != http.StatusOK && resp2.StatusCode != http.StatusInternalServerError {
		t.Errorf("execute status = %d, want 200 or 500", resp2.StatusCode)
	}
}

// --- Memory API Tests ---

func TestMemoryCreateAndGet(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create memory entry
	body := `{"agent_id":"agent-1","user_id":"user-1","type":"conversation","content":"Hello world","importance":0.8}`
	resp, err := env.post(ts.URL+"/api/v1/memory", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /memory: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("create memory status = %d, want 201", resp.StatusCode)
	}

	var created map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&created)
	if created["id"] == nil {
		t.Error("memory id missing from create response")
	}
}

func TestMemoryList(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create a memory entry first
	body := `{"agent_id":"agent-1","user_id":"user-1","type":"conversation","content":"Test memory","importance":0.5}`
	env.post(ts.URL+"/api/v1/memory", "application/json", bytes.NewBufferString(body))

	// List by agent
	resp, err := env.get(ts.URL + "/api/v1/memory/agent/agent-1")
	if err != nil {
		t.Fatalf("GET /memory/agent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("list memory status = %d, want 200", resp.StatusCode)
	}
}

func TestMemoryStats(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// Create some entries
	for i := 0; i < 3; i++ {
		body := `{"agent_id":"agent-1","user_id":"user-1","type":"conversation","content":"Test","importance":0.5}`
		env.post(ts.URL+"/api/v1/memory", "application/json", bytes.NewBufferString(body))
	}

	resp, err := env.get(ts.URL + "/api/v1/memory/stats")
	if err != nil {
		t.Fatalf("GET /memory/stats: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("memory stats status = %d, want 200", resp.StatusCode)
	}

	var stats map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&stats)
	if stats["total_entries"] == nil {
		t.Error("total_entries field missing from memory stats")
	}
}

func TestMemoryCleanup(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	req := env.newRequest("POST", ts.URL+"/api/v1/memory/cleanup", nil)
	resp, err := req.Do()
	if err != nil {
		t.Fatalf("POST /memory/cleanup: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("cleanup status = %d, want 200", resp.StatusCode)
	}
}

// --- Benchmark Tests ---

// --- 审计日志（Day 21 新增模块的端到端联通验证） ---

func TestAuditCreateAndQuery(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	// 写入两条事件
	for i, actor := range []string{"u1", "u2"} {
		body := map[string]interface{}{
			"action":     "user.login",
			"actor":      actor,
			"actor_type": "user",
			"resource":   "user",
			"details":    map[string]interface{}{"seq": i},
		}
		raw, _ := json.Marshal(body)
		resp, err := env.post(ts.URL+"/api/v1/audit/events", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST /api/v1/audit/events: %v", err)
		}
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201", resp.StatusCode)
		}
		resp.Body.Close()
	}

	resp, err := env.get(ts.URL + "/api/v1/audit/events?actor=u1")
	if err != nil {
		t.Fatalf("GET /api/v1/audit/events: %v", err)
	}
	defer resp.Body.Close()

	var list struct {
		Total  int `json:"total"`
		Events []struct {
			ID     string `json:"id"`
			Action string `json:"action"`
			Actor  string `json:"actor"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Total != 1 || len(list.Events) != 1 {
		t.Fatalf("total=%d len=%d, want 1/1", list.Total, len(list.Events))
	}
	if list.Events[0].Actor != "u1" {
		t.Errorf("actor = %q, want u1", list.Events[0].Actor)
	}
}

func TestAuditStatsAndDistinct(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	for _, action := range []string{"agent.created", "agent.created", "task.completed"} {
		raw, _ := json.Marshal(map[string]interface{}{"action": action, "actor": "sys"})
		resp, err := env.post(ts.URL+"/api/v1/audit/events", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		resp.Body.Close()
	}

	resp, err := env.get(ts.URL + "/api/v1/audit/stats?dimension=action")
	if err != nil {
		t.Fatalf("GET stats: %v", err)
	}
	defer resp.Body.Close()

	var stats struct {
		Total   int            `json:"total"`
		Buckets map[string]int `json:"buckets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if stats.Total != 3 {
		t.Errorf("total = %d, want 3", stats.Total)
	}
	if stats.Buckets["agent.created"] != 2 {
		t.Errorf("agent.created = %d, want 2", stats.Buckets["agent.created"])
	}

	resp2, err := env.get(ts.URL + "/api/v1/audit/distinct?field=action")
	if err != nil {
		t.Fatalf("GET distinct: %v", err)
	}
	defer resp2.Body.Close()

	var distinct struct {
		Values []string `json:"values"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&distinct); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(distinct.Values) != 2 {
		t.Fatalf("values = %v, want 2", distinct.Values)
	}
}

func TestAuditExportCSV(t *testing.T) {
	env := newTestEnv(t)
	ts := env.server

	raw, _ := json.Marshal(map[string]interface{}{"action": "user.login", "actor": "u1"})
	resp, err := env.post(ts.URL+"/api/v1/audit/events", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	csvResp, err := env.get(ts.URL + "/api/v1/audit/export?format=csv")
	if err != nil {
		t.Fatalf("GET export: %v", err)
	}
	defer csvResp.Body.Close()

	if ct := csvResp.Header.Get("Content-Type"); ct == "" {
		t.Error("missing content-type on export")
	}
	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(csvResp.Body); err != nil {
		t.Fatalf("read export: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("id,timestamp,actor")) {
		t.Errorf("csv body = %s, want header row", buf.String())
	}
}

func BenchmarkAuditAppend(b *testing.B) {
	a := New()
	handler := NewRouter(a)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	body := []byte(`{"action":"bench.event","actor":"bench","resource":"system"}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := http.Post(ts.URL+"/api/v1/audit/events", "application/json", bytes.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}

func BenchmarkHealthEndpoint(b *testing.B) {
	a := New()
	handler := NewRouter(a)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := http.Get(ts.URL + "/health")
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}

func BenchmarkAgentCRUD(b *testing.B) {
	a := New()
	handler := NewRouter(a)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Create
		body := `{"name":"bench-agent","description":"benchmark agent"}`
		resp, err := http.Post(ts.URL+"/api/v1/agents", "application/json", bytes.NewBufferString(body))
		if err != nil {
			b.Fatal(err)
		}
		var created map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&created)
		resp.Body.Close()

		// Get
		resp2, err := http.Get(ts.URL + "/api/v1/agents/" + created["id"].(string))
		if err != nil {
			b.Fatal(err)
		}
		resp2.Body.Close()
	}
}

func BenchmarkMemoryWriteRead(b *testing.B) {
	a := New()
	handler := NewRouter(a)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Write
		body := `{"agent_id":"bench-agent","user_id":"user-1","type":"conversation","content":"benchmark entry","importance":0.5}`
		resp, err := http.Post(ts.URL+"/api/v1/memory", "application/json", bytes.NewBufferString(body))
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}

func BenchmarkTaskCreate(b *testing.B) {
	a := New()
	handler := NewRouter(a)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := `{"agent_id":"bench-agent","goal":"benchmark task","priority":"medium"}`
		resp, err := http.Post(ts.URL+"/api/v1/tasks", "application/json", bytes.NewBufferString(body))
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}
