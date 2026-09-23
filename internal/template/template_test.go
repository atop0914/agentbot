package template

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func setupTestService() (*TemplateService, *InMemoryRecorder) {
	repo := NewMemoryRepository()
	execRepo := NewMemoryExecRepository()
	svc := NewService(repo, execRepo)
	recorder := NewInMemoryRecorder(repo)
	return svc, recorder
}

func TestTemplateService_Create(t *testing.T) {
	svc, _ := setupTestService()
	ctx := context.Background()

	tmpl, err := svc.Create(ctx, Template{Name: "test", Description: "desc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tmpl.ID == "" {
		t.Error("expected non-empty ID")
	}
	if tmpl.Name != "test" {
		t.Errorf("expected name 'test', got %q", tmpl.Name)
	}
}

func TestTemplateService_CreateValidation(t *testing.T) {
	svc, _ := setupTestService()
	ctx := context.Background()

	_, err := svc.Create(ctx, Template{})
	if err != ErrInvalidInput {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

func TestTemplateService_Get(t *testing.T) {
	svc, _ := setupTestService()
	ctx := context.Background()

	created, _ := svc.Create(ctx, Template{Name: "test"})
	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "test" {
		t.Errorf("expected name 'test', got %q", got.Name)
	}
}

func TestTemplateService_GetNotFound(t *testing.T) {
	svc, _ := setupTestService()
	_, err := svc.Get(context.Background(), "nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestTemplateService_Update(t *testing.T) {
	svc, _ := setupTestService()
	ctx := context.Background()

	created, _ := svc.Create(ctx, Template{Name: "original"})
	updated, err := svc.Update(ctx, created.ID, Template{Name: "updated", Description: "new desc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Name != "updated" {
		t.Errorf("expected name 'updated', got %q", updated.Name)
	}
}

func TestTemplateService_Delete(t *testing.T) {
	svc, _ := setupTestService()
	ctx := context.Background()

	created, _ := svc.Create(ctx, Template{Name: "to delete"})
	if err := svc.Delete(ctx, created.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err := svc.Get(ctx, created.ID)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestTemplateService_ListFilter(t *testing.T) {
	svc, _ := setupTestService()
	ctx := context.Background()

	public := true
	svc.Create(ctx, Template{Name: "pub", Category: "devops", Public: true})
	svc.Create(ctx, Template{Name: "priv", Category: "internal"})

	results, err := svc.List(ctx, Filter{Public: &public})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 public template, got %d", len(results))
	}

	results, _ = svc.List(ctx, Filter{Category: "devops"})
	if len(results) != 1 {
		t.Errorf("expected 1 devops template, got %d", len(results))
	}
}

func TestTemplateService_ListSearch(t *testing.T) {
	svc, _ := setupTestService()
	ctx := context.Background()

	svc.Create(ctx, Template{Name: "Deploy Pipeline", Description: "CI/CD"})
	svc.Create(ctx, Template{Name: "Monitor Setup", Description: "alerts"})

	results, _ := svc.List(ctx, Filter{Search: "pipeline"})
	if len(results) != 1 {
		t.Errorf("expected 1 result for 'pipeline', got %d", len(results))
	}
}

func TestTemplateService_ImportExport(t *testing.T) {
	svc, _ := setupTestService()
	ctx := context.Background()

	original, _ := svc.Create(ctx, Template{Name: "exportable", Steps: []Step{{ID: "s1", Name: "step1", Type: StepTerminal}}})
	data, err := svc.Export(ctx, original.ID)
	if err != nil {
		t.Fatalf("export error: %v", err)
	}

	imported, err := svc.Import(ctx, data)
	if err != nil {
		t.Fatalf("import error: %v", err)
	}
	if imported.Name != "exportable" {
		t.Errorf("expected name 'exportable', got %q", imported.Name)
	}
	if imported.ID == original.ID {
		t.Error("imported template should have a new ID")
	}
}

func TestTemplateService_Rate(t *testing.T) {
	svc, _ := setupTestService()
	ctx := context.Background()

	tmpl, _ := svc.Create(ctx, Template{Name: "ratable"})
	svc.Rate(ctx, tmpl.ID, 4.0)
	svc.Rate(ctx, tmpl.ID, 5.0)

	got, _ := svc.Get(ctx, tmpl.ID)
	if got.Rating < 4.0 || got.Rating > 5.0 {
		t.Errorf("expected rating between 4 and 5, got %f", got.Rating)
	}
	if got.UsageCount != 2 {
		t.Errorf("expected usage count 2, got %d", got.UsageCount)
	}
}

func TestTemplateService_Execute(t *testing.T) {
	svc, _ := setupTestService()
	ctx := context.Background()

	tmpl, _ := svc.Create(ctx, Template{Name: "executable"})
	exec, err := svc.Execute(ctx, tmpl.ID, "agent-1", map[string]string{"env": "prod"})
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if exec.Status != "running" {
		t.Errorf("expected status 'running', got %q", exec.Status)
	}

	execs, _ := svc.ListExecutions(ctx, tmpl.ID)
	if len(execs) != 1 {
		t.Errorf("expected 1 execution, got %d", len(execs))
	}
}

func TestRecorder_StartStop(t *testing.T) {
	_, recorder := setupTestService()
	ctx := context.Background()

	if recorder.IsRecording() {
		t.Error("should not be recording initially")
	}

	err := recorder.StartRecording(ctx, "agent-1", "test workflow")
	if err != nil {
		t.Fatalf("start error: %v", err)
	}
	if !recorder.IsRecording() {
		t.Error("should be recording after start")
	}

	// Record some steps
	recorder.RecordStep(ctx, Step{Name: "step1", Type: StepTerminal}, StepResult{Status: "completed"})
	recorder.RecordStep(ctx, Step{Name: "step2", Type: StepBrowser}, StepResult{Status: "completed"})
	if recorder.StepCount() != 2 {
		t.Errorf("expected 2 steps, got %d", recorder.StepCount())
	}

	tmpl, err := recorder.StopRecording(ctx)
	if err != nil {
		t.Fatalf("stop error: %v", err)
	}
	if recorder.IsRecording() {
		t.Error("should not be recording after stop")
	}
	if tmpl.Name != "test workflow" {
		t.Errorf("expected name 'test workflow', got %q", tmpl.Name)
	}
	if len(tmpl.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(tmpl.Steps))
	}
}

func TestRecorder_DoubleStart(t *testing.T) {
	_, recorder := setupTestService()
	ctx := context.Background()

	recorder.StartRecording(ctx, "agent-1", "test")
	err := recorder.StartRecording(ctx, "agent-1", "test2")
	if err != ErrAlreadyRecording {
		t.Errorf("expected ErrAlreadyRecording, got %v", err)
	}
}

func TestRecorder_RecordWithoutStart(t *testing.T) {
	_, recorder := setupTestService()
	err := recorder.RecordStep(context.Background(), Step{Name: "x"}, StepResult{})
	if err != ErrNotRecording {
		t.Errorf("expected ErrNotRecording, got %v", err)
	}
}

func TestRecorder_StopWithoutStart(t *testing.T) {
	_, recorder := setupTestService()
	_, err := recorder.StopRecording(context.Background())
	if err != ErrNotRecording {
		t.Errorf("expected ErrNotRecording, got %v", err)
	}
}

// Handler tests

func TestHandler_CreateAndGet(t *testing.T) {
	svc, recorder := setupTestService()
	h := NewHandler(svc, recorder)

	body := `{"name":"test template","description":"a test","category":"devops"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/templates", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.handleTemplates(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", w.Code)
	}

	var created Template
	json.NewDecoder(w.Body).Decode(&created)
	if created.Name != "test template" {
		t.Errorf("expected name 'test template', got %q", created.Name)
	}

	// Get by ID
	req = httptest.NewRequest(http.MethodGet, "/api/v1/templates/"+created.ID, nil)
	w = httptest.NewRecorder()
	h.handleTemplateByID(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandler_List(t *testing.T) {
	svc, recorder := setupTestService()
	ctx := context.Background()
	svc.Create(ctx, Template{Name: "alpha"})
	svc.Create(ctx, Template{Name: "beta"})

	h := NewHandler(svc, recorder)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/templates", nil)
	w := httptest.NewRecorder()
	h.handleTemplates(w, req)

	var resp map[string][]*Template
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp["templates"]) != 2 {
		t.Errorf("expected 2 templates, got %d", len(resp["templates"]))
	}
}

func TestHandler_Delete(t *testing.T) {
	svc, recorder := setupTestService()
	ctx := context.Background()
	created, _ := svc.Create(ctx, Template{Name: "delete me"})

	h := NewHandler(svc, recorder)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/templates/"+created.ID, nil)
	w := httptest.NewRecorder()
	h.handleTemplateByID(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandler_RecordingFlow(t *testing.T) {
	svc, recorder := setupTestService()
	h := NewHandler(svc, recorder)

	// Start
	body := `{"agent_id":"agent-1","name":"recorded flow"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/recordings/start", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.handleStartRecording(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("start: expected 200, got %d", w.Code)
	}

	// Record step
	body = `{"step":{"name":"step1","type":"terminal"},"result":{"status":"completed","output":"ok"}}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/recordings/step", bytes.NewBufferString(body))
	w = httptest.NewRecorder()
	h.handleRecordStep(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("record step: expected 200, got %d", w.Code)
	}

	// Stop
	req = httptest.NewRequest(http.MethodPost, "/api/v1/recordings/stop", nil)
	w = httptest.NewRecorder()
	h.handleStopRecording(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("stop: expected 201, got %d", w.Code)
	}

	var tmpl Template
	json.NewDecoder(w.Body).Decode(&tmpl)
	if tmpl.Name != "recorded flow" {
		t.Errorf("expected name 'recorded flow', got %q", tmpl.Name)
	}
}

func TestHandler_Import(t *testing.T) {
	svc, recorder := setupTestService()
	h := NewHandler(svc, recorder)

	body := `{"name":"imported","steps":[{"id":"s1","name":"run","type":"terminal"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/templates", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.handleTemplates(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	svc, recorder := setupTestService()
	h := NewHandler(svc, recorder)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/templates", nil)
	w := httptest.NewRecorder()
	h.handleTemplates(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestMemoryRepository_DuplicateCreate(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	tmpl := &Template{ID: "dup", Name: "test"}
	repo.Create(ctx, tmpl)
	err := repo.Create(ctx, tmpl)
	if err != ErrTemplateExists {
		t.Errorf("expected ErrTemplateExists, got %v", err)
	}
}

func TestMemoryExecRepository_ListByTemplate(t *testing.T) {
	repo := NewMemoryExecRepository()
	ctx := context.Background()

	repo.Create(ctx, &Execution{ID: "e1", TemplateID: "t1", StartedAt: timeNow()})
	repo.Create(ctx, &Execution{ID: "e2", TemplateID: "t1", StartedAt: timeNow()})
	repo.Create(ctx, &Execution{ID: "e3", TemplateID: "t2", StartedAt: timeNow()})

	execs, err := repo.ListByTemplate(ctx, "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(execs) != 2 {
		t.Errorf("expected 2 executions, got %d", len(execs))
	}
}

func timeNow() time.Time { return time.Now() }

func TestTemplateService_GetPopular(t *testing.T) {
	svc, _ := setupTestService()
	ctx := context.Background()

	svc.Create(ctx, Template{Name: "a"})
	svc.Create(ctx, Template{Name: "b"})
	svc.Create(ctx, Template{Name: "c"})

	// Rate some
	tmpls, _ := svc.List(ctx, Filter{})
	svc.Rate(ctx, tmpls[0].ID, 5.0)
	svc.Rate(ctx, tmpls[0].ID, 5.0)
	svc.Rate(ctx, tmpls[1].ID, 3.0)

	popular, err := svc.GetPopular(ctx, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(popular) != 2 {
		t.Errorf("expected 2 popular, got %d", len(popular))
	}
	// First should be highest usage
	if popular[0].UsageCount < popular[1].UsageCount {
		t.Error("expected popular sorted by usage_count desc")
	}
}

func TestTemplateService_GetByCategory(t *testing.T) {
	svc, _ := setupTestService()
	ctx := context.Background()

	svc.Create(ctx, Template{Name: "a", Category: "ci"})
	svc.Create(ctx, Template{Name: "b", Category: "ci"})
	svc.Create(ctx, Template{Name: "c", Category: "monitoring"})

	results, err := svc.GetByCategory(ctx, "ci")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 ci templates, got %d", len(results))
	}
}
