package template

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func setupMarketplace(t *testing.T) (*MarketplaceService, Repository, *InMemoryMarketplaceRepo) {
	t.Helper()
	tmplRepo := NewMemoryRepository()
	mpRepo := NewInMemoryMarketplaceRepo()
	rvRepo := NewInMemoryReviewRepo()
	svc := NewMarketplaceService(mpRepo, rvRepo, tmplRepo)
	return svc, tmplRepo, mpRepo
}

func createTestTemplate(t *testing.T, repo Repository, name string) *Template {
	t.Helper()
	tmpl := &Template{
		ID:          uuid.New().String(),
		Name:        name,
		Description: "Test template for " + name,
		Category:    "automation",
		Author:      "user1",
		Steps:       []Step{{ID: "s1", Name: "step1", Type: StepTerminal}},
		Tags:        []string{"test", "automation"},
		Public:      false,
	}
	if err := repo.Create(context.Background(), tmpl); err != nil {
		t.Fatalf("create template: %v", err)
	}
	return tmpl
}

func TestMarketplace_Publish(t *testing.T) {
	svc, repo, _ := setupMarketplace(t)
	ctx := context.Background()
	tmpl := createTestTemplate(t, repo, "deploy-app")

	entry := MarketplaceEntry{
		Version:  "1.0.0",
		Summary:  "Deploy application to cloud",
		License:  "MIT",
	}

	published, err := svc.Publish(ctx, tmpl.ID, entry)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.TemplateID != tmpl.ID {
		t.Errorf("template ID = %s, want %s", published.TemplateID, tmpl.ID)
	}
	if published.Status != ListingActive {
		t.Errorf("status = %s, want %s", published.Status, ListingActive)
	}
	if published.Version != "1.0.0" {
		t.Errorf("version = %s, want 1.0.0", published.Version)
	}
	if published.Downloads != 0 || published.Installs != 0 {
		t.Errorf("downloads/installs should be 0, got %d/%d", published.Downloads, published.Installs)
	}

	// Verify template is now public
	updated, _ := repo.Get(ctx, tmpl.ID)
	if !updated.Public {
		t.Error("template should be public after publishing")
	}
}

func TestMarketplace_PublishDuplicate(t *testing.T) {
	svc, repo, _ := setupMarketplace(t)
	ctx := context.Background()
	tmpl := createTestTemplate(t, repo, "test-dup")

	entry := MarketplaceEntry{Version: "1.0.0", Summary: "test"}
	if _, err := svc.Publish(ctx, tmpl.ID, entry); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if _, err := svc.Publish(ctx, tmpl.ID, entry); err != ErrAlreadyPublished {
		t.Errorf("duplicate publish: got %v, want ErrAlreadyPublished", err)
	}
}

func TestMarketplace_PublishNonExistent(t *testing.T) {
	svc, _, _ := setupMarketplace(t)
	ctx := context.Background()
	entry := MarketplaceEntry{Version: "1.0.0"}
	if _, err := svc.Publish(ctx, "nonexistent", entry); err != ErrNotFound {
		t.Errorf("non-existent publish: got %v, want ErrNotFound", err)
	}
}

func TestMarketplace_Unpublish(t *testing.T) {
	svc, repo, _ := setupMarketplace(t)
	ctx := context.Background()
	tmpl := createTestTemplate(t, repo, "test-unpub")

	entry := MarketplaceEntry{Version: "1.0.0", Summary: "test"}
	if _, err := svc.Publish(ctx, tmpl.ID, entry); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := svc.Unpublish(ctx, tmpl.ID); err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	if _, err := svc.GetListing(ctx, tmpl.ID); err != ErrNotPublished {
		t.Errorf("get after unpublish: got %v, want ErrNotPublished", err)
	}
}

func TestMarketplace_Browse(t *testing.T) {
	svc, repo, _ := setupMarketplace(t)
	ctx := context.Background()

	tmpl1 := createTestTemplate(t, repo, "alpha-deploy")
	tmpl2 := createTestTemplate(t, repo, "beta-monitor")

	pub1 := MarketplaceEntry{Version: "1.0.0", Summary: "Deploy tool"}
	pub2 := MarketplaceEntry{Version: "2.0.0", Summary: "Monitor tool"}
	svc.Publish(ctx, tmpl1.ID, pub1)
	svc.Publish(ctx, tmpl2.ID, pub2)

	// Browse all
	entries, templates, err := svc.Browse(ctx, ListingFilter{})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("browse all: got %d entries, want 2", len(entries))
	}
	if len(templates) != 2 {
		t.Errorf("browse all: got %d templates, want 2", len(templates))
	}
}

