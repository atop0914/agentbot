package websocket

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// Hub maintains the set of active clients and broadcasts messages.
type Hub struct {
	mu      sync.RWMutex
	clients map[*Client]struct{}

	// Indexed lookups for fast routing
	clientsByUser  map[string]map[*Client]struct{} // userID -> clients
	clientsByID    map[string]*Client              // clientID -> client

	// Channels for hub operations
	register   chan *Client
	unregister chan *Client
	broadcast  chan *Envelope

	logger   *slog.Logger
	stopOnce sync.Once
	stopCh   chan struct{}
}

// Envelope wraps a message with routing information.
type Envelope struct {
	// Target routing - exactly one should be set
	ToUser  string `json:"to_user,omitempty"`  // send to all connections of a user
	ToConn  string `json:"to_conn,omitempty"`  // send to a specific connection
	ToGroup string `json:"to_group,omitempty"` // broadcast to a group (future)

	// Message payload
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	ReplyTo string          `json:"reply_to,omitempty"`

	Timestamp time.Time `json:"timestamp"`
}

// NewHub creates a new Hub.
func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		clients:        make(map[*Client]struct{}),
		clientsByUser:  make(map[string]map[*Client]struct{}),
		clientsByID:    make(map[string]*Client),
		register:       make(chan *Client, 64),
		unregister:     make(chan *Client, 64),
		broadcast:      make(chan *Envelope, 256),
		logger:         logger,
		stopCh:         make(chan struct{}),
	}
}

// Run starts the hub's main event loop. Call in a goroutine.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.onRegister(client)

		case client := <-h.unregister:
			h.onUnregister(client)

		case env := <-h.broadcast:
			h.onBroadcast(env)

		case <-h.stopCh:
			return
		}
	}
}

// Stop gracefully shuts down the hub.
func (h *Hub) Stop() {
	h.stopOnce.Do(func() {
		close(h.stopCh)
		// Close all client connections
		h.mu.Lock()
		for c := range h.clients {
			c.Close()
		}
		h.mu.Unlock()
	})
}

// Register queues a client for registration.
func (h *Hub) Register(c *Client) {
	h.register <- c
}

// Unregister queues a client for removal.
func (h *Hub) Unregister(c *Client) {
	h.unregister <- c
}

// Send sends an envelope to its target.
func (h *Hub) Send(env *Envelope) {
	env.Timestamp = time.Now()
	h.broadcast <- env
}

// SendToUser sends a message to all connections of a user.
func (h *Hub) SendToUser(userID, msgType string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	h.Send(&Envelope{
		ToUser:  userID,
		Type:    msgType,
		Payload: data,
	})
	return nil
}

// SendToConn sends a message to a specific connection.
func (h *Hub) SendToConn(connID, msgType string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	h.Send(&Envelope{
		ToConn:  connID,
		Type:    msgType,
		Payload: data,
	})
	return nil
}

// Broadcast sends a message to all connected clients.
func (h *Hub) Broadcast(msgType string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	h.Send(&Envelope{
		Type:    msgType,
		Payload: data,
	})
	return nil
}

// UserCount returns the number of connected users.
func (h *Hub) UserCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clientsByUser)
}

// ConnCount returns the number of active connections.
func (h *Hub) ConnCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// IsUserConnected checks if a user has any active connections.
func (h *Hub) IsUserConnected(userID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	conns, ok := h.clientsByUser[userID]
	return ok && len(conns) > 0
}

func (h *Hub) onRegister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.clients[c] = struct{}{}
	h.clientsByID[c.ID] = c

	if c.UserID != "" {
		if h.clientsByUser[c.UserID] == nil {
			h.clientsByUser[c.UserID] = make(map[*Client]struct{})
		}
		h.clientsByUser[c.UserID][c] = struct{}{}
	}

	h.logger.Info("client connected",
		"client_id", c.ID,
		"user_id", c.UserID,
		"total_conns", len(h.clients),
	)
}

func (h *Hub) onUnregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.clients[c]; !ok {
		return
	}

	delete(h.clients, c)
	delete(h.clientsByID, c.ID)

	if c.UserID != "" {
		if conns, ok := h.clientsByUser[c.UserID]; ok {
			delete(conns, c)
			if len(conns) == 0 {
				delete(h.clientsByUser, c.UserID)
			}
		}
	}

	// Only close send if Close() hasn't already done it
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.send)
	}
	c.mu.Unlock()

	h.logger.Info("client disconnected",
		"client_id", c.ID,
		"user_id", c.UserID,
		"total_conns", len(h.clients),
	)
}

func (h *Hub) onBroadcast(env *Envelope) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	data, err := json.Marshal(env)
	if err != nil {
		h.logger.Error("failed to marshal envelope", "error", err)
		return
	}

	switch {
	case env.ToConn != "":
		// Unicast to specific connection
		if c, ok := h.clientsByID[env.ToConn]; ok {
			select {
			case c.send <- data:
			default:
				h.logger.Warn("client send buffer full, dropping", "client_id", env.ToConn)
			}
		}

	case env.ToUser != "":
		// Send to all connections of a user
		conns, ok := h.clientsByUser[env.ToUser]
		if !ok {
			return
		}
		for c := range conns {
			select {
			case c.send <- data:
			default:
				h.logger.Warn("client send buffer full, dropping", "client_id", c.ID)
			}
		}

	default:
		// Broadcast to all
		for c := range h.clients {
			select {
			case c.send <- data:
			default:
				h.logger.Warn("client send buffer full, dropping", "client_id", c.ID)
			}
		}
	}
}
