package template

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// MarketplaceRepository defines the marketplace listing storage interface.
type MarketplaceRepository interface {
	Create(ctx context.Context, entry *MarketplaceEntry) error
	Get(ctx context.Context, templateID string) (*MarketplaceEntry, error)
	Update(ctx context.Context, entry *MarketplaceEntry) error
	Delete(ctx context.Context, templateID string) error
	List(ctx context.Context, filter ListingFilter) ([]*MarketplaceEntry, error)
}

// ReviewRepository defines the review storage interface.
type ReviewRepository interface {
	Create(ctx context.Context, review *Review) error
	ListByTemplate(ctx context.Context, templateID string) ([]*Review, error)
}

// InMemoryMarketplaceRepo is an in-memory marketplace repository.
type InMemoryMarketplaceRepo struct {
	mu    sync.RWMutex
	data  map[string]*MarketplaceEntry // keyed by template_id
}

// NewInMemoryMarketplaceRepo creates a new in-memory marketplace repo.
func NewInMemoryMarketplaceRepo() *InMemoryMarketplaceRepo {
	return &InMemoryMarketplaceRepo{data: make(map[string]*MarketplaceEntry)}
}

func (r *InMemoryMarketplaceRepo) Create(_ context.Context, entry *MarketplaceEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.data[entry.TemplateID]; exists {
		return ErrAlreadyPublished
	}
	r.data[entry.TemplateID] = entry
	return nil
}

func (r *InMemoryMarketplaceRepo) Get(_ context.Context, templateID string) (*MarketplaceEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.data[templateID]
	if !ok {
		return nil, ErrNotPublished
	}
	cp := *entry
	return &cp, nil
}

func (r *InMemoryMarketplaceRepo) Update(_ context.Context, entry *MarketplaceEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.data[entry.TemplateID]; !exists {
		return ErrNotPublished
	}
	r.data[entry.TemplateID] = entry
	return nil
}

func (r *InMemoryMarketplaceRepo) Delete(_ context.Context, templateID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.data[templateID]; !exists {
		return ErrNotPublished
	}
	delete(r.data, templateID)
	return nil
}

func (r *InMemoryMarketplaceRepo) List(_ context.Context, filter ListingFilter) ([]*MarketplaceEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*MarketplaceEntry
	for _, e := range r.data {
		if !matchListingFilter(e, filter) {
			continue
		}
		cp := *e
		result = append(result, &cp)
	}

	sortListings(result, filter.SortBy, filter.SortOrder)

	if filter.Offset > 0 && filter.Offset < len(result) {
		result = result[filter.Offset:]
	}
	if filter.Limit > 0 && filter.Limit < len(result) {
		result = result[:filter.Limit]
	}
	return result, nil
}

func matchListingFilter(e *MarketplaceEntry, f ListingFilter) bool {
	if e.Status != ListingActive {
		return false
	}
	if f.Featured != nil && e.Featured != *f.Featured {
		return false
	}
	if f.Verified != nil && e.Verified != *f.Verified {
		return false
	}
	if f.MinRating > 0 {
		// Note: rating is on the Template, not on the MarketplaceEntry.
		// We'll handle this in the service layer where we have access to both.
	}
	return true
}

func sortListings(entries []*MarketplaceEntry, sortBy, order string) {
	if sortBy == "" {
		sortBy = "published_at"
	}
	desc := order != "asc"

	sort.Slice(entries, func(i, j int) bool {
		var less bool
		switch sortBy {
		case "downloads":
			less = entries[i].Downloads < entries[j].Downloads
		case "rating":
			less = false // rating is on Template, sort in service layer
		case "installs":
			less = entries[i].Installs < entries[j].Installs
		default: // published_at
			less = entries[i].PublishedAt.Before(entries[j].PublishedAt)
		}
		if desc {
			return !less
		}
		return less
	})
}

// InMemoryReviewRepo is an in-memory review repository.
type InMemoryReviewRepo struct {
	mu    sync.RWMutex
	data  map[string][]*Review // keyed by template_id
}

// NewInMemoryReviewRepo creates a new in-memory review repo.
func NewInMemoryReviewRepo() *InMemoryReviewRepo {
	return &InMemoryReviewRepo{data: make(map[string][]*Review)}
}

func (r *InMemoryReviewRepo) Create(_ context.Context, review *Review) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[review.TemplateID] = append(r.data[review.TemplateID], review)
	return nil
}

func (r *InMemoryReviewRepo) ListByTemplate(_ context.Context, templateID string) ([]*Review, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reviews, ok := r.data[templateID]
	if !ok {
		return []*Review{}, nil
	}
	result := make([]*Review, len(reviews))
	for i, rv := range reviews {
		cp := *rv
		result[i] = &cp
	}
	// Sort by created_at descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

// SearchByTags filters marketplace entries by tags (needs template repo access).
func searchByTags(tags []string, templateTags []string) bool {
	if len(tags) == 0 {
		return true
	}
	tagSet := make(map[string]bool, len(templateTags))
	for _, t := range templateTags {
		tagSet[strings.ToLower(t)] = true
	}
	for _, ft := range tags {
		if !tagSet[strings.ToLower(ft)] {
			return false
		}
	}
	return true
}