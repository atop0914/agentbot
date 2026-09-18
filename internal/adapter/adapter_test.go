package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- Helper ---

func setupTestService() *Service {
	registry := NewMemoryRegistry()
	registry.RegisterFactory(AdapterTypeEmail, NewEmailAdapter)
	registry.RegisterFactory(AdapterTypeCalendar, NewCalendarAdapter)
	return NewService(registry)
}

func registerTestAdapter(t *testing.T, svc *Service, id, agentID string, adapterType AdapterType) *AdapterInfo {
	t.Helper()
	settings := map[string]string{"smtp_host": "smtp.example.com"}
	if adapterType == AdapterTypeCalendar {
		settings = map[string]string{"provider": "google"}
	}
	cfg := &Config{
		ID:       id,
		Name:     "Test " + string(adapterType),
		Type:     adapterType,
		AgentID:  agentID,
		Settings: settings,
	}
	info, err := svc.Register(context.Background(), cfg)
	if err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	return info
}

// --- Registry Tests ---

func TestMemoryRegistry_Register(t *testing.T) {
	svc := setupTestService()
	info := registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)

	if info.ID != "email-1" {
		t.Errorf("expected ID email-1, got %s", info.ID)
	}
	if info.Status != StatusConnected {
		t.Errorf("expected status connected, got %s", info.Status)
	}
	if info.Actions != 4 {
		t.Errorf("expected 4 actions for email adapter, got %d", info.Actions)
	}
}

func TestMemoryRegistry_RegisterDuplicate(t *testing.T) {
	svc := setupTestService()
	registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)

	cfg := &Config{
		ID:      "email-1",
		Name:    "Duplicate",
		Type:    AdapterTypeEmail,
		AgentID: "agent-1",
		Settings: map[string]string{"smtp_host": "smtp.example.com"},
	}
	_, err := svc.Register(context.Background(), cfg)
	if err == nil {
		t.Error("expected error for duplicate registration")
	}
}

func TestMemoryRegistry_RegisterUnknownType(t *testing.T) {
	svc := setupTestService()
	cfg := &Config{
		ID:      "unknown-1",
		Name:    "Unknown",
		Type:    AdapterTypeCustom,
		AgentID: "agent-1",
	}
	_, err := svc.Register(context.Background(), cfg)
	if err == nil {
		t.Error("expected error for unknown adapter type")
	}
}

func TestMemoryRegistry_Unregister(t *testing.T) {
	svc := setupTestService()
	registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)

	if err := svc.Unregister(context.Background(), "email-1"); err != nil {
		t.Fatalf("unregister: %v", err)
	}

	_, ok := svc.Get("email-1")
	if ok {
		t.Error("expected adapter to be removed")
	}
}

func TestMemoryRegistry_UnregisterNotFound(t *testing.T) {
	svc := setupTestService()
	err := svc.Unregister(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent adapter")
	}
}

func TestMemoryRegistry_ListByAgent(t *testing.T) {
	svc := setupTestService()
	registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)
	registerTestAdapter(t, svc, "cal-1", "agent-1", AdapterTypeCalendar)
	registerTestAdapter(t, svc, "email-2", "agent-2", AdapterTypeEmail)

	list := svc.List("agent-1")
	if len(list) != 2 {
		t.Errorf("expected 2 adapters for agent-1, got %d", len(list))
	}

	list = svc.List("agent-2")
	if len(list) != 1 {
		t.Errorf("expected 1 adapter for agent-2, got %d", len(list))
	}
}

func TestMemoryRegistry_ListAll(t *testing.T) {
	svc := setupTestService()
	registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)
	registerTestAdapter(t, svc, "cal-1", "agent-2", AdapterTypeCalendar)

	list := svc.ListAll()
	if len(list) != 2 {
		t.Errorf("expected 2 adapters total, got %d", len(list))
	}
}

// --- Email Adapter Tests ---

func TestEmailAdapter_Actions(t *testing.T) {
	adapter := NewEmailAdapter()
	actions := adapter.ListActions()

	expected := []string{"send", "list", "read", "search"}
	if len(actions) != len(expected) {
		t.Fatalf("expected %d actions, got %d", len(expected), len(actions))
	}
	for i, name := range expected {
		if actions[i].Name != name {
			t.Errorf("action[%d]: expected %s, got %s", i, name, actions[i].Name)
		}
	}
}

