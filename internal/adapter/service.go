package adapter

import (
	"context"
	"fmt"
)

// Service provides business logic for managing adapters.
type Service struct {
	registry *MemoryRegistry
}

// NewService creates a new adapter service with the given registry.
func NewService(registry *MemoryRegistry) *Service {
	return &Service{registry: registry}
}

// Register creates a new adapter for an agent.
func (s *Service) Register(ctx context.Context, cfg *Config) (*AdapterInfo, error) {
	adapter, err := s.registry.Register(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return &AdapterInfo{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Type:        cfg.Type,
		Description: cfg.Description,
		AgentID:     cfg.AgentID,
		Status:      adapter.Status(),
		Actions:     len(adapter.ListActions()),
		CreatedAt:   cfg.CreatedAt,
	}, nil
}

// Unregister removes an adapter.
func (s *Service) Unregister(ctx context.Context, id string) error {
	return s.registry.Unregister(ctx, id)
}

// Get returns an adapter by ID.
func (s *Service) Get(id string) (Adapter, bool) {
	return s.registry.Get(id)
}

// GetInfo returns adapter info by ID.
func (s *Service) GetInfo(id string) (*AdapterInfo, error) {
	adapter, ok := s.registry.Get(id)
	if !ok {
		return nil, fmt.Errorf("adapter %s not found", id)
	}

	cfg := s.registry.configs[id]
	return &AdapterInfo{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Type:        cfg.Type,
		Description: cfg.Description,
		AgentID:     cfg.AgentID,
		Status:      adapter.Status(),
		Actions:     len(adapter.ListActions()),
		CreatedAt:   cfg.CreatedAt,
	}, nil
}

// List returns all adapters for an agent.
func (s *Service) List(agentID string) []AdapterInfo {
	return s.registry.List(agentID)
}

// ListAll returns all registered adapters.
func (s *Service) ListAll() []AdapterInfo {
	return s.registry.ListAll()
}

// ListActions returns available actions for an adapter.
func (s *Service) ListActions(id string) ([]Action, error) {
	adapter, ok := s.registry.Get(id)
	if !ok {
		return nil, fmt.Errorf("adapter %s not found", id)
	}
	return adapter.ListActions(), nil
}

// Execute performs an action on an adapter.
func (s *Service) Execute(ctx context.Context, id string, req *ActionRequest) (*ActionResult, error) {
	adapter, ok := s.registry.Get(id)
	if !ok {
		return nil, fmt.Errorf("adapter %s not found", id)
	}
	return adapter.Execute(ctx, req)
}

// Registry returns the underlying registry for factory registration.
func (s *Service) Registry() *MemoryRegistry {
	return s.registry
}
