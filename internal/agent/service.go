package agent

import (
	"context"
	"fmt"

	"github.com/atop0914/agentbot/internal/pkg/errors"
)

// agentService 实现 Service 接口
type agentService struct {
	repo Repository
}

// NewService 创建 Agent 服务
func NewService(repo Repository) Service {
	return &agentService{repo: repo}
}

// Create 创建新 Agent
func (s *agentService) Create(ctx context.Context, req CreateRequest) (*Agent, error) {
	if req.Name == "" {
		return nil, errors.NewAppError(errors.ErrBadRequest, 400, "name is required", "")
	}

	agent := NewAgent(req)

	// 应用默认配置
	applyDefaults(&agent.Config)

	if err := s.repo.Create(ctx, agent); err != nil {
		return nil, errors.NewAppError(errors.ErrInternal, 500, "failed to create agent", err.Error())
	}

	return agent, nil
}

// Get 获取 Agent
func (s *agentService) Get(ctx context.Context, id string) (*Agent, error) {
	if id == "" {
		return nil, errors.NewAppError(errors.ErrBadRequest, 400, "agent id is required", "")
	}

	agent, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, errors.NewAppError(errors.ErrNotFound, 404, "agent not found", err.Error())
	}

	return agent, nil
}

// Update 更新 Agent 配置
func (s *agentService) Update(ctx context.Context, id string, req UpdateRequest) (*Agent, error) {
	agent, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, errors.NewAppError(errors.ErrNotFound, 404, "agent not found", err.Error())
	}

	// 只有非运行状态的 Agent 才能修改配置
	if agent.State == StateRunning {
		return nil, errors.NewAppError(errors.ErrBadRequest, 400, "cannot update running agent", "stop the agent first")
	}

	agent.ApplyUpdate(req)

	if err := s.repo.Update(ctx, agent); err != nil {
		return nil, errors.NewAppError(errors.ErrInternal, 500, "failed to update agent", err.Error())
	}

	return agent, nil
}

// Delete 删除 Agent
func (s *agentService) Delete(ctx context.Context, id string) error {
	agent, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return errors.NewAppError(errors.ErrNotFound, 404, "agent not found", err.Error())
	}

	// 运行中的 Agent 不能删除
	if agent.State == StateRunning {
		return errors.NewAppError(errors.ErrBadRequest, 400, "cannot delete running agent", "stop the agent first")
	}

	return s.repo.Delete(ctx, id)
}

// List 列出所有 Agent
func (s *agentService) List(ctx context.Context, offset, limit int) ([]*Agent, error) {
	if limit <= 0 {
		limit = 20 // 默认分页大小
	}
	if limit > 100 {
		limit = 100 // 最大分页大小
	}
	if offset < 0 {
		offset = 0
	}

	return s.repo.List(ctx, offset, limit)
}

// Start 启动 Agent
func (s *agentService) Start(ctx context.Context, id string) error {
	agent, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return errors.NewAppError(errors.ErrNotFound, 404, "agent not found", err.Error())
	}

	if err := agent.SetState(StateRunning); err != nil {
		return errors.NewAppError(errors.ErrBadRequest, 400, "cannot start agent", err.Error())
	}

	return s.repo.Update(ctx, agent)
}

// Stop 停止 Agent（回到 idle 状态）
func (s *agentService) Stop(ctx context.Context, id string) error {
	agent, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return errors.NewAppError(errors.ErrNotFound, 404, "agent not found", err.Error())
	}

	// 所有非终态都可以直接 stop 到 idle
	if agent.State == StateTerminated {
		return errors.NewAppError(errors.ErrBadRequest, 400, "agent is already terminated", "")
	}

	if err := agent.SetState(StateIdle); err != nil {
		return errors.NewAppError(errors.ErrBadRequest, 400, "cannot stop agent", err.Error())
	}

	return s.repo.Update(ctx, agent)
}

// Pause 暂停 Agent
func (s *agentService) Pause(ctx context.Context, id string) error {
	agent, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return errors.NewAppError(errors.ErrNotFound, 404, "agent not found", err.Error())
	}

	if err := agent.SetState(StatePaused); err != nil {
		return errors.NewAppError(errors.ErrBadRequest, 400, "cannot pause agent", err.Error())
	}

	return s.repo.Update(ctx, agent)
}

// Resume 恢复 Agent
func (s *agentService) Resume(ctx context.Context, id string) error {
	agent, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return errors.NewAppError(errors.ErrNotFound, 404, "agent not found", err.Error())
	}

	if err := agent.SetState(StateRunning); err != nil {
		return errors.NewAppError(errors.ErrBadRequest, 400, "cannot resume agent", err.Error())
	}

	return s.repo.Update(ctx, agent)
}

// SendMessage 向 Agent 发送消息
func (s *agentService) SendMessage(ctx context.Context, id string, msg Message) error {
	agent, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return errors.NewAppError(errors.ErrNotFound, 404, "agent not found", err.Error())
	}

	if !agent.IsRunning() {
		return errors.NewAppError(errors.ErrBadRequest, 400, "agent is not running", "agent must be in running state to receive messages")
	}

	// TODO: 将消息推送到 Agent 的消息队列
	// 当前仅验证 Agent 状态，后续实现消息队列时扩展
	_ = fmt.Sprintf("message sent to agent %s: %s", agent.ID, msg.Content)
	return nil
}

// applyDefaults 为未设置的配置项应用默认值
func applyDefaults(cfg *Config) {
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 4096
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = 0.7
	}
	if cfg.Resources.CPU == "" {
		cfg.Resources.CPU = "1"
	}
	if cfg.Resources.Memory == "" {
		cfg.Resources.Memory = "512Mi"
	}
	if cfg.Resources.Disk == "" {
		cfg.Resources.Disk = "1Gi"
	}
	if cfg.Memory.ShortTermSize == 0 {
		cfg.Memory.ShortTermSize = 100
	}
	if cfg.Memory.LongTermSize == 0 {
		cfg.Memory.LongTermSize = 1000
	}
}
