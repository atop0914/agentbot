package template

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// TemplateService implements the Service interface.
type TemplateService struct {
	repo   Repository
	execRepo ExecRepository
}

// NewService creates a new template service.
func NewService(repo Repository, execRepo ExecRepository) *TemplateService {
	return &TemplateService{repo: repo, execRepo: execRepo}
}

func (s *TemplateService) Create(ctx context.Context, tmpl Template) (*Template, error) {
	if tmpl.Name == "" {
		return nil, ErrInvalidInput
	}
	tmpl.ID = uuid.New().String()
	now := time.Now()
	tmpl.CreatedAt = now
	tmpl.UpdatedAt = now
	if tmpl.Steps == nil {
		tmpl.Steps = []Step{}
	}
	if err := s.repo.Create(ctx, &tmpl); err != nil {
		return nil, err
	}
	return &tmpl, nil
}

func (s *TemplateService) Get(ctx context.Context, id string) (*Template, error) {
	return s.repo.Get(ctx, id)
}

func (s *TemplateService) Update(ctx context.Context, id string, tmpl Template) (*Template, error) {
	existing, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if tmpl.Name != "" {
		existing.Name = tmpl.Name
	}
	if tmpl.Description != "" {
		existing.Description = tmpl.Description
	}
	if tmpl.Category != "" {
		existing.Category = tmpl.Category
	}
	if tmpl.Steps != nil {
		existing.Steps = tmpl.Steps
	}
	if tmpl.Variables != nil {
		existing.Variables = tmpl.Variables
	}
	if tmpl.Tags != nil {
		existing.Tags = tmpl.Tags
	}
	existing.Public = tmpl.Public
	existing.UpdatedAt = time.Now()
	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

func (s *TemplateService) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

func (s *TemplateService) List(ctx context.Context, filter Filter) ([]*Template, error) {
	return s.repo.List(ctx, filter)
}

func (s *TemplateService) Execute(ctx context.Context, templateID, agentID string, variables map[string]string) (*Execution, error) {
	_, err := s.repo.Get(ctx, templateID)
	if err != nil {
		return nil, err
	}

	exec := &Execution{
		ID:         uuid.New().String(),
		TemplateID: templateID,
		AgentID:    agentID,
		Status:     "running",
		Variables:  variables,
		Results:    []StepResult{},
		StartedAt:  time.Now(),
	}
	if err := s.execRepo.Create(ctx, exec); err != nil {
		return nil, err
	}
	return exec, nil
}

func (s *TemplateService) GetExecution(ctx context.Context, id string) (*Execution, error) {
	return s.execRepo.Get(ctx, id)
}

func (s *TemplateService) ListExecutions(ctx context.Context, templateID string) ([]*Execution, error) {
	return s.execRepo.ListByTemplate(ctx, templateID)
}

func (s *TemplateService) Share(_ context.Context, _ string, _ string) error {
	// In a real implementation, this would add to a sharing table
	return nil
}

func (s *TemplateService) Unshare(_ context.Context, _ string, _ string) error {
	return nil
}

func (s *TemplateService) Import(ctx context.Context, data []byte) (*Template, error) {
	var tmpl Template
	if err := json.Unmarshal(data, &tmpl); err != nil {
		return nil, ErrInvalidInput
	}
	tmpl.ID = ""
	return s.Create(ctx, tmpl)
}

func (s *TemplateService) Export(_ context.Context, templateID string) ([]byte, error) {
	tmpl, err := s.repo.Get(context.Background(), templateID)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(tmpl, "", "  ")
}

func (s *TemplateService) Rate(ctx context.Context, templateID string, rating float64) error {
	if rating < 0 || rating > 5 {
		return ErrInvalidInput
	}
	tmpl, err := s.repo.Get(ctx, templateID)
	if err != nil {
		return err
	}
	// Simple moving average
	total := float64(tmpl.UsageCount)*tmpl.Rating + rating
	tmpl.UsageCount++
	tmpl.Rating = total / float64(tmpl.UsageCount)
	tmpl.UpdatedAt = time.Now()
	return s.repo.Update(ctx, tmpl)
}

func (s *TemplateService) GetPopular(ctx context.Context, limit int) ([]*Template, error) {
	return s.repo.List(ctx, Filter{
		SortBy:    "usage_count",
		SortOrder: "desc",
		Limit:     limit,
	})
}

func (s *TemplateService) GetByCategory(ctx context.Context, category string) ([]*Template, error) {
	return s.repo.List(ctx, Filter{Category: category})
}
