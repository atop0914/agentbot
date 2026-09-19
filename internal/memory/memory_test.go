package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMemoryStore_CreateAndGet(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	ttl := 5 * time.Minute
	entry := &MemoryEntry{
		AgentID:    "agent-1",
		Type:       MemoryTypeFact,
		Scope:      MemoryScopeAgent,
		Content:    "用户喜欢中文回复",
		Importance: 0.8,
		TTL:        &ttl,
	}

	if err := store.Create(ctx, entry); err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if entry.ID == "" {
		t.Fatal("expected ID to be set")
	}
	if entry.ExpiresAt == nil {
		t.Fatal("expected ExpiresAt to be set")
	}

	got, err := store.Get(ctx, entry.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.Content != "用户喜欢中文回复" {
		t.Errorf("content mismatch: got %q", got.Content)
	}
	if got.AccessCount != 1 {
		t.Errorf("access count: got %d, want 1", got.AccessCount)
	}
}

func TestMemoryStore_ExpiredEntry(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	negativeTTL := -1 * time.Second
	entry := &MemoryEntry{
		AgentID: "agent-1",
		Type:    MemoryTypeConversation,
		Scope:   MemoryScopeAgent,
		Content: "expired memory",
		TTL:     &negativeTTL,
	}

	if err := store.Create(ctx, entry); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	_, err := store.Get(ctx, entry.ID)
	if err == nil {
		t.Fatal("expected error for expired entry")
	}
}

func TestMemoryStore_Update(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	entry := &MemoryEntry{
		AgentID: "agent-1",
		Type:    MemoryTypeFact,
		Scope:   MemoryScopeAgent,
		Content: "original",
	}
	store.Create(ctx, entry)

	entry.Content = "updated"
	if err := store.Update(ctx, entry); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	got, _ := store.Get(ctx, entry.ID)
	if got.Content != "updated" {
		t.Errorf("content not updated: got %q", got.Content)
	}
}

func TestMemoryStore_Delete(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	entry := &MemoryEntry{
		AgentID: "agent-1",
		Type:    MemoryTypeFact,
		Scope:   MemoryScopeAgent,
		Content: "to delete",
	}
	store.Create(ctx, entry)

	if err := store.Delete(ctx, entry.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err := store.Get(ctx, entry.ID)
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestMemoryStore_Search(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	entries := []*MemoryEntry{
		{AgentID: "agent-1", Type: MemoryTypeFact, Scope: MemoryScopeAgent, Content: "Go is a programming language", Importance: 0.9, Tags: []string{"go", "lang"}},
		{AgentID: "agent-1", Type: MemoryTypePreference, Scope: MemoryScopeUser, Content: "用户偏好简洁风格", Importance: 0.7, Tags: []string{"style"}},
		{AgentID: "agent-2", Type: MemoryTypeFact, Scope: MemoryScopeAgent, Content: "Python is popular", Importance: 0.6},
	}

	for _, e := range entries {
		store.Create(ctx, e)
	}

	// 搜索 Agent 1 的记忆
	results, total, err := store.Search(ctx, &SearchMemoryRequest{AgentID: "agent-1"})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if total != 2 {
		t.Errorf("total: got %d, want 2", total)
	}
	if len(results) != 2 {
		t.Errorf("results: got %d, want 2", len(results))
	}

	// 按内容搜索
	results, total, _ = store.Search(ctx, &SearchMemoryRequest{Query: "Go"})
	if total != 1 {
		t.Errorf("search 'Go' total: got %d, want 1", total)
	}

	// 按类型搜索
	results, total, _ = store.Search(ctx, &SearchMemoryRequest{Type: MemoryTypePreference})
	if total != 1 {
		t.Errorf("search preference total: got %d, want 1", total)
	}

	// 按标签搜索
	results, total, _ = store.Search(ctx, &SearchMemoryRequest{Tags: []string{"go"}})
	if total != 1 {
		t.Errorf("search tag 'go' total: got %d, want 1", total)
	}

	// 按重要性过滤
	results, total, _ = store.Search(ctx, &SearchMemoryRequest{MinScore: 0.8})
	if total != 1 {
		t.Errorf("search min_score 0.8 total: got %d, want 1", total)
	}
}

func TestMemoryStore_Cleanup(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	negTTL := -1 * time.Second
	store.Create(ctx, &MemoryEntry{AgentID: "a1", Type: MemoryTypeFact, Scope: MemoryScopeAgent, Content: "expired", TTL: &negTTL})
	store.Create(ctx, &MemoryEntry{AgentID: "a1", Type: MemoryTypeFact, Scope: MemoryScopeAgent, Content: "alive"})

	count, err := store.Cleanup(ctx)
	if err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}
	if count != 1 {
		t.Errorf("cleaned: got %d, want 1", count)
	}

	stats, _ := store.Stats(ctx)
	if stats.TotalEntries != 1 {
		t.Errorf("remaining: got %d, want 1", stats.TotalEntries)
	}
}

func TestMemoryStore_Stats(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	store.Create(ctx, &MemoryEntry{AgentID: "a1", Type: MemoryTypeFact, Scope: MemoryScopeAgent, Content: "f1", Importance: 0.8})
	store.Create(ctx, &MemoryEntry{AgentID: "a1", Type: MemoryTypePreference, Scope: MemoryScopeUser, Content: "p1", Importance: 0.6})

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if stats.TotalEntries != 2 {
		t.Errorf("total: got %d, want 2", stats.TotalEntries)
	}
	if stats.ByType["fact"] != 1 {
		t.Errorf("fact count: got %d, want 1", stats.ByType["fact"])
	}
	if stats.AvgImportance != 0.7 {
		t.Errorf("avg importance: got %f, want 0.7", stats.AvgImportance)
	}
}

func TestMemoryService_Create(t *testing.T) {
	store := NewMemoryStore()
	svc := NewMemoryService(store)
	ctx := context.Background()

	ttlSec := 300
	entry, err := svc.Create(ctx, &CreateMemoryRequest{
		AgentID:    "agent-1",
		Type:       MemoryTypeFact,
		Scope:      MemoryScopeAgent,
		Content:    "test content",
		Importance: 0.9,
		TTLSeconds: &ttlSec,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if entry.Content != "test content" {
		t.Errorf("content: got %q", entry.Content)
	}
	if entry.ExpiresAt == nil {
		t.Error("expected ExpiresAt to be set")
	}
}

func TestMemoryService_CreateValidation(t *testing.T) {
	store := NewMemoryStore()
	svc := NewMemoryService(store)
	ctx := context.Background()

	// 缺少 content
	_, err := svc.Create(ctx, &CreateMemoryRequest{AgentID: "a1"})
	if err == nil {
		t.Error("expected error for missing content")
	}

	// 缺少 agent_id
	_, err = svc.Create(ctx, &CreateMemoryRequest{Content: "test"})
	if err == nil {
		t.Error("expected error for missing agent_id")
	}
}

func TestMemoryService_Update(t *testing.T) {
	store := NewMemoryStore()
	svc := NewMemoryService(store)
	ctx := context.Background()

	entry, _ := svc.Create(ctx, &CreateMemoryRequest{
		AgentID: "agent-1",
		Type:    MemoryTypeFact,
		Scope:   MemoryScopeAgent,
		Content: "original",
	})

	newContent := "updated content"
	newScore := 0.99
	updated, err := svc.Update(ctx, entry.ID, &UpdateMemoryRequest{
		Content:    &newContent,
		Importance: &newScore,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if updated.Content != "updated content" {
		t.Errorf("content: got %q", updated.Content)
	}
	if updated.Importance != 0.99 {
		t.Errorf("importance: got %f, want 0.99", updated.Importance)
	}
}

func TestHandler_CreateAndGet(t *testing.T) {
	store := NewMemoryStore()
	svc := NewMemoryService(store)
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// 创建记忆
	body := `{"agent_id":"agent-1","type":"fact","scope":"agent","content":"test memory","importance":0.8}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create: got status %d, want %d", w.Code, http.StatusCreated)
	}

	var created MemoryEntry
	json.NewDecoder(w.Body).Decode(&created)
	if created.ID == "" {
		t.Fatal("expected ID in response")
	}

	// 获取记忆
	req = httptest.NewRequest(http.MethodGet, "/api/v1/memory/"+created.ID, nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("get: got status %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandler_Search(t *testing.T) {
	store := NewMemoryStore()
	svc := NewMemoryService(store)
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// 创建几条记忆
	for i, content := range []string{"Go is great", "Python is popular", "Go concurrency"} {
		svc.Create(context.Background(), &CreateMemoryRequest{
			AgentID:    "agent-1",
			Type:       MemoryTypeFact,
			Scope:      MemoryScopeAgent,
			Content:    content,
			Importance: float64(3-i) / 3.0,
		})
	}

	// 搜索
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memory?q=Go&agent_id=agent-1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("search: got status %d, want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		Items []MemoryEntry `json:"items"`
		Total int           `json:"total"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 2 {
		t.Errorf("search total: got %d, want 2", resp.Total)
	}
}

func TestHandler_Stats(t *testing.T) {
	store := NewMemoryStore()
	svc := NewMemoryService(store)
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/memory/stats", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("stats: got status %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandler_Cleanup(t *testing.T) {
	store := NewMemoryStore()
	svc := NewMemoryService(store)
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/cleanup", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("cleanup: got status %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	store := NewMemoryStore()
	svc := NewMemoryService(store)
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/memory/stats", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("got status %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}
