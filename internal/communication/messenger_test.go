package communication

import (
	"context"
	"sync"
	"testing"
	"time"
)

// --- Presence Tests ---

func TestPresenceManager_HeartbeatAndStatus(t *testing.T) {
	pm := NewPresenceManager(5 * time.Second)
	defer pm.Stop()
	ctx := context.Background()

	// Unknown agent is offline
	entry := pm.GetStatus(ctx, "agent1")
	if entry.Status != AgentStatusOffline {
		t.Errorf("expected offline, got %s", entry.Status)
	}

	// Heartbeat sets online
	pm.Heartbeat(ctx, "agent1", AgentStatusOnline, nil)
	entry = pm.GetStatus(ctx, "agent1")
	if entry.Status != AgentStatusOnline {
		t.Errorf("expected online, got %s", entry.Status)
	}

	// Set busy
	pm.SetStatus(ctx, "agent1", AgentStatusBusy)
	entry = pm.GetStatus(ctx, "agent1")
	if entry.Status != AgentStatusBusy {
		t.Errorf("expected busy, got %s", entry.Status)
	}
}

func TestPresenceManager_Expiry(t *testing.T) {
	pm := NewPresenceManager(100 * time.Millisecond)
	defer pm.Stop()
	ctx := context.Background()

	pm.Heartbeat(ctx, "agent1", AgentStatusOnline, nil)
	entry := pm.GetStatus(ctx, "agent1")
	if entry.Status != AgentStatusOnline {
		t.Fatalf("expected online, got %s", entry.Status)
	}

	// Wait for expiry
	time.Sleep(150 * time.Millisecond)
	entry = pm.GetStatus(ctx, "agent1")
	if entry.Status != AgentStatusOffline {
		t.Errorf("expected offline after expiry, got %s", entry.Status)
	}
}

func TestPresenceManager_ListOnline(t *testing.T) {
	pm := NewPresenceManager(5 * time.Second)
	defer pm.Stop()
	ctx := context.Background()

	pm.Heartbeat(ctx, "a", AgentStatusOnline, nil)
	pm.Heartbeat(ctx, "b", AgentStatusBusy, nil)
	pm.Heartbeat(ctx, "c", AgentStatusOffline, nil) // explicitly offline

	online := pm.ListOnline(ctx)
	if len(online) != 2 {
		t.Errorf("expected 2 online, got %d", len(online))
	}
}

func TestPresenceManager_RemoveAgent(t *testing.T) {
	pm := NewPresenceManager(5 * time.Second)
	defer pm.Stop()
	ctx := context.Background()

	pm.Heartbeat(ctx, "a", AgentStatusOnline, nil)
	pm.RemoveAgent(ctx, "a")
	entry := pm.GetStatus(ctx, "a")
	if entry.Status != AgentStatusOffline {
		t.Errorf("expected offline after remove, got %s", entry.Status)
	}
}

func TestPresenceManager_Concurrent(t *testing.T) {
	pm := NewPresenceManager(5 * time.Second)
	defer pm.Stop()
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			agentID := "agent" + string(rune('A'+id%26))
			pm.Heartbeat(ctx, agentID, AgentStatusOnline, nil)
			pm.GetStatus(ctx, agentID)
			pm.ListOnline(ctx)
		}(i)
	}
	wg.Wait()
}

// --- Protocol Type Tests ---

func TestEnvelope_Creation(t *testing.T) {
	env := Envelope{
		MessageID: "msg-1",
		From:      "agent1",
		To:        "agent2",
		Priority:  PriorityHigh,
		TTL:       10 * time.Second,
		CreatedAt: time.Now(),
		Status:    StatusPending,
	}
	if env.Priority != PriorityHigh {
		t.Errorf("expected priority high, got %d", env.Priority)
	}
	if env.Status != StatusPending {
		t.Errorf("expected pending, got %s", env.Status)
	}
}

func TestRequestResponseEnvelope(t *testing.T) {
	req := RequestEnvelope{
		RequestID: "req-1",
		From:      "agent1",
		To:        "agent2",
		Action:    "execute_task",
		Params:    map[string]string{"task": "analyze"},
		Timeout:   5 * time.Second,
		CreatedAt: time.Now(),
	}
	if req.Action != "execute_task" {
		t.Errorf("expected action 'execute_task', got %q", req.Action)
	}

	resp := ResponseEnvelope{
		RequestID: req.RequestID,
		From:      req.To,
		To:        req.From,
		Success:   true,
		Result:    map[string]string{"output": "done"},
		Timestamp: time.Now(),
	}
	if !resp.Success {
		t.Error("expected success")
	}
}

