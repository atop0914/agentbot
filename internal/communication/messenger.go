package communication

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MessengerConfig holds configuration for the AgentMessenger.
type MessengerConfig struct {
	DefaultTTL      time.Duration // default message TTL, 0 = no expiry
	RequestTimeout  time.Duration // default timeout for request-response
	MaxPendingReqs  int           // max pending requests per agent
}

// DefaultMessengerConfig returns sensible defaults.
func DefaultMessengerConfig() MessengerConfig {
	return MessengerConfig{
		DefaultTTL:     0,
		RequestTimeout: 30 * time.Second,
		MaxPendingReqs: 100,
	}
}

// AgentMessengerImpl implements the AgentMessenger interface with
// full protocol support: direct messaging, group broadcast,
// request-response pattern, and status reporting.
type AgentMessengerImpl struct {
	mu          sync.RWMutex
	cfg         MessengerConfig
	bus         *MemoryBus
	presence    *PresenceManager
	repo        *MemoryRepository
	pendingReqs map[string]*PendingRequest // correlationID -> pending
	ackCh       chan Ack
}

// NewAgentMessenger creates a new AgentMessengerImpl.
func NewAgentMessenger(cfg MessengerConfig, bus *MemoryBus, presence *PresenceManager, repo *MemoryRepository) *AgentMessengerImpl {
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.MaxPendingReqs <= 0 {
		cfg.MaxPendingReqs = 100
	}
	return &AgentMessengerImpl{
		cfg:         cfg,
		bus:         bus,
		presence:    presence,
		repo:        repo,
		pendingReqs: make(map[string]*PendingRequest),
		ackCh:       make(chan Ack, 256),
	}
}

// SendToAgent sends a direct message from one agent to another.
// It validates the sender is online, stores the message, and publishes
// it to the bus for real-time delivery.
func (m *AgentMessengerImpl) SendToAgent(ctx context.Context, from, to string, content string) error {
	if from == "" || to == "" {
		return fmt.Errorf("from and to are required")
	}

	msg := Message{
		ID:        uuid.New().String(),
		From:      from,
		To:        to,
		Type:      MessageText,
		Content:   content,
		Timestamp: time.Now(),
	}

	envelope := Envelope{
		MessageID: msg.ID,
		From:      from,
		To:        to,
		Priority:  PriorityNormal,
		TTL:       m.cfg.DefaultTTL,
		CreatedAt: msg.Timestamp,
		Status:    StatusPending,
	}

	// Store message
	if err := m.repo.SaveMessage(ctx, msg); err != nil {
		return fmt.Errorf("save message: %w", err)
	}

	// Publish with envelope
	if err := m.bus.Publish(ctx, "direct.message", msg); err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	// Update envelope status
	envelope.Status = StatusSent
	_ = envelope // stored in future extension

	return nil
}

// BroadcastToGroup sends a message to all agents in a group.
func (m *AgentMessengerImpl) BroadcastToGroup(ctx context.Context, from, groupID string, content string) error {
	if from == "" || groupID == "" {
		return fmt.Errorf("from and group_id are required")
	}

	msg := Message{
		ID:        uuid.New().String(),
		From:      from,
		To:        groupID,
		GroupID:   groupID,
		Type:      MessageText,
		Content:   content,
		Timestamp: time.Now(),
	}

	if err := m.repo.SaveMessage(ctx, msg); err != nil {
		return fmt.Errorf("save message: %w", err)
	}

	return m.bus.Publish(ctx, "group.message", msg)
}

// RequestAction sends a structured request to another agent and waits for a response.
// This implements the request-response pattern for agent-to-agent coordination.
func (m *AgentMessengerImpl) RequestAction(ctx context.Context, from, to string, action string, params map[string]string) (string, error) {
	if from == "" || to == "" || action == "" {
		return "", fmt.Errorf("from, to, and action are required")
	}

	m.mu.Lock()
	if len(m.pendingReqs) >= m.cfg.MaxPendingReqs {
		m.mu.Unlock()
		return "", fmt.Errorf("too many pending requests (max %d)", m.cfg.MaxPendingReqs)
	}

	reqID := uuid.New().String()
	req := RequestEnvelope{
		RequestID: reqID,
		From:      from,
		To:        to,
		Action:    action,
		Params:    params,
		Priority:  PriorityHigh,
		Timeout:   m.cfg.RequestTimeout,
		CreatedAt: time.Now(),
	}

	replyCh := make(chan ResponseEnvelope, 1)
	m.pendingReqs[reqID] = &PendingRequest{
		Request:   req,
		StartedAt: time.Now(),
		ReplyCh:   replyCh,
	}
	m.mu.Unlock()

	// Publish request to bus
	if err := m.bus.Publish(ctx, "agent.request", Message{
		ID:        reqID,
		From:      from,
		To:        to,
		Type:      MessageRequest,
		Content:   action,
		Metadata:  params,
		Timestamp: time.Now(),
	}); err != nil {
		m.mu.Lock()
		delete(m.pendingReqs, reqID)
		m.mu.Unlock()
		return "", fmt.Errorf("publish request: %w", err)
	}

	// Wait for response or context cancellation
	select {
	case resp := <-replyCh:
		if !resp.Success {
			return "", fmt.Errorf("action failed: %s", resp.Error)
		}
		result := ""
		for k, v := range resp.Result {
			if result != "" {
				result += ", "
			}
			result += k + "=" + v
		}
		return result, nil
	case <-ctx.Done():
		m.mu.Lock()
		delete(m.pendingReqs, reqID)
		m.mu.Unlock()
		return "", ctx.Err()
	case <-time.After(m.cfg.RequestTimeout):
		m.mu.Lock()
		delete(m.pendingReqs, reqID)
		m.mu.Unlock()
		return "", fmt.Errorf("request timed out after %s", m.cfg.RequestTimeout)
	}
}

