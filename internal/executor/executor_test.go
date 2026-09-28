package executor

import (
	"bytes"
	"container/heap"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/task"
)

// --- 内存仓库测试 ---

func TestMemoryRepository_CreateAndGet(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	tt := &task.Task{
		ID:      "task-1",
		AgentID: "agent-1",
		Goal:    "test goal",
		State:   task.StatePending,
	}

	if err := repo.Create(ctx, tt); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	got, err := repo.GetByID(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}

	if got.ID != "task-1" {
		t.Errorf("expected ID task-1, got %s", got.ID)
	}
	if got.Goal != "test goal" {
		t.Errorf("expected goal 'test goal', got %s", got.Goal)
	}
}

func TestMemoryRepository_CreateDuplicate(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	tt := &task.Task{ID: "task-1", AgentID: "agent-1", Goal: "goal"}
	if err := repo.Create(ctx, tt); err != nil {
		t.Fatalf("first Create failed: %v", err)
	}

	if err := repo.Create(ctx, tt); err == nil {
		t.Fatal("expected error on duplicate create, got nil")
	}
}

func TestMemoryRepository_GetNotFound(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	_, err := repo.GetByID(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestMemoryRepository_Update(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	tt := &task.Task{ID: "task-1", AgentID: "agent-1", Goal: "original", State: task.StatePending}
	repo.Create(ctx, tt)

	tt.Goal = "updated"
	tt.State = task.StateInProgress
	if err := repo.Update(ctx, tt); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	got, _ := repo.GetByID(ctx, "task-1")
	if got.Goal != "updated" {
		t.Errorf("expected goal 'updated', got %s", got.Goal)
	}
	if got.State != task.StateInProgress {
		t.Errorf("expected state in_progress, got %s", got.State)
	}
}

func TestMemoryRepository_Delete(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	tt := &task.Task{ID: "task-1", AgentID: "agent-1", Goal: "goal"}
	repo.Create(ctx, tt)

	if err := repo.Delete(ctx, "task-1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err := repo.GetByID(ctx, "task-1")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestMemoryRepository_ListWithFilter(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	repo.Create(ctx, &task.Task{ID: "t1", AgentID: "agent-1", Goal: "g1", State: task.StatePending})
	repo.Create(ctx, &task.Task{ID: "t2", AgentID: "agent-1", Goal: "g2", State: task.StateCompleted})
	repo.Create(ctx, &task.Task{ID: "t3", AgentID: "agent-2", Goal: "g3", State: task.StatePending})

	// 按 agent_id 过滤
	tasks, err := repo.List(ctx, TaskFilter{AgentID: "agent-1"}, 0, 10)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks for agent-1, got %d", len(tasks))
	}

	// 按 state 过滤
	tasks, err = repo.List(ctx, TaskFilter{State: task.StatePending}, 0, 10)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 pending tasks, got %d", len(tasks))
	}

	// 分页
	tasks, err = repo.List(ctx, TaskFilter{}, 1, 1)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task with offset=1, limit=1, got %d", len(tasks))
	}
}

func TestMemoryRepository_UpdateState(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	tt := &task.Task{ID: "task-1", AgentID: "agent-1", Goal: "goal", State: task.StatePending}
	repo.Create(ctx, tt)

	if err := repo.UpdateState(ctx, "task-1", task.StateInProgress); err != nil {
		t.Fatalf("UpdateState failed: %v", err)
	}

	got, _ := repo.GetByID(ctx, "task-1")
	if got.State != task.StateInProgress {
		t.Errorf("expected in_progress, got %s", got.State)
	}
}

// --- Task Manager 测试 ---

func newTestService() (task.Manager, Repository) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	return svc, repo
}

func TestTaskService_Create(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()

	created, err := svc.Create(ctx, "agent-1", "build a website")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if created.ID == "" {
		t.Error("expected non-empty task ID")
	}
	if created.AgentID != "agent-1" {
		t.Errorf("expected agent-1, got %s", created.AgentID)
	}
	if created.Goal != "build a website" {
		t.Errorf("expected 'build a website', got %s", created.Goal)
	}
	if created.State != task.StatePending {
		t.Errorf("expected pending, got %s", created.State)
	}
	if created.MaxRetries != 3 {
		t.Errorf("expected max_retries 3, got %d", created.MaxRetries)
	}
}

