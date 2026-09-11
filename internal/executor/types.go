package executor

import (
	"time"

	"github.com/atop0914/agentbot/internal/task"
)

// Priority 任务优先级
type Priority int

const (
	PriorityLow    Priority = 0
	PriorityNormal Priority = 5
	PriorityHigh   Priority = 10
	PriorityUrgent Priority = 15
)

// QueueItem 任务队列元素
type QueueItem struct {
	TaskID   string
	Priority Priority
	EnqueuedAt time.Time
}

// TaskFilter 任务查询过滤条件
type TaskFilter struct {
	AgentID string
	State   task.TaskState
}

// EngineConfig 执行引擎配置
type EngineConfig struct {
	// MaxConcurrent 最大并发执行任务数
	MaxConcurrent int `json:"max_concurrent"`
	// PollInterval 轮询间隔（毫秒）
	PollInterval int `json:"poll_interval_ms"`
	// DefaultMaxRetries 默认最大重试次数
	DefaultMaxRetries int `json:"default_max_retries"`
	// TaskTimeout 单个任务超时时间
	TaskTimeout time.Duration `json:"task_timeout"`
}

// DefaultEngineConfig 返回默认引擎配置
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		MaxConcurrent:     10,
		PollInterval:      1000,
		DefaultMaxRetries: 3,
		TaskTimeout:       30 * time.Minute,
	}
}

// TaskEvent 任务事件，用于通知观察者
type TaskEvent struct {
	TaskID    string
	AgentID   string
	OldState  task.TaskState
	NewState  task.TaskState
	Timestamp time.Time
}

// TaskObserver 任务状态变化观察者
type TaskObserver interface {
	OnTaskEvent(event TaskEvent)
}
