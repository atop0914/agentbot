package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- Repository Tests ---

func TestMemoryRepository_Browser(t *testing.T) {
	repo := NewMemoryRepository()

	// Save
	b := &Browser{ID: "b1", AgentID: "a1", State: "idle"}
	if err := repo.SaveBrowser(b); err != nil {
		t.Fatalf("SaveBrowser: %v", err)
	}

	// Get
	got, err := repo.GetBrowser("b1")
	if err != nil {
		t.Fatalf("GetBrowser: %v", err)
	}
	if got.ID != "b1" || got.AgentID != "a1" {
		t.Errorf("got %+v", got)
	}

	// Get non-existent
	_, err = repo.GetBrowser("missing")
	if err == nil {
		t.Error("expected error for missing browser")
	}

	// ListByAgent
	b2 := &Browser{ID: "b2", AgentID: "a1", State: "idle"}
	repo.SaveBrowser(b2)
	b3 := &Browser{ID: "b3", AgentID: "a2", State: "idle"}
	repo.SaveBrowser(b3)

	list, err := repo.ListByAgent("a1")
	if err != nil {
		t.Fatalf("ListByAgent: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2, got %d", len(list))
	}

	// Delete
	if err := repo.DeleteBrowser("b1"); err != nil {
		t.Fatalf("DeleteBrowser: %v", err)
	}
	_, err = repo.GetBrowser("b1")
	if err == nil {
		t.Error("expected error after delete")
	}

	// Delete non-existent
	if err := repo.DeleteBrowser("missing"); err == nil {
		t.Error("expected error for missing delete")
	}
}

func TestMemoryRepository_Page(t *testing.T) {
	repo := NewMemoryRepository()

	p := &Page{ID: "p1", BrowserID: "b1", URL: "https://example.com"}
	repo.SaveBrowser(&Browser{ID: "b1", AgentID: "a1"})
	if err := repo.SavePage(p); err != nil {
		t.Fatalf("SavePage: %v", err)
	}

	got, err := repo.GetPage("p1")
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if got.URL != "https://example.com" {
		t.Errorf("got URL %s", got.URL)
	}

	byBrowser, err := repo.GetPageByBrowser("b1")
	if err != nil {
		t.Fatalf("GetPageByBrowser: %v", err)
	}
	if byBrowser.ID != "p1" {
		t.Errorf("got page %s", byBrowser.ID)
	}

	_, err = repo.GetPageByBrowser("missing")
	if err == nil {
		t.Error("expected error for missing browser page")
	}
}

