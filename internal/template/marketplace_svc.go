package template

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// MarketplaceService implements the Marketplace interface.
type MarketplaceService struct {
	marketplaceRepo MarketplaceRepository
	reviewRepo      ReviewRepository
	templateRepo    Repository
}

// NewMarketplaceService creates a new marketplace service.
func NewMarketplaceService(marketplaceRepo MarketplaceRepository, reviewRepo ReviewRepository, templateRepo Repository) *MarketplaceService {
	return &MarketplaceService{
		marketplaceRepo: marketplaceRepo,
		reviewRepo:      reviewRepo,
		templateRepo:    templateRepo,
	}
}

func (s *MarketplaceService) Publish(ctx context.Context, templateID string, entry MarketplaceEntry) (*MarketplaceEntry, error) {
	// Verify the template exists
	tmpl, err := s.templateRepo.Get(ctx, templateID)
	if err != nil {
		return nil, ErrNotFound
	}

	// Check if already published
	if _, err := s.marketplaceRepo.Get(ctx, templateID); err == nil {
		return nil, ErrAlreadyPublished
	}

	now := time.Now()
	entry.TemplateID = templateID
	entry.Status = ListingActive
	entry.Downloads = 0
	entry.Installs = 0
	entry.PublishedAt = now
	entry.UpdatedAt = now

	if entry.Version == "" {
		entry.Version = "1.0.0"
	}
	if entry.Summary == "" {
		entry.Summary = tmpl.Description
	}

	// Mark the template as public
	tmpl.Public = true
	if err := s.templateRepo.Update(ctx, tmpl); err != nil {
		return nil, err
	}

	if err := s.marketplaceRepo.Create(ctx, &entry); err != nil {
		return nil, err
	}
	return &entry, nil
}

func (s *MarketplaceService) Unpublish(ctx context.Context, templateID string) error {
	return s.marketplaceRepo.Delete(ctx, templateID)
}

func (s *MarketplaceService) GetListing(ctx context.Context, templateID string) (*MarketplaceEntry, error) {
	return s.marketplaceRepo.Get(ctx, templateID)
}

