package adapter

import "context"

// Adapter defines the interface that all application adapters must implement.
// Each adapter wraps an external service (email, calendar, etc.) and provides
// a unified interface for the Agent to interact with it.
type Adapter interface {
	// Name returns the human-readable name of the adapter.
	Name() string

	// Type returns the adapter category.
	Type() AdapterType

	// Connect establishes a connection to the external service using the provided config.
	Connect(ctx context.Context, cfg *Config) error

	// Disconnect closes the connection and releases resources.
	Disconnect(ctx context.Context) error

	// Status returns the current connection status.
	Status() AdapterStatus

	// ListActions returns all available actions for this adapter.
	ListActions() []Action

	// Execute performs a specific action with the given parameters.
	Execute(ctx context.Context, req *ActionRequest) (*ActionResult, error)
}

// Registry manages the lifecycle of adapter instances for agents.
type Registry interface {
	// Register creates and registers a new adapter instance.
	Register(ctx context.Context, cfg *Config) (Adapter, error)

	// Unregister removes an adapter instance.
	Unregister(ctx context.Context, id string) error

	// Get returns an adapter instance by ID.
	Get(id string) (Adapter, bool)

	// List returns all registered adapters for an agent.
	List(agentID string) []AdapterInfo

	// ListAll returns all registered adapters.
	ListAll() []AdapterInfo
}

// Factory creates adapter instances from configuration.
type Factory func() Adapter