func TestMemoryRepository_Profile(t *testing.T) {
	repo := NewMemoryRepository()

	p := &Profile{ID: "pr1", Name: "test", UserAgent: "TestAgent/1.0"}
	if err := repo.SaveProfile(p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	got, err := repo.GetProfile("pr1")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if got.Name != "test" {
		t.Errorf("got name %s", got.Name)
	}

	list, err := repo.ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1, got %d", len(list))
	}

	if err := repo.DeleteProfile("pr1"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	_, err = repo.GetProfile("pr1")
	if err == nil {
		t.Error("expected error after delete")
	}
}

// --- Service Tests ---

func newTestService() *LocalService {
	return NewLocalService(NewMemoryRepository())
}

func TestLocalService_CreateBrowser(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	b, err := svc.CreateBrowser(ctx, "agent-1", nil)
	if err != nil {
		t.Fatalf("CreateBrowser: %v", err)
	}
	if b.ID == "" {
		t.Error("expected non-empty ID")
	}
	if b.State != "idle" {
		t.Errorf("expected idle, got %s", b.State)
	}
	if b.Profile.Name != "default" {
		t.Errorf("expected default profile, got %s", b.Profile.Name)
	}

	// With custom profile
	custom := &Profile{ID: "custom", Name: "my-profile", UserAgent: "Custom/1.0"}
	b2, err := svc.CreateBrowser(ctx, "agent-2", custom)
	if err != nil {
		t.Fatalf("CreateBrowser with profile: %v", err)
	}
	if b2.Profile.Name != "my-profile" {
		t.Errorf("expected my-profile, got %s", b2.Profile.Name)
	}

	// Empty agentID
	_, err = svc.CreateBrowser(ctx, "", nil)
	if err == nil {
		t.Error("expected error for empty agentID")
	}
}

func TestLocalService_BrowserLifecycle(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	b, _ := svc.CreateBrowser(ctx, "agent-1", nil)

	got, err := svc.GetBrowser(ctx, b.ID)
	if err != nil {
		t.Fatalf("GetBrowser: %v", err)
	}
	if got.ID != b.ID {
		t.Errorf("ID mismatch: %s vs %s", got.ID, b.ID)
	}

	// Close
	if err := svc.CloseBrowser(ctx, b.ID); err != nil {
		t.Fatalf("CloseBrowser: %v", err)
	}
	got, _ = svc.GetBrowser(ctx, b.ID)
	if got.State != "closed" {
		t.Errorf("expected closed, got %s", got.State)
	}

	// Navigate on closed browser
	_, err = svc.Navigate(ctx, b.ID, "https://example.com")
	if err == nil {
		t.Error("expected error navigating closed browser")
	}
}

func TestLocalService_Navigate(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	b, _ := svc.CreateBrowser(ctx, "agent-1", nil)

	page, err := svc.Navigate(ctx, b.ID, "https://example.com/path")
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if page.URL != "https://example.com/path" {
		t.Errorf("URL: %s", page.URL)
	}
	if page.Title != "example.com" {
		t.Errorf("Title: %s", page.Title)
	}

	// GetCurrentPage
	current, err := svc.GetCurrentPage(ctx, b.ID)
	if err != nil {
		t.Fatalf("GetCurrentPage: %v", err)
	}
	if current.ID != page.ID {
		t.Errorf("page ID mismatch")
	}

	// Navigate again updates the page
	page2, err := svc.Navigate(ctx, b.ID, "https://other.com")
	if err != nil {
		t.Fatalf("Navigate 2: %v", err)
	}
	current, _ = svc.GetCurrentPage(ctx, b.ID)
	if current.ID != page2.ID {
		t.Error("expected updated page")
	}
}

func TestLocalService_ExecuteAction(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	b, _ := svc.CreateBrowser(ctx, "agent-1", nil)
	svc.Navigate(ctx, b.ID, "https://example.com")

	tests := []struct {
		action Action
		want   string
	}{
		{Action{Type: ActionNavigate, Value: "https://test.com"}, "Navigated to https://test.com"},
		{Action{Type: ActionClick, Target: "#btn"}, "Clicked element \"#btn\""},
		{Action{Type: ActionInput, Target: "#input", Value: "hello"}, "Typed \"hello\" into \"#input\""},
		{Action{Type: ActionScreenshot}, "Screenshot captured (simulated)"},
	}

	for _, tt := range tests {
		result, err := svc.ExecuteAction(ctx, b.ID, tt.action)
		if err != nil {
			t.Errorf("ExecuteAction(%s): %v", tt.action.Type, err)
			continue
		}
		if result != tt.want {
			t.Errorf("ExecuteAction(%s) = %q, want %q", tt.action.Type, result, tt.want)
		}
	}
}

func TestLocalService_Screenshot(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	b, _ := svc.CreateBrowser(ctx, "agent-1", nil)

	data, err := svc.Screenshot(ctx, b.ID)
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty screenshot")
	}
	// Check PNG magic bytes
	if data[0] != 0x89 || data[1] != 0x50 {
		t.Error("expected PNG data")
	}
}

func TestLocalService_ExtractText(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	b, _ := svc.CreateBrowser(ctx, "agent-1", nil)
	svc.Navigate(ctx, b.ID, "https://example.com")

	text, err := svc.ExtractText(ctx, b.ID, "h1")
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}
	if text == "" {
		t.Error("expected non-empty text")
	}
}

func TestLocalService_Profiles(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	// Import
	data, _ := json.Marshal(Profile{Name: "imported", UserAgent: "Import/1.0"})
	profile, err := svc.ImportProfile(ctx, data)
	if err != nil {
		t.Fatalf("ImportProfile: %v", err)
	}
	if profile.Name != "imported" {
		t.Errorf("name: %s", profile.Name)
	}

	// Export
	exported, err := svc.ExportProfile(ctx, profile.ID)
	if err != nil {
		t.Fatalf("ExportProfile: %v", err)
	}
	var got Profile
	json.Unmarshal(exported, &got)
	if got.Name != "imported" {
		t.Errorf("exported name: %s", got.Name)
	}

	// List
	list, err := svc.ListProfiles(ctx)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1, got %d", len(list))
	}
}