func TestTaskService_CreateValidation(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()

	_, err := svc.Create(ctx, "", "goal")
	if err == nil {
		t.Fatal("expected error for empty agent_id")
	}

	_, err = svc.Create(ctx, "agent-1", "")
	if err == nil {
		t.Fatal("expected error for empty goal")
	}
}

func TestTaskService_Get(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()

	created, _ := svc.Create(ctx, "agent-1", "test")
	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("expected %s, got %s", created.ID, got.ID)
	}
}

func TestTaskService_List(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()

	svc.Create(ctx, "agent-1", "task 1")
	svc.Create(ctx, "agent-1", "task 2")
	svc.Create(ctx, "agent-2", "task 3")

	// 按 agent 过滤
	tasks, err := svc.List(ctx, "agent-1", "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(tasks))
	}
}

func TestTaskService_StartAndComplete(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()

	created, _ := svc.Create(ctx, "agent-1", "test")

	if err := svc.Start(ctx, created.ID); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	got, _ := svc.Get(ctx, created.ID)
	if got.State != task.StateInProgress {
		t.Errorf("expected in_progress, got %s", got.State)
	}

	// 无子任务的任务不会被异步执行器推进，Complete 的返回值得以确定断言。
	usingComplete, _ := svc.Create(ctx, "agent-1", "noop")
	usingComplete.Subtasks = nil
	if err := svc.Start(ctx, usingComplete.ID); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := svc.Complete(ctx, usingComplete.ID, "done!"); err != nil {
		t.Fatalf("Complete failed: %v", err)
	}

	done, _ := svc.Get(ctx, usingComplete.ID)
	if done.State != task.StateCompleted {
		t.Errorf("expected completed, got %s", done.State)
	}
	if done.Result != "done!" {
		t.Errorf("expected result 'done!', got %s", done.Result)
	}
}

func TestTaskService_Cancel(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	created, _ := svc.Create(ctx, "agent-1", "test")

	if err := svc.Cancel(ctx, created.ID); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	got, _ := svc.Get(ctx, created.ID)
	if got.State != task.StateCancelled {
		t.Errorf("expected cancelled, got %s", got.State)
	}
}

func TestTaskService_FailAndRetry(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	// 使用无子任务的任务：无异步执行器参与，Fail 的状态迁移可确定断言。
	created, _ := svc.Create(ctx, "agent-1", "test")
	created.Subtasks = nil
	if err := svc.Start(ctx, created.ID); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if err := svc.Fail(ctx, created.ID, errForTest("something broke")); err != nil {
		t.Fatalf("Fail failed: %v", err)
	}

	got, _ := svc.Get(ctx, created.ID)
	if got.State != task.StateFailed {
		t.Errorf("expected failed, got %s", got.State)
	}
	if got.Error != "something broke" {
		t.Errorf("expected error 'something broke', got %s", got.Error)
	}

	// 重试
	if err := svc.Retry(ctx, created.ID); err != nil {
		t.Fatalf("Retry failed: %v", err)
	}

	got, _ = svc.Get(ctx, created.ID)
	if got.State != task.StatePending {
		t.Errorf("expected pending after retry, got %s", got.State)
	}
	if got.RetryCount != 1 {
		t.Errorf("expected retry_count 1, got %d", got.RetryCount)
	}
}

func TestTaskService_RetryMaxExceeded(t *testing.T) {
	svc, repo := newTestService()
	ctx := context.Background()

	created, _ := svc.Create(ctx, "agent-1", "test")
	// 手动设置重试次数到上限
	created.RetryCount = 3
	repo.Update(ctx, created)

	svc.Start(ctx, created.ID)
	svc.Fail(ctx, created.ID, errForTest("fail"))

	if err := svc.Retry(ctx, created.ID); err == nil {
		t.Fatal("expected error when max retries exceeded")
	}
}