func TestMarketplace_BrowseWithSearch(t *testing.T) {
	svc, repo, _ := setupMarketplace(t)
	ctx := context.Background()

	tmpl1 := createTestTemplate(t, repo, "deploy-app")
	tmpl2 := createTestTemplate(t, repo, "monitor-logs")

	pub1 := MarketplaceEntry{Version: "1.0.0", Summary: "Deploy tool"}
	pub2 := MarketplaceEntry{Version: "2.0.0", Summary: "Monitor tool"}
	svc.Publish(ctx, tmpl1.ID, pub1)
	svc.Publish(ctx, tmpl2.ID, pub2)

	entries, _, _ := svc.Browse(ctx, ListingFilter{Search: "deploy"})
	if len(entries) != 1 {
		t.Errorf("search 'deploy': got %d, want 1", len(entries))
	}
}

func TestMarketplace_BrowseWithTags(t *testing.T) {
	svc, repo, _ := setupMarketplace(t)
	ctx := context.Background()

	tmpl1 := createTestTemplate(t, repo, "deploy-app") // tags: test, automation
	tmpl2 := createTestTemplate(t, repo, "monitor-logs")
	tmpl2.Tags = []string{"monitoring", "logs"}
	repo.Update(ctx, tmpl2)

	pub1 := MarketplaceEntry{Version: "1.0.0", Summary: "Deploy"}
	pub2 := MarketplaceEntry{Version: "2.0.0", Summary: "Monitor"}
	svc.Publish(ctx, tmpl1.ID, pub1)
	svc.Publish(ctx, tmpl2.ID, pub2)

	entries, _, _ := svc.Browse(ctx, ListingFilter{Tags: []string{"monitoring"}})
	if len(entries) != 1 {
		t.Errorf("filter by tag 'monitoring': got %d, want 1", len(entries))
	}
}

func TestMarketplace_BrowseWithFeatured(t *testing.T) {
	svc, repo, mpRepo := setupMarketplace(t)
	ctx := context.Background()

	tmpl1 := createTestTemplate(t, repo, "featured-tmpl")
	tmpl2 := createTestTemplate(t, repo, "normal-tmpl")

	pub1 := MarketplaceEntry{Version: "1.0.0", Summary: "Featured", Featured: true}
	pub2 := MarketplaceEntry{Version: "1.0.0", Summary: "Normal"}
	svc.Publish(ctx, tmpl1.ID, pub1)
	svc.Publish(ctx, tmpl2.ID, pub2)

	// Note: Featured is set at publish time, verify via repo
	entry1, _ := mpRepo.Get(ctx, tmpl1.ID)
	if !entry1.Featured {
		t.Error("entry1 should be featured")
	}

	featured := true
	entries, _, _ := svc.Browse(ctx, ListingFilter{Featured: &featured})
	if len(entries) != 1 {
		t.Errorf("filter featured: got %d, want 1", len(entries))
	}
}

func TestMarketplace_Install(t *testing.T) {
	svc, repo, _ := setupMarketplace(t)
	ctx := context.Background()
	tmpl := createTestTemplate(t, repo, "shared-tmpl")

	entry := MarketplaceEntry{Version: "1.0.0", Summary: "Shared"}
	svc.Publish(ctx, tmpl.ID, entry)

	installed, err := svc.Install(ctx, tmpl.ID, "user2")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if installed.ID == tmpl.ID {
		t.Error("installed template should have different ID")
	}
	if installed.Author != "user2" {
		t.Errorf("installed author = %s, want user2", installed.Author)
	}
	if installed.Public {
		t.Error("installed template should not be public")
	}
	if installed.Name != "shared-tmpl" {
		t.Errorf("installed name = %s, want shared-tmpl", installed.Name)
	}
}

func TestMarketplace_SelfInstall(t *testing.T) {
	svc, repo, _ := setupMarketplace(t)
	ctx := context.Background()
	tmpl := createTestTemplate(t, repo, "my-tmpl")

	entry := MarketplaceEntry{Version: "1.0.0", Summary: "Mine"}
	svc.Publish(ctx, tmpl.ID, entry)

	if _, err := svc.Install(ctx, tmpl.ID, "user1"); err != ErrSelfInstall {
		t.Errorf("self-install: got %v, want ErrSelfInstall", err)
	}
}

func TestMarketplace_InstallNotPublished(t *testing.T) {
	svc, _, _ := setupMarketplace(t)
	ctx := context.Background()
	if _, err := svc.Install(ctx, "nonexistent", "user2"); err != ErrNotPublished {
		t.Errorf("install non-published: got %v, want ErrNotPublished", err)
	}
}