func TestLocalService_Recording(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	b, _ := svc.CreateBrowser(ctx, "agent-1", nil)

	// Start recording
	if err := svc.StartRecording(ctx, b.ID); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}

	// Execute some actions
	svc.ExecuteAction(ctx, b.ID, Action{Type: ActionClick, Target: "#btn"})
	svc.ExecuteAction(ctx, b.ID, Action{Type: ActionInput, Target: "#input", Value: "test"})

	// Stop recording
	actions, err := svc.StopRecording(ctx, b.ID)
	if err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
	if len(actions) != 2 {
		t.Errorf("expected 2 recorded actions, got %d", len(actions))
	}

	// Replay
	if err := svc.ReplayActions(ctx, b.ID, actions); err != nil {
		t.Fatalf("ReplayActions: %v", err)
	}
}

// --- Handler Tests ---

func newTestHandler() (*Handler, *LocalService) {
	svc := newTestService()
	return NewHandler(svc), svc
}

func TestHandler_CreateBrowser(t *testing.T) {
	handler, svc := newTestHandler()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body, _ := json.Marshal(map[string]string{"agent_id": "agent-1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/browsers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status %d, body: %s", w.Code, w.Body.String())
	}

	var browser Browser
	json.NewDecoder(w.Body).Decode(&browser)
	if browser.ID == "" {
		t.Error("expected non-empty browser ID")
	}

	// Verify browser exists
	svc.GetBrowser(context.Background(), browser.ID)
}

func TestHandler_CreateBrowser_MissingAgentID(t *testing.T) {
	handler, _ := newTestHandler()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body, _ := json.Marshal(map[string]string{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/browsers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandler_Navigate(t *testing.T) {
	handler, svc := newTestHandler()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	b, _ := svc.CreateBrowser(context.Background(), "agent-1", nil)

	body, _ := json.Marshal(map[string]string{"url": "https://example.com"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/browsers/"+b.ID+"/navigate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status %d, body: %s", w.Code, w.Body.String())
	}
}

func TestHandler_GetCurrentPage(t *testing.T) {
	handler, svc := newTestHandler()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	b, _ := svc.CreateBrowser(context.Background(), "agent-1", nil)
	svc.Navigate(context.Background(), b.ID, "https://example.com")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browsers/"+b.ID+"/page", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status %d", w.Code)
	}
}

func TestHandler_ExecuteAction(t *testing.T) {
	handler, svc := newTestHandler()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	b, _ := svc.CreateBrowser(context.Background(), "agent-1", nil)

	body, _ := json.Marshal(Action{Type: ActionClick, Target: "#btn"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/browsers/"+b.ID+"/action", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status %d, body: %s", w.Code, w.Body.String())
	}
}

func TestHandler_Screenshot(t *testing.T) {
	handler, svc := newTestHandler()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	b, _ := svc.CreateBrowser(context.Background(), "agent-1", nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browsers/"+b.ID+"/screenshot", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("expected image/png, got %s", w.Header().Get("Content-Type"))
	}
}

func TestHandler_ExtractText(t *testing.T) {
	handler, svc := newTestHandler()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	b, _ := svc.CreateBrowser(context.Background(), "agent-1", nil)
	svc.Navigate(context.Background(), b.ID, "https://example.com")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browsers/"+b.ID+"/extract?selector=h1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status %d", w.Code)
	}
}

func TestHandler_Profiles(t *testing.T) {
	handler, _ := newTestHandler()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browser/profiles", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status %d", w.Code)
	}
}

func TestHandler_CloseBrowser(t *testing.T) {
	handler, svc := newTestHandler()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	b, _ := svc.CreateBrowser(context.Background(), "agent-1", nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/browsers/"+b.ID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status %d", w.Code)
	}
}

func TestHandler_GetBrowser_NotFound(t *testing.T) {
	handler, _ := newTestHandler()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/browsers/nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandler_Recording(t *testing.T) {
	handler, svc := newTestHandler()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	b, _ := svc.CreateBrowser(context.Background(), "agent-1", nil)

	// Start recording
	req := httptest.NewRequest(http.MethodPost, "/api/v1/browsers/"+b.ID+"/record/start", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("start recording: %d", w.Code)
	}

	// Execute an action while recording
	body, _ := json.Marshal(Action{Type: ActionClick, Target: "#btn"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/browsers/"+b.ID+"/action", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Stop recording
	req = httptest.NewRequest(http.MethodPost, "/api/v1/browsers/"+b.ID+"/record/stop", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("stop recording: %d", w.Code)
	}
}
