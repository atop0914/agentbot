package task_test

import (
	"context"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/task"
)

// --- Decomposer Tests ---

func TestRuleBasedDecomposer_Deploy(t *testing.T) {
	d := task.NewRuleBasedDecomposer()

	tests := []struct {
		name      string
		goal      string
		wantCount int
		wantFirst string
	}{
		{
			name:      "deploy goal",
			goal:      "部署应用到生产环境",
			wantCount: 4,
			wantFirst: "环境检查",
		},
		{
			name:      "release goal",
			goal:      "发布新版本 v2.0",
			wantCount: 4,
			wantFirst: "环境检查",
		},
		{
			name:      "deploy english",
			goal:      "Deploy the application to production",
			wantCount: 4,
			wantFirst: "环境检查",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subtasks, err := d.Decompose(context.Background(), tt.goal)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(subtasks) != tt.wantCount {
				t.Errorf("got %d subtasks, want %d", len(subtasks), tt.wantCount)
			}
			if subtasks[0].Name != tt.wantFirst {
				t.Errorf("first subtask name = %q, want %q", subtasks[0].Name, tt.wantFirst)
			}
		})
	}
}

func TestRuleBasedDecomposer_Test(t *testing.T) {
	d := task.NewRuleBasedDecomposer()

	subtasks, err := d.Decompose(context.Background(), "测试所有API接口")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(subtasks) != 3 {
		t.Errorf("got %d subtasks, want 3", len(subtasks))
	}

	// 验证依赖关系
	if len(subtasks[1].DependsOn) == 0 {
		t.Error("second subtask should have dependencies")
	}
	if subtasks[1].DependsOn[0] != subtasks[0].ID {
		t.Errorf("second subtask depends on %q, want %q", subtasks[1].DependsOn[0], subtasks[0].ID)
	}
}

func TestRuleBasedDecomposer_FileOp(t *testing.T) {
	d := task.NewRuleBasedDecomposer()

	subtasks, err := d.Decompose(context.Background(), "下载服务器上的日志文件")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(subtasks) != 2 {
		t.Errorf("got %d subtasks, want 2", len(subtasks))
	}

	// 验证动作类型
	if subtasks[0].Actions[0].Type != task.ActionFile {
		t.Errorf("action type = %q, want %q", subtasks[0].Actions[0].Type, task.ActionFile)
	}
}

func TestRuleBasedDecomposer_Browser(t *testing.T) {
	d := task.NewRuleBasedDecomposer()

	subtasks, err := d.Decompose(context.Background(), "爬取网页数据")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(subtasks) != 3 {
		t.Errorf("got %d subtasks, want 3", len(subtasks))
	}

	// 验证浏览器动作
	for _, st := range subtasks {
		for _, act := range st.Actions {
			if act.Type != task.ActionBrowser {
				t.Errorf("action type = %q, want %q", act.Type, task.ActionBrowser)
			}
		}
	}
}

func TestRuleBasedDecomposer_API(t *testing.T) {
	d := task.NewRuleBasedDecomposer()

	subtasks, err := d.Decompose(context.Background(), "调用第三方API获取数据")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(subtasks) != 2 {
		t.Errorf("got %d subtasks, want 2", len(subtasks))
	}
}

func TestRuleBasedDecomposer_Database(t *testing.T) {
	d := task.NewRuleBasedDecomposer()

	subtasks, err := d.Decompose(context.Background(), "查询数据库中的用户信息")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(subtasks) != 3 {
		t.Errorf("got %d subtasks, want 3", len(subtasks))
	}
}

func TestRuleBasedDecomposer_Default(t *testing.T) {
	d := task.NewRuleBasedDecomposer()

	subtasks, err := d.Decompose(context.Background(), "完成一个复杂的数据处理任务")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(subtasks) != 3 {
		t.Errorf("got %d subtasks, want 3", len(subtasks))
	}

	// 验证默认策略的子任务名称
	expectedNames := []string{"分析目标", "执行任务", "验证结果"}
	for i, st := range subtasks {
		if st.Name != expectedNames[i] {
			t.Errorf("subtask[%d].Name = %q, want %q", i, st.Name, expectedNames[i])
		}
	}
}

func TestRuleBasedDecomposer_EmptyGoal(t *testing.T) {
	d := task.NewRuleBasedDecomposer()

	_, err := d.Decompose(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty goal")
	}
}

