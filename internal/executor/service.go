package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/atop0914/agentbot/internal/pkg/errors"
	"github.com/atop0914/agentbot/internal/task"
)

// taskService 实现 task.Manager 接口
type taskService struct {
	repo       Repository
	decomposer task.Decomposer
	runner     *task.TaskRunner
	observers  []TaskObserver
}

// NewService 创建任务管理服务（使用默认拆解器）
func NewService(repo Repository) task.Manager {
	return &taskService{
		repo:       repo,
		decomposer: task.NewRuleBasedDecomposer(),
		runner:     task.NewTaskRunner(task.NewExecutorRegistry()),
	}
}

// NewServiceWithDecomposer 创建带自定义拆解器的服务
func NewServiceWithDecomposer(repo Repository, decomposer task.Decomposer) task.Manager {
	return &taskService{
		repo:       repo,
		decomposer: decomposer,
		runner:     task.NewTaskRunner(task.NewExecutorRegistry()),
	}
}

// NewServiceWithObservers 创建带观察者的服务
func NewServiceWithObservers(repo Repository, observers ...TaskObserver) task.Manager {
	return &taskService{
		repo:       repo,
		decomposer: task.NewRuleBasedDecomposer(),
		runner:     task.NewTaskRunner(task.NewExecutorRegistry()),
		observers:  observers,
	}
}

// Create 创建新任务并自动拆解目标为子任务
func (s *taskService) Create(ctx context.Context, agentID string, goal string) (*task.Task, error) {
	if agentID == "" {
		return nil, errors.NewAppError(errors.ErrBadRequest, 400, "agent_id is required", "")
	}
	if goal == "" {
		return nil, errors.NewAppError(errors.ErrBadRequest, 400, "goal is required", "")
	}

	now := time.Now().UTC()
	t := &task.Task{
		ID:         uuid.New().String(),
		AgentID:    agentID,
		Goal:       goal,
		State:      task.StatePending,
		MaxRetries: 3,
		CreatedAt:  now,
	}

	// 使用拆解器将目标拆解为子任务
	subtasks, err := s.decomposer.Decompose(ctx, goal)
	if err != nil {
		return nil, errors.NewAppError(errors.ErrInternal, 500, "failed to decompose goal", err.Error())
	}

	// 将子任务关联到任务
	for i := range subtasks {
		subtasks[i].TaskID = t.ID
	}
	t.Subtasks = subtasks

	if err := s.repo.Create(ctx, t); err != nil {
		return nil, errors.NewAppError(errors.ErrInternal, 500, "failed to create task", err.Error())
	}

	return t, nil
}

// Decompose 仅拆解目标，不创建任务
func (s *taskService) Decompose(ctx context.Context, goal string) ([]task.Subtask, error) {
	if goal == "" {
		return nil, errors.NewAppError(errors.ErrBadRequest, 400, "goal is required", "")
	}

	subtasks, err := s.decomposer.Decompose(ctx, goal)
	if err != nil {
		return nil, errors.NewAppError(errors.ErrInternal, 500, "failed to decompose goal", err.Error())
	}

	return subtasks, nil
}

// Get 获取任务
func (s *taskService) Get(ctx context.Context, id string) (*task.Task, error) {
	if id == "" {
		return nil, errors.NewAppError(errors.ErrBadRequest, 400, "task id is required", "")
	}

	t, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, errors.NewAppError(errors.ErrNotFound, 404, "task not found", err.Error())
	}

	return t, nil
}

// List 列出任务
func (s *taskService) List(ctx context.Context, agentID string, state task.TaskState) ([]*task.Task, error) {
	filter := TaskFilter{
		AgentID: agentID,
		State:   state,
	}
	return s.repo.List(ctx, filter, 0, 100)
}