func TestMarketplace_InstallIncrementsCounters(t *testing.T) {
	svc, repo, mpRepo := setupMarketplace(t)
	ctx := context.Background()
	tmpl := createTestTemplate(t, repo, "counter-tmpl")

	entry := MarketplaceEntry{Version: "1.0.0", Summary: "Counter"}
	svc.Publish(ctx, tmpl.ID, entry)

	svc.Install(ctx, tmpl.ID, "user2")
	svc.Install(ctx, tmpl.ID, "user3")

	mpEntry, _ := mpRepo.Get(ctx, tmpl.ID)
	if mpEntry.Installs != 2 {
		t.Errorf("installs = %d, want 2", mpEntry.Installs)
	}
	origTmpl, _ := repo.Get(ctx, tmpl.ID)
	if origTmpl.UsageCount != 2 {
		t.Errorf("usage_count = %d, want 2", origTmpl.UsageCount)
	}
}

func TestMarketplace_AddReview(t *testing.T) {
	svc, repo, _ := setupMarketplace(t)
	ctx := context.Background()
	tmpl := createTestTemplate(t, repo, "review-tmpl")

	entry := MarketplaceEntry{Version: "1.0.0", Summary: "Review me"}
	svc.Publish(ctx, tmpl.ID, entry)

	review := Review{
		TemplateID: tmpl.ID,
		UserID:     "user2",
		Rating:     4.5,
		Comment:    "Great template!",
	}

	created, err := svc.AddReview(ctx, review)
	if err != nil {
		t.Fatalf("add review: %v", err)
	}
	if created.ID == "" {
		t.Error("review ID should not be empty")
	}
	if created.Rating != 4.5 {
		t.Errorf("rating = %f, want 4.5", created.Rating)
	}
}

func TestMarketplace_ReviewUpdatesRating(t *testing.T) {
	svc, repo, _ := setupMarketplace(t)
	ctx := context.Background()
	tmpl := createTestTemplate(t, repo, "rating-tmpl")

	entry := MarketplaceEntry{Version: "1.0.0", Summary: "Rate me"}
	svc.Publish(ctx, tmpl.ID, entry)

	svc.AddReview(ctx, Review{TemplateID: tmpl.ID, UserID: "u1", Rating: 4})
	svc.AddReview(ctx, Review{TemplateID: tmpl.ID, UserID: "u2", Rating: 5})

	updated, _ := repo.Get(ctx, tmpl.ID)
	expected := 4.5
	diff := updated.Rating - expected
	if diff > 0.01 || diff < -0.01 {
		t.Errorf("avg rating = %f, want %f", updated.Rating, expected)
	}
}

func TestMarketplace_ReviewInvalidRating(t *testing.T) {
	svc, repo, _ := setupMarketplace(t)
	ctx := context.Background()
	tmpl := createTestTemplate(t, repo, "bad-rating-tmpl")
	entry := MarketplaceEntry{Version: "1.0.0", Summary: "test"}
	svc.Publish(ctx, tmpl.ID, entry)

	if _, err := svc.AddReview(ctx, Review{TemplateID: tmpl.ID, UserID: "u1", Rating: 0}); err != ErrInvalidInput {
		t.Errorf("rating 0: got %v, want ErrInvalidInput", err)
	}
	if _, err := svc.AddReview(ctx, Review{TemplateID: tmpl.ID, UserID: "u1", Rating: 6}); err != ErrInvalidInput {
		t.Errorf("rating 6: got %v, want ErrInvalidInput", err)
	}
}

func TestMarketplace_ReviewNotPublished(t *testing.T) {
	svc, _, _ := setupMarketplace(t)
	ctx := context.Background()
	if _, err := svc.AddReview(ctx, Review{TemplateID: "nonexistent", UserID: "u1", Rating: 3}); err != ErrNotPublished {
		t.Errorf("review not published: got %v, want ErrNotPublished", err)
	}
}

func TestMarketplace_GetReviews(t *testing.T) {
	svc, repo, _ := setupMarketplace(t)
	ctx := context.Background()
	tmpl := createTestTemplate(t, repo, "reviews-tmpl")
	entry := MarketplaceEntry{Version: "1.0.0", Summary: "test"}
	svc.Publish(ctx, tmpl.ID, entry)

	svc.AddReview(ctx, Review{TemplateID: tmpl.ID, UserID: "u1", Rating: 5, Comment: "Best!"})
	svc.AddReview(ctx, Review{TemplateID: tmpl.ID, UserID: "u2", Rating: 3, Comment: "OK"})

	reviews, err := svc.GetReviews(ctx, tmpl.ID)
	if err != nil {
		t.Fatalf("get reviews: %v", err)
	}
	if len(reviews) != 2 {
		t.Errorf("got %d reviews, want 2", len(reviews))
	}
	// Should be sorted by created_at desc (newest first)
	if reviews[0].UserID != "u2" {
		t.Errorf("first review should be u2 (newest), got %s", reviews[0].UserID)
	}
}

