package template

import (
	"context"
	"time"
)

// Template represents a workflow template
type Template struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	Author      string    `json:"author"`
	Steps       []Step    `json:"steps"`
	Variables   []Variable `json:"variables,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	Public      bool      `json:"public"`
	UsageCount  int       `json:"usage_count"`
	Rating      float64   `json:"rating"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Step represents a step in a template
type Step struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Type        StepType          `json:"type"`
	Config      map[string]string `json:"config"`
	Optional    bool              `json:"optional"`
	Condition   string            `json:"condition,omitempty"`
	Timeout     time.Duration     `json:"timeout,omitempty"`
}

// StepType represents the type of step
type StepType string

const (
	StepBrowser     StepType = "browser"
	StepTerminal    StepType = "terminal"
	StepFile        StepType = "file"
	StepAPI         StepType = "api"
	StepWait        StepType = "wait"
	StepCondition   StepType = "condition"
	StepLoop        StepType = "loop"
	StepSubWorkflow StepType = "sub_workflow"
)

// Variable represents a template variable
type Variable struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"` // "string", "number", "boolean", "list"
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required"`
}

// Execution represents a template execution
type Execution struct {
	ID          string        `json:"id"`
	TemplateID  string        `json:"template_id"`
	AgentID     string        `json:"agent_id"`
	Status      string        `json:"status"` // "running", "completed", "failed"
	Variables   map[string]string `json:"variables"`
	Results     []StepResult  `json:"results"`
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at,omitempty"`
	Error       string        `json:"error,omitempty"`
}

// StepResult represents the result of a step execution
type StepResult struct {
	StepID    string    `json:"step_id"`
	Status    string    `json:"status"`
	Output    string    `json:"output,omitempty"`
	Error     string    `json:"error,omitempty"`
	Duration  time.Duration `json:"duration"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

// Service defines the template service interface
type Service interface {
	// Create creates a new template
	Create(ctx context.Context, template Template) (*Template, error)
	// Get returns a template by ID
	Get(ctx context.Context, id string) (*Template, error)
	// Update updates a template
	Update(ctx context.Context, id string, template Template) (*Template, error)
	// Delete deletes a template
	Delete(ctx context.Context, id string) error
	// List lists templates
	List(ctx context.Context, filter Filter) ([]*Template, error)
	
	// Execute executes a template
	Execute(ctx context.Context, templateID string, agentID string, variables map[string]string) (*Execution, error)
	// GetExecution returns an execution by ID
	GetExecution(ctx context.Context, id string) (*Execution, error)
	// ListExecutions lists executions for a template
	ListExecutions(ctx context.Context, templateID string) ([]*Execution, error)
	
	// Share shares a template
	Share(ctx context.Context, templateID string, userID string) error
	// Unshare unshares a template
	Unshare(ctx context.Context, templateID string, userID string) error
	// Import imports a template from file
	Import(ctx context.Context, data []byte) (*Template, error)
	// Export exports a template to file
	Export(ctx context.Context, templateID string) ([]byte, error)
	
	// Rate rates a template
	Rate(ctx context.Context, templateID string, rating float64) error
	// GetPopular returns popular templates
	GetPopular(ctx context.Context, limit int) ([]*Template, error)
	// GetByCategory returns templates by category
	GetByCategory(ctx context.Context, category string) ([]*Template, error)
}

// Filter defines template filter
type Filter struct {
	Author    string   `json:"author,omitempty"`
	Category  string   `json:"category,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Public    *bool    `json:"public,omitempty"`
	Search    string   `json:"search,omitempty"`
	Limit     int      `json:"limit,omitempty"`
	Offset    int      `json:"offset,omitempty"`
	SortBy    string   `json:"sort_by,omitempty"` // "name", "rating", "usage_count", "created_at"
	SortOrder string   `json:"sort_order,omitempty"` // "asc", "desc"
}

// Recorder records workflow for template creation
type Recorder interface {
	// StartRecording starts recording a workflow
	StartRecording(ctx context.Context, agentID string, name string) error
	// RecordStep records a step execution
	RecordStep(ctx context.Context, step Step, result StepResult) error
	// StopRecording stops recording and creates template
	StopRecording(ctx context.Context) (*Template, error)
}
