package executor

import (
	"container/heap"
	"context"
	"sync"
	"time"

	"github.com/atop0914/agentbot/internal/task"
)

// ExecutionEngine 任务执行引擎
type ExecutionEngine struct {
	mu       sync.RWMutex
	config   EngineConfig
	repo     Repository
	queue    *taskQueue
	agents   map[string]*agentSlot // agentID -> slot
	running  map[string]context.CancelFunc
	stopCh   chan struct{}
	stopped  bool
}

// agentSlot Agent 执行槽位
type agentSlot struct {
	AgentID    string
	CurrentTask string
	Busy       bool
}

// NewEngine 创建执行引擎
func NewEngine(repo Repository, config EngineConfig) *ExecutionEngine {
	q := &taskQueue{}
	heap.Init(q)

	return &ExecutionEngine{
		config:  config,
		repo:    repo,
		queue:   q,
		agents:  make(map[string]*agentSlot),
		running: make(map[string]context.CancelFunc),
		stopCh:  make(chan struct{}),
	}
}

// Submit 提交任务到队列
func (e *ExecutionEngine) Submit(ctx context.Context, taskID string, priority Priority) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	item := &QueueItem{
		TaskID:     taskID,
		Priority:   priority,
		EnqueuedAt: time.Now().UTC(),
	}
	heap.Push(e.queue, item)
	return nil
}

// RegisterAgent 注册 Agent 到引擎
func (e *ExecutionEngine) RegisterAgent(agentID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.agents[agentID]; !exists {
		e.agents[agentID] = &agentSlot{
			AgentID: agentID,
		}
	}
}

// UnregisterAgent 从引擎注销 Agent
func (e *ExecutionEngine) UnregisterAgent(agentID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.agents, agentID)
}

// Start 启动执行引擎
func (e *ExecutionEngine) Start(ctx context.Context) error {
	go e.runLoop(ctx)
	return nil
}

// Stop 停止执行引擎
func (e *ExecutionEngine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.stopped {
		return
	}
	e.stopped = true
	close(e.stopCh)

	// 取消所有正在运行的任务
	for _, cancel := range e.running {
		cancel()
	}
}

// QueueLen 返回队列长度
func (e *ExecutionEngine) QueueLen() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.queue.Len()
}

// RunningCount 返回正在运行的任务数
func (e *ExecutionEngine) RunningCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.running)
}

// IsAgentAvailable 检查 Agent 是否空闲
func (e *ExecutionEngine) IsAgentAvailable(agentID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	slot, ok := e.agents[agentID]
	if !ok {
		return false
	}
	return !slot.Busy
}

// runLoop 主调度循环
func (e *ExecutionEngine) runLoop(ctx context.Context) {
	interval := time.Duration(e.config.PollInterval) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.dispatch(ctx)
		}
	}
}

// dispatch 从队列取出任务并分配给空闲 Agent
func (e *ExecutionEngine) dispatch(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.queue.Len() == 0 {
		return
	}

	// 找到空闲 Agent
	var availableAgent *agentSlot
	for _, slot := range e.agents {
		if !slot.Busy {
			availableAgent = slot
			break
		}
	}

	if availableAgent == nil {
		return
	}

	// 检查并发限制
	if len(e.running) >= e.config.MaxConcurrent {
		return
	}

	// 取出最高优先级任务
	item := heap.Pop(e.queue).(*QueueItem)

	// 加载任务详情
	t, err := e.repo.GetByID(ctx, item.TaskID)
	if err != nil || t.State != task.StatePending {
		// 任务不存在或已不在 pending 状态，跳过
		return
	}

	// 分配任务给 Agent
	availableAgent.Busy = true
	availableAgent.CurrentTask = item.TaskID

	// 更新任务状态
	t.State = task.StateInProgress
	t.StartedAt = time.Now().UTC()
	e.repo.Update(ctx, t)

	// 启动异步执行
	taskCtx, cancel := context.WithTimeout(ctx, e.config.TaskTimeout)
	e.running[item.TaskID] = cancel

	go e.executeTask(taskCtx, availableAgent, t, cancel)
}

// executeTask 执行单个任务
func (e *ExecutionEngine) executeTask(ctx context.Context, slot *agentSlot, t *task.Task, cancel context.CancelFunc) {
	defer func() {
		e.mu.Lock()
		delete(e.running, t.ID)
		slot.Busy = false
		slot.CurrentTask = ""
		cancel()
		e.mu.Unlock()
	}()

	// 按顺序执行子任务
	allCompleted := true
	for i := range t.Subtasks {
		// 检查上下文取消
		select {
		case <-ctx.Done():
			t.State = task.StateFailed
			t.Error = "task timeout or cancelled"
			e.repo.Update(ctx, t)
			return
		default:
		}

		// 检查子任务依赖
		if !dependenciesMet(t, i) {
			continue
		}

		// 执行子任务
		t.Subtasks[i].State = task.StateInProgress
		e.repo.Update(ctx, t)

		// 模拟执行（实际执行器后续集成）
		// TODO: 集成真实的 Action 执行器
		t.Subtasks[i].State = task.StateCompleted
		t.Subtasks[i].CompletedAt = time.Now().UTC()

		if t.Subtasks[i].State == task.StateFailed {
			allCompleted = false
			break
		}
	}

	// 更新任务最终状态
	if allCompleted {
		t.State = task.StateCompleted
		t.CompletedAt = time.Now().UTC()
	} else {
		t.State = task.StateFailed
	}

	e.repo.Update(ctx, t)
}

// dependenciesMet 检查子任务的依赖是否已满足
func dependenciesMet(t *task.Task, index int) bool {
	st := t.Subtasks[index]
	if len(st.DependsOn) == 0 {
		return true
	}

	depSet := make(map[string]bool, len(st.DependsOn))
	for _, depID := range st.DependsOn {
		depSet[depID] = true
	}

	for _, other := range t.Subtasks {
		if depSet[other.ID] {
			if other.State != task.StateCompleted {
				return false
			}
		}
	}

	return true
}

// taskQueue 优先级队列实现
type taskQueue []*QueueItem

func (q taskQueue) Len() int { return len(q) }

func (q taskQueue) Less(i, j int) bool {
	// 优先级高的排前面
	if q[i].Priority != q[j].Priority {
		return q[i].Priority > q[j].Priority
	}
	// 同优先级按先进先出
	return q[i].EnqueuedAt.Before(q[j].EnqueuedAt)
}

func (q taskQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
}

func (q *taskQueue) Push(x interface{}) {
	*q = append(*q, x.(*QueueItem))
}

func (q *taskQueue) Pop() interface{} {
	old := *q
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // 避免内存泄漏
	*q = old[:n-1]
	return item
}