func TestEmailAdapter_SendSuccess(t *testing.T) {
	adapter := NewEmailAdapter()
	cfg := &Config{
		ID:       "test-email",
		Settings: map[string]string{"smtp_host": "smtp.example.com"},
	}
	if err := adapter.Connect(context.Background(), cfg); err != nil {
		t.Fatalf("connect: %v", err)
	}

	result, err := adapter.Execute(context.Background(), &ActionRequest{
		Action: "send",
		Parameters: map[string]string{
			"to":      "user@example.com",
			"subject": "Test",
			"body":    "Hello",
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
}

func TestEmailAdapter_SendMissingTo(t *testing.T) {
	adapter := NewEmailAdapter()
	cfg := &Config{
		ID:       "test-email",
		Settings: map[string]string{"smtp_host": "smtp.example.com"},
	}
	adapter.Connect(context.Background(), cfg)

	result, _ := adapter.Execute(context.Background(), &ActionRequest{
		Action:     "send",
		Parameters: map[string]string{"subject": "Test"},
	})
	if result.Success {
		t.Error("expected failure for missing 'to' parameter")
	}
}

func TestEmailAdapter_ConnectMissingHost(t *testing.T) {
	adapter := NewEmailAdapter()
	cfg := &Config{
		ID:       "test-email",
		Settings: map[string]string{},
	}
	err := adapter.Connect(context.Background(), cfg)
	if err == nil {
		t.Error("expected error for missing smtp_host")
	}
}

func TestEmailAdapter_NotConnected(t *testing.T) {
	adapter := NewEmailAdapter()
	result, err := adapter.Execute(context.Background(), &ActionRequest{Action: "send"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Success {
		t.Error("expected failure when not connected")
	}
}

func TestEmailAdapter_UnknownAction(t *testing.T) {
	adapter := NewEmailAdapter()
	cfg := &Config{
		ID:       "test",
		Settings: map[string]string{"smtp_host": "smtp.example.com"},
	}
	adapter.Connect(context.Background(), cfg)

	result, _ := adapter.Execute(context.Background(), &ActionRequest{Action: "nonexistent"})
	if result.Success {
		t.Error("expected failure for unknown action")
	}
}

func TestEmailAdapter_Disconnect(t *testing.T) {
	adapter := NewEmailAdapter()
	cfg := &Config{
		ID:       "test",
		Settings: map[string]string{"smtp_host": "smtp.example.com"},
	}
	adapter.Connect(context.Background(), cfg)
	adapter.Disconnect(context.Background())

	if adapter.Status() != StatusDisconnected {
		t.Errorf("expected disconnected status, got %s", adapter.Status())
	}
}

// --- Calendar Adapter Tests ---

func TestCalendarAdapter_Actions(t *testing.T) {
	adapter := NewCalendarAdapter()
	actions := adapter.ListActions()

	expected := []string{"list_events", "create_event", "update_event", "delete_event", "get_freebusy"}
	if len(actions) != len(expected) {
		t.Fatalf("expected %d actions, got %d", len(expected), len(actions))
	}
}

func TestCalendarAdapter_CreateEvent(t *testing.T) {
	adapter := NewCalendarAdapter()
	cfg := &Config{
		ID:       "test-cal",
		Settings: map[string]string{"provider": "google"},
	}
	adapter.Connect(context.Background(), cfg)

	result, err := adapter.Execute(context.Background(), &ActionRequest{
		Action: "create_event",
		Parameters: map[string]string{
			"summary": "Team Meeting",
			"start":   "2026-09-18T10:00:00Z",
			"end":     "2026-09-18T11:00:00Z",
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Data["summary"] != "Team Meeting" {
		t.Errorf("expected summary 'Team Meeting', got %s", result.Data["summary"])
	}
}

func TestCalendarAdapter_ConnectMissingProvider(t *testing.T) {
	adapter := NewCalendarAdapter()
	cfg := &Config{
		ID:       "test-cal",
		Settings: map[string]string{},
	}
	err := adapter.Connect(context.Background(), cfg)
	if err == nil {
		t.Error("expected error for missing provider")
	}
}

// --- Handler Tests ---

func TestHandler_Create(t *testing.T) {
	svc := setupTestService()
	handler := NewHandler(svc)

	body, _ := json.Marshal(Config{
		ID:      "email-test",
		Name:    "Test Email",
		Type:    AdapterTypeEmail,
		AgentID: "agent-1",
		Settings: map[string]string{
			"smtp_host": "smtp.example.com",
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/adapters", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.create(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandler_List(t *testing.T) {
	svc := setupTestService()
	registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)

	handler := NewHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/adapters", nil)
	w := httptest.NewRecorder()

	handler.list(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var list []AdapterInfo
	json.NewDecoder(w.Body).Decode(&list)
	if len(list) != 1 {
		t.Errorf("expected 1 adapter, got %d", len(list))
	}
}

func TestHandler_ListFiltered(t *testing.T) {
	svc := setupTestService()
	registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)
	registerTestAdapter(t, svc, "cal-1", "agent-2", AdapterTypeCalendar)

	handler := NewHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/adapters?agent_id=agent-1", nil)
	w := httptest.NewRecorder()

	handler.list(w, req)

	var list []AdapterInfo
	json.NewDecoder(w.Body).Decode(&list)
	if len(list) != 1 {
		t.Errorf("expected 1 adapter for agent-1, got %d", len(list))
	}
}

func TestHandler_Get(t *testing.T) {
	svc := setupTestService()
	registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)

	handler := NewHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/adapters/email-1", nil)
	w := httptest.NewRecorder()

	handler.get(w, req, "email-1")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandler_GetNotFound(t *testing.T) {
	svc := setupTestService()
	handler := NewHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/adapters/nonexistent", nil)
	w := httptest.NewRecorder()

	handler.get(w, req, "nonexistent")

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandler_Delete(t *testing.T) {
	svc := setupTestService()
	registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)

	handler := NewHandler(svc)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/adapters/email-1", nil)
	w := httptest.NewRecorder()

	handler.delete(w, req, "email-1")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandler_Execute(t *testing.T) {
	svc := setupTestService()
	registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)

	handler := NewHandler(svc)
	body, _ := json.Marshal(ActionRequest{
		Action: "send",
		Parameters: map[string]string{
			"to":      "user@example.com",
			"subject": "Test",
			"body":    "Hello",
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/adapters/email-1/execute", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.execute(w, req, "email-1")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandler_ExecuteNoAction(t *testing.T) {
	svc := setupTestService()
	registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)

	handler := NewHandler(svc)
	body, _ := json.Marshal(ActionRequest{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/adapters/email-1/execute", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.execute(w, req, "email-1")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandler_ListActions(t *testing.T) {
	svc := setupTestService()
	registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)

	handler := NewHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/adapters/email-1/actions", nil)
	w := httptest.NewRecorder()

	handler.listActions(w, req, "email-1")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var actions []Action
	json.NewDecoder(w.Body).Decode(&actions)
	if len(actions) != 4 {
		t.Errorf("expected 4 actions, got %d", len(actions))
	}
}

// --- Service Tests ---

func TestService_GetInfo(t *testing.T) {
	svc := setupTestService()
	registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)

	info, err := svc.GetInfo("email-1")
	if err != nil {
		t.Fatalf("get info: %v", err)
	}
	if info.Name != "Test email" {
		t.Errorf("expected 'Test email', got %s", info.Name)
	}
}

func TestService_GetInfoNotFound(t *testing.T) {
	svc := setupTestService()
	_, err := svc.GetInfo("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent adapter")
	}
}

func TestService_ListActions(t *testing.T) {
	svc := setupTestService()
	registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)

	actions, err := svc.ListActions("email-1")
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	if len(actions) != 4 {
		t.Errorf("expected 4 actions, got %d", len(actions))
	}
}

func TestService_Execute(t *testing.T) {
	svc := setupTestService()
	registerTestAdapter(t, svc, "email-1", "agent-1", AdapterTypeEmail)

	result, err := svc.Execute(context.Background(), "email-1", &ActionRequest{
		Action: "send",
		Parameters: map[string]string{
			"to":      "user@example.com",
			"subject": "Test",
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Error)
	}
}