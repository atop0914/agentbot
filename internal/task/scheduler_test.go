package task_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/atop0914/agentbot/internal/task"
)

// --- DependencyGraph Tests ---

func TestDependencyGraph_BasicConstruction(t *testing.T) {
	subtasks := []task.Subtask{
		{ID: "a", State: task.StatePending},
		{ID: "b", State: task.StatePending, DependsOn: []string{"a"}},
		{ID: "c", State: task.StatePending, DependsOn: []string{"a"}},
		{ID: "d", State: task.StatePending, DependsOn: []string{"b", "c"}},
	}

	g := task.NewDependencyGraph(subtasks)

	if g.NodeCount() != 4 {
		t.Errorf("NodeCount = %d, want 4", g.NodeCount())
	}
	if g.EdgeCount() != 4 {
		t.Errorf("EdgeCount = %d, want 4", g.EdgeCount())
	}
}

func TestDependencyGraph_Dependencies(t *testing.T) {
	subtasks := []task.Subtask{
		{ID: "a", State: task.StatePending},
		{ID: "b", State: task.StatePending, DependsOn: []string{"a"}},
		{ID: "c", State: task.StatePending, DependsOn: []string{"a", "b"}},
	}

	g := task.NewDependencyGraph(subtasks)

	// a has no dependencies
	deps := g.GetDependencies("a")
	if len(deps) != 0 {
		t.Errorf("a dependencies = %v, want []", deps)
	}

	// c depends on a and b
	deps = g.GetDependencies("c")
	if len(deps) != 2 {
		t.Errorf("c dependencies count = %d, want 2", len(deps))
	}

	// a is depended on by b and c
	dependents := g.GetDependents("a")
	if len(dependents) != 2 {
		t.Errorf("a dependents count = %d, want 2", len(dependents))
	}
}

func TestDependencyGraph_TopologicalSort_LinearChain(t *testing.T) {
	subtasks := []task.Subtask{
		{ID: "a", State: task.StatePending},
		{ID: "b", State: task.StatePending, DependsOn: []string{"a"}},
		{ID: "c", State: task.StatePending, DependsOn: []string{"b"}},
	}

	g := task.NewDependencyGraph(subtasks)
	levels, err := g.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 线性链应该是 3 层
	if len(levels) != 3 {
		t.Fatalf("levels = %d, want 3", len(levels))
	}
	if len(levels[0]) != 1 || levels[0][0] != "a" {
		t.Errorf("level[0] = %v, want [a]", levels[0])
	}
	if len(levels[1]) != 1 || levels[1][0] != "b" {
		t.Errorf("level[1] = %v, want [b]", levels[1])
	}
	if len(levels[2]) != 1 || levels[2][0] != "c" {
		t.Errorf("level[2] = %v, want [c]", levels[2])
	}
}

func TestDependencyGraph_TopologicalSort_Diamond(t *testing.T) {
	// 钻石形依赖: a -> b, a -> c, b -> d, c -> d
	subtasks := []task.Subtask{
		{ID: "a", State: task.StatePending},
		{ID: "b", State: task.StatePending, DependsOn: []string{"a"}},
		{ID: "c", State: task.StatePending, DependsOn: []string{"a"}},
		{ID: "d", State: task.StatePending, DependsOn: []string{"b", "c"}},
	}

	g := task.NewDependencyGraph(subtasks)
	levels, err := g.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 钻石形应该是 3 层: [a], [b, c], [d]
	if len(levels) != 3 {
		t.Fatalf("levels = %d, want 3", len(levels))
	}
	if len(levels[0]) != 1 || levels[0][0] != "a" {
		t.Errorf("level[0] = %v, want [a]", levels[0])
	}
	if len(levels[1]) != 2 {
		t.Errorf("level[1] count = %d, want 2", len(levels[1]))
	}
	// b 和 c 应该在同一层（顺序不确定）
	level1Set := make(map[string]bool)
	for _, id := range levels[1] {
		level1Set[id] = true
	}
	if !level1Set["b"] || !level1Set["c"] {
		t.Errorf("level[1] = %v, want [b, c] (any order)", levels[1])
	}
	if len(levels[2]) != 1 || levels[2][0] != "d" {
		t.Errorf("level[2] = %v, want [d]", levels[2])
	}
}

