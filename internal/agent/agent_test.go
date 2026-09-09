package agent

import (
	"context"
	"testing"
)

func TestStateTransitions(t *testing.T) {
	tests := []struct {
		name     string
		from     State
		to       State
		expected bool
	}{
		{"idle to running", StateIdle, StateRunning, true},
		{"idle to terminated", StateIdle, StateTerminated, true},
		{"idle to paused", StateIdle, StatePaused, false},
		{"idle to error", StateIdle, StateError, false},
		{"running to paused", StateRunning, StatePaused, true},
		{"running to error", StateRunning, StateError, true},
		{"running to completed", StateRunning, StateCompleted, true},
		{"running to terminated", StateRunning, StateTerminated, true},
		{"running to idle", StateRunning, StateIdle, true},
		{"paused to running", StatePaused, StateRunning, true},
		{"paused to terminated", StatePaused, StateTerminated, true},
		{"paused to idle", StatePaused, StateIdle, false},
		{"error to idle", StateError, StateIdle, true},
		{"error to running", StateError, StateRunning, true},
		{"error to terminated", StateError, StateTerminated, true},
		{"error to paused", StateError, StatePaused, false},
		{"completed to idle", StateCompleted, StateIdle, true},
		{"completed to terminated", StateCompleted, StateTerminated, true},
		{"completed to running", StateCompleted, StateRunning, false},
		{"terminated to anything", StateTerminated, StateIdle, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanTransition(tt.from, tt.to)
			if got != tt.expected {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", tt.from, tt.to, got, tt.expected)
			}
		})
	}
}

func TestValidateTransition(t *testing.T) {
	// 合法转换不报错
	if err := ValidateTransition(StateIdle, StateRunning); err != nil {
		t.Errorf("expected no error for valid transition, got: %v", err)
	}

	// 非法转换报错
	if err := ValidateTransition(StateIdle, StatePaused); err == nil {
		t.Error("expected error for invalid transition")
	}
}

