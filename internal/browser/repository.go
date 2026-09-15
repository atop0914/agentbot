package browser

import (
	"fmt"
	"sync"
)

// Repository defines the browser storage interface.
type Repository interface {
	// Browser operations
	SaveBrowser(b *Browser) error
	GetBrowser(id string) (*Browser, error)
	DeleteBrowser(id string) error
	ListByAgent(agentID string) ([]*Browser, error)

	// Page operations
	SavePage(p *Page) error
	GetPage(id string) (*Page, error)
	GetPageByBrowser(browserID string) (*Page, error)

	// Profile operations
	SaveProfile(p *Profile) error
	GetProfile(id string) (*Profile, error)
	DeleteProfile(id string) error
	ListProfiles() ([]Profile, error)
}

// MemoryRepository implements Repository with in-memory storage.
type MemoryRepository struct {
	mu       sync.RWMutex
	browsers map[string]*Browser
	pages    map[string]*Page
	// pagesByBrowser tracks current page per browser
	pagesByBrowser map[string]*Page
	profiles       map[string]*Profile
}

// NewMemoryRepository creates a new in-memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		browsers:       make(map[string]*Browser),
		pages:          make(map[string]*Page),
		pagesByBrowser: make(map[string]*Page),
		profiles:       make(map[string]*Profile),
	}
}

func (r *MemoryRepository) SaveBrowser(b *Browser) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.browsers[b.ID] = b
	return nil
}

func (r *MemoryRepository) GetBrowser(id string) (*Browser, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.browsers[id]
	if !ok {
		return nil, fmt.Errorf("browser %s not found", id)
	}
	return b, nil
}

func (r *MemoryRepository) DeleteBrowser(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.browsers[id]; !ok {
		return fmt.Errorf("browser %s not found", id)
	}
	delete(r.browsers, id)
	// Clean up associated pages
	delete(r.pagesByBrowser, id)
	return nil
}

func (r *MemoryRepository) ListByAgent(agentID string) ([]*Browser, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*Browser
	for _, b := range r.browsers {
		if b.AgentID == agentID {
			result = append(result, b)
		}
	}
	return result, nil
}

func (r *MemoryRepository) SavePage(p *Page) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pages[p.ID] = p
	r.pagesByBrowser[p.BrowserID] = p
	return nil
}

func (r *MemoryRepository) GetPage(id string) (*Page, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.pages[id]
	if !ok {
		return nil, fmt.Errorf("page %s not found", id)
	}
	return p, nil
}

func (r *MemoryRepository) GetPageByBrowser(browserID string) (*Page, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.pagesByBrowser[browserID]
	if !ok {
		return nil, fmt.Errorf("no active page for browser %s", browserID)
	}
	return p, nil
}

func (r *MemoryRepository) SaveProfile(p *Profile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.profiles[p.ID] = p
	return nil
}

func (r *MemoryRepository) GetProfile(id string) (*Profile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.profiles[id]
	if !ok {
		return nil, fmt.Errorf("profile %s not found", id)
	}
	return p, nil
}

func (r *MemoryRepository) DeleteProfile(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.profiles[id]; !ok {
		return fmt.Errorf("profile %s not found", id)
	}
	delete(r.profiles, id)
	return nil
}

func (r *MemoryRepository) ListProfiles() ([]Profile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Profile, 0, len(r.profiles))
	for _, p := range r.profiles {
		result = append(result, *p)
	}
	return result, nil
}