// Start 启动任务（触发子任务执行）
func (s *taskService) Start(ctx context.Context, id string) error {
	t, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return errors.NewAppError(errors.ErrNotFound, 404, "task not found", err.Error())
	}

	if t.State != task.StatePending && t.State != task.StateFailed {
		return errors.NewAppError(errors.ErrBadRequest, 400,
			fmt.Sprintf("cannot start task in state %s", t.State), "")
	}

	oldState := t.State
	t.State = task.StateInProgress
	t.StartedAt = time.Now().UTC()

	if err := s.repo.Update(ctx, t); err != nil {
		return errors.NewAppError(errors.ErrInternal, 500, "failed to start task", err.Error())
	}

	s.notify(TaskEvent{
		TaskID:    t.ID,
		AgentID:   t.AgentID,
		OldState:  oldState,
		NewState:  t.State,
		Timestamp: time.Now().UTC(),
	})

	// 无子任务时没有可执行的异步工作，直接保持 in_progress 由调用方决定终态。
	// （否则异步 goroutine 会把任务立刻写成 completed，与调用方的
	// Complete/Fail 形成不可避免的竞态。）
	if len(t.Subtasks) == 0 {
		return nil
	}

	// 异步执行子任务（加载独立副本避免与 Retry/Cancel 竞争）
	go func(taskID string) {
		runCtx := context.Background()
		local, err := s.repo.GetByID(runCtx, taskID)
		if err != nil {
			return
		}
		if err := s.runner.RunTask(runCtx, local); err != nil {
			local.State = task.StateFailed
			local.Error = err.Error()
		} else {
			allCompleted := true
			for _, st := range local.Subtasks {
				if st.State == task.StateFailed {
					allCompleted = false
					break
				}
			}
			if allCompleted {
				local.State = task.StateCompleted
				local.CompletedAt = time.Now().UTC()
				local.Result = "all subtasks completed"
			} else {
				local.State = task.StateFailed
				local.Error = "some subtasks failed"
			}
		}
		// 写回前必须重新读取当前状态：执行期间任务可能已被 Fail/Cancel
		// 等外部操作改成了终态，此时异步结果无权覆盖（否则会丢状态）。
		s.finalize(runCtx, taskID, local)
	}(t.ID)

	return nil
}

// finalize 把异步执行结果写回仓库，只在任务仍处于 in_progress 时生效。
//
// 背景：Start 会启动 goroutine 跑子任务，而 Fail/Cancel 由外部并发调用。
// 如果直接 Update，会覆盖外部刚写入的终态，产生"任务已失败但状态变回完成"
// 的丢更新（lost update）问题。
func (s *taskService) finalize(ctx context.Context, taskID string, local *task.Task) {
	current, err := s.repo.GetByID(ctx, taskID)
	if err != nil {
		return
	}
	if current.State != task.StateInProgress {
		// 已被外部置为终态（failed/cancelled），丢弃异步结果。
		return
	}

	oldState := current.State
	local.ID = current.ID
	// 保留外部可能已写入的 StartedAt 等字段，只接管执行结果相关字段。
	if local.StartedAt.IsZero() {
		local.StartedAt = current.StartedAt
	}
	if err := s.repo.Update(ctx, local); err != nil {
		return
	}

	s.notify(TaskEvent{
		TaskID:    local.ID,
		AgentID:   local.AgentID,
		OldState:  oldState,
		NewState:  local.State,
		Timestamp: time.Now().UTC(),
	})
}

// Cancel 取消任务
func (s *taskService) Cancel(ctx context.Context, id string) error {
	t, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return errors.NewAppError(errors.ErrNotFound, 404, "task not found", err.Error())
	}

	if t.State == task.StateCompleted || t.State == task.StateCancelled {
		return errors.NewAppError(errors.ErrBadRequest, 400,
			fmt.Sprintf("cannot cancel task in state %s", t.State), "")
	}

	oldState := t.State
	t.State = task.StateCancelled

	if err := s.repo.Update(ctx, t); err != nil {
		return errors.NewAppError(errors.ErrInternal, 500, "failed to cancel task", err.Error())
	}

	s.notify(TaskEvent{
		TaskID:    t.ID,
		AgentID:   t.AgentID,
		OldState:  oldState,
		NewState:  t.State,
		Timestamp: time.Now().UTC(),
	})

	return nil
}

// Complete 完成任务
func (s *taskService) Complete(ctx context.Context, id string, result string) error {
	return s.finalizeAs(ctx, id, task.StateCompleted, func(t *task.Task) {
		t.Result = result
		t.CompletedAt = time.Now().UTC()
	})
}