// --- AgentMessenger Tests ---

func newTestMessenger(t *testing.T) *AgentMessengerImpl {
	t.Helper()
	bus := NewMemoryBus()
	presence := NewPresenceManager(5 * time.Second)
	repo := NewMemoryRepository()
	cfg := DefaultMessengerConfig()
	cfg.RequestTimeout = 2 * time.Second
	return NewAgentMessenger(cfg, bus, presence, repo)
}

func TestMessenger_SendToAgent(t *testing.T) {
	m := newTestMessenger(t)
	ctx := context.Background()

	err := m.SendToAgent(ctx, "agent1", "agent2", "hello")
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// Verify message stored
	msgs, _ := m.repo.GetMessages(ctx, "agent2", 10)
	if len(msgs) != 1 {
		t.Errorf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "hello" {
		t.Errorf("expected 'hello', got %q", msgs[0].Content)
	}
}

func TestMessenger_SendToAgent_Validation(t *testing.T) {
	m := newTestMessenger(t)
	ctx := context.Background()

	if err := m.SendToAgent(ctx, "", "b", "hi"); err == nil {
		t.Error("expected error for empty from")
	}
	if err := m.SendToAgent(ctx, "a", "", "hi"); err == nil {
		t.Error("expected error for empty to")
	}
}

func TestMessenger_BroadcastToGroup(t *testing.T) {
	m := newTestMessenger(t)
	ctx := context.Background()

	// Create a group first via repo
	m.repo.SaveGroup(ctx, &Group{
		ID:      "grp1",
		Name:    "team",
		Members: []string{"a", "b", "c"},
	})

	err := m.BroadcastToGroup(ctx, "a", "grp1", "announcement")
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}

	// Verify message has group ID
	msgs, _ := m.repo.GetMessages(ctx, "a", 10)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].GroupID != "grp1" {
		t.Errorf("expected group_id 'grp1', got %q", msgs[0].GroupID)
	}
}

func TestMessenger_RequestAction_Timeout(t *testing.T) {
	ctx := context.Background()

	// Request with no handler -> should timeout
	cfg := DefaultMessengerConfig()
	cfg.RequestTimeout = 100 * time.Millisecond
	bus := NewMemoryBus()
	presence := NewPresenceManager(5 * time.Second)
	repo := NewMemoryRepository()
	fastMessenger := NewAgentMessenger(cfg, bus, presence, repo)

	_, err := fastMessenger.RequestAction(ctx, "a", "b", "do_something", nil)
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestMessenger_RequestAction_Complete(t *testing.T) {
	m := newTestMessenger(t)
	ctx := context.Background()

	// Subscribe a handler that responds immediately
	m.bus.Subscribe(ctx, "agent.request", func(msg Message) {
		if msg.Type == MessageRequest {
			m.CompleteRequest(ctx, msg.ID, true, map[string]string{"result": "ok"}, "")
		}
	})

	result, err := m.RequestAction(ctx, "agent1", "agent2", "ping", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestMessenger_ReportStatus(t *testing.T) {
	m := newTestMessenger(t)
	ctx := context.Background()

	// Subscribe to status topic
	var received Message
	m.bus.Subscribe(ctx, "agent.status", func(msg Message) {
		received = msg
	})

	err := m.ReportStatus(ctx, "agent1", "busy")
	if err != nil {
		t.Fatalf("report status: %v", err)
	}

	if received.From != "agent1" {
		t.Errorf("expected from 'agent1', got %q", received.From)
	}
	if received.Content != "busy" {
		t.Errorf("expected content 'busy', got %q", received.Content)
	}

	// Verify presence updated
	entry := m.presence.GetStatus(ctx, "agent1")
	if entry.Status != AgentStatusBusy {
		t.Errorf("expected presence busy, got %s", entry.Status)
	}
}

func TestMessenger_GetPendingRequests(t *testing.T) {
	m := newTestMessenger(t)
	if m.GetPendingRequests() != 0 {
		t.Errorf("expected 0 pending, got %d", m.GetPendingRequests())
	}
}

func TestMessenger_SendWithPriority(t *testing.T) {
	m := newTestMessenger(t)
	ctx := context.Background()

	err := m.SendWithPriority(ctx, "a", "b", "urgent!", PriorityUrgent)
	if err != nil {
		t.Fatalf("send with priority: %v", err)
	}

	msgs, _ := m.repo.GetMessages(ctx, "b", 10)
	if len(msgs) != 1 {
		t.Errorf("expected 1 message, got %d", len(msgs))
	}
}
