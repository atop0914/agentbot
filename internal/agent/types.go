package agent

import (
	"context"
	"time"
)

// State represents the agent's current state
type State string

const (
	StateIdle       State = "idle"
	StateRunning    State = "running"
	StatePaused     State = "paused"
	StateError      State = "error"
	StateCompleted  State = "completed"
	StateTerminated State = "terminated"
)

// Agent represents an AI agent instance
type Agent struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	State       State     `json:"state"`
	ContainerID string    `json:"container_id"`
	Config      Config    `json:"config"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Config holds agent configuration
type Config struct {
	Model       string            `json:"model"`
	MaxTokens   int               `json:"max_tokens"`
	Temperature float64           `json:"temperature"`
	Tools       []string          `json:"tools"`
	Memory      MemoryConfig      `json:"memory"`
	Resources   ResourceConfig    `json:"resources"`
	Env         map[string]string `json:"env"`
}

// MemoryConfig configures agent memory
type MemoryConfig struct {
	ShortTermSize int `json:"short_term_size"` // Max items in short-term memory
	LongTermSize  int `json:"long_term_size"`  // Max items in long-term memory
}

// ResourceConfig defines resource limits
type ResourceConfig struct {
	CPU    string `json:"cpu"`    // e.g., "1" or "500m"
	Memory string `json:"memory"` // e.g., "512Mi"
	Disk   string `json:"disk"`   // e.g., "1Gi"
}

// Task represents a task assigned to an agent
type Task struct {
	ID          string    `json:"id"`
	AgentID     string    `json:"agent_id"`
	Goal        string    `json:"goal"`
	Subtasks    []Subtask `json:"subtasks"`
	State       TaskState `json:"state"`
	Result      string    `json:"result,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

// TaskState represents task progress
type TaskState string

const (
	TaskPending    TaskState = "pending"
	TaskInProgress TaskState = "in_progress"
	TaskCompleted  TaskState = "completed"
	TaskFailed     TaskState = "failed"
	TaskCancelled  TaskState = "cancelled"
)

// Subtask represents a decomposed task unit
type Subtask struct {
	ID       string    `json:"id"`
	TaskID   string    `json:"task_id"`
	Name     string    `json:"name"`
	State    TaskState `json:"state"`
	Result   string    `json:"result,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
}

// Repository defines the interface for agent persistence
type Repository interface {
	Create(ctx context.Context, agent *Agent) error
	GetByID(ctx context.Context, id string) (*Agent, error)
	Update(ctx context.Context, agent *Agent) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, offset, limit int) ([]*Agent, error)
	UpdateState(ctx context.Context, id string, state State) error
}

// Service defines the interface for agent operations
type Service interface {
	Create(ctx context.Context, req CreateRequest) (*Agent, error)
	Get(ctx context.Context, id string) (*Agent, error)
	Update(ctx context.Context, id string, req UpdateRequest) (*Agent, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, offset, limit int) ([]*Agent, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Pause(ctx context.Context, id string) error
	Resume(ctx context.Context, id string) error
	SendMessage(ctx context.Context, id string, msg Message) error
}

// CreateRequest defines the request to create an agent
type CreateRequest struct {
	Name        string         `json:"name" validate:"required"`
	Description string         `json:"description"`
	Config      Config         `json:"config"`
}

// UpdateRequest defines the request to update an agent
type UpdateRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Config      *Config `json:"config,omitempty"`
}

// Message represents a message to/from an agent
type Message struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Role      string    `json:"role"` // "user" or "agent"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Executor defines the interface for executing actions in cloud environment
type Executor interface {
	// CreateEnvironment creates a new isolated environment for an agent
	CreateEnvironment(ctx context.Context, agentID string, config ResourceConfig) (envID string, err error)
	// DestroyEnvironment destroys the agent's environment
	DestroyEnvironment(ctx context.Context, envID string) error
	// ExecuteCommand runs a command in the environment
	ExecuteCommand(ctx context.Context, envID string, command string) (output string, err error)
	// UploadFile uploads a file to the environment
	UploadFile(ctx context.Context, envID string, path string, content []byte) error
	// DownloadFile downloads a file from the environment
	DownloadFile(ctx context.Context, envID string, path string) (content []byte, err error)
	// GetStatus returns the environment status
	GetStatus(ctx context.Context, envID string) (string, error)
}