func (s *MarketplaceService) UpdateListing(ctx context.Context, templateID string, entry MarketplaceEntry) (*MarketplaceEntry, error) {
	existing, err := s.marketplaceRepo.Get(ctx, templateID)
	if err != nil {
		return nil, err
	}
	if entry.Version != "" {
		existing.Version = entry.Version
	}
	if entry.Summary != "" {
		existing.Summary = entry.Summary
	}
	if entry.License != "" {
		existing.License = entry.License
	}
	if entry.Homepage != "" {
		existing.Homepage = entry.Homepage
	}
	existing.Featured = entry.Featured
	existing.Verified = entry.Verified
	if entry.Status != "" {
		existing.Status = entry.Status
	}
	existing.UpdatedAt = time.Now()

	if err := s.marketplaceRepo.Update(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

func (s *MarketplaceService) Browse(ctx context.Context, filter ListingFilter) ([]*MarketplaceEntry, []*Template, error) {
	entries, err := s.marketplaceRepo.List(ctx, filter)
	if err != nil {
		return nil, nil, err
	}

	var templates []*Template
	for _, e := range entries {
		tmpl, err := s.templateRepo.Get(ctx, e.TemplateID)
		if err != nil {
			continue // skip if template was deleted
		}

		// Apply tag and rating filters (need template data)
		if len(filter.Tags) > 0 && !searchByTags(filter.Tags, tmpl.Tags) {
			continue
		}
		if filter.MinRating > 0 && tmpl.Rating < filter.MinRating {
			continue
		}
		if filter.Search != "" {
			q := filter.Search
			if !contains(tmpl.Name, q) && !contains(tmpl.Description, q) && !contains(e.Summary, q) {
				continue
			}
		}

		templates = append(templates, tmpl)
	}

	// Re-filter entries to match the filtered templates
	filteredEntries := make([]*MarketplaceEntry, 0, len(templates))
	tmplSet := make(map[string]bool, len(templates))
	for _, t := range templates {
		tmplSet[t.ID] = true
	}
	for _, e := range entries {
		if tmplSet[e.TemplateID] {
			filteredEntries = append(filteredEntries, e)
		}
	}

	return filteredEntries, templates, nil
}

func (s *MarketplaceService) Install(ctx context.Context, templateID string, userID string) (*Template, error) {
	entry, err := s.marketplaceRepo.Get(ctx, templateID)
	if err != nil {
		return nil, ErrNotPublished
	}

	// Get the original template
	original, err := s.templateRepo.Get(ctx, templateID)
	if err != nil {
		return nil, ErrNotFound
	}

	// Prevent self-install (optional, can be removed if not needed)
	if original.Author == userID {
		return nil, ErrSelfInstall
	}

	// Fork the template
	forked := *original
	forked.ID = uuid.New().String()
	forked.Author = userID
	forked.Public = false
	forked.UsageCount = 0
	forked.Rating = 0
	forked.CreatedAt = time.Now()
	forked.UpdatedAt = time.Now()

	// Deep copy steps
	forked.Steps = make([]Step, len(original.Steps))
	copy(forked.Steps, original.Steps)

	// Deep copy variables
	if original.Variables != nil {
		forked.Variables = make([]Variable, len(original.Variables))
		copy(forked.Variables, original.Variables)
	}

	// Deep copy tags
	if original.Tags != nil {
		forked.Tags = make([]string, len(original.Tags))
		copy(forked.Tags, original.Tags)
	}

	if err := s.templateRepo.Create(ctx, &forked); err != nil {
		return nil, err
	}

	// Increment install count
	entry.Installs++
	entry.UpdatedAt = time.Now()
	_ = s.marketplaceRepo.Update(ctx, entry)

	// Also increment usage count on original
	original.UsageCount++
	_ = s.templateRepo.Update(ctx, original)

	return &forked, nil
}

func (s *MarketplaceService) AddReview(ctx context.Context, review Review) (*Review, error) {
	if review.UserID == "" || review.TemplateID == "" {
		return nil, ErrInvalidInput
	}
	if review.Rating < 1 || review.Rating > 5 {
		return nil, ErrInvalidInput
	}

	// Verify template is published
	if _, err := s.marketplaceRepo.Get(ctx, review.TemplateID); err != nil {
		return nil, ErrNotPublished
	}

	review.ID = uuid.New().String()
	review.CreatedAt = time.Now()

	if err := s.reviewRepo.Create(ctx, &review); err != nil {
		return nil, err
	}

	// Update template rating with new review
	tmpl, err := s.templateRepo.Get(ctx, review.TemplateID)
	if err == nil {
		reviews, _ := s.reviewRepo.ListByTemplate(ctx, review.TemplateID)
		if len(reviews) > 0 {
			var total float64
			for _, r := range reviews {
				total += r.Rating
			}
			tmpl.Rating = total / float64(len(reviews))
			tmpl.UpdatedAt = time.Now()
			_ = s.templateRepo.Update(ctx, tmpl)
		}
	}

	return &review, nil
}

func (s *MarketplaceService) GetReviews(ctx context.Context, templateID string) ([]*Review, error) {
	return s.reviewRepo.ListByTemplate(ctx, templateID)
}

func (s *MarketplaceService) GetStats(ctx context.Context) (*MarketplaceStats, error) {
	entries, err := s.marketplaceRepo.List(ctx, ListingFilter{})
	if err != nil {
		return nil, err
	}

	stats := &MarketplaceStats{
		TotalListings: len(entries),
		Categories:    make([]CategoryCount, 0),
	}

	catMap := make(map[string]int)
	var totalRating float64
	ratingCount := 0

	for _, e := range entries {
		stats.TotalDownloads += e.Downloads
		stats.TotalInstalls += e.Installs

		// Get template for category and rating
		tmpl, err := s.templateRepo.Get(ctx, e.TemplateID)
		if err != nil {
			continue
		}
		if tmpl.Category != "" {
			catMap[tmpl.Category]++
		}
		if tmpl.Rating > 0 {
			totalRating += tmpl.Rating
			ratingCount++
		}
	}

	if ratingCount > 0 {
		stats.AvgRating = totalRating / float64(ratingCount)
	}

	for cat, count := range catMap {
		stats.Categories = append(stats.Categories, CategoryCount{Category: cat, Count: count})
	}

	return stats, nil
}

func (s *MarketplaceService) SetFeatured(ctx context.Context, templateID string, featured bool) error {
	entry, err := s.marketplaceRepo.Get(ctx, templateID)
	if err != nil {
		return err
	}
	entry.Featured = featured
	entry.UpdatedAt = time.Now()
	return s.marketplaceRepo.Update(ctx, entry)
}

// contains checks if s contains substr (case-insensitive).
func contains(s, substr string) bool {
	return len(substr) == 0 || len(s) >= len(substr) && containsLower(s, substr)
}

func containsLower(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if toLower(s[i:i+len(substr)]) == toLower(substr) {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// Ensure MarketplaceService implements Marketplace interface at compile time.
var _ Marketplace = (*MarketplaceService)(nil)

// Ensure JSON encoding works for all types.
var _ = json.Marshal