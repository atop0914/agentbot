package template

import (
	"context"
	"time"
)

// MarketplaceEntry represents a template published to the marketplace.
type MarketplaceEntry struct {
	TemplateID    string    `json:"template_id"`
	Version       string    `json:"version"`
	Summary       string    `json:"summary"`
	License       string    `json:"license,omitempty"`
	Homepage      string    `json:"homepage,omitempty"`
	Downloads     int       `json:"downloads"`
	Installs      int       `json:"installs"`
	Featured      bool      `json:"featured"`
	Verified      bool      `json:"verified"`
	Status        ListingStatus `json:"status"`
	PublishedAt   time.Time `json:"published_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ListingStatus represents the status of a marketplace listing.
type ListingStatus string

const (
	ListingActive   ListingStatus = "active"
	ListingDraft    ListingStatus = "draft"
	ListingArchived ListingStatus = "archived"
	ListingRemoved  ListingStatus = "removed"
)

// Review represents a user review of a marketplace template.
type Review struct {
	ID         string    `json:"id"`
	TemplateID string    `json:"template_id"`
	UserID     string    `json:"user_id"`
	Rating     float64   `json:"rating"` // 1-5
	Comment    string    `json:"comment,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// ListingFilter defines filters for browsing the marketplace.
type ListingFilter struct {
	Category   string   `json:"category,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Search     string   `json:"search,omitempty"`
	MinRating  float64  `json:"min_rating,omitempty"`
	Featured   *bool    `json:"featured,omitempty"`
	Verified   *bool    `json:"verified,omitempty"`
	SortBy     string   `json:"sort_by,omitempty"`  // "downloads", "rating", "published_at", "installs"
	SortOrder  string   `json:"sort_order,omitempty"` // "asc", "desc"
	Limit      int      `json:"limit,omitempty"`
	Offset     int      `json:"offset,omitempty"`
}

// MarketplaceStats holds aggregate marketplace statistics.
type MarketplaceStats struct {
	TotalListings int     `json:"total_listings"`
	TotalDownloads int    `json:"total_downloads"`
	TotalInstalls int    `json:"total_installs"`
	AvgRating     float64 `json:"avg_rating"`
	Categories    []CategoryCount `json:"categories"`
}

// CategoryCount holds a category name and its count.
type CategoryCount struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
}

// Marketplace defines the marketplace service interface.
type Marketplace interface {
	// Publish publishes a template to the marketplace.
	Publish(ctx context.Context, templateID string, entry MarketplaceEntry) (*MarketplaceEntry, error)
	// Unpublish removes a template from the marketplace.
	Unpublish(ctx context.Context, templateID string) error
	// GetListing returns a marketplace listing by template ID.
	GetListing(ctx context.Context, templateID string) (*MarketplaceEntry, error)
	// UpdateListing updates a marketplace listing.
	UpdateListing(ctx context.Context, templateID string, entry MarketplaceEntry) (*MarketplaceEntry, error)
	// Browse lists marketplace entries with filters.
	Browse(ctx context.Context, filter ListingFilter) ([]*MarketplaceEntry, []*Template, error)
	// Install copies a marketplace template to the user's workspace.
	Install(ctx context.Context, templateID string, userID string) (*Template, error)
	// AddReview adds a review to a marketplace template.
	AddReview(ctx context.Context, review Review) (*Review, error)
	// GetReviews returns reviews for a marketplace template.
	GetReviews(ctx context.Context, templateID string) ([]*Review, error)
	// GetStats returns aggregate marketplace statistics.
	GetStats(ctx context.Context) (*MarketplaceStats, error)
	// SetFeatured marks or unmarks a template as featured.
	SetFeatured(ctx context.Context, templateID string, featured bool) error
}