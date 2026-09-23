package template

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// InMemoryRecorder implements the Recorder interface for workflow recording.
type InMemoryRecorder struct {
	mu       sync.Mutex
	repo     Repository
	recording bool
	agentID  string
	name     string
	steps    []Step
	results  []StepResult
}

// NewInMemoryRecorder creates a new in-memory workflow recorder.
func NewInMemoryRecorder(repo Repository) *InMemoryRecorder {
	return &InMemoryRecorder{repo: repo}
}

func (r *InMemoryRecorder) StartRecording(_ context.Context, agentID, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recording {
		return ErrAlreadyRecording
	}
	if agentID == "" || name == "" {
		return ErrInvalidInput
	}
	r.recording = true
	r.agentID = agentID
	r.name = name
	r.steps = nil
	r.results = nil
	return nil
}

func (r *InMemoryRecorder) RecordStep(_ context.Context, step Step, result StepResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.recording {
		return ErrNotRecording
	}
	if step.ID == "" {
		step.ID = uuid.New().String()
	}
	r.steps = append(r.steps, step)
	r.results = append(r.results, result)
	return nil
}

func (r *InMemoryRecorder) StopRecording(ctx context.Context) (*Template, error) {
	r.mu.Lock()
	if !r.recording {
		r.mu.Unlock()
		return nil, ErrNotRecording
	}

	steps := make([]Step, len(r.steps))
	copy(steps, r.steps)

	r.recording = false
	r.mu.Unlock()

	tmpl := &Template{
		ID:          uuid.New().String(),
		Name:        r.name,
		Description: "Recorded from agent " + r.agentID,
		Category:    "recorded",
		Author:      r.agentID,
		Steps:       steps,
		Public:      false,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := r.repo.Create(ctx, tmpl); err != nil {
		return nil, err
	}
	return tmpl, nil
}

// IsRecording returns whether the recorder is currently active.
func (r *InMemoryRecorder) IsRecording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recording
}

// StepCount returns the number of recorded steps.
func (r *InMemoryRecorder) StepCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.steps)
}
