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

	// 异步执行子任务
	go func() {
		runCtx := context.Background()
		if err := s.runner.RunTask(runCtx, t); err != nil {
			t.State = task.StateFailed
			t.Error = err.Error()
		} else {
			// 检查是否有失败的子任务
			allCompleted := true
			for _, st := range t.Subtasks {
				if st.State == task.StateFailed {
					allCompleted = false
					break
				}
			}
			if allCompleted {
				t.State = task.StateCompleted
				t.CompletedAt = time.Now().UTC()
				t.Result = "all subtasks completed"
			} else {
				t.State = task.StateFailed
				t.Error = "some subtasks failed"
			}
		}
		s.repo.Update(runCtx, t)
	}()

	return nil
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
	t, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return errors.NewAppError(errors.ErrNotFound, 404, "task not found", err.Error())
	}

	if t.State != task.StateInProgress {
		return errors.NewAppError(errors.ErrBadRequest, 400,
			fmt.Sprintf("cannot complete task in state %s", t.State), "")
	}

	oldState := t.State
	t.State = task.StateCompleted
	t.Result = result
	t.CompletedAt = time.Now().UTC()

	if err := s.repo.Update(ctx, t); err != nil {
		return errors.NewAppError(errors.ErrInternal, 500, "failed to complete task", err.Error())
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

// Fail 标记任务失败
func (s *taskService) Fail(ctx context.Context, id string, taskErr error) error {
	t, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return errors.NewAppError(errors.ErrNotFound, 404, "task not found", err.Error())
	}

	if t.State != task.StateInProgress {
		return errors.NewAppError(errors.ErrBadRequest, 400,
			fmt.Sprintf("cannot fail task in state %s", t.State), "")
	}

	oldState := t.State
	t.State = task.StateFailed
	if taskErr != nil {
		t.Error = taskErr.Error()
	}

	if err := s.repo.Update(ctx, t); err != nil {
		return errors.NewAppError(errors.ErrInternal, 500, "failed to mark task as failed", err.Error())
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