func TestDependencyGraph_TopologicalSort_FullyParallel(t *testing.T) {
	// 所有子任务互相独立
	subtasks := []task.Subtask{
		{ID: "a", State: task.StatePending},
		{ID: "b", State: task.StatePending},
		{ID: "c", State: task.StatePending},
	}

	g := task.NewDependencyGraph(subtasks)
	levels, err := g.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 全部独立应该只有 1 层
	if len(levels) != 1 {
		t.Fatalf("levels = %d, want 1", len(levels))
	}
	if len(levels[0]) != 3 {
		t.Errorf("level[0] count = %d, want 3", len(levels[0]))
	}
}

func TestDependencyGraph_DetectCycles_NoCycle(t *testing.T) {
	subtasks := []task.Subtask{
		{ID: "a", State: task.StatePending},
		{ID: "b", State: task.StatePending, DependsOn: []string{"a"}},
		{ID: "c", State: task.StatePending, DependsOn: []string{"b"}},
	}

	g := task.NewDependencyGraph(subtasks)
	hasCycle, nodes := g.DetectCycles()

	if hasCycle {
		t.Errorf("expected no cycle, got cycle with nodes: %v", nodes)
	}
}

func TestDependencyGraph_DetectCycles_SimpleCycle(t *testing.T) {
	// a -> b -> a 形成环
	subtasks := []task.Subtask{
		{ID: "a", State: task.StatePending, DependsOn: []string{"b"}},
		{ID: "b", State: task.StatePending, DependsOn: []string{"a"}},
	}

	g := task.NewDependencyGraph(subtasks)
	hasCycle, nodes := g.DetectCycles()

	if !hasCycle {
		t.Error("expected cycle detection")
	}
	if len(nodes) != 2 {
		t.Errorf("cycle nodes count = %d, want 2", len(nodes))
	}
}

func TestDependencyGraph_TopologicalSort_CycleError(t *testing.T) {
	subtasks := []task.Subtask{
		{ID: "a", State: task.StatePending, DependsOn: []string{"c"}},
		{ID: "b", State: task.StatePending, DependsOn: []string{"a"}},
		{ID: "c", State: task.StatePending, DependsOn: []string{"b"}},
	}

	g := task.NewDependencyGraph(subtasks)
	_, err := g.TopologicalSort()

	if err == nil {
		t.Error("expected error for circular dependency")
	}
}

func TestDependencyGraph_EmptyGraph(t *testing.T) {
	g := task.NewDependencyGraph(nil)
	if g.NodeCount() != 0 {
		t.Errorf("NodeCount = %d, want 0", g.NodeCount())
	}
	if g.EdgeCount() != 0 {
		t.Errorf("EdgeCount = %d, want 0", g.EdgeCount())
	}

	levels, err := g.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(levels) != 0 {
		t.Errorf("levels = %d, want 0", len(levels))
	}
}

// --- ParallelScheduler Tests ---

