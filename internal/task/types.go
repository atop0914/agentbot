package task

import (
	"context"
	"time"
)

// TaskState represents the state of a task
type TaskState string

const (
	StatePending    TaskState = "pending"
	StateInProgress TaskState = "in_progress"
	StateCompleted  TaskState = "completed"
	StateFailed     TaskState = "failed"
	StateCancelled  TaskState = "cancelled"
	StatePaused     TaskState = "paused"
)

// Task represents a high-level goal
type Task struct {
	ID          string    `json:"id"`
	AgentID     string    `json:"agent_id"`
	Goal        string    `json:"goal"`
	State       TaskState `json:"state"`
	Subtasks    []Subtask `json:"subtasks"`
	Result      string    `json:"result,omitempty"`
	Error       string    `json:"error,omitempty"`
	MaxRetries  int       `json:"max_retries"`
	RetryCount  int       `json:"retry_count"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

// Subtask represents a decomposed unit of work
type Subtask struct {
	ID          string    `json:"id"`
	TaskID      string    `json:"task_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	State       TaskState `json:"state"`
	Result      string    `json:"result,omitempty"`
	Error       string    `json:"error,omitempty"`
	DependsOn   []string  `json:"depends_on,omitempty"`
	Actions     []Action  `json:"actions"`
	CreatedAt   time.Time `json:"created_at"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

// Action represents a single executable action
type Action struct {
	ID          string            `json:"id"`
	SubtaskID   string            `json:"subtask_id"`
	Type        ActionType        `json:"type"`
	Name        string            `json:"name"`
	Params      map[string]string `json:"params"`
	State       TaskState         `json:"state"`
	Result      string            `json:"result,omitempty"`
	Error       string            `json:"error,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	CompletedAt time.Time         `json:"completed_at,omitempty"`
}

// ActionType represents the type of action
type ActionType string

const (
	ActionBrowser     ActionType = "browser"
	ActionTerminal    ActionType = "terminal"
	ActionFile        ActionType = "file"
	ActionAPI         ActionType = "api"
	ActionWait        ActionType = "wait"
	ActionConditional ActionType = "conditional"
)

// Decomposer breaks down goals into subtasks
type Decomposer interface {
	Decompose(ctx context.Context, goal string) ([]Subtask, error)
}

// Executor executes actions
type Executor interface {
	Execute(ctx context.Context, action Action) (result string, err error)
}

// Manager manages task lifecycle
type Manager interface {
	Create(ctx context.Context, agentID string, goal string) (*Task, error)
	Get(ctx context.Context, id string) (*Task, error)
	List(ctx context.Context, agentID string, state TaskState) ([]*Task, error)
	Start(ctx context.Context, id string) error
	Cancel(ctx context.Context, id string) error
	Complete(ctx context.Context, id string, result string) error
	Fail(ctx context.Context, id string, err error) error
	Retry(ctx context.Context, id string) error
	GetProgress(ctx context.Context, id string) (*Progress, error)
}

// Progress represents task execution progress
type Progress struct {
	TaskID       string    `json:"task_id"`
	Total        int       `json:"total"`
	Completed    int       `json:"completed"`
	Failed       int       `json:"failed"`
	InProgress   int       `json:"in_progress"`
	Percentage   float64   `json:"percentage"`
	EstimatedETA time.Time `json:"estimated_eta"`
}

// Recorder records workflow for template creation
type Recorder interface {
	StartRecording(ctx context.Context, taskID string) error
	RecordAction(ctx context.Context, action Action) error
	StopRecording(ctx context.Context) (*WorkflowTemplate, error)
}

// WorkflowTemplate represents a recorded workflow
type WorkflowTemplate struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Steps       []Step    `json:"steps"`
	CreatedAt   time.Time `json:"created_at"`
}

// Step represents a step in a workflow template
type Step struct {
	Name     string     `json:"name"`
	Action   ActionType `json:"action"`
	Params   map[string]string `json:"params"`
	Optional bool       `json:"optional"`
}