func TestMarketplace_GetStats(t *testing.T) {
	svc, repo, _ := setupMarketplace(t)
	ctx := context.Background()

	tmpl1 := createTestTemplate(t, repo, "stats-tmpl1")
	tmpl1.Category = "devops"
	repo.Update(ctx, tmpl1)
	tmpl2 := createTestTemplate(t, repo, "stats-tmpl2")
	tmpl2.Category = "monitoring"
	repo.Update(ctx, tmpl2)

	svc.Publish(ctx, tmpl1.ID, MarketplaceEntry{Version: "1.0.0", Summary: "a"})
	svc.Publish(ctx, tmpl2.ID, MarketplaceEntry{Version: "1.0.0", Summary: "b"})

	// Install once
	svc.Install(ctx, tmpl1.ID, "user2")

	stats, err := svc.GetStats(ctx)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if stats.TotalListings != 2 {
		t.Errorf("total listings = %d, want 2", stats.TotalListings)
	}
	if stats.TotalInstalls != 1 {
		t.Errorf("total installs = %d, want 1", stats.TotalInstalls)
	}
	if len(stats.Categories) != 2 {
		t.Errorf("categories count = %d, want 2", len(stats.Categories))
	}
}

func TestMarketplace_SetFeatured(t *testing.T) {
	svc, repo, mpRepo := setupMarketplace(t)
	ctx := context.Background()
	tmpl := createTestTemplate(t, repo, "featured-test")
	entry := MarketplaceEntry{Version: "1.0.0", Summary: "test"}
	svc.Publish(ctx, tmpl.ID, entry)

	if err := svc.SetFeatured(ctx, tmpl.ID, true); err != nil {
		t.Fatalf("set featured: %v", err)
	}
	mpEntry, _ := mpRepo.Get(ctx, tmpl.ID)
	if !mpEntry.Featured {
		t.Error("should be featured")
	}

	if err := svc.SetFeatured(ctx, tmpl.ID, false); err != nil {
		t.Fatalf("unset featured: %v", err)
	}
	mpEntry, _ = mpRepo.Get(ctx, tmpl.ID)
	if mpEntry.Featured {
		t.Error("should not be featured")
	}
}

func TestMarketplace_UpdateListing(t *testing.T) {
	svc, repo, mpRepo := setupMarketplace(t)
	ctx := context.Background()
	tmpl := createTestTemplate(t, repo, "update-test")
	entry := MarketplaceEntry{Version: "1.0.0", Summary: "Initial"}
	svc.Publish(ctx, tmpl.ID, entry)

	updated, err := svc.UpdateListing(ctx, tmpl.ID, MarketplaceEntry{
		Version: "2.0.0",
		Summary: "Updated summary",
		License: "Apache-2.0",
	})
	if err != nil {
		t.Fatalf("update listing: %v", err)
	}
	if updated.Version != "2.0.0" {
		t.Errorf("version = %s, want 2.0.0", updated.Version)
	}
	if updated.Summary != "Updated summary" {
		t.Errorf("summary = %s, want Updated summary", updated.Summary)
	}
	if updated.License != "Apache-2.0" {
		t.Errorf("license = %s, want Apache-2.0", updated.License)
	}

	// Verify persisted
	mpEntry, _ := mpRepo.Get(ctx, tmpl.ID)
	if mpEntry.Version != "2.0.0" {
		t.Errorf("persisted version = %s, want 2.0.0", mpEntry.Version)
	}
}

func TestMarketplace_UpdateNotPublished(t *testing.T) {
	svc, _, _ := setupMarketplace(t)
	ctx := context.Background()
	if _, err := svc.UpdateListing(ctx, "nonexistent", MarketplaceEntry{}); err != ErrNotPublished {
		t.Errorf("update not published: got %v, want ErrNotPublished", err)
	}
}

func TestMarketplace_ReviewEmptyFields(t *testing.T) {
	svc, repo, _ := setupMarketplace(t)
	ctx := context.Background()
	tmpl := createTestTemplate(t, repo, "empty-review-tmpl")
	entry := MarketplaceEntry{Version: "1.0.0", Summary: "test"}
	svc.Publish(ctx, tmpl.ID, entry)

	// Empty user ID
	if _, err := svc.AddReview(ctx, Review{TemplateID: tmpl.ID, Rating: 3}); err != ErrInvalidInput {
		t.Errorf("empty user: got %v, want ErrInvalidInput", err)
	}
	// Empty template ID (caught by input validation)
	if _, err := svc.AddReview(ctx, Review{UserID: "u1", Rating: 3}); err != ErrInvalidInput {
		t.Errorf("empty template: got %v, want ErrInvalidInput", err)
	}
}