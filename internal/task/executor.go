package task

import (
	"context"
	"fmt"
	"time"
)

// ActionExecutor 动作执行器接口
// 每种 ActionType 对应一个具体的执行器实现
type ActionExecutor interface {
	// Execute 执行动作并返回结果
	Execute(ctx context.Context, action Action) (string, error)
	// Type 返回执行器支持的动作类型
	Type() ActionType
}

// ExecutorRegistry 执行器注册表
type ExecutorRegistry struct {
	executors map[ActionType]ActionExecutor
}

// NewExecutorRegistry 创建执行器注册表
func NewExecutorRegistry() *ExecutorRegistry {
	r := &ExecutorRegistry{
		executors: make(map[ActionType]ActionExecutor),
	}
	// 注册内置执行器
	r.Register(&TerminalActionExecutor{})
	r.Register(&FileActionExecutor{})
	r.Register(&BrowserActionExecutor{})
	r.Register(&APIActionExecutor{})
	r.Register(&WaitActionExecutor{})
	r.Register(&ConditionalActionExecutor{})
	return r
}

// Register 注册执行器
func (r *ExecutorRegistry) Register(executor ActionExecutor) {
	r.executors[executor.Type()] = executor
}

// Get 获取指定类型的执行器
func (r *ExecutorRegistry) Get(actionType ActionType) (ActionExecutor, bool) {
	e, ok := r.executors[actionType]
	return e, ok
}

// Execute 执行动作（自动路由到对应执行器）
func (r *ExecutorRegistry) Execute(ctx context.Context, action Action) (string, error) {
	executor, ok := r.executors[action.Type]
	if !ok {
		return "", fmt.Errorf("unsupported action type: %s", action.Type)
	}
	return executor.Execute(ctx, action)
}

// --- 终端命令执行器 ---

// TerminalActionExecutor 终端命令执行器
type TerminalActionExecutor struct{}

func (e *TerminalActionExecutor) Type() ActionType { return ActionTerminal }

func (e *TerminalActionExecutor) Execute(ctx context.Context, action Action) (string, error) {
	command, ok := action.Params["command"]
	if !ok {
		return "", fmt.Errorf("terminal action requires 'command' param")
	}

	// 检查 context 取消
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	// TODO: 集成真实的 shell 执行器
	// 目前返回模拟结果
	return fmt.Sprintf("executed: %s", command), nil
}

// --- 文件操作执行器 ---

// FileActionExecutor 文件操作执行器
type FileActionExecutor struct{}

func (e *FileActionExecutor) Type() ActionType { return ActionFile }

