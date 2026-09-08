package browser

import (
	"context"
	"time"
)

// Browser represents a browser instance
type Browser struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	State     string    `json:"state"` // "idle", "navigating", "executing"
	Profile   Profile   `json:"profile"`
	CreatedAt time.Time `json:"created_at"`
}

// Profile represents a browser profile with cookies and settings
type Profile struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	UserAgent string            `json:"user_agent"`
	Cookies   []Cookie          `json:"cookies"`
	Headers   map[string]string `json:"headers"`
	Proxy     string            `json:"proxy,omitempty"`
}

// Cookie represents a browser cookie
type Cookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Domain   string    `json:"domain"`
	Path     string    `json:"path"`
	Expires  time.Time `json:"expires"`
	HTTPOnly bool      `json:"http_only"`
	Secure   bool      `json:"secure"`
}

// Page represents a browser page
type Page struct {
	ID        string `json:"id"`
	BrowserID string `json:"browser_id"`
	URL       string `json:"url"`
	Title     string `json:"title"`
}

// Action represents a browser action
type Action struct {
	Type   ActionType        `json:"type"`
	Target string            `json:"target,omitempty"` // CSS selector or XPath
	Value  string            `json:"value,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// ActionType represents browser action types
type ActionType string

const (
	ActionNavigate    ActionType = "navigate"
	ActionClick       ActionType = "click"
	ActionInput       ActionType = "input"
	ActionSelect      ActionType = "select"
	ActionScreenshot  ActionType = "screenshot"
	ActionExtract     ActionType = "extract"
	ActionWait        ActionType = "wait"
	ActionScroll      ActionType = "scroll"
	ActionKeyPress    ActionType = "keypress"
	ActionHover       ActionType = "hover"
)

// Service defines browser automation interface
type Service interface {
	// CreateBrowser creates a new browser instance
	CreateBrowser(ctx context.Context, agentID string, profile *Profile) (*Browser, error)
	// CloseBrowser closes a browser instance
	CloseBrowser(ctx context.Context, browserID string) error
	// GetBrowser returns browser info
	GetBrowser(ctx context.Context, browserID string) (*Browser, error)
	
	// Navigate navigates to a URL
	Navigate(ctx context.Context, browserID string, url string) (*Page, error)
	// GetCurrentPage returns the current page
	GetCurrentPage(ctx context.Context, browserID string) (*Page, error)
	
	// ExecuteAction executes a browser action
	ExecuteAction(ctx context.Context, browserID string, action Action) (result string, err error)
	// Screenshot takes a screenshot
	Screenshot(ctx context.Context, browserID string) ([]byte, error)
	// ExtractText extracts text from page
	ExtractText(ctx context.Context, browserID string, selector string) (string, error)
	
	// ImportProfile imports a Chrome profile
	ImportProfile(ctx context.Context, profileData []byte) (*Profile, error)
	// ExportProfile exports a browser profile
	ExportProfile(ctx context.Context, profileID string) ([]byte, error)
	// ListProfiles lists available profiles
	ListProfiles(ctx context.Context) ([]Profile, error)
}

// Recorder records browser actions for replay
type Recorder interface {
	StartRecording(ctx context.Context, browserID string) error
	StopRecording(ctx context.Context) ([]Action, error)
	ReplayActions(ctx context.Context, browserID string, actions []Action) error
}