func TestRuleBasedDecomposer_SubtaskStructure(t *testing.T) {
	d := task.NewRuleBasedDecomposer()

	subtasks, err := d.Decompose(context.Background(), "部署应用")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, st := range subtasks {
		if st.ID == "" {
			t.Error("subtask ID should not be empty")
		}
		if st.Name == "" {
			t.Error("subtask Name should not be empty")
		}
		if st.Description == "" {
			t.Error("subtask Description should not be empty")
		}
		if st.State != task.StatePending {
			t.Errorf("subtask State = %q, want %q", st.State, task.StatePending)
		}
		if st.CreatedAt.IsZero() {
			t.Error("subtask CreatedAt should not be zero")
		}
		if len(st.Actions) == 0 {
			t.Error("subtask should have at least one action")
		}

		for _, act := range st.Actions {
			if act.ID == "" {
				t.Error("action ID should not be empty")
			}
			if act.Type == "" {
				t.Error("action Type should not be empty")
			}
			if act.Name == "" {
				t.Error("action Name should not be empty")
			}
			if act.State != task.StatePending {
				t.Errorf("action State = %q, want %q", act.State, task.StatePending)
			}
		}
	}
}

func TestRuleBasedDecomposer_DependencyChain(t *testing.T) {
	d := task.NewRuleBasedDecomposer()

	subtasks, err := d.Decompose(context.Background(), "部署应用到生产环境")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 第一个子任务无依赖
	if len(subtasks[0].DependsOn) != 0 {
		t.Error("first subtask should have no dependencies")
	}

	// 后续子任务依赖前一个
	for i := 1; i < len(subtasks); i++ {
		if len(subtasks[i].DependsOn) == 0 {
			t.Errorf("subtask[%d] should have dependencies", i)
		}
		if subtasks[i].DependsOn[0] != subtasks[i-1].ID {
			t.Errorf("subtask[%d] depends on %q, want %q", i, subtasks[i].DependsOn[0], subtasks[i-1].ID)
		}
	}
}

// --- Executor Registry Tests ---