func (e *FileActionExecutor) Execute(ctx context.Context, action Action) (string, error) {
	operation, ok := action.Params["operation"]
	if !ok {
		return "", fmt.Errorf("file action requires 'operation' param")
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	switch operation {
	case "read":
		path := action.Params["path"]
		return fmt.Sprintf("read file: %s", path), nil
	case "write":
		path := action.Params["path"]
		return fmt.Sprintf("wrote file: %s", path), nil
	case "list":
		path := action.Params["path"]
		if path == "" {
			path = "."
		}
		return fmt.Sprintf("listed directory: %s", path), nil
	case "upload":
		return "file uploaded", nil
	case "download":
		return "file downloaded", nil
	default:
		return "", fmt.Errorf("unsupported file operation: %s", operation)
	}
}

// --- 浏览器执行器 ---

// BrowserActionExecutor 浏览器操作执行器
type BrowserActionExecutor struct{}

func (e *BrowserActionExecutor) Type() ActionType { return ActionBrowser }

func (e *BrowserActionExecutor) Execute(ctx context.Context, action Action) (string, error) {
	browserAction, ok := action.Params["action"]
	if !ok {
		return "", fmt.Errorf("browser action requires 'action' param")
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	switch browserAction {
	case "launch":
		return "browser launched", nil
	case "navigate":
		url := action.Params["url"]
		return fmt.Sprintf("navigated to: %s", url), nil
	case "click":
		selector := action.Params["selector"]
		return fmt.Sprintf("clicked: %s", selector), nil
	case "type":
		selector := action.Params["selector"]
		return fmt.Sprintf("typed into: %s", selector), nil
	case "extract":
		return "data extracted from page", nil
	case "screenshot":
		return "screenshot captured", nil
	default:
		return "", fmt.Errorf("unsupported browser action: %s", browserAction)
	}
}

// --- API 执行器 ---

// APIActionExecutor API 调用执行器
type APIActionExecutor struct{}

func (e *APIActionExecutor) Type() ActionType { return ActionAPI }

func (e *APIActionExecutor) Execute(ctx context.Context, action Action) (string, error) {
	apiAction, ok := action.Params["action"]
	if !ok {
		return "", fmt.Errorf("api action requires 'action' param")
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	switch apiAction {
	case "prepare":
		return "request prepared", nil
	case "execute":
		method := action.Params["method"]
		url := action.Params["url"]
		if method == "" {
			method = "GET"
		}
		return fmt.Sprintf("API %s %s completed", method, url), nil
	default:
		return "", fmt.Errorf("unsupported api action: %s", apiAction)
	}
}

// --- 等待执行器 ---

// WaitActionExecutor 等待执行器
type WaitActionExecutor struct{}

func (e *WaitActionExecutor) Type() ActionType { return ActionWait }

func (e *WaitActionExecutor) Execute(ctx context.Context, action Action) (string, error) {
	durationStr, ok := action.Params["duration"]
	if !ok {
		durationStr = "1s"
	}

	duration, err := time.ParseDuration(durationStr)
	if err != nil {
		return "", fmt.Errorf("invalid wait duration: %s", durationStr)
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(duration):
		return fmt.Sprintf("waited %s", duration), nil
	}
}

// --- 条件执行器 ---

// ConditionalActionExecutor 条件执行器
type ConditionalActionExecutor struct{}

func (e *ConditionalActionExecutor) Type() ActionType { return ActionConditional }

func (e *ConditionalActionExecutor) Execute(ctx context.Context, action Action) (string, error) {
	condition, ok := action.Params["condition"]
	if !ok {
		return "", fmt.Errorf("conditional action requires 'condition' param")
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	// TODO: 集成真实的条件评估引擎
	// 目前简单返回条件文本
	return fmt.Sprintf("condition evaluated: %s", condition), nil
}

// TaskRunner 任务执行协调器
// 负责按依赖顺序执行子任务中的动作
type TaskRunner struct {
	registry *ExecutorRegistry
}

// NewTaskRunner 创建任务执行器
func NewTaskRunner(registry *ExecutorRegistry) *TaskRunner {
	return &TaskRunner{registry: registry}
}

// RunSubtask 执行单个子任务的所有动作
func (r *TaskRunner) RunSubtask(ctx context.Context, subtask *Subtask) error {
	subtask.State = StateInProgress

	for i := range subtask.Actions {
		select {
		case <-ctx.Done():
			subtask.State = StateFailed
			subtask.Error = "context cancelled"
			return ctx.Err()
		default:
		}

		subtask.Actions[i].State = StateInProgress
		result, err := r.registry.Execute(ctx, subtask.Actions[i])
		if err != nil {
			subtask.Actions[i].State = StateFailed
			subtask.Actions[i].Error = err.Error()
			subtask.State = StateFailed
			subtask.Error = err.Error()
			return err
		}

		subtask.Actions[i].State = StateCompleted
		subtask.Actions[i].Result = result
		subtask.Actions[i].CompletedAt = time.Now().UTC()
	}

	subtask.State = StateCompleted
	subtask.Result = "all actions completed"
	subtask.CompletedAt = time.Now().UTC()
	return nil
}

// RunTask 按依赖顺序执行任务的所有子任务
func (r *TaskRunner) RunTask(ctx context.Context, t *Task) error {
	completed := make(map[string]bool)

	for {
		// 检查 context 取消
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		progressed := false
		for i := range t.Subtasks {
			st := &t.Subtasks[i]

			// 跳过已完成或失败的子任务
			if st.State == StateCompleted || st.State == StateFailed {
				completed[st.ID] = (st.State == StateCompleted)
				continue
			}

			// 检查依赖是否满足
			depsReady := true
			for _, depID := range st.DependsOn {
				if !completed[depID] {
					depsReady = false
					break
				}
			}

			if !depsReady {
				continue
			}

			// 执行子任务
			if err := r.RunSubtask(ctx, st); err != nil {
				st.State = StateFailed
				st.Error = err.Error()
				completed[st.ID] = false
				progressed = true
				continue
			}

			completed[st.ID] = true
			progressed = true
		}

		// 检查是否所有子任务都已完成
		allDone := true
		for _, st := range t.Subtasks {
			if st.State != StateCompleted && st.State != StateFailed {
				allDone = false
				break
			}
		}

		if allDone {
			break
		}

		// 如果没有进展，说明存在循环依赖
		if !progressed {
			return fmt.Errorf("deadlock detected: circular dependency in subtasks")
		}
	}

	return nil
}
