package websocket

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Allow all origins in development; production should restrict this.
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Handler provides HTTP handlers for WebSocket connections.
type Handler struct {
	hub    *Hub
	logger *slog.Logger

	// AuthFunc extracts userID from the request.
	// If nil, anonymous connections are accepted with userID="".
	AuthFunc func(r *http.Request) (userID string, err error)
}

// NewHandler creates a new WebSocket handler.
func NewHandler(hub *Hub, logger *slog.Logger) *Handler {
	return &Handler{
		hub:    hub,
		logger: logger,
	}
}

// HandleWS upgrades an HTTP connection to WebSocket.
// Query params:
//   - user_id (optional): override user ID for testing
func (h *Handler) HandleWS(w http.ResponseWriter, r *http.Request) {
	var userID string

	if h.AuthFunc != nil {
		var err error
		userID, err = h.AuthFunc(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	} else {
		// Fallback: accept user_id from query for testing
		userID = r.URL.Query().Get("user_id")
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("websocket upgrade failed", "error", err)
		return
	}

	clientID := uuid.New().String()
	client := NewClient(clientID, userID, conn, h.hub, h.logger)

	h.hub.Register(client)

	// Start pumps in goroutines
	go client.WritePump()
	go client.ReadPump()
}

// HandleStatus returns hub connection stats.
func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"connections": h.hub.ConnCount(),
		"users":       h.hub.UserCount(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// RegisterRoutes registers WebSocket routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix+"/ws", h.HandleWS)
	mux.HandleFunc(prefix+"/ws/status", h.HandleStatus)
}
