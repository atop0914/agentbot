package terminal

import (
	"time"
)

// SessionState 终端会话状态
type SessionState string

const (
	StateIdle     SessionState = "idle"
	StateRunning  SessionState = "running"
	StateClosed   SessionState = "closed"
	StateError    SessionState = "error"
)

// ConnectionType 连接类型
type ConnectionType string

const (
	ConnTypeLocal     ConnectionType = "local"
	ConnTypeSSH       ConnectionType = "ssh"
	ConnTypeContainer ConnectionType = "container"
)

// Session 终端会话
type Session struct {
	ID           string         `json:"id"`
	AgentID      string         `json:"agent_id"`
	EnvID        string         `json:"env_id,omitempty"`
	Type         ConnectionType `json:"type"`
	State        SessionState   `json:"state"`
	Shell        string         `json:"shell"`
	WorkingDir   string         `json:"working_dir"`
	Rows         int            `json:"rows"`
	Cols         int            `json:"cols"`
	StartedAt    time.Time      `json:"started_at"`
	LastActiveAt time.Time      `json:"last_active_at"`
	ClosedAt     *time.Time     `json:"closed_at,omitempty"`
	ExitCode     *int           `json:"exit_code,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// ExecResult 命令执行结果
type ExecResult struct {
	SessionID string        `json:"session_id"`
	Command   string        `json:"command"`
	ExitCode  int           `json:"exit_code"`
	Stdout    string        `json:"stdout"`
	Stderr    string        `json:"stderr"`
	Duration  time.Duration `json:"duration"`
}

// ExecRequest 命令执行请求
type ExecRequest struct {
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec,omitempty"`
}

// ResizeRequest 终端窗口大小调整请求
type ResizeRequest struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// SSHConfig SSH 连接配置
type SSHConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}

// ContainerConfig 容器连接配置
type ContainerConfig struct {
	ContainerID string `json:"container_id"`
	User        string `json:"user,omitempty"`
}

// CreateSessionRequest 创建会话请求
type CreateSessionRequest struct {
	AgentID   string           `json:"agent_id"`
	EnvID     string           `json:"env_id,omitempty"`
	Type      ConnectionType   `json:"type"`
	Shell     string           `json:"shell,omitempty"`
	SSH       *SSHConfig       `json:"ssh,omitempty"`
	Container *ContainerConfig `json:"container,omitempty"`
}

// SessionFilter 会话查询过滤
type SessionFilter struct {
	AgentID string
	State   SessionState
}

// DefaultShell 返回默认 shell
func DefaultShell() string {
	return "/bin/sh"
}

// DefaultSize 返回默认终端大小
func DefaultSize() (rows, cols int) {
	return 24, 80
}
