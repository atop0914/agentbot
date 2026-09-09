package agent

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// StateTransition 定义状态转换规则
// key 是当前状态，value 是允许转换到的目标状态列表
var stateTransitions = map[State][]State{
	StateIdle:      {StateRunning, StateTerminated},
	StateRunning:   {StatePaused, StateError, StateCompleted, StateTerminated},
	StatePaused:    {StateRunning, StateTerminated},
	StateError:     {StateIdle, StateRunning, StateTerminated},
	StateCompleted: {StateIdle, StateTerminated},
	StateTerminated: {},
}

// CanTransition 检查是否可以从当前状态转换到目标状态
func CanTransition(from, to State) bool {
	targets, ok := stateTransitions[from]
	if !ok {
		return false
	}
	for _, t := range targets {
		if t == to {
			return true
		}
	}
	return false
}

// ValidateTransition 验证状态转换，如果不合法返回错误
func ValidateTransition(from, to State) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("invalid state transition: %s -> %s", from, to)
	}
	return nil
}

// NewAgent 创建一个新的 Agent 实例
func NewAgent(req CreateRequest) *Agent {
	now := time.Now().UTC()
	return &Agent{
		ID:          uuid.New().String(),
		Name:        req.Name,
		Description: req.Description,
		State:       StateIdle,
		Config:      req.Config,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// SetState 更改 Agent 状态，自动验证转换合法性并更新时间戳
func (a *Agent) SetState(to State) error {
	if err := ValidateTransition(a.State, to); err != nil {
		return err
	}
	a.State = to
	a.UpdatedAt = time.Now().UTC()
	return nil
}

// IsRunning 检查 Agent 是否正在运行
func (a *Agent) IsRunning() bool {
	return a.State == StateRunning
}

// IsTerminal 检查 Agent 是否处于终态
func (a *Agent) IsTerminal() bool {
	return a.State == StateTerminated || a.State == StateCompleted
}

// IsAvailable 检查 Agent 是否可以接受新任务
func (a *Agent) IsAvailable() bool {
	return a.State == StateIdle
}

// ApplyUpdate 应用更新请求到 Agent
func (a *Agent) ApplyUpdate(req UpdateRequest) {
	if req.Name != nil {
		a.Name = *req.Name
	}
	if req.Description != nil {
		a.Description = *req.Description
	}
	if req.Config != nil {
		a.Config = *req.Config
	}
	a.UpdatedAt = time.Now().UTC()
}

// ToResponse 将 Agent 转换为 API 响应格式
func (a *Agent) ToResponse() AgentResponse {
	return AgentResponse{
		ID:          a.ID,
		Name:        a.Name,
		Description: a.Description,
		State:       a.State,
		ContainerID: a.ContainerID,
		Config:      a.Config,
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
	}
}

// AgentResponse 定义 API 响应中的 Agent 格式
type AgentResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	State       State     `json:"state"`
	ContainerID string    `json:"container_id,omitempty"`
	Config      Config    `json:"config"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ListResponse 分页列表响应
type ListResponse struct {
	Agents []*AgentResponse `json:"agents"`
	Total  int              `json:"total"`
	Offset int              `json:"offset"`
	Limit  int              `json:"limit"`
}