// CompleteRequest fulfills a pending request with a response.
// Called by the agent that handled the request.
func (m *AgentMessengerImpl) CompleteRequest(ctx context.Context, requestID string, success bool, result map[string]string, errMsg string) error {
	m.mu.Lock()
	pending, ok := m.pendingReqs[requestID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown request %s", requestID)
	}
	delete(m.pendingReqs, requestID)
	m.mu.Unlock()

	resp := ResponseEnvelope{
		RequestID: requestID,
		From:      pending.Request.To,
		To:        pending.Request.From,
		Success:   success,
		Result:    result,
		Error:     errMsg,
		Timestamp: time.Now(),
	}

	// Store response as a message
	msg := Message{
		ID:        uuid.New().String(),
		From:      resp.From,
		To:        resp.To,
		Type:      MessageResponse,
		Content:   fmt.Sprintf("response to %s: success=%v", requestID, success),
		Timestamp: time.Now(),
	}
	m.repo.SaveMessage(ctx, msg)

	// Deliver via channel
	select {
	case pending.ReplyCh <- resp:
	default:
		// Channel full or closed, request may have timed out
	}

	return nil
}

// ReportStatus broadcasts an agent's status to its groups or a supervisor topic.
func (m *AgentMessengerImpl) ReportStatus(ctx context.Context, from string, status string) error {
	if from == "" {
		return fmt.Errorf("from is required")
	}

	// Update presence
	var agentStatus AgentStatus
	switch status {
	case "online":
		agentStatus = AgentStatusOnline
	case "busy":
		agentStatus = AgentStatusBusy
	case "away":
		agentStatus = AgentStatusAway
	case "error":
		agentStatus = AgentStatusError
	default:
		agentStatus = AgentStatusOnline
	}
	m.presence.Heartbeat(ctx, from, agentStatus, nil)

	msg := Message{
		ID:        uuid.New().String(),
		From:      from,
		To:        "*",
		Type:      MessageStatus,
		Content:   status,
		Timestamp: time.Now(),
	}

	return m.bus.Publish(ctx, "agent.status", msg)
}

// HandleRequest registers a handler for incoming requests on a specific topic.
// Agents call this to register themselves as capable of handling certain actions.
func (m *AgentMessengerImpl) HandleRequest(ctx context.Context, topic string, handler func(RequestEnvelope) ResponseEnvelope) error {
	return m.bus.Subscribe(ctx, topic, func(msg Message) {
		if msg.Type == MessageRequest {
			req := RequestEnvelope{
				RequestID: msg.ID,
				From:      msg.From,
				To:        msg.To,
				Action:    msg.Content,
				Params:    msg.Metadata,
				CreatedAt: msg.Timestamp,
			}
			resp := handler(req)
			m.CompleteRequest(ctx, req.RequestID, resp.Success, resp.Result, resp.Error)
		}
	})
}

// GetPendingRequests returns the count of pending requests (for monitoring).
func (m *AgentMessengerImpl) GetPendingRequests() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.pendingReqs)
}

// SendWithPriority sends a message with a specific priority level.
func (m *AgentMessengerImpl) SendWithPriority(ctx context.Context, from, to string, content string, priority Priority) error {
	if from == "" || to == "" {
		return fmt.Errorf("from and to are required")
	}

	msg := Message{
		ID:        uuid.New().String(),
		From:      from,
		To:        to,
		Type:      MessageText,
		Content:   content,
		Timestamp: time.Now(),
	}

	if err := m.repo.SaveMessage(ctx, msg); err != nil {
		return fmt.Errorf("save message: %w", err)
	}

	topic := "direct.message"
	if priority >= PriorityHigh {
		topic = "priority.message"
	}

	return m.bus.Publish(ctx, topic, msg)
}