func TestExecutorRegistry_Terminal(t *testing.T) {
	r := task.NewExecutorRegistry()

	result, err := r.Execute(context.Background(), task.Action{
		Type: task.ActionTerminal,
		Params: map[string]string{
			"command": "echo hello",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("result should not be empty")
	}
}

func TestExecutorRegistry_File(t *testing.T) {
	r := task.NewExecutorRegistry()

	tests := []struct {
		name      string
		operation string
		wantErr   bool
	}{
		{"read", "read", false},
		{"write", "write", false},
		{"list", "list", false},
		{"upload", "upload", false},
		{"download", "download", false},
		{"invalid", "invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := r.Execute(context.Background(), task.Action{
				Type: task.ActionFile,
				Params: map[string]string{
					"operation": tt.operation,
					"path":      "/tmp/test",
				},
			})
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExecutorRegistry_Browser(t *testing.T) {
	r := task.NewExecutorRegistry()

	tests := []struct {
		name    string
		action  string
		wantErr bool
	}{
		{"launch", "launch", false},
		{"navigate", "navigate", false},
		{"click", "click", false},
		{"type", "type", false},
		{"extract", "extract", false},
		{"screenshot", "screenshot", false},
		{"invalid", "invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := r.Execute(context.Background(), task.Action{
				Type: task.ActionBrowser,
				Params: map[string]string{
					"action": tt.action,
				},
			})
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExecutorRegistry_Wait(t *testing.T) {
	r := task.NewExecutorRegistry()

	start := time.Now()
	_, err := r.Execute(context.Background(), task.Action{
		Type: task.ActionWait,
		Params: map[string]string{
			"duration": "10ms",
		},
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed < 10*time.Millisecond {
		t.Errorf("wait too short: %v", elapsed)
	}
}

func TestExecutorRegistry_UnsupportedType(t *testing.T) {
	r := task.NewExecutorRegistry()

	_, err := r.Execute(context.Background(), task.Action{
		Type:   "unsupported",
		Params: map[string]string{},
	})
	if err == nil {
		t.Error("expected error for unsupported action type")
	}
}

func TestExecutorRegistry_ContextCancellation(t *testing.T) {
	r := task.NewExecutorRegistry()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	_, err := r.Execute(ctx, task.Action{
		Type: task.ActionTerminal,
		Params: map[string]string{
			"command": "echo hello",
		},
	})
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestExecutorRegistry_MissingParams(t *testing.T) {
	r := task.NewExecutorRegistry()

	tests := []struct {
		name   string
		action task.Action
	}{
		{
			"terminal without command",
			task.Action{Type: task.ActionTerminal, Params: map[string]string{}},
		},
		{
			"file without operation",
			task.Action{Type: task.ActionFile, Params: map[string]string{}},
		},
		{
			"browser without action",
			task.Action{Type: task.ActionBrowser, Params: map[string]string{}},
		},
		{
			"api without action",
			task.Action{Type: task.ActionAPI, Params: map[string]string{}},
		},
		{
			"conditional without condition",
			task.Action{Type: task.ActionConditional, Params: map[string]string{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := r.Execute(context.Background(), tt.action)
			if err == nil {
				t.Error("expected error for missing required params")
			}
		})
	}
}

// --- TaskRunner Tests ---

func TestTaskRunner_RunSubtask(t *testing.T) {
	runner := task.NewTaskRunner(task.NewExecutorRegistry())

	subtask := &task.Subtask{
		ID:    "test-st-001",
		Name:  "Test Subtask",
		State: task.StatePending,
		Actions: []task.Action{
			{
				ID:   "test-act-001",
				Type: task.ActionTerminal,
				Name: "Run command",
				Params: map[string]string{
					"command": "echo test",
				},
				State: task.StatePending,
			},
		},
	}

	err := runner.RunSubtask(context.Background(), subtask)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if subtask.State != task.StateCompleted {
		t.Errorf("subtask state = %q, want %q", subtask.State, task.StateCompleted)
	}
	if subtask.Actions[0].State != task.StateCompleted {
		t.Errorf("action state = %q, want %q", subtask.Actions[0].State, task.StateCompleted)
	}
}

func TestTaskRunner_RunTask_Sequential(t *testing.T) {
	runner := task.NewTaskRunner(task.NewExecutorRegistry())

	taskObj := &task.Task{
		ID:    "test-task-001",
		Goal:  "Test goal",
		State: task.StatePending,
		Subtasks: []task.Subtask{
			{
				ID:    "st-1",
				Name:  "Step 1",
				State: task.StatePending,
				Actions: []task.Action{
					{ID: "a-1", Type: task.ActionTerminal, Name: "Cmd1", Params: map[string]string{"command": "echo 1"}, State: task.StatePending},
				},
			},
			{
				ID:        "st-2",
				Name:      "Step 2",
				State:     task.StatePending,
				DependsOn: []string{"st-1"},
				Actions: []task.Action{
					{ID: "a-2", Type: task.ActionTerminal, Name: "Cmd2", Params: map[string]string{"command": "echo 2"}, State: task.StatePending},
				},
			},
			{
				ID:        "st-3",
				Name:      "Step 3",
				State:     task.StatePending,
				DependsOn: []string{"st-2"},
				Actions: []task.Action{
					{ID: "a-3", Type: task.ActionTerminal, Name: "Cmd3", Params: map[string]string{"command": "echo 3"}, State: task.StatePending},
				},
			},
		},
	}

	err := runner.RunTask(context.Background(), taskObj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, st := range taskObj.Subtasks {
		if st.State != task.StateCompleted {
			t.Errorf("subtask[%d] state = %q, want %q", i, st.State, task.StateCompleted)
		}
	}
}

func TestTaskRunner_RunTask_ContextCancellation(t *testing.T) {
	runner := task.NewTaskRunner(task.NewExecutorRegistry())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	taskObj := &task.Task{
		ID:    "test-task-002",
		Goal:  "Test goal",
		State: task.StatePending,
		Subtasks: []task.Subtask{
			{
				ID:    "st-1",
				Name:  "Step 1",
				State: task.StatePending,
				Actions: []task.Action{
					{ID: "a-1", Type: task.ActionTerminal, Name: "Cmd1", Params: map[string]string{"command": "echo 1"}, State: task.StatePending},
				},
			},
		},
	}

	err := runner.RunTask(ctx, taskObj)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}
