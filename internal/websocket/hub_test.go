package websocket

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ws "github.com/gorilla/websocket"
)

func newTestHub(t *testing.T) (*Hub, *slog.Logger) {
	t.Helper()
	logger := slog.Default()
	hub := NewHub(logger)
	go hub.Run()
	t.Cleanup(func() { hub.Stop() })
	return hub, logger
}

func mustDialWS(t *testing.T, serverURL, path string) *ws.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + path
	conn, _, err := ws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func readWSMessage(t *testing.T, conn *ws.Conn, timeout time.Duration) *Envelope {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	return &env
}

func TestHub_Broadcast(t *testing.T) {
	hub, logger := newTestHub(t)
	handler := NewHandler(hub, logger)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, "/api/v1")
	server := httptest.NewServer(mux)
	defer server.Close()

	// Connect two clients
	conn1 := mustDialWS(t, server.URL, "/api/v1/ws?user_id=user1")
	conn2 := mustDialWS(t, server.URL, "/api/v1/ws?user_id=user2")

	// Wait for registrations
	time.Sleep(50 * time.Millisecond)

	if hub.ConnCount() != 2 {
		t.Errorf("expected 2 connections, got %d", hub.ConnCount())
	}

	// Broadcast a message
	payload, _ := json.Marshal(map[string]string{"text": "hello"})
	hub.Send(&Envelope{
		Type:    "chat",
		Payload: payload,
	})

	// Both clients should receive it
	env1 := readWSMessage(t, conn1, 2*time.Second)
	if env1.Type != "chat" {
		t.Errorf("expected type 'chat', got %q", env1.Type)
	}

	env2 := readWSMessage(t, conn2, 2*time.Second)
	if env2.Type != "chat" {
		t.Errorf("expected type 'chat', got %q", env2.Type)
	}
}

func TestHub_SendToUser(t *testing.T) {
	hub, logger := newTestHub(t)
	handler := NewHandler(hub, logger)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, "/api/v1")
	server := httptest.NewServer(mux)
	defer server.Close()

	// Connect two users
	conn1 := mustDialWS(t, server.URL, "/api/v1/ws?user_id=user1")
	_ = mustDialWS(t, server.URL, "/api/v1/ws?user_id=user2")
	time.Sleep(50 * time.Millisecond)

	// Send to user1 only
	err := hub.SendToUser("user1", "private", map[string]string{"msg": "secret"})
	if err != nil {
		t.Fatalf("SendToUser: %v", err)
	}

	env := readWSMessage(t, conn1, 2*time.Second)
	if env.Type != "private" {
		t.Errorf("expected type 'private', got %q", env.Type)
	}
}

func TestHub_UserTracking(t *testing.T) {
	hub, logger := newTestHub(t)
	handler := NewHandler(hub, logger)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, "/api/v1")
	server := httptest.NewServer(mux)
	defer server.Close()

	// Connect same user with two connections
	conn1 := mustDialWS(t, server.URL, "/api/v1/ws?user_id=user1")
	conn2 := mustDialWS(t, server.URL, "/api/v1/ws?user_id=user1")
	time.Sleep(50 * time.Millisecond)

	if hub.UserCount() != 1 {
		t.Errorf("expected 1 user, got %d", hub.UserCount())
	}
	if hub.ConnCount() != 2 {
		t.Errorf("expected 2 connections, got %d", hub.ConnCount())
	}
	if !hub.IsUserConnected("user1") {
		t.Error("user1 should be connected")
	}

	// Disconnect one
	conn1.Close()
	time.Sleep(50 * time.Millisecond)

	if hub.ConnCount() != 1 {
		t.Errorf("expected 1 connection after disconnect, got %d", hub.ConnCount())
	}
	if !hub.IsUserConnected("user1") {
		t.Error("user1 should still be connected (has conn2)")
	}

	// Disconnect second
	conn2.Close()
	time.Sleep(50 * time.Millisecond)

	if hub.UserCount() != 0 {
		t.Errorf("expected 0 users, got %d", hub.UserCount())
	}
}

func TestHandler_Status(t *testing.T) {
	hub, logger := newTestHub(t)
	handler := NewHandler(hub, logger)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, "/api/v1")
	server := httptest.NewServer(mux)
	defer server.Close()

	// Check status
	resp, err := http.Get(server.URL + "/api/v1/ws/status")
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	defer resp.Body.Close()

	var status map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&status)

	if status["connections"].(float64) != 0 {
		t.Errorf("expected 0 connections, got %v", status["connections"])
	}
}

func TestClient_PingPong(t *testing.T) {
	hub, logger := newTestHub(t)
	handler := NewHandler(hub, logger)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, "/api/v1")
	server := httptest.NewServer(mux)
	defer server.Close()

	conn := mustDialWS(t, server.URL, "/api/v1/ws?user_id=pingtest")
	time.Sleep(50 * time.Millisecond)

	// The client should stay connected (ping/pong handled)
	// Verify by checking hub still has the connection
	if hub.ConnCount() != 1 {
		t.Errorf("expected 1 connection, got %d", hub.ConnCount())
	}

	// Send a message from client side
	msg := IncomingMessage{
		Type:    "echo",
		Payload: json.RawMessage(`{"test":true}`),
	}
	data, _ := json.Marshal(msg)
	if err := conn.WriteMessage(ws.TextMessage, data); err != nil {
		t.Fatalf("write message: %v", err)
	}

	// Since no MessageHandler is set, it routes through hub as broadcast
	time.Sleep(50 * time.Millisecond)
}

func TestHandler_AuthFunc(t *testing.T) {
	hub, logger := newTestHub(t)
	handler := NewHandler(hub, logger)
	handler.AuthFunc = func(r *http.Request) (string, error) {
		token := r.Header.Get("Authorization")
		if token == "Bearer valid" {
			return "authenticated-user", nil
		}
		return "", net.ErrClosed
	}

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, "/api/v1")
	server := httptest.NewServer(mux)
	defer server.Close()

	// Try unauthorized
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws"
	_, resp, err := ws.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		// If dial succeeded, status should be 401
		resp.Body.Close()
		t.Error("expected unauthorized, but dial succeeded")
	}

	// Try authorized
	header := http.Header{}
	header.Set("Authorization", "Bearer valid")
	conn, _, err := ws.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("authorized dial failed: %v", err)
	}
	defer conn.Close()
	time.Sleep(50 * time.Millisecond)

	if hub.UserCount() != 1 {
		t.Errorf("expected 1 authenticated user, got %d", hub.UserCount())
	}
}

func TestHub_MultiConnBroadcast(t *testing.T) {
	hub, logger := newTestHub(t)
	handler := NewHandler(hub, logger)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, "/api/v1")
	server := httptest.NewServer(mux)
	defer server.Close()

	// Connect 5 clients
	conns := make([]*ws.Conn, 5)
	for i := range conns {
		conns[i] = mustDialWS(t, server.URL, "/api/v1/ws")
	}
	time.Sleep(100 * time.Millisecond)

	if hub.ConnCount() != 5 {
		t.Fatalf("expected 5 connections, got %d", hub.ConnCount())
	}

	// Broadcast
	payload, _ := json.Marshal(map[string]string{"msg": "broadcast"})
	hub.Send(&Envelope{Type: "test", Payload: payload})

	// All should receive
	for i, conn := range conns {
		env := readWSMessage(t, conn, 2*time.Second)
		if env.Type != "test" {
			t.Errorf("conn %d: expected type 'test', got %q", i, env.Type)
		}
	}
}
