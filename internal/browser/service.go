package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// LocalService implements Service using in-memory storage.
// Browser actions are simulated locally (no real browser process).
type LocalService struct {
	repo    Repository
	mu      sync.Mutex
	// recordings tracks action recordings per browser
	recordings map[string][]Action
	recMu      sync.RWMutex
}

// NewLocalService creates a new local browser service.
func NewLocalService(repo Repository) *LocalService {
	return &LocalService{
		repo:       repo,
		recordings: make(map[string][]Action),
	}
}

func generateID(prefix string) string {
	b := make([]byte, 8)
	rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

func (s *LocalService) CreateBrowser(_ context.Context, agentID string, profile *Profile) (*Browser, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agentID is required")
	}

	browser := &Browser{
		ID:        generateID("brw"),
		AgentID:   agentID,
		State:     "idle",
		CreatedAt: time.Now(),
	}

	if profile != nil {
		browser.Profile = *profile
	} else {
		browser.Profile = Profile{
			ID:        generateID("prof"),
			Name:      "default",
			UserAgent: "AgentBot/1.0",
		}
	}

	if err := s.repo.SaveBrowser(browser); err != nil {
		return nil, fmt.Errorf("save browser: %w", err)
	}
	return browser, nil
}

func (s *LocalService) CloseBrowser(_ context.Context, browserID string) error {
	browser, err := s.repo.GetBrowser(browserID)
	if err != nil {
		return err
	}
	browser.State = "closed"
	return s.repo.SaveBrowser(browser)
}

func (s *LocalService) GetBrowser(_ context.Context, browserID string) (*Browser, error) {
	return s.repo.GetBrowser(browserID)
}

func (s *LocalService) Navigate(_ context.Context, browserID string, url string) (*Page, error) {
	browser, err := s.repo.GetBrowser(browserID)
	if err != nil {
		return nil, err
	}
	if browser.State == "closed" {
		return nil, fmt.Errorf("browser %s is closed", browserID)
	}

	browser.State = "navigating"
	if err := s.repo.SaveBrowser(browser); err != nil {
		return nil, err
	}

	page := &Page{
		ID:        generateID("page"),
		BrowserID: browserID,
		URL:       url,
		Title:     extractTitle(url),
	}

	if err := s.repo.SavePage(page); err != nil {
		return nil, err
	}

	browser.State = "idle"
	s.repo.SaveBrowser(browser)
	return page, nil
}

func (s *LocalService) GetCurrentPage(_ context.Context, browserID string) (*Page, error) {
	_, err := s.repo.GetBrowser(browserID)
	if err != nil {
		return nil, err
	}
	return s.repo.GetPageByBrowser(browserID)
}

func (s *LocalService) ExecuteAction(ctx context.Context, browserID string, action Action) (string, error) {
	browser, err := s.repo.GetBrowser(browserID)
	if err != nil {
		return "", err
	}
	if browser.State == "closed" {
		return "", fmt.Errorf("browser %s is closed", browserID)
	}

	// Record action if recording
	s.recMu.RLock()
	if _, recording := s.recordings[browserID]; recording {
		s.recMu.RUnlock()
		s.recMu.Lock()
		s.recordings[browserID] = append(s.recordings[browserID], action)
		s.recMu.Unlock()
	} else {
		s.recMu.RUnlock()
	}

	browser.State = "executing"
	s.repo.SaveBrowser(browser)

	result := s.simulateAction(browserID, action)

	browser.State = "idle"
	s.repo.SaveBrowser(browser)
	return result, nil
}

func (s *LocalService) Screenshot(_ context.Context, browserID string) ([]byte, error) {
	_, err := s.repo.GetBrowser(browserID)
	if err != nil {
		return nil, err
	}
	// Return a minimal placeholder PNG
	return generatePlaceholderPNG(), nil
}

func (s *LocalService) ExtractText(_ context.Context, browserID string, selector string) (string, error) {
	_, err := s.repo.GetBrowser(browserID)
	if err != nil {
		return "", err
	}
	page, err := s.repo.GetPageByBrowser(browserID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("[simulated] Text content from %q on %s", selector, page.URL), nil
}

func (s *LocalService) ImportProfile(_ context.Context, profileData []byte) (*Profile, error) {
	var profile Profile
	if err := json.Unmarshal(profileData, &profile); err != nil {
		return nil, fmt.Errorf("invalid profile data: %w", err)
	}
	if profile.ID == "" {
		profile.ID = generateID("prof")
	}
	if err := s.repo.SaveProfile(&profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

func (s *LocalService) ExportProfile(_ context.Context, profileID string) ([]byte, error) {
	profile, err := s.repo.GetProfile(profileID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(profile)
}

func (s *LocalService) ListProfiles(_ context.Context) ([]Profile, error) {
	return s.repo.ListProfiles()
}

// Recorder implementation

func (s *LocalService) StartRecording(_ context.Context, browserID string) error {
	_, err := s.repo.GetBrowser(browserID)
	if err != nil {
		return err
	}
	s.recMu.Lock()
	defer s.recMu.Unlock()
	s.recordings[browserID] = []Action{}
	return nil
}

func (s *LocalService) StopRecording(_ context.Context, browserID string) ([]Action, error) {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	actions, ok := s.recordings[browserID]
	if !ok {
		return nil, fmt.Errorf("no recording for browser %s", browserID)
	}
	delete(s.recordings, browserID)
	return actions, nil
}

func (s *LocalService) ReplayActions(ctx context.Context, browserID string, actions []Action) error {
	for _, action := range actions {
		if _, err := s.ExecuteAction(ctx, browserID, action); err != nil {
			return fmt.Errorf("replay action %s failed: %w", action.Type, err)
		}
	}
	return nil
}

// simulateAction returns a simulated result for a browser action.
func (s *LocalService) simulateAction(browserID string, action Action) string {
	switch action.Type {
	case ActionNavigate:
		return fmt.Sprintf("Navigated to %s", action.Value)
	case ActionClick:
		return fmt.Sprintf("Clicked element %q", action.Target)
	case ActionInput:
		return fmt.Sprintf("Typed %q into %q", action.Value, action.Target)
	case ActionSelect:
		return fmt.Sprintf("Selected %q in %q", action.Value, action.Target)
	case ActionScreenshot:
		return "Screenshot captured (simulated)"
	case ActionExtract:
		return fmt.Sprintf("[simulated] Extracted content from %q", action.Target)
	case ActionWait:
		return fmt.Sprintf("Waited for %s", action.Value)
	case ActionScroll:
		return fmt.Sprintf("Scrolled %s", action.Value)
	case ActionKeyPress:
		return fmt.Sprintf("Pressed key %q", action.Value)
	case ActionHover:
		return fmt.Sprintf("Hovered over %q", action.Target)
	default:
		return fmt.Sprintf("Unknown action: %s", action.Type)
	}
}

// extractTitle derives a page title from a URL.
func extractTitle(url string) string {
	// Simple heuristic: use domain as title
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	if idx := strings.Index(url, "/"); idx > 0 {
		return url[:idx]
	}
	return url
}

// generatePlaceholderPNG returns a minimal valid PNG (1x1 white pixel).
func generatePlaceholderPNG() []byte {
	// Minimal PNG: 1x1 white pixel
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG signature
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, // 8-bit RGB
		0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, // IDAT
		0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00, // compressed data
		0x00, 0x02, 0x00, 0x01, 0xe2, 0x21, 0xbc, 0x33,
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, // IEND
		0xae, 0x42, 0x60, 0x82,
	}
	return png
}