func TestNewAgent(t *testing.T) {
	req := CreateRequest{
		Name:        "test-agent",
		Description: "A test agent",
		Config: Config{
			Model:       "gpt-4",
			MaxTokens:   2048,
			Temperature: 0.5,
		},
	}

	agent := NewAgent(req)

	if agent.ID == "" {
		t.Error("agent ID should not be empty")
	}
	if agent.Name != "test-agent" {
		t.Errorf("expected name 'test-agent', got '%s'", agent.Name)
	}
	if agent.State != StateIdle {
		t.Errorf("expected initial state idle, got '%s'", agent.State)
	}
	if agent.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

func TestAgent_SetState(t *testing.T) {
	agent := NewAgent(CreateRequest{Name: "test"})

	// idle -> running
	if err := agent.SetState(StateRunning); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if agent.State != StateRunning {
		t.Errorf("expected running, got %s", agent.State)
	}

	// running -> paused
	if err := agent.SetState(StatePaused); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// paused -> idle (invalid)
	if err := agent.SetState(StateIdle); err == nil {
		t.Error("expected error for paused -> idle transition")
	}
}

func TestAgent_IsRunning(t *testing.T) {
	agent := NewAgent(CreateRequest{Name: "test"})
	if agent.IsRunning() {
		t.Error("new agent should not be running")
	}

	agent.SetState(StateRunning)
	if !agent.IsRunning() {
		t.Error("agent should be running after SetState(running)")
	}
}

func TestAgent_IsAvailable(t *testing.T) {
	agent := NewAgent(CreateRequest{Name: "test"})
	if !agent.IsAvailable() {
		t.Error("new agent should be available")
	}

	agent.SetState(StateRunning)
	if agent.IsAvailable() {
		t.Error("running agent should not be available")
	}
}

func TestAgent_IsTerminal(t *testing.T) {
	agent := NewAgent(CreateRequest{Name: "test"})
	if agent.IsTerminal() {
		t.Error("new agent should not be terminal")
	}

	agent.SetState(StateRunning)
	agent.SetState(StateCompleted)
	if !agent.IsTerminal() {
		t.Error("completed agent should be terminal")
	}
}

func TestAgent_ApplyUpdate(t *testing.T) {
	agent := NewAgent(CreateRequest{
		Name:        "original",
		Description: "original desc",
	})

	newName := "updated"
	agent.ApplyUpdate(UpdateRequest{Name: &newName})

	if agent.Name != "updated" {
		t.Errorf("expected name 'updated', got '%s'", agent.Name)
	}
	if agent.Description != "original desc" {
		t.Error("description should not change when not provided")
	}
}

// ---- Repository 测试 ----

func TestMemoryRepository_Create(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	agent := NewAgent(CreateRequest{Name: "test"})

	if err := repo.Create(ctx, agent); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// 重复创建应该失败
	if err := repo.Create(ctx, agent); err == nil {
		t.Error("expected error for duplicate create")
	}
}

func TestMemoryRepository_GetByID(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	agent := NewAgent(CreateRequest{Name: "test"})
	repo.Create(ctx, agent)

	// 获取存在的 agent
	got, err := repo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got.Name != "test" {
		t.Errorf("expected name 'test', got '%s'", got.Name)
	}

	// 获取不存在的 agent
	_, err = repo.GetByID(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent agent")
	}
}

func TestMemoryRepository_Update(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	agent := NewAgent(CreateRequest{Name: "original"})
	repo.Create(ctx, agent)

	agent.Name = "updated"
	if err := repo.Update(ctx, agent); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	got, _ := repo.GetByID(ctx, agent.ID)
	if got.Name != "updated" {
		t.Errorf("expected name 'updated', got '%s'", got.Name)
	}
}

func TestMemoryRepository_Delete(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	agent := NewAgent(CreateRequest{Name: "test"})
	repo.Create(ctx, agent)

	if err := repo.Delete(ctx, agent.ID); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	_, err := repo.GetByID(ctx, agent.ID)
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestMemoryRepository_List(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	// 创建多个 agent
	for i := 0; i < 5; i++ {
		agent := NewAgent(CreateRequest{Name: "agent"})
		repo.Create(ctx, agent)
	}

	// 列出全部
	agents, err := repo.List(ctx, 0, 10)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(agents) != 5 {
		t.Errorf("expected 5 agents, got %d", len(agents))
	}

	// 分页
	agents, _ = repo.List(ctx, 2, 2)
	if len(agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(agents))
	}

	// 超出范围
	agents, _ = repo.List(ctx, 100, 10)
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

func TestMemoryRepository_Count(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	count, _ := repo.Count(ctx)
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	repo.Create(ctx, NewAgent(CreateRequest{Name: "a"}))
	repo.Create(ctx, NewAgent(CreateRequest{Name: "b"}))

	count, _ = repo.Count(ctx)
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}

func TestMemoryRepository_ListByState(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	a1 := NewAgent(CreateRequest{Name: "idle"})
	a2 := NewAgent(CreateRequest{Name: "running"})
	a2.SetState(StateRunning)

	repo.Create(ctx, a1)
	repo.Create(ctx, a2)

	idle, _ := repo.ListByState(ctx, StateIdle)
	if len(idle) != 1 {
		t.Errorf("expected 1 idle agent, got %d", len(idle))
	}

	running, _ := repo.ListByState(ctx, StateRunning)
	if len(running) != 1 {
		t.Errorf("expected 1 running agent, got %d", len(running))
	}
}

// ---- Service 测试 ----

func TestService_Create(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	agent, err := svc.Create(ctx, CreateRequest{
		Name:   "test-agent",
		Config: Config{Model: "gpt-4"},
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if agent.Name != "test-agent" {
		t.Errorf("expected name 'test-agent', got '%s'", agent.Name)
	}
	// 检查默认值
	if agent.Config.MaxTokens != 4096 {
		t.Errorf("expected default MaxTokens 4096, got %d", agent.Config.MaxTokens)
	}
	if agent.Config.Resources.CPU != "1" {
		t.Errorf("expected default CPU '1', got '%s'", agent.Config.Resources.CPU)
	}
}

func TestService_Create_NoName(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	_, err := svc.Create(ctx, CreateRequest{})
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestService_Get(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	created, _ := svc.Create(ctx, CreateRequest{Name: "test"})

	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ID mismatch: %s vs %s", got.ID, created.ID)
	}
}

func TestService_Update_Running(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	agent, _ := svc.Create(ctx, CreateRequest{Name: "test"})
	svc.Start(ctx, agent.ID)

	newName := "updated"
	_, err := svc.Update(ctx, agent.ID, UpdateRequest{Name: &newName})
	if err == nil {
		t.Error("expected error when updating running agent")
	}
}

func TestService_Delete_Running(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	agent, _ := svc.Create(ctx, CreateRequest{Name: "test"})
	svc.Start(ctx, agent.ID)

	err := svc.Delete(ctx, agent.ID)
	if err == nil {
		t.Error("expected error when deleting running agent")
	}
}

func TestService_StartStop(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	agent, _ := svc.Create(ctx, CreateRequest{Name: "test"})

	// Start
	if err := svc.Start(ctx, agent.ID); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	got, _ := svc.Get(ctx, agent.ID)
	if got.State != StateRunning {
		t.Errorf("expected running, got %s", got.State)
	}

	// Stop
	if err := svc.Stop(ctx, agent.ID); err != nil {
		t.Fatalf("stop failed: %v", err)
	}

	got, _ = svc.Get(ctx, agent.ID)
	if got.State != StateIdle {
		t.Errorf("expected idle, got %s", got.State)
	}
}

func TestService_PauseResume(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	agent, _ := svc.Create(ctx, CreateRequest{Name: "test"})
	svc.Start(ctx, agent.ID)

	// Pause
	if err := svc.Pause(ctx, agent.ID); err != nil {
		t.Fatalf("pause failed: %v", err)
	}
	got, _ := svc.Get(ctx, agent.ID)
	if got.State != StatePaused {
		t.Errorf("expected paused, got %s", got.State)
	}

	// Resume
	if err := svc.Resume(ctx, agent.ID); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	got, _ = svc.Get(ctx, agent.ID)
	if got.State != StateRunning {
		t.Errorf("expected running, got %s", got.State)
	}
}

func TestService_List(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		svc.Create(ctx, CreateRequest{Name: "agent"})
	}

	agents, err := svc.List(ctx, 0, 10)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(agents) != 3 {
		t.Errorf("expected 3 agents, got %d", len(agents))
	}
}

func TestApplyDefaults(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)

	if cfg.MaxTokens != 4096 {
		t.Errorf("expected 4096, got %d", cfg.MaxTokens)
	}
	if cfg.Temperature != 0.7 {
		t.Errorf("expected 0.7, got %f", cfg.Temperature)
	}
	if cfg.Resources.CPU != "1" {
		t.Errorf("expected '1', got '%s'", cfg.Resources.CPU)
	}
	if cfg.Resources.Memory != "512Mi" {
		t.Errorf("expected '512Mi', got '%s'", cfg.Resources.Memory)
	}
	if cfg.Resources.Disk != "1Gi" {
		t.Errorf("expected '1Gi', got '%s'", cfg.Resources.Disk)
	}
	if cfg.Memory.ShortTermSize != 100 {
		t.Errorf("expected 100, got %d", cfg.Memory.ShortTermSize)
	}
	if cfg.Memory.LongTermSize != 1000 {
		t.Errorf("expected 1000, got %d", cfg.Memory.LongTermSize)
	}
}
