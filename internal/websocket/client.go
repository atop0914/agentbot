package websocket

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 64 * 1024 // 64KB
)

// Client represents a single WebSocket connection.
type Client struct {
	mu     sync.Mutex
	ID     string // unique connection ID (uuid)
	UserID string // authenticated user ID
	conn   *websocket.Conn
	hub    *Hub
	send   chan []byte
	logger *slog.Logger

	// Metadata
	ConnectedAt time.Time
	RemoteAddr  string
	UserAgent   string

	// MessageHandler is called when the client sends a message.
	// If nil, messages are published to the hub.
	MessageHandler func(c *Client, msg *IncomingMessage)

	closed bool
}

// IncomingMessage is a message received from the client.
type IncomingMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	ID      string          `json:"id,omitempty"` // client-side message ID for ack
}

// OutgoingMessage is a message sent to the client.
type OutgoingMessage struct {
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	ID        string          `json:"id,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
}

// NewClient creates a new Client wrapping a WebSocket connection.
func NewClient(id, userID string, conn *websocket.Conn, hub *Hub, logger *slog.Logger) *Client {
	return &Client{
		ID:          id,
		UserID:      userID,
		conn:        conn,
		hub:         hub,
		send:        make(chan []byte, 256),
		logger:      logger,
		ConnectedAt: time.Now(),
		RemoteAddr:  conn.RemoteAddr().String(),
	}
}

// ReadPump pumps messages from the WebSocket connection to the hub.
func (c *Client) ReadPump() {
	defer func() {
		c.hub.Unregister(c)
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
			) {
				c.logger.Warn("unexpected websocket close",
					"client_id", c.ID,
					"error", err,
				)
			}
			return
		}

		var msg IncomingMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			c.logger.Warn("invalid message format",
				"client_id", c.ID,
				"error", err,
			)
			continue
		}

		c.handleMessage(&msg)
	}
}

// WritePump pumps messages from the hub to the WebSocket connection.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub closed the channel
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Batch queued messages
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte("\n"))
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Send sends a message to this client.
func (c *Client) Send(msgType string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	msg := OutgoingMessage{
		Type:      msgType,
		Payload:   data,
		Timestamp: time.Now(),
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}

	select {
	case c.send <- raw:
	default:
		c.logger.Warn("client send buffer full", "client_id", c.ID)
	}
	return nil
}

// Close gracefully closes the connection by signaling the WritePump
// and unblocking the ReadPump via conn.Close().
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	close(c.send)
	c.mu.Unlock()
	// Close conn to unblock ReadPump's blocking ReadMessage call.
	// This is safe: gorilla/websocket conn.Close() is idempotent.
	c.conn.Close()
}

func (c *Client) handleMessage(msg *IncomingMessage) {
	if c.MessageHandler != nil {
		c.MessageHandler(c, msg)
		return
	}

	// Default: route through the hub
	env := &Envelope{
		Type:    msg.Type,
		Payload: msg.Payload,
	}

	// If message has an ID, include it as reply_to for acknowledgment
	if msg.ID != "" {
		env.ReplyTo = msg.ID
	}

	c.hub.Send(env)
}
