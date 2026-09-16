package terminal

import (
	"context"
	"io"
)

// Manager 终端会话管理器接口
type Manager interface {
	// CreateSession 创建新的终端会话
	CreateSession(ctx context.Context, req CreateSessionRequest) (*Session, error)
	// CloseSession 关闭终端会话
	CloseSession(ctx context.Context, sessionID string) error
	// Execute 在会话中执行命令
	Execute(ctx context.Context, sessionID string, cmd ExecRequest) (*ExecResult, error)
	// Resize 调整终端窗口大小
	Resize(ctx context.Context, sessionID string, rows, cols int) error
	// Stream 执行命令并流式输出
	Stream(ctx context.Context, sessionID string, cmd ExecRequest) (io.Reader, io.Reader, error)
	// GetSession 获取会话信息
	GetSession(ctx context.Context, sessionID string) (*Session, error)
	// ListSessions 列出所有会话
	ListSessions(ctx context.Context, filter SessionFilter) ([]*Session, error)
}

// Repository 终端会话持久化接口
type Repository interface {
	// Save 保存会话
	Save(session *Session) error
	// Get 获取会话
	Get(id string) (*Session, error)
	// Delete 删除会话
	Delete(id string) error
	// List 列出会话
	List(filter SessionFilter) ([]*Session, error)
	// Update 更新会话
	Update(session *Session) error
}
