package communication

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/atop0914/agentbot/internal/websocket"
)

// MemoryBus implements Bus using an in-memory pub/sub.
type MemoryBus struct {
	mu       sync.RWMutex
	handlers map[string][]func(Message)
}

// NewMemoryBus creates a new in-memory message bus.
func NewMemoryBus() *MemoryBus {
	return &MemoryBus{
		handlers: make(map[string][]func(Message)),
	}
}

func (b *MemoryBus) Publish(_ context.Context, topic string, msg Message) error {
	b.mu.RLock()
	handlers := b.handlers[topic]
	b.mu.RUnlock()

	for _, h := range handlers {
		h(msg)
	}
	return nil
}

func (b *MemoryBus) Subscribe(_ context.Context, topic string, handler func(Message)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[topic] = append(b.handlers[topic], handler)
	return nil
}

func (b *MemoryBus) Unsubscribe(_ context.Context, topic string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.handlers, topic)
	return nil
}

// WSBridge connects the in-memory bus to the WebSocket hub.
// When messages are published to the bus, WSBridge forwards them
// to connected WebSocket clients.
type WSBridge struct {
	bus *MemoryBus
	hub *websocket.Hub
}

// NewWSBridge creates a bridge between bus and WebSocket hub.
func NewWSBridge(bus *MemoryBus, hub *websocket.Hub) *WSBridge {
	return &WSBridge{bus: bus, hub: hub}
}

// Start subscribes to key topics and forwards to WS clients.
func (b *WSBridge) Start(ctx context.Context) error {
	// Subscribe to agent status updates
	b.bus.Subscribe(ctx, "agent.status", func(msg Message) {
		data, _ := json.Marshal(msg)
		b.hub.Send(&websocket.Envelope{
			Type:    "message",
			Payload: data,
		})
	})

	// Subscribe to task updates
	b.bus.Subscribe(ctx, "task.update", func(msg Message) {
		data, _ := json.Marshal(msg)
		b.hub.Send(&websocket.Envelope{
			Type:    "message",
			Payload: data,
		})
	})

	// Subscribe to group messages
	b.bus.Subscribe(ctx, "group.message", func(msg Message) {
		data, _ := json.Marshal(msg)
		if msg.GroupID != "" {
			b.hub.Send(&websocket.Envelope{
				Type:    "group_message",
				Payload: data,
			})
		}
	})

	return nil
}
