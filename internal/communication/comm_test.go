package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestCommService(t *testing.T) *CommService {
	t.Helper()
	repo := NewMemoryRepository()
	bus := NewMemoryBus()
	return NewCommService(repo, bus)
}

func TestCommService_SendAndGetMessages(t *testing.T) {
	svc := newTestCommService(t)
	ctx := context.Background()

	// Send messages
	msg1 := Message{From: "agent1", To: "agent2", Type: MessageText, Content: "hello"}
	msg2 := Message{From: "agent2", To: "agent1", Type: MessageText, Content: "hi"}
	msg3 := Message{From: "agent1", To: "agent2", Type: MessageText, Content: "how are you"}

	if err := svc.SendMessage(ctx, msg1); err != nil {
		t.Fatalf("send msg1: %v", err)
	}
	if err := svc.SendMessage(ctx, msg2); err != nil {
		t.Fatalf("send msg2: %v", err)
	}
	if err := svc.SendMessage(ctx, msg3); err != nil {
		t.Fatalf("send msg3: %v", err)
	}

	// Get messages for agent1
	msgs, err := svc.GetMessages(ctx, "agent1", 10)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
	}
}

func TestCommService_GetConversation(t *testing.T) {
	svc := newTestCommService(t)
	ctx := context.Background()

	svc.SendMessage(ctx, Message{From: "a", To: "b", Type: MessageText, Content: "1"})
	svc.SendMessage(ctx, Message{From: "c", To: "d", Type: MessageText, Content: "other"})
	svc.SendMessage(ctx, Message{From: "b", To: "a", Type: MessageText, Content: "2"})

	msgs, err := svc.GetConversation(ctx, "a", "b", 10)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages in conversation, got %d", len(msgs))
	}
}

func TestCommService_Groups(t *testing.T) {
	svc := newTestCommService(t)
	ctx := context.Background()

	// Create group
	group, err := svc.CreateGroup(ctx, Group{
		Name:    "team-alpha",
		Members: []string{"agent1", "agent2"},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if group.ID == "" {
		t.Error("group ID should not be empty")
	}

	// Get group
	got, err := svc.GetGroup(ctx, group.ID)
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	if got.Name != "team-alpha" {
		t.Errorf("expected name 'team-alpha', got %q", got.Name)
	}

	// List groups for agent1
	groups, err := svc.ListGroups(ctx, "agent1")
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	if len(groups) != 1 {
		t.Errorf("expected 1 group, got %d", len(groups))
	}

	// Add agent3
	if err := svc.AddToGroup(ctx, group.ID, "agent3"); err != nil {
		t.Fatalf("add to group: %v", err)
	}

	// Remove agent1
	if err := svc.RemoveFromGroup(ctx, group.ID, "agent1"); err != nil {
		t.Fatalf("remove from group: %v", err)
	}

	groups, _ = svc.ListGroups(ctx, "agent1")
	if len(groups) != 0 {
		t.Errorf("agent1 should not be in any groups, got %d", len(groups))
	}
}

func TestCommService_Channels(t *testing.T) {
	svc := newTestCommService(t)
	ctx := context.Background()

	ch, err := svc.CreateChannel(ctx, Channel{
		Type:    ChannelGroup,
		Name:    "general",
		Members: []string{"agent1", "agent2"},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	channels, err := svc.ListChannels(ctx, "agent1")
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	if len(channels) != 1 {
		t.Errorf("expected 1 channel, got %d", len(channels))
	}

	got, _ := svc.GetChannel(ctx, ch.ID)
	if got.Name != "general" {
		t.Errorf("expected channel name 'general', got %q", got.Name)
	}
}

func TestMemoryBus_PubSub(t *testing.T) {
	bus := NewMemoryBus()
	ctx := context.Background()

	received := make(chan Message, 1)
	bus.Subscribe(ctx, "test", func(msg Message) {
		received <- msg
	})

	bus.Publish(ctx, "test", Message{Content: "hello"})

	msg := <-received
	if msg.Content != "hello" {
		t.Errorf("expected 'hello', got %q", msg.Content)
	}
}

func TestHandler_MessagesAPI(t *testing.T) {
	svc := newTestCommService(t)
	logger := slog.Default()
	handler := NewHandler(svc, logger)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, "/api/v1")
	server := httptest.NewServer(mux)
	defer server.Close()

	// Send a message
	body, _ := json.Marshal(Message{
		From:    "agent1",
		To:      "agent2",
		Type:    MessageText,
		Content: "test message",
	})
	resp, err := http.Post(server.URL+"/api/v1/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post message: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}

	// Get messages
	resp, err = http.Get(server.URL + "/api/v1/messages?agent_id=agent1")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	var msgs []Message
	json.NewDecoder(resp.Body).Decode(&msgs)
	resp.Body.Close()
	if len(msgs) != 1 {
		t.Errorf("expected 1 message, got %d", len(msgs))
	}
}

func TestHandler_GroupsAPI(t *testing.T) {
	svc := newTestCommService(t)
	logger := slog.Default()
	handler := NewHandler(svc, logger)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, "/api/v1")
	server := httptest.NewServer(mux)
	defer server.Close()

	// Create group
	body, _ := json.Marshal(Group{
		Name:    "test-group",
		Members: []string{"a", "b"},
	})
	resp, err := http.Post(server.URL+"/api/v1/groups", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}

	// List groups
	resp, err = http.Get(server.URL + "/api/v1/groups?agent_id=a")
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	var groups []Group
	json.NewDecoder(resp.Body).Decode(&groups)
	resp.Body.Close()
	if len(groups) != 1 {
		t.Errorf("expected 1 group, got %d", len(groups))
	}
}

func TestHandler_ConversationAPI(t *testing.T) {
	svc := newTestCommService(t)
	ctx := context.Background()
	svc.SendMessage(ctx, Message{From: "x", To: "y", Type: MessageText, Content: "hi"})
	svc.SendMessage(ctx, Message{From: "y", To: "x", Type: MessageText, Content: "hey"})

	logger := slog.Default()
	handler := NewHandler(svc, logger)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, "/api/v1")
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/v1/messages/conversation?agent1=x&agent2=y")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	var msgs []Message
	json.NewDecoder(resp.Body).Decode(&msgs)
	resp.Body.Close()
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages, got %d", len(msgs))
	}
}