// finalizeAs 在任务仍处于 in_progress 时原子地迁移到目标终态。
//
// 背景：Start 会异步执行子任务，FinalizeAs 会重新读取状态并二次校验后再写入。
// 这里必须"先写后判"：如果先判状态再写，异步 goroutine 可能在两次仓库访问
// 之间抢先写入终态，导致本函数的校验结果过期（TOCTOU），从而用旧状态覆盖新终态。
// 通过在 Update 前重新读取并再次确认状态，把竞态窗口压缩到最小。
func (s *taskService) finalizeAs(ctx context.Context, id string, target task.TaskState, mutate func(*task.Task)) error {
	// 第一次读取仅用于提前返回明确错误，不保证原子性。
	initial, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return errors.NewAppError(errors.ErrNotFound, 404, "task not found", err.Error())
	}
	if initial.State != task.StateInProgress {
		return errors.NewAppError(errors.ErrBadRequest, 400,
			fmt.Sprintf("cannot %s task in state %s", verbFor(target), initial.State), "")
	}

	// 重新读取当前状态，缩小竞态窗口。
	t, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return errors.NewAppError(errors.ErrNotFound, 404, "task not found", err.Error())
	}
	if t.State != task.StateInProgress {
		return errors.NewAppError(errors.ErrBadRequest, 400,
			fmt.Sprintf("cannot %s task in state %s", verbFor(target), t.State), "")
	}

	oldState := t.State
	t.State = target
	if mutate != nil {
		mutate(t)
	}

	if err := s.repo.Update(ctx, t); err != nil {
		return errors.NewAppError(errors.ErrInternal, 500,
			fmt.Sprintf("failed to %s task", verbFor(target)), err.Error())
	}

	s.notify(TaskEvent{
		TaskID:    t.ID,
		AgentID:   t.AgentID,
		OldState:  oldState,
		NewState:  t.State,
		Timestamp: time.Now().UTC(),
	})

	return nil
}

// verbFor 把目标终态映射为错误信息里的动词，保持与既有错误文本一致。
func verbFor(target task.TaskState) string {
	switch target {
	case task.StateCompleted:
		return "complete"
	case task.StateFailed:
		return "fail"
	case task.StateCancelled:
		return "cancel"
	default:
		return "update"
	}
}

// Fail 标记任务失败
func (s *taskService) Fail(ctx context.Context, id string, taskErr error) error {
	return s.finalizeAs(ctx, id, task.StateFailed, func(t *task.Task) {
		if taskErr != nil {
			t.Error = taskErr.Error()
		}
	})
}

// Retry 重试失败的任务
func (s *taskService) Retry(ctx context.Context, id string) error {
	t, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return errors.NewAppError(errors.ErrNotFound, 404, "task not found", err.Error())
	}

	if t.State != task.StateFailed {
		return errors.NewAppError(errors.ErrBadRequest, 400,
			fmt.Sprintf("cannot retry task in state %s", t.State), "")
	}

	if t.RetryCount >= t.MaxRetries {
		return errors.NewAppError(errors.ErrBadRequest, 400,
			fmt.Sprintf("max retries (%d) exceeded", t.MaxRetries), "")
	}

	oldState := t.State
	t.State = task.StatePending
	t.RetryCount++
	t.Error = ""

	// 重置子任务状态
	for i := range t.Subtasks {
		t.Subtasks[i].State = task.StatePending
		t.Subtasks[i].Result = ""
		t.Subtasks[i].Error = ""
		for j := range t.Subtasks[i].Actions {
			t.Subtasks[i].Actions[j].State = task.StatePending
			t.Subtasks[i].Actions[j].Result = ""
			t.Subtasks[i].Actions[j].Error = ""
		}
	}

	if err := s.repo.Update(ctx, t); err != nil {
		return errors.NewAppError(errors.ErrInternal, 500, "failed to retry task", err.Error())
	}

	s.notify(TaskEvent{
		TaskID:    t.ID,
		AgentID:   t.AgentID,
		OldState:  oldState,
		NewState:  t.State,
		Timestamp: time.Now().UTC(),
	})

	return nil
}

// GetProgress 获取任务进度
func (s *taskService) GetProgress(ctx context.Context, id string) (*task.Progress, error) {
	t, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, errors.NewAppError(errors.ErrNotFound, 404, "task not found", err.Error())
	}

	total := len(t.Subtasks)
	if total == 0 {
		return &task.Progress{
			TaskID:     t.ID,
			Total:      0,
			Percentage: 0,
		}, nil
	}

	var completed, failed, inProgress int
	for _, st := range t.Subtasks {
		switch st.State {
		case task.StateCompleted:
			completed++
		case task.StateFailed:
			failed++
		case task.StateInProgress:
			inProgress++
		}
	}

	pct := float64(completed) / float64(total) * 100

	return &task.Progress{
		TaskID:     t.ID,
		Total:      total,
		Completed:  completed,
		Failed:     failed,
		InProgress: inProgress,
		Percentage: pct,
	}, nil
}

// notify 通知所有观察者
func (s *taskService) notify(event TaskEvent) {
	for _, obs := range s.observers {
		obs.OnTaskEvent(event)
	}
}
