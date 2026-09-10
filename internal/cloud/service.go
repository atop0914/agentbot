package cloud

import (
	"context"
	"fmt"
	"time"
)

// Service 云环境管理服务
type Service struct {
	manager Manager
	repo    Repository
}

// NewService 创建云环境服务
func NewService(manager Manager, repo Repository) *Service {
	return &Service{
		manager: manager,
		repo:    repo,
	}
}

// CreateEnvironment 创建云环境
func (s *Service) CreateEnvironment(ctx context.Context, agentID string, req CreateEnvRequest) (*Environment, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}

	// 应用默认配置
	config := EnvironmentConfig{
		Type:  req.Type,
		Image: req.Image,
		Resources: Resources{
			CPU:    req.Resources.CPU,
			Memory: req.Resources.Memory,
			Disk:   req.Resources.Disk,
		},
		Network: req.Network,
		Env:     req.Env,
		Volumes: req.Volumes,
	}

	applyEnvDefaults(&config)

	env, err := s.manager.Create(ctx, agentID, config)
	if err != nil {
		return nil, fmt.Errorf("create environment: %w", err)
	}

	// 持久化环境记录
	if err := s.repo.Create(ctx, env); err != nil {
		// 创建成功但持久化失败，尝试清理
		s.manager.Destroy(ctx, env.ID)
		return nil, fmt.Errorf("persist environment: %w", err)
	}

	return env, nil
}

// DestroyEnvironment 销毁云环境
func (s *Service) DestroyEnvironment(ctx context.Context, envID string) error {
	env, err := s.repo.GetByID(ctx, envID)
	if err != nil {
		return fmt.Errorf("environment not found: %w", err)
	}

	// 运行中的环境需要先停止
	if env.State == EnvStateRunning || env.State == EnvStatePaused {
		if err := s.manager.Stop(ctx, envID); err != nil {
			return fmt.Errorf("stop environment: %w", err)
		}
	}

	if err := s.manager.Destroy(ctx, envID); err != nil {
		return fmt.Errorf("destroy environment: %w", err)
	}

	return s.repo.Delete(ctx, envID)
}

// GetEnvironment 获取环境详情
func (s *Service) GetEnvironment(ctx context.Context, envID string) (*Environment, error) {
	env, err := s.manager.Get(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("get environment: %w", err)
	}
	return env, nil
}

// ListEnvironments 列出 Agent 的所有环境
func (s *Service) ListEnvironments(ctx context.Context, agentID string) ([]*Environment, error) {
	envs, err := s.manager.List(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	return envs, nil
}

// StartEnvironment 启动环境
func (s *Service) StartEnvironment(ctx context.Context, envID string) error {
	if err := s.manager.Start(ctx, envID); err != nil {
		return fmt.Errorf("start environment: %w", err)
	}

	env, _ := s.manager.Get(ctx, envID)
	if env != nil {
		s.repo.Update(ctx, env)
	}

	return nil
}

// StopEnvironment 停止环境
func (s *Service) StopEnvironment(ctx context.Context, envID string) error {
	if err := s.manager.Stop(ctx, envID); err != nil {
		return fmt.Errorf("stop environment: %w", err)
	}

	env, _ := s.manager.Get(ctx, envID)
	if env != nil {
		s.repo.Update(ctx, env)
	}

	return nil
}

// PauseEnvironment 暂停环境
func (s *Service) PauseEnvironment(ctx context.Context, envID string) error {
	if err := s.manager.Pause(ctx, envID); err != nil {
		return fmt.Errorf("pause environment: %w", err)
	}

	env, _ := s.manager.Get(ctx, envID)
	if env != nil {
		s.repo.Update(ctx, env)
	}

	return nil
}

// ResumeEnvironment 恢复环境
func (s *Service) ResumeEnvironment(ctx context.Context, envID string) error {
	if err := s.manager.Resume(ctx, envID); err != nil {
		return fmt.Errorf("resume environment: %w", err)
	}

	env, _ := s.manager.Get(ctx, envID)
	if env != nil {
		s.repo.Update(ctx, env)
	}

	return nil
}

// ExecuteCommand 在环境中执行命令
func (s *Service) ExecuteCommand(ctx context.Context, envID string, req ExecRequest) (*ExecResult, error) {
	if req.Command == "" {
		return nil, fmt.Errorf("command is required")
	}

	result, err := s.manager.ExecuteCommand(ctx, envID, req.Command)
	if err != nil {
		return nil, fmt.Errorf("execute command: %w", err)
	}

	return result, nil
}

// UploadFile 上传文件到环境
func (s *Service) UploadFile(ctx context.Context, envID string, path string, content []byte) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}

	return s.manager.UploadFile(ctx, envID, path, content)
}

// DownloadFile 从环境下载文件
func (s *Service) DownloadFile(ctx context.Context, envID string, path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}

	return s.manager.DownloadFile(ctx, envID, path)
}

// ListFiles 列出环境中的文件
func (s *Service) ListFiles(ctx context.Context, envID string, path string) ([]FileInfo, error) {
	return s.manager.ListFiles(ctx, envID, path)
}

// GetMetrics 获取环境资源指标
func (s *Service) GetMetrics(ctx context.Context, envID string) (*Metrics, error) {
	return s.manager.GetMetrics(ctx, envID)
}

// CreateEnvRequest 创建环境请求
type CreateEnvRequest struct {
	Type      EnvironmentType   `json:"type"`
	Image     string            `json:"image"`
	Resources Resources         `json:"resources"`
	Network   NetworkConfig     `json:"network"`
	Env       map[string]string `json:"env,omitempty"`
	Volumes   []Volume          `json:"volumes,omitempty"`
}

// ExecRequest 执行命令请求
type ExecRequest struct {
	Command string `json:"command"`
}

// applyEnvDefaults 为环境配置应用默认值
func applyEnvDefaults(config *EnvironmentConfig) {
	if config.Type == "" {
		config.Type = EnvTypeContainer
	}
	if config.Image == "" {
		config.Image = "ubuntu:22.04"
	}
	if config.Resources.CPU == "" {
		config.Resources.CPU = "1"
	}
	if config.Resources.Memory == "" {
		config.Resources.Memory = "512Mi"
	}
	if config.Resources.Disk == "" {
		config.Resources.Disk = "1Gi"
	}
}

// EnvResponse 环境 API 响应
type EnvResponse struct {
	ID          string          `json:"id"`
	AgentID     string          `json:"agent_id"`
	Type        EnvironmentType `json:"type"`
	State       EnvironmentState `json:"state"`
	ContainerID string          `json:"container_id,omitempty"`
	Resources   Resources       `json:"resources"`
	Network     NetworkConfig   `json:"network"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// ToResponse 转换为 API 响应格式
func ToResponse(env *Environment) EnvResponse {
	return EnvResponse{
		ID:          env.ID,
		AgentID:     env.AgentID,
		Type:        env.Type,
		State:       env.State,
		ContainerID: env.ContainerID,
		Resources:   env.Resources,
		Network:     env.Network,
		CreatedAt:   env.CreatedAt,
		UpdatedAt:   env.UpdatedAt,
	}
}

// EnvListResponse 环境列表响应
type EnvListResponse struct {
	Environments []EnvResponse `json:"environments"`
	Total        int           `json:"total"`
}