func TestTaskService_GetProgress(t *testing.T) {
	svc, repo := newTestService()
	ctx := context.Background()

	created, _ := svc.Create(ctx, "agent-1", "test")
	created.Subtasks = []task.Subtask{
		{ID: "st1", TaskID: created.ID, Name: "step 1", State: task.StateCompleted},
		{ID: "st2", TaskID: created.ID, Name: "step 2", State: task.StateInProgress},
		{ID: "st3", TaskID: created.ID, Name: "step 3", State: task.StatePending},
	}
	repo.Update(ctx, created)

	progress, err := svc.GetProgress(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetProgress failed: %v", err)
	}

	if progress.Total != 3 {
		t.Errorf("expected total 3, got %d", progress.Total)
	}
	if progress.Completed != 1 {
		t.Errorf("expected completed 1, got %d", progress.Completed)
	}
	if progress.InProgress != 1 {
		t.Errorf("expected in_progress 1, got %d", progress.InProgress)
	}
}

// --- 优先级队列测试 ---

func TestTaskQueue_PriorityOrdering(t *testing.T) {
	q := &taskQueue{}
	now := time.Now()

	heap.Push(q, &QueueItem{TaskID: "low", Priority: PriorityLow, EnqueuedAt: now})
	heap.Push(q, &QueueItem{TaskID: "urgent", Priority: PriorityUrgent, EnqueuedAt: now})
	heap.Push(q, &QueueItem{TaskID: "normal", Priority: PriorityNormal, EnqueuedAt: now})
	heap.Push(q, &QueueItem{TaskID: "high", Priority: PriorityHigh, EnqueuedAt: now})

	expected := []string{"urgent", "high", "normal", "low"}
	for i, exp := range expected {
		item := heap.Pop(q).(*QueueItem)
		if item.TaskID != exp {
			t.Errorf("position %d: expected %s, got %s", i, exp, item.TaskID)
		}
	}
}

func TestTaskQueue_FIFOWithinSamePriority(t *testing.T) {
	q := &taskQueue{}
	base := time.Now()

	heap.Push(q, &QueueItem{TaskID: "first", Priority: PriorityNormal, EnqueuedAt: base})
	heap.Push(q, &QueueItem{TaskID: "second", Priority: PriorityNormal, EnqueuedAt: base.Add(time.Second)})
	heap.Push(q, &QueueItem{TaskID: "third", Priority: PriorityNormal, EnqueuedAt: base.Add(2 * time.Second)})

	expected := []string{"first", "second", "third"}
	for _, exp := range expected {
		item := heap.Pop(q).(*QueueItem)
		if item.TaskID != exp {
			t.Errorf("expected %s, got %s", exp, item.TaskID)
		}
	}
}

// --- 执行引擎测试 ---

func TestEngine_SubmitAndQueueLen(t *testing.T) {
	repo := NewMemoryRepository()
	engine := NewEngine(repo, DefaultEngineConfig())

	engine.Submit(context.Background(), "task-1", PriorityNormal)
	engine.Submit(context.Background(), "task-2", PriorityHigh)

	if engine.QueueLen() != 2 {
		t.Errorf("expected queue len 2, got %d", engine.QueueLen())
	}
}

func TestEngine_RegisterAgent(t *testing.T) {
	repo := NewMemoryRepository()
	engine := NewEngine(repo, DefaultEngineConfig())

	engine.RegisterAgent("agent-1")
	if !engine.IsAgentAvailable("agent-1") {
		t.Error("expected agent-1 to be available")
	}
	if engine.IsAgentAvailable("agent-unknown") {
		t.Error("expected unknown agent to not be available")
	}
}

func TestEngine_UnregisterAgent(t *testing.T) {
	repo := NewMemoryRepository()
	engine := NewEngine(repo, DefaultEngineConfig())

	engine.RegisterAgent("agent-1")
	engine.UnregisterAgent("agent-1")

	if engine.IsAgentAvailable("agent-1") {
		t.Error("expected agent-1 to not be available after unregister")
	}
}

// --- HTTP Handler 测试 ---