func TestParallelScheduler_AllParallel(t *testing.T) {
	// 3 个完全独立的子任务应该并发执行
	var maxConcurrent int64
	var currentConcurrent int64

	registry := task.NewExecutorRegistry()
	// 注入自定义执行器来验证并发性
	registry.Register(&countingExecutor{
		max:     &maxConcurrent,
		current: &currentConcurrent,
	})

	runner := task.NewTaskRunner(registry)
	scheduler := task.NewParallelScheduler(runner, 0)

	taskObj := &task.Task{
		ID:    "parallel-test",
		Goal:  "Test parallel execution",
		State: task.StatePending,
		Subtasks: []task.Subtask{
			{ID: "p1", State: task.StatePending, Actions: []task.Action{
				{ID: "a1", Type: "counter", State: task.StatePending},
			}},
			{ID: "p2", State: task.StatePending, Actions: []task.Action{
				{ID: "a2", Type: "counter", State: task.StatePending},
			}},
			{ID: "p3", State: task.StatePending, Actions: []task.Action{
				{ID: "a3", Type: "counter", State: task.StatePending},
			}},
		},
	}

	result, err := scheduler.Schedule(taskObj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 所有子任务应成功
	if result.CompletedCount != 3 {
		t.Errorf("CompletedCount = %d, want 3", result.CompletedCount)
	}
	if result.FailedCount != 0 {
		t.Errorf("FailedCount = %d, want 0", result.FailedCount)
	}

	// 只有 1 层（全部并行）
	if len(result.Levels) != 1 {
		t.Errorf("levels = %d, want 1", len(result.Levels))
	}
}

func TestParallelScheduler_DependencyOrder(t *testing.T) {
	// 验证依赖关系被正确执行：b 依赖 a，c 依赖 b
	var order []string
	var mu = &sync.Mutex{}

	registry := task.NewExecutorRegistry()
	registry.Register(&orderTrackerExecutor{order: &order, mu: mu})

	runner := task.NewTaskRunner(registry)
	scheduler := task.NewParallelScheduler(runner, 0)

	taskObj := &task.Task{
		ID:    "dep-test",
		Goal:  "Test dependency order",
		State: task.StatePending,
		Subtasks: []task.Subtask{
			{ID: "first", State: task.StatePending, Actions: []task.Action{
				{ID: "a1", Type: "tracker", Params: map[string]string{"name": "first"}, State: task.StatePending},
			}},
			{ID: "second", State: task.StatePending, DependsOn: []string{"first"}, Actions: []task.Action{
				{ID: "a2", Type: "tracker", Params: map[string]string{"name": "second"}, State: task.StatePending},
			}},
			{ID: "third", State: task.StatePending, DependsOn: []string{"second"}, Actions: []task.Action{
				{ID: "a3", Type: "tracker", Params: map[string]string{"name": "third"}, State: task.StatePending},
			}},
		},
	}

	result, err := scheduler.Schedule(taskObj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.CompletedCount != 3 {
		t.Errorf("CompletedCount = %d, want 3", result.CompletedCount)
	}

	// 验证执行顺序
	if len(order) != 3 {
		t.Fatalf("execution order count = %d, want 3", len(order))
	}
	expectedOrder := []string{"first", "second", "third"}
	for i, name := range order {
		if name != expectedOrder[i] {
			t.Errorf("order[%d] = %q, want %q", i, name, expectedOrder[i])
		}
	}
}

func TestParallelScheduler_DiamondDependency(t *testing.T) {
	// 钻石形: a -> b, a -> c, b -> d, c -> d
	var order []string
	var mu = &sync.Mutex{}

	registry := task.NewExecutorRegistry()
	registry.Register(&orderTrackerExecutor{order: &order, mu: mu})

	runner := task.NewTaskRunner(registry)
	scheduler := task.NewParallelScheduler(runner, 0)

	taskObj := &task.Task{
		ID:    "diamond-test",
		Goal:  "Test diamond dependency",
		State: task.StatePending,
		Subtasks: []task.Subtask{
			{ID: "root", State: task.StatePending, Actions: []task.Action{
				{ID: "a1", Type: "tracker", Params: map[string]string{"name": "root"}, State: task.StatePending},
			}},
			{ID: "left", State: task.StatePending, DependsOn: []string{"root"}, Actions: []task.Action{
				{ID: "a2", Type: "tracker", Params: map[string]string{"name": "left"}, State: task.StatePending},
			}},
			{ID: "right", State: task.StatePending, DependsOn: []string{"root"}, Actions: []task.Action{
				{ID: "a3", Type: "tracker", Params: map[string]string{"name": "right"}, State: task.StatePending},
			}},
			{ID: "final", State: task.StatePending, DependsOn: []string{"left", "right"}, Actions: []task.Action{
				{ID: "a4", Type: "tracker", Params: map[string]string{"name": "final"}, State: task.StatePending},
			}},
		},
	}

	result, err := scheduler.Schedule(taskObj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.CompletedCount != 4 {
		t.Errorf("CompletedCount = %d, want 4", result.CompletedCount)
	}

	// 3 层: [root], [left, right], [final]
	if len(result.Levels) != 3 {
		t.Errorf("levels = %d, want 3", len(result.Levels))
	}

	// root 必须在 left 和 right 之前
	// final 必须在 left 和 right 之后
	rootIdx := findIndex(order, "root")
	leftIdx := findIndex(order, "left")
	rightIdx := findIndex(order, "right")
	finalIdx := findIndex(order, "final")

	if rootIdx >= leftIdx || rootIdx >= rightIdx {
		t.Error("root should execute before left and right")
	}
	if finalIdx <= leftIdx || finalIdx <= rightIdx {
		t.Error("final should execute after left and right")
	}
}

func TestParallelScheduler_MaxConcurrency(t *testing.T) {
	// 测试并发限制
	var maxConcurrent int64
	var currentConcurrent int64

	registry := task.NewExecutorRegistry()
	registry.Register(&countingExecutor{
		max:     &maxConcurrent,
		current: &currentConcurrent,
	})

	runner := task.NewTaskRunner(registry)
	// 限制最大并发为 2
	scheduler := task.NewParallelScheduler(runner, 2)

	taskObj := &task.Task{
		ID:    "concurrency-test",
		Goal:  "Test concurrency limit",
		State: task.StatePending,
		Subtasks: []task.Subtask{
			{ID: "c1", State: task.StatePending, Actions: []task.Action{
				{ID: "a1", Type: "counter", State: task.StatePending},
			}},
			{ID: "c2", State: task.StatePending, Actions: []task.Action{
				{ID: "a2", Type: "counter", State: task.StatePending},
			}},
			{ID: "c3", State: task.StatePending, Actions: []task.Action{
				{ID: "a3", Type: "counter", State: task.StatePending},
			}},
			{ID: "c4", State: task.StatePending, Actions: []task.Action{
				{ID: "a4", Type: "counter", State: task.StatePending},
			}},
		},
	}

	result, err := scheduler.Schedule(taskObj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.CompletedCount != 4 {
		t.Errorf("CompletedCount = %d, want 4", result.CompletedCount)
	}

	// 最大并发不应超过 2
	if maxConcurrent > 2 {
		t.Errorf("maxConcurrent = %d, want <= 2", maxConcurrent)
	}
}

func TestParallelScheduler_CycleDetection(t *testing.T) {
	// 环形依赖应该返回错误
	runner := task.NewTaskRunner(task.NewExecutorRegistry())
	scheduler := task.NewParallelScheduler(runner, 0)

	taskObj := &task.Task{
		ID:    "cycle-test",
		Goal:  "Test cycle detection",
		State: task.StatePending,
		Subtasks: []task.Subtask{
			{ID: "x", State: task.StatePending, DependsOn: []string{"z"}, Actions: []task.Action{}},
			{ID: "y", State: task.StatePending, DependsOn: []string{"x"}, Actions: []task.Action{}},
			{ID: "z", State: task.StatePending, DependsOn: []string{"y"}, Actions: []task.Action{}},
		},
	}

	_, err := scheduler.Schedule(taskObj)
	if err == nil {
		t.Error("expected error for circular dependency")
	}
}

// --- Helper executors for testing ---

// countingExecutor 追踪最大并发数
type countingExecutor struct {
	max     *int64
	current *int64
}

func (e *countingExecutor) Type() task.ActionType { return "counter" }

func (e *countingExecutor) Execute(ctx context.Context, action task.Action) (string, error) {
	cur := atomic.AddInt64(e.current, 1)
	// 更新最大并发数
	for {
		old := atomic.LoadInt64(e.max)
		if cur <= old || atomic.CompareAndSwapInt64(e.max, old, cur) {
			break
		}
	}
	// 模拟一些工作
	select {
	case <-ctx.Done():
		atomic.AddInt64(e.current, -1)
		return "", ctx.Err()
	default:
	}
	atomic.AddInt64(e.current, -1)
	return "done", nil
}

// orderTrackerExecutor 记录执行顺序
type orderTrackerExecutor struct {
	order *[]string
	mu    *sync.Mutex
}

func (e *orderTrackerExecutor) Type() task.ActionType { return "tracker" }

func (e *orderTrackerExecutor) Execute(ctx context.Context, action task.Action) (string, error) {
	name := action.Params["name"]
	e.mu.Lock()
	*e.order = append(*e.order, name)
	e.mu.Unlock()
	return name, nil
}

func findIndex(slice []string, target string) int {
	for i, v := range slice {
		if v == target {
			return i
		}
	}
	return -1
}
