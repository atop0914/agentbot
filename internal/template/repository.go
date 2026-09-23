package template

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// Repository defines the template storage interface.
type Repository interface {
	Create(ctx context.Context, t *Template) error
	Get(ctx context.Context, id string) (*Template, error)
	Update(ctx context.Context, t *Template) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, filter Filter) ([]*Template, error)
}

// ExecRepository defines the execution storage interface.
type ExecRepository interface {
	Create(ctx context.Context, e *Execution) error
	Get(ctx context.Context, id string) (*Execution, error)
	Update(ctx context.Context, e *Execution) error
	ListByTemplate(ctx context.Context, templateID string) ([]*Execution, error)
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu   sync.RWMutex
	data map[string]*Template
}

// NewMemoryRepository creates a new in-memory template repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{data: make(map[string]*Template)}
}

func (r *MemoryRepository) Create(_ context.Context, t *Template) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.data[t.ID]; exists {
		return ErrTemplateExists
	}
	r.data[t.ID] = t
	return nil
}

func (r *MemoryRepository) Get(_ context.Context, id string) (*Template, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.data[id]
	if !ok {
		return nil, ErrNotFound
	}
	return t, nil
}

func (r *MemoryRepository) Update(_ context.Context, t *Template) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.data[t.ID]; !exists {
		return ErrNotFound
	}
	r.data[t.ID] = t
	return nil
}

func (r *MemoryRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.data[id]; !exists {
		return ErrNotFound
	}
	delete(r.data, id)
	return nil
}

func (r *MemoryRepository) List(_ context.Context, filter Filter) ([]*Template, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*Template
	for _, t := range r.data {
		if !matchFilter(t, filter) {
			continue
		}
		cp := *t
		result = append(result, &cp)
	}

	// Sort
	sortTemplates(result, filter.SortBy, filter.SortOrder)

	// Paginate
	if filter.Offset > 0 && filter.Offset < len(result) {
		result = result[filter.Offset:]
	}
	if filter.Limit > 0 && filter.Limit < len(result) {
		result = result[:filter.Limit]
	}
	return result, nil
}

func matchFilter(t *Template, f Filter) bool {
	if f.Author != "" && t.Author != f.Author {
		return false
	}
	if f.Category != "" && t.Category != f.Category {
		return false
	}
	if f.Public != nil && t.Public != *f.Public {
		return false
	}
	if f.Search != "" {
		q := strings.ToLower(f.Search)
		if !strings.Contains(strings.ToLower(t.Name), q) &&
			!strings.Contains(strings.ToLower(t.Description), q) {
			return false
		}
	}
	if len(f.Tags) > 0 {
		tagSet := make(map[string]bool, len(t.Tags))
		for _, tag := range t.Tags {
			tagSet[tag] = true
		}
		for _, ft := range f.Tags {
			if !tagSet[ft] {
				return false
			}
		}
	}
	return true
}

func sortTemplates(ts []*Template, sortBy, order string) {
	if sortBy == "" {
		sortBy = "created_at"
	}
	desc := order == "desc"

	sort.Slice(ts, func(i, j int) bool {
		var less bool
		switch sortBy {
		case "name":
			less = ts[i].Name < ts[j].Name
		case "rating":
			less = ts[i].Rating < ts[j].Rating
		case "usage_count":
			less = ts[i].UsageCount < ts[j].UsageCount
		default: // created_at
			less = ts[i].CreatedAt.Before(ts[j].CreatedAt)
		}
		if desc {
			return !less
		}
		return less
	})
}

// MemoryExecRepository is an in-memory implementation of ExecRepository.
type MemoryExecRepository struct {
	mu   sync.RWMutex
	data map[string]*Execution
}

// NewMemoryExecRepository creates a new in-memory execution repository.
func NewMemoryExecRepository() *MemoryExecRepository {
	return &MemoryExecRepository{data: make(map[string]*Execution)}
}

func (r *MemoryExecRepository) Create(_ context.Context, e *Execution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[e.ID] = e
	return nil
}

func (r *MemoryExecRepository) Get(_ context.Context, id string) (*Execution, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.data[id]
	if !ok {
		return nil, ErrNotFound
	}
	return e, nil
}

func (r *MemoryExecRepository) Update(_ context.Context, e *Execution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.data[e.ID]; !exists {
		return ErrNotFound
	}
	r.data[e.ID] = e
	return nil
}

func (r *MemoryExecRepository) ListByTemplate(_ context.Context, templateID string) ([]*Execution, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*Execution
	for _, e := range r.data {
		if e.TemplateID == templateID {
			cp := *e
			result = append(result, &cp)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].StartedAt.After(result[j].StartedAt)
	})
	return result, nil
}