func TestHandler_Create(t *testing.T) {
	svc, _ := newTestService()
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body, _ := json.Marshal(CreateRequest{
		AgentID: "agent-1",
		Goal:    "build a website",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp task.Task
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.AgentID != "agent-1" {
		t.Errorf("expected agent-1, got %s", resp.AgentID)
	}
}

func TestHandler_Get(t *testing.T) {
	svc, _ := newTestService()
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// 先创建任务
	created, _ := svc.Create(context.Background(), "agent-1", "test")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+created.ID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandler_GetNotFound(t *testing.T) {
	svc, _ := newTestService()
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandler_List(t *testing.T) {
	svc, _ := newTestService()
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	svc.Create(context.Background(), "agent-1", "task 1")
	svc.Create(context.Background(), "agent-2", "task 2")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?agent_id=agent-1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if int(resp["total"].(float64)) != 1 {
		t.Errorf("expected total 1, got %v", resp["total"])
	}
}

func TestHandler_Start(t *testing.T) {
	svc, _ := newTestService()
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	created, _ := svc.Create(context.Background(), "agent-1", "test")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/start", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandler_Cancel(t *testing.T) {
	svc, _ := newTestService()
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	created, _ := svc.Create(context.Background(), "agent-1", "test")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/cancel", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandler_Progress(t *testing.T) {
	svc, _ := newTestService()
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	created, _ := svc.Create(context.Background(), "agent-1", "test")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/progress", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var progress task.Progress
	json.Unmarshal(w.Body.Bytes(), &progress)
	if progress.TaskID != created.ID {
		t.Errorf("expected task_id %s, got %s", created.ID, progress.TaskID)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	svc, _ := newTestService()
	handler := NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/tasks", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// --- 观察者测试 ---

type testObserver struct {
	mu     sync.Mutex
	events []TaskEvent
}

// OnTaskEvent 由 notify 直接调用（Start 的异步 goroutine 也会触发），
// 因此必须自行加锁：testing 的 -race 会把未同步的切片追加判为数据竞争。
func (o *testObserver) OnTaskEvent(event TaskEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

func (o *testObserver) snapshot() []TaskEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]TaskEvent(nil), o.events...)
}

func TestTaskService_ObserverNotified(t *testing.T) {
	repo := NewMemoryRepository()
	obs := &testObserver{}
	svc := NewServiceWithObservers(repo, obs)
	ctx := context.Background()

	// 无子任务 => Start 不启动异步执行器，事件序列完全确定。
	created, _ := svc.Create(ctx, "agent-1", "test")
	created.Subtasks = nil
	if err := svc.Start(ctx, created.ID); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := svc.Complete(ctx, created.ID, "done"); err != nil {
		t.Fatalf("Complete failed: %v", err)
	}

	// Create 不触发事件，Start 和 Complete 各触发一次
	events := obs.snapshot()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d (%+v)", len(events), events)
	}

	if events[0].OldState != task.StatePending || events[0].NewState != task.StateInProgress {
		t.Errorf("event 0 = %s -> %s, want pending -> in_progress",
			events[0].OldState, events[0].NewState)
	}
	if events[1].OldState != task.StateInProgress || events[1].NewState != task.StateCompleted {
		t.Errorf("event 1 = %s -> %s, want in_progress -> completed",
			events[1].OldState, events[1].NewState)
	}
}

// --- 依赖测试 ---

func TestDependenciesMet(t *testing.T) {
	task := &task.Task{
		Subtasks: []task.Subtask{
			{ID: "st1", State: task.StateCompleted},
			{ID: "st2", State: task.StateCompleted},
			{ID: "st3", State: task.StatePending, DependsOn: []string{"st1", "st2"}},
			{ID: "st4", State: task.StatePending, DependsOn: []string{"st1", "st3"}},
		},
	}

	if !dependenciesMet(task, 2) {
		t.Error("st3 deps (st1, st2 completed) should be met")
	}
	if dependenciesMet(task, 3) {
		t.Error("st4 deps (st3 pending) should NOT be met")
	}
}

// helper
type errForTest string

func (e errForTest) Error() string { return string(e) }
