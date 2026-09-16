package terminal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// LocalManager 基于本地进程的终端管理器
type LocalManager struct {
	mu       sync.RWMutex
	sessions map[string]*localSession
}

// localSession 包装本地终端会话的额外状态
type localSession struct {
	*Session
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bytes.Buffer
	stderr  *bytes.Buffer
	workDir string
}

// NewLocalManager 创建本地终端管理器
func NewLocalManager() *LocalManager {
	return &LocalManager{
		sessions: make(map[string]*localSession),
	}
}

// CreateSession 创建新的终端会话
func (m *LocalManager) CreateSession(ctx context.Context, req CreateSessionRequest) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := uuid.New().String()
	now := time.Now().UTC()

	shell := req.Shell
	if shell == "" {
		shell = DefaultShell()
	}

	rows, cols := DefaultSize()

	workDir := ""
	if req.EnvID != "" {
		workDir = "/tmp/agentbot-envs/" + req.EnvID
	}

	session := &Session{
		ID:           id,
		AgentID:      req.AgentID,
		EnvID:        req.EnvID,
		Type:         req.Type,
		State:        StateIdle,
		Shell:        shell,
		WorkingDir:   workDir,
		Rows:         rows,
		Cols:         cols,
		StartedAt:    now,
		LastActiveAt: now,
		Metadata:     make(map[string]string),
	}

	// 如果是容器类型，记录容器信息
	if req.Container != nil {
		session.Metadata["container_id"] = req.Container.ContainerID
		if req.Container.User != "" {
			session.Metadata["container_user"] = req.Container.User
		}
	}

	// 如果是 SSH 类型，记录连接信息
	if req.SSH != nil {
		session.Type = ConnTypeSSH
		session.Metadata["ssh_host"] = req.SSH.Host
		session.Metadata["ssh_port"] = fmt.Sprintf("%d", req.SSH.Port)
		session.Metadata["ssh_user"] = req.SSH.User
	}

	local := &localSession{
		Session: session,
		workDir: workDir,
	}

	m.sessions[id] = local

	copy := *session
	return &copy, nil
}

// CloseSession 关闭终端会话
func (m *LocalManager) CloseSession(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	local, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}

	if local.State == StateClosed {
		return fmt.Errorf("session %s already closed", sessionID)
	}

	// 终止正在运行的命令
	if local.cmd != nil && local.cmd.Process != nil {
		local.cmd.Process.Kill()
		local.cmd = nil
	}

	now := time.Now().UTC()
	local.State = StateClosed
	local.ClosedAt = &now
	local.LastActiveAt = now

	return nil
}

// Execute 在会话中执行命令
func (m *LocalManager) Execute(ctx context.Context, sessionID string, req ExecRequest) (*ExecResult, error) {
	m.mu.RLock()
	local, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}

	if local.State == StateClosed {
		return nil, fmt.Errorf("session %s is closed", sessionID)
	}

	// 更新状态
	m.mu.Lock()
	local.State = StateRunning
	local.LastActiveAt = time.Now().UTC()
	m.mu.Unlock()

	start := time.Now()

	// 构建命令
	shell := local.Shell
	if shell == "" {
		shell = DefaultShell()
	}

	// 使用 shell -c 执行命令
	cmd := exec.CommandContext(ctx, shell, "-c", req.Command)
	if local.workDir != "" {
		cmd.Dir = local.workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	duration := time.Since(start)

	// 恢复状态
	m.mu.Lock()
	local.State = StateIdle
	local.LastActiveAt = time.Now().UTC()
	m.mu.Unlock()

	// 检查 context 取消（超时）
	if ctx.Err() != nil {
		return nil, fmt.Errorf("command cancelled: %w", ctx.Err())
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	return &ExecResult{
		SessionID: sessionID,
		Command:   req.Command,
		ExitCode:  exitCode,
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Duration:  duration,
	}, nil
}

// Resize 调整终端窗口大小
func (m *LocalManager) Resize(ctx context.Context, sessionID string, rows, cols int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	local, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}

	if local.State == StateClosed {
		return fmt.Errorf("session %s is closed", sessionID)
	}

	local.Rows = rows
	local.Cols = cols
	local.LastActiveAt = time.Now().UTC()

	return nil
}

// Stream 执行命令并流式输出
func (m *LocalManager) Stream(ctx context.Context, sessionID string, req ExecRequest) (io.Reader, io.Reader, error) {
	m.mu.RLock()
	local, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return nil, nil, fmt.Errorf("session %s not found", sessionID)
	}

	if local.State == StateClosed {
		return nil, nil, fmt.Errorf("session %s is closed", sessionID)
	}

	// 更新状态
	m.mu.Lock()
	local.State = StateRunning
	local.LastActiveAt = time.Now().UTC()
	m.mu.Unlock()

	shell := local.Shell
	if shell == "" {
		shell = DefaultShell()
	}

	cmd := exec.CommandContext(ctx, shell, "-c", req.Command)
	if local.workDir != "" {
		cmd.Dir = local.workDir
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start command: %w", err)
	}

	// 异步等待命令完成并恢复状态
	go func() {
		_ = cmd.Wait()
		m.mu.Lock()
		local.State = StateIdle
		local.LastActiveAt = time.Now().UTC()
		m.mu.Unlock()
	}()

	return stdoutPipe, stderrPipe, nil
}

// GetSession 获取会话信息
func (m *LocalManager) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	local, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}

	copy := *local.Session
	return &copy, nil
}

// ListSessions 列出所有会话
func (m *LocalManager) ListSessions(ctx context.Context, filter SessionFilter) ([]*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Session, 0)
	for _, local := range m.sessions {
		if filter.AgentID != "" && local.AgentID != filter.AgentID {
			continue
		}
		if filter.State != "" && local.State != filter.State {
			continue
		}
		copy := *local.Session
		result = append(result, &copy)
	}
	return result, nil
}

// formatCommand 格式化命令（去除首尾空格）
func formatCommand(cmd string) string {
	return strings.TrimSpace(cmd)
}
