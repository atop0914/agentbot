package adapter

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryRegistry is an in-memory implementation of the Registry interface.
type MemoryRegistry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
	configs  map[string]*Config
	factories map[AdapterType]Factory
}

// NewMemoryRegistry creates a new in-memory adapter registry.
func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{
		adapters:  make(map[string]Adapter),
		configs:   make(map[string]*Config),
		factories: make(map[AdapterType]Factory),
	}
}

// RegisterFactory registers a factory for a specific adapter type.
func (r *MemoryRegistry) RegisterFactory(t AdapterType, f Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[t] = f
}

// Register creates and registers a new adapter instance.
func (r *MemoryRegistry) Register(ctx context.Context, cfg *Config) (Adapter, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if cfg.ID == "" {
		return nil, fmt.Errorf("adapter ID is required")
	}

	if _, exists := r.adapters[cfg.ID]; exists {
		return nil, fmt.Errorf("adapter %s already registered", cfg.ID)
	}

	factory, ok := r.factories[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("no factory registered for adapter type %s", cfg.Type)
	}

	adapter := factory()
	if err := adapter.Connect(ctx, cfg); err != nil {
		return nil, fmt.Errorf("connect adapter: %w", err)
	}

	now := time.Now()
	cfg.CreatedAt = now
	cfg.UpdatedAt = now

	r.adapters[cfg.ID] = adapter
	r.configs[cfg.ID] = cfg

	return adapter, nil
}

// Unregister removes an adapter instance.
func (r *MemoryRegistry) Unregister(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	adapter, ok := r.adapters[id]
	if !ok {
		return fmt.Errorf("adapter %s not found", id)
	}

	if err := adapter.Disconnect(ctx); err != nil {
		return fmt.Errorf("disconnect adapter: %w", err)
	}

	delete(r.adapters, id)
	delete(r.configs, id)
	return nil
}

// Get returns an adapter instance by ID.
func (r *MemoryRegistry) Get(id string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	adapter, ok := r.adapters[id]
	return adapter, ok
}

// List returns all registered adapters for an agent.
func (r *MemoryRegistry) List(agentID string) []AdapterInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []AdapterInfo
	for id, adapter := range r.adapters {
		cfg := r.configs[id]
		if cfg.AgentID != agentID {
			continue
		}
		result = append(result, AdapterInfo{
			ID:          cfg.ID,
			Name:        cfg.Name,
			Type:        cfg.Type,
			Description: cfg.Description,
			AgentID:     cfg.AgentID,
			Status:      adapter.Status(),
			Actions:     len(adapter.ListActions()),
			CreatedAt:   cfg.CreatedAt,
		})
	}
	return result
}

// ListAll returns all registered adapters.
func (r *MemoryRegistry) ListAll() []AdapterInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]AdapterInfo, 0, len(r.adapters))
	for id, adapter := range r.adapters {
		cfg := r.configs[id]
		result = append(result, AdapterInfo{
			ID:          cfg.ID,
			Name:        cfg.Name,
			Type:        cfg.Type,
			Description: cfg.Description,
			AgentID:     cfg.AgentID,
			Status:      adapter.Status(),
			Actions:     len(adapter.ListActions()),
			CreatedAt:   cfg.CreatedAt,
		})
	}
	return result
}

// Count returns the number of registered adapters.
func (r *MemoryRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.adapters)
}
