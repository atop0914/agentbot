package communication

import (
	"context"
	"sync"
	"time"
)

// PresenceEntry tracks a single agent's presence state.
type PresenceEntry struct {
	AgentID   string            `json:"agent_id"`
	Status    AgentStatus       `json:"status"`
	LastSeen  time.Time         `json:"last_seen"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// PresenceManager tracks online/offline status of all agents.
// It provides heartbeat-based liveness detection and status queries.
type PresenceManager struct {
	mu       sync.RWMutex
	entries  map[string]*PresenceEntry
	timeout  time.Duration // how long before an agent is considered offline
	stopCh   chan struct{}
}

// NewPresenceManager creates a new PresenceManager.
// timeout is the heartbeat expiry duration; agents that haven't
// sent a heartbeat within this window are marked offline.
func NewPresenceManager(timeout time.Duration) *PresenceManager {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	pm := &PresenceManager{
		entries: make(map[string]*PresenceEntry),
		timeout: timeout,
		stopCh:  make(chan struct{}),
	}
	go pm.reaper()
	return pm
}

// Heartbeat records a heartbeat from the given agent, setting it online.
func (pm *PresenceManager) Heartbeat(_ context.Context, agentID string, status AgentStatus, meta map[string]string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if status == "" {
		status = AgentStatusOnline
	}
	pm.entries[agentID] = &PresenceEntry{
		AgentID:  agentID,
		Status:   status,
		LastSeen: time.Now(),
		Metadata: meta,
	}
}

// SetStatus explicitly sets an agent's status without refreshing the heartbeat timer.
func (pm *PresenceManager) SetStatus(_ context.Context, agentID string, status AgentStatus) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if e, ok := pm.entries[agentID]; ok {
		e.Status = status
	} else {
		pm.entries[agentID] = &PresenceEntry{
			AgentID:  agentID,
			Status:   status,
			LastSeen: time.Now(),
		}
	}
}

// GetStatus returns the current status of an agent.
// Returns AgentStatusOffline if the agent is unknown or expired.
func (pm *PresenceManager) GetStatus(_ context.Context, agentID string) PresenceEntry {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	e, ok := pm.entries[agentID]
	if !ok {
		return PresenceEntry{AgentID: agentID, Status: AgentStatusOffline}
	}
	// Check expiry
	if time.Since(e.LastSeen) > pm.timeout {
		return PresenceEntry{AgentID: agentID, Status: AgentStatusOffline, LastSeen: e.LastSeen}
	}
	return *e
}

// ListOnline returns all agents that are currently online (not expired).
func (pm *PresenceManager) ListOnline(_ context.Context) []PresenceEntry {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var result []PresenceEntry
	now := time.Now()
	for _, e := range pm.entries {
		if now.Sub(e.LastSeen) <= pm.timeout && e.Status != AgentStatusOffline {
			result = append(result, *e)
		}
	}
	return result
}

// ListAll returns all tracked agents, including expired ones.
func (pm *PresenceManager) ListAll(_ context.Context) []PresenceEntry {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]PresenceEntry, 0, len(pm.entries))
	for _, e := range pm.entries {
		result = append(result, *e)
	}
	return result
}

// RemoveAgent removes an agent from the presence table entirely.
func (pm *PresenceManager) RemoveAgent(_ context.Context, agentID string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.entries, agentID)
}

// Stop terminates the background reaper goroutine.
func (pm *PresenceManager) Stop() {
	close(pm.stopCh)
}

// reaper runs in the background, marking expired agents as offline.
func (pm *PresenceManager) reaper() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pm.stopCh:
			return
		case <-ticker.C:
			pm.mu.Lock()
			now := time.Now()
			for _, e := range pm.entries {
				if e.Status != AgentStatusOffline && now.Sub(e.LastSeen) > pm.timeout {
					e.Status = AgentStatusOffline
				}
			}
			pm.mu.Unlock()
		}
	}
}
