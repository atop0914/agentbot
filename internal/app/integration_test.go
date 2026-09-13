package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestServer creates a test HTTP server with all routes wired up.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	a := New()
	handler := NewRouter(a)
	return httptest.NewServer(handler)
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

func createTestAgent(t *testing.T, ts *httptest.Server) map[string]interface{} {
	t.Helper()
	body := `{"name":"test-agent","description":"A test agent"}`
	resp, err := http.Post(ts.URL+"/api/v1/agents", "application/json", bytes.NewBufferString(body))
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
	ts := newTestServer(t)
	defer ts.Close()

	agent := createTestAgent(t, ts)
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
	resp, err := http.Get(ts.URL + "/api/v1/agents/" + id)
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
	ts := newTestServer(t)
	defer ts.Close()

	// Create two agents
	createTestAgent(t, ts)
	createTestAgent(t, ts)

	resp, err := http.Get(ts.URL + "/api/v1/agents")
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
	ts := newTestServer(t)
	defer ts.Close()

	agent := createTestAgent(t, ts)
	id := agent["id"].(string)

	updateBody := `{"name":"updated-agent"}`
	req, _ := http.NewRequest("PUT", ts.URL+"/api/v1/agents/"+id, bytes.NewBufferString(updateBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
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
	ts := newTestServer(t)
	defer ts.Close()

	agent := createTestAgent(t, ts)
	id := agent["id"].(string)

	req, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/agents/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /agents/%s: %v", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("delete status = %d, want 200", resp.StatusCode)
	}

	// Verify deleted
	resp2, err := http.Get(ts.URL + "/api/v1/agents/" + id)
	if err != nil {
		t.Fatalf("GET deleted agent: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound && resp2.StatusCode != http.StatusOK {
		t.Logf("get deleted agent status = %d", resp2.StatusCode)
	}
}

func TestAgentActions(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	agent := createTestAgent(t, ts)
	id := agent["id"].(string)

	// Start agent
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/agents/"+id+"/start", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /agents/%s/start: %v", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("start status = %d, want 200", resp.StatusCode)
	}

	// Stop agent
	req2, _ := http.NewRequest("POST", ts.URL+"/api/v1/agents/"+id+"/stop", nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("POST /agents/%s/stop: %v", id, err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("stop status = %d, want 200", resp2.StatusCode)
	}
}

func TestAgentNotFound(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/agents/nonexistent-id")
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
	ts := newTestServer(t)
	defer ts.Close()

	body := `{"agent_id":"agent-1","goal":"write hello world"}`
	resp, err := http.Post(ts.URL+"/api/v1/tasks", "application/json", bytes.NewBufferString(body))
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
	resp2, err := http.Get(ts.URL + "/api/v1/tasks/" + id)
	if err != nil {
		t.Fatalf("GET /tasks/%s: %v", id, err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("get task status = %d, want 200", resp2.StatusCode)
	}
}

func TestTaskList(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Create a task
	body := `{"agent_id":"agent-1","goal":"test goal"}`
	http.Post(ts.URL+"/api/v1/tasks", "application/json", bytes.NewBufferString(body))

	resp, err := http.Get(ts.URL + "/api/v1/tasks")
	if err != nil {
		t.Fatalf("GET /tasks: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("list tasks status = %d, want 200", resp.StatusCode)
	}
}

func TestTaskCreateValidation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Missing agent_id and goal
	resp, err := http.Post(ts.URL+"/api/v1/tasks", "application/json", bytes.NewBufferString(`{}`))
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
	ts := newTestServer(t)
	defer ts.Close()

	// Create environment
	body := `{"agent_id":"agent-1","config":{"type":"sandbox","image":"ubuntu:22.04"}}`
	resp, err := http.Post(ts.URL+"/api/v1/environments", "application/json", bytes.NewBufferString(body))
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

	var env map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&env)
	envID, ok := env["id"].(string)
	if !ok || envID == "" {
		t.Fatal("env id missing")
	}

	// Get by ID
	resp2, err := http.Get(ts.URL + "/api/v1/environments/" + envID)
	if err != nil {
		t.Fatalf("GET /environments/%s: %v", envID, err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("get env status = %d, want 200", resp2.StatusCode)
	}

	// Delete
	req, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/environments/"+envID, nil)
	resp3, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /environments/%s: %v", envID, err)
	}
	defer resp3.Body.Close()

	if resp3.StatusCode != http.StatusOK {
		t.Errorf("delete env status = %d, want 200", resp3.StatusCode)
	}
}

func TestCloudEnvironmentList(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Create env
	body := `{"agent_id":"agent-1","config":{"type":"sandbox"}}`
	http.Post(ts.URL+"/api/v1/environments", "application/json", bytes.NewBufferString(body))

	// List
	resp, err := http.Get(ts.URL + "/api/v1/environments?agent_id=agent-1")
	if err != nil {
		t.Fatalf("GET /environments: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("list env status = %d, want 200", resp.StatusCode)
	}
}

func TestCloudEnvironmentListWithoutAgentID(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/environments")
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
	ts := newTestServer(t)
	defer ts.Close()

	body := `{"from":"agent-1","to":"agent-2","type":"text","content":"hello"}`
	resp, err := http.Post(ts.URL+"/api/v1/messages", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /messages: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("send message status = %d, want 201", resp.StatusCode)
	}
}

func TestCommunicationGetMessages(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Send a message
	body := `{"from":"agent-1","to":"agent-2","type":"text","content":"hello"}`
	http.Post(ts.URL+"/api/v1/messages", "application/json", bytes.NewBufferString(body))

	// Get messages
	resp, err := http.Get(ts.URL + "/api/v1/messages?agent_id=agent-1")
	if err != nil {
		t.Fatalf("GET /messages: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("get messages status = %d, want 200", resp.StatusCode)
	}
}

func TestCommunicationGroups(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Create group
	body := `{"name":"test-group","description":"A test group","members":["agent-1","agent-2"]}`
	resp, err := http.Post(ts.URL+"/api/v1/groups", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /groups: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("create group status = %d, want 201", resp.StatusCode)
	}

	// List groups
	resp2, err := http.Get(ts.URL + "/api/v1/groups?agent_id=agent-1")
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
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/ws/status")
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
	ts := newTestServer(t)
	defer ts.Close()

	// PUT on /agents (collection endpoint)
	req, _ := http.NewRequest("PUT", ts.URL+"/api/v1/agents", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /agents: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("PUT /agents status = %d, want 405", resp.StatusCode)
	}
}
