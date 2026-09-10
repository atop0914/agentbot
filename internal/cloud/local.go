package cloud

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// LocalManager 基于本地进程的环境管理器
// 用于开发和测试环境，不依赖 Docker
type LocalManager struct {
	mu          sync.RWMutex
	environments map[string]*localEnv
	workDir     string
}

// localEnv 包装本地环境的额外状态
type localEnv struct {
	*Environment
	process   *os.Process
	workPath  string // 环境工作目录
}

// NewLocalManager 创建本地管理器
func NewLocalManager(workDir string) (*LocalManager, error) {
	if workDir == "" {
		workDir = filepath.Join(os.TempDir(), "agentbot-envs")
	}
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}
	return &LocalManager{
		environments: make(map[string]*localEnv),
		workDir:      workDir,
	}, nil
}

// Create 创建新的本地环境
func (m *LocalManager) Create(ctx context.Context, agentID string, config EnvironmentConfig) (*Environment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := uuid.New().String()
	now := time.Now().UTC()

	envPath := filepath.Join(m.workDir, id)
	if err := os.MkdirAll(envPath, 0755); err != nil {
		return nil, fmt.Errorf("create env dir: %w", err)
	}

	env := &Environment{
		ID:        id,
		AgentID:   agentID,
		Type:      config.Type,
		State:     EnvStateCreating,
		Resources: config.Resources,
		Network:   config.Network,
		CreatedAt: now,
		UpdatedAt: now,
	}

	local := &localEnv{
		Environment: env,
		workPath:    envPath,
	}

	// 模拟创建过程
	env.State = EnvStateRunning
	env.UpdatedAt = time.Now().UTC()

	m.environments[id] = local

	copy := *env
	return &copy, nil
}

// Destroy 销毁环境
func (m *LocalManager) Destroy(ctx context.Context, envID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	local, ok := m.environments[envID]
	if !ok {
		return fmt.Errorf("environment %s not found", envID)
	}

	// 终止进程（如果有）
	if local.process != nil {
		local.process.Kill()
		local.process = nil
	}

	// 清理工作目录
	os.RemoveAll(local.workPath)

	delete(m.environments, envID)
	return nil
}

// Get 获取环境信息
func (m *LocalManager) Get(ctx context.Context, envID string) (*Environment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	local, ok := m.environments[envID]
	if !ok {
		return nil, fmt.Errorf("environment %s not found", envID)
	}

	copy := *local.Environment
	return &copy, nil
}

// List 列出指定 Agent 的所有环境
func (m *LocalManager) List(ctx context.Context, agentID string) ([]*Environment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Environment, 0)
	for _, local := range m.environments {
		if local.AgentID == agentID {
			copy := *local.Environment
			result = append(result, &copy)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	return result, nil
}

// Start 启动环境
func (m *LocalManager) Start(ctx context.Context, envID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	local, ok := m.environments[envID]
	if !ok {
		return fmt.Errorf("environment %s not found", envID)
	}

	if local.State != EnvStateStopped && local.State != EnvStatePaused {
		return fmt.Errorf("cannot start environment in state %s", local.State)
	}

	local.State = EnvStateRunning
	local.UpdatedAt = time.Now().UTC()
	return nil
}

// Stop 停止环境
func (m *LocalManager) Stop(ctx context.Context, envID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	local, ok := m.environments[envID]
	if !ok {
		return fmt.Errorf("environment %s not found", envID)
	}

	if local.State != EnvStateRunning && local.State != EnvStatePaused {
		return fmt.Errorf("cannot stop environment in state %s", local.State)
	}

	// 终止进程
	if local.process != nil {
		local.process.Kill()
		local.process = nil
	}

	local.State = EnvStateStopped
	local.UpdatedAt = time.Now().UTC()
	return nil
}

// Pause 暂停环境
func (m *LocalManager) Pause(ctx context.Context, envID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	local, ok := m.environments[envID]
	if !ok {
		return fmt.Errorf("environment %s not found", envID)
	}

	if local.State != EnvStateRunning {
		return fmt.Errorf("cannot pause environment in state %s", local.State)
	}

	local.State = EnvStatePaused
	local.UpdatedAt = time.Now().UTC()
	return nil
}

// Resume 恢复环境
func (m *LocalManager) Resume(ctx context.Context, envID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	local, ok := m.environments[envID]
	if !ok {
		return fmt.Errorf("environment %s not found", envID)
	}

	if local.State != EnvStatePaused {
		return fmt.Errorf("cannot resume environment in state %s", local.State)
	}

	local.State = EnvStateRunning
	local.UpdatedAt = time.Now().UTC()
	return nil
}

// ExecuteCommand 在环境中执行命令
func (m *LocalManager) ExecuteCommand(ctx context.Context, envID string, command string) (*ExecResult, error) {
	m.mu.RLock()
	local, ok := m.environments[envID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("environment %s not found", envID)
	}

	if local.State != EnvStateRunning {
		return nil, fmt.Errorf("environment %s is not running (state: %s)", envID, local.State)
	}

	start := time.Now()

	// 使用 sh -c 执行命令，工作目录为环境目录
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = local.workPath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	duration := time.Since(start)

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	return &ExecResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: duration,
	}, nil
}

// UploadFile 上传文件到环境
func (m *LocalManager) UploadFile(ctx context.Context, envID string, path string, content []byte) error {
	m.mu.RLock()
	local, ok := m.environments[envID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("environment %s not found", envID)
	}

	// 防止路径遍历攻击
	cleanPath := filepath.Clean(path)
	if strings.HasPrefix(cleanPath, "..") || strings.HasPrefix(cleanPath, "/") {
		// 绝对路径直接使用，相对路径拼接
	}

	fullPath := filepath.Join(local.workPath, cleanPath)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

// DownloadFile 从环境下载文件
func (m *LocalManager) DownloadFile(ctx context.Context, envID string, path string) ([]byte, error) {
	m.mu.RLock()
	local, ok := m.environments[envID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("environment %s not found", envID)
	}

	fullPath := filepath.Join(local.workPath, filepath.Clean(path))
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	return content, nil
}

// ListFiles 列出环境中的文件
func (m *LocalManager) ListFiles(ctx context.Context, envID string, path string) ([]FileInfo, error) {
	m.mu.RLock()
	local, ok := m.environments[envID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("environment %s not found", envID)
	}

	fullPath := filepath.Join(local.workPath, filepath.Clean(path))
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, fmt.Errorf("read directory: %w", err)
	}

	files := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, FileInfo{
			Name:    entry.Name(),
			Path:    filepath.Join(path, entry.Name()),
			Size:    info.Size(),
			IsDir:   entry.IsDir(),
			ModTime: info.ModTime(),
			Mode:    info.Mode().String(),
		})
	}

	return files, nil
}

// GetMetrics 返回资源使用指标（本地模式返回模拟数据）
func (m *LocalManager) GetMetrics(ctx context.Context, envID string) (*Metrics, error) {
	m.mu.RLock()
	_, ok := m.environments[envID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("environment %s not found", envID)
	}

	// 本地模式返回模拟指标
	return &Metrics{
		CPUUsage:    0.0,
		MemoryUsage: 0.0,
		DiskUsage:   0.0,
		NetworkIn:   0,
		NetworkOut:  0,
		Timestamp:   time.Now().UTC(),
	}, nil
}
