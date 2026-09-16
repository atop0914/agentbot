package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalManager_CreateSession(t *testing.T) {
	m := NewLocalManager()
	ctx := context.Background()

	session, err := m.CreateSession(ctx, CreateSessionRequest{
		AgentID: "agent-1",
		Type:    ConnTypeLocal,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if session.ID == "" {
		t.Error("session ID should not be empty")
	}
	if session.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want %q", session.AgentID, "agent-1")
	}
	if session.State != StateIdle {
		t.Errorf("State = %q, want %q", session.State, StateIdle)
	}
	if session.Type != ConnTypeLocal {
		t.Errorf("Type = %q, want %q", session.Type, ConnTypeLocal)
	}
}

func TestLocalManager_CloseSession(t *testing.T) {
	m := NewLocalManager()
	ctx := context.Background()

	session, _ := m.CreateSession(ctx, CreateSessionRequest{AgentID: "agent-1"})

	err := m.CloseSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	// 验证状态已更新
	s, _ := m.GetSession(ctx, session.ID)
	if s.State != StateClosed {
		t.Errorf("State = %q, want %q", s.State, StateClosed)
	}

	// 关闭已关闭的会话应报错
	err = m.CloseSession(ctx, session.ID)
	if err == nil {
		t.Error("closing already closed session should return error")
	}
}

func TestLocalManager_Execute(t *testing.T) {
	m := NewLocalManager()
	ctx := context.Background()

	session, _ := m.CreateSession(ctx, CreateSessionRequest{AgentID: "agent-1"})

	result, err := m.Execute(ctx, session.ID, ExecRequest{
		Command: "echo hello",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "hello") {
		t.Errorf("Stdout = %q, want contain 'hello'", result.Stdout)
	}
}

func TestLocalManager_ExecuteError(t *testing.T) {
	m := NewLocalManager()
	ctx := context.Background()

	session, _ := m.CreateSession(ctx, CreateSessionRequest{AgentID: "agent-1"})

	result, err := m.Execute(ctx, session.ID, ExecRequest{
		Command: "exit 1",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.ExitCode)
	}
}

func TestLocalManager_Resize(t *testing.T) {
	m := NewLocalManager()
	ctx := context.Background()

	session, _ := m.CreateSession(ctx, CreateSessionRequest{AgentID: "agent-1"})

	err := m.Resize(ctx, session.ID, 50, 120)
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}

	s, _ := m.GetSession(ctx, session.ID)
	if s.Rows != 50 || s.Cols != 120 {
		t.Errorf("size = %dx%d, want 50x120", s.Rows, s.Cols)
	}
}

func TestLocalManager_SessionNotFound(t *testing.T) {
	m := NewLocalManager()
	ctx := context.Background()

	_, err := m.GetSession(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}

	_, err = m.Execute(ctx, "nonexistent", ExecRequest{Command: "echo test"})
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

func TestLocalManager_ListSessions(t *testing.T) {
	m := NewLocalManager()
	ctx := context.Background()

	m.CreateSession(ctx, CreateSessionRequest{AgentID: "agent-1"})
	m.CreateSession(ctx, CreateSessionRequest{AgentID: "agent-2"})
	m.CreateSession(ctx, CreateSessionRequest{AgentID: "agent-1"})

	// 列出所有
	all, _ := m.ListSessions(ctx, SessionFilter{})
	if len(all) != 3 {
		t.Errorf("total sessions = %d, want 3", len(all))
	}

	// 按 agent 过滤
	agent1, _ := m.ListSessions(ctx, SessionFilter{AgentID: "agent-1"})
	if len(agent1) != 2 {
		t.Errorf("agent-1 sessions = %d, want 2", len(agent1))
	}
}

func TestMemoryRepository_CRUD(t *testing.T) {
	repo := NewMemoryRepository()

	session := &Session{
		ID:      "test-1",
		AgentID: "agent-1",
		State:   StateIdle,
	}

	// Save
	if err := repo.Save(session); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 重复保存应报错
	if err := repo.Save(session); err == nil {
		t.Error("duplicate save should return error")
	}

	// Get
	got, err := repo.Get("test-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "test-1" {
		t.Errorf("ID = %q, want %q", got.ID, "test-1")
	}

	// Update
	session.State = StateRunning
	if err := repo.Update(session); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = repo.Get("test-1")
	if got.State != StateRunning {
		t.Errorf("State = %q, want %q", got.State, StateRunning)
	}

	// List
	sessions, _ := repo.List(SessionFilter{})
	if len(sessions) != 1 {
		t.Errorf("sessions count = %d, want 1", len(sessions))
	}

	// Delete
	if err := repo.Delete("test-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = repo.Get("test-1")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestService_CreateAndExecute(t *testing.T) {
	m := NewLocalManager()
	repo := NewMemoryRepository()
	svc := NewService(m, repo)
	ctx := context.Background()

	session, err := svc.CreateSession(ctx, CreateSessionRequest{
		AgentID: "agent-1",
		Type:    ConnTypeLocal,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 验证已持久化
	_, err = repo.Get(session.ID)
	if err != nil {
		t.Fatalf("session not persisted: %v", err)
	}

	// 执行命令
	result, err := svc.Execute(ctx, session.ID, ExecRequest{
		Command: "echo test-output",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result.Stdout, "test-output") {
		t.Errorf("stdout = %q, want contain 'test-output'", result.Stdout)
	}

	// 关闭会话
	err = svc.CloseSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
}

func TestService_Timeout(t *testing.T) {
	m := NewLocalManager()
	repo := NewMemoryRepository()
	svc := NewService(m, repo)
	ctx := context.Background()

	session, _ := svc.CreateSession(ctx, CreateSessionRequest{AgentID: "agent-1"})

	// 执行一个会超时的命令
	_, err := svc.Execute(ctx, session.ID, ExecRequest{
		Command:    "sleep 10",
		TimeoutSec: 1,
	})
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestHandler_CreateSession(t *testing.T) {
	m := NewLocalManager()
	repo := NewMemoryRepository()
	svc := NewService(m, repo)
	h := NewHandler(svc)

	body, _ := json.Marshal(CreateSessionRequest{
		AgentID: "agent-1",
		Type:    ConnTypeLocal,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/terminals", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.handleSessions(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var session Session
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if session.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want %q", session.AgentID, "agent-1")
	}
}

func TestHandler_GetSession(t *testing.T) {
	m := NewLocalManager()
	repo := NewMemoryRepository()
	svc := NewService(m, repo)
	h := NewHandler(svc)

	// 先创建会话
	session, _ := svc.CreateSession(context.Background(), CreateSessionRequest{
		AgentID: "agent-1",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/terminals/"+session.ID, nil)
	w := httptest.NewRecorder()

	h.handleSessionByID(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandler_Execute(t *testing.T) {
	m := NewLocalManager()
	repo := NewMemoryRepository()
	svc := NewService(m, repo)
	h := NewHandler(svc)

	session, _ := svc.CreateSession(context.Background(), CreateSessionRequest{
		AgentID: "agent-1",
	})

	body, _ := json.Marshal(ExecRequest{Command: "echo handler-test"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/terminals/"+session.ID+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.handleSessionByID(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var result ExecResult
	json.Unmarshal(w.Body.Bytes(), &result)
	if !strings.Contains(result.Stdout, "handler-test") {
		t.Errorf("stdout = %q, want contain 'handler-test'", result.Stdout)
	}
}

func TestHandler_ListSessions(t *testing.T) {
	m := NewLocalManager()
	repo := NewMemoryRepository()
	svc := NewService(m, repo)
	h := NewHandler(svc)

	svc.CreateSession(context.Background(), CreateSessionRequest{AgentID: "agent-1"})
	svc.CreateSession(context.Background(), CreateSessionRequest{AgentID: "agent-2"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/terminals", nil)
	w := httptest.NewRecorder()

	h.handleSessions(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var sessions []Session
	json.Unmarshal(w.Body.Bytes(), &sessions)
	if len(sessions) != 2 {
		t.Errorf("sessions count = %d, want 2", len(sessions))
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	m := NewLocalManager()
	repo := NewMemoryRepository()
	svc := NewService(m, repo)
	h := NewHandler(svc)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/terminals", nil)
	w := httptest.NewRecorder()

	h.handleSessions(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}
