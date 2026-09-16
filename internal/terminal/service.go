package terminal

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Service 终端服务层
type Service struct {
	manager Manager
	repo    Repository
}

// NewService 创建终端服务
func NewService(manager Manager, repo Repository) *Service {
	return &Service{
		manager: manager,
		repo:    repo,
	}
}

// CreateSession 创建终端会话
func (s *Service) CreateSession(ctx context.Context, req CreateSessionRequest) (*Session, error) {
	session, err := s.manager.CreateSession(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// 持久化
	if err := s.repo.Save(session); err != nil {
		// 回滚：关闭已创建的会话
		_ = s.manager.CloseSession(ctx, session.ID)
		return nil, fmt.Errorf("save session: %w", err)
	}

	return session, nil
}

// CloseSession 关闭终端会话
func (s *Service) CloseSession(ctx context.Context, sessionID string) error {
	if err := s.manager.CloseSession(ctx, sessionID); err != nil {
		return fmt.Errorf("close session: %w", err)
	}

	// 更新持久化
	session, err := s.manager.GetSession(ctx, sessionID)
	if err != nil {
		return nil // 已关闭，忽略错误
	}

	if err := s.repo.Update(session); err != nil {
		return fmt.Errorf("update session: %w", err)
	}

	return nil
}

// Execute 执行命令
func (s *Service) Execute(ctx context.Context, sessionID string, req ExecRequest) (*ExecResult, error) {
	// 超时处理
	if req.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSec)*time.Second)
		defer cancel()
	}

	result, err := s.manager.Execute(ctx, sessionID, req)
	if err != nil {
		return nil, fmt.Errorf("execute command: %w", err)
	}

	// 更新会话活动时间
	session, _ := s.manager.GetSession(ctx, sessionID)
	if session != nil {
		_ = s.repo.Update(session)
	}

	return result, nil
}

// Resize 调整终端窗口大小
func (s *Service) Resize(ctx context.Context, sessionID string, rows, cols int) error {
	if err := s.manager.Resize(ctx, sessionID, rows, cols); err != nil {
		return fmt.Errorf("resize: %w", err)
	}

	// 更新持久化
	session, _ := s.manager.GetSession(ctx, sessionID)
	if session != nil {
		_ = s.repo.Update(session)
	}

	return nil
}

// Stream 流式执行命令
func (s *Service) Stream(ctx context.Context, sessionID string, req ExecRequest) (io.Reader, io.Reader, error) {
	stdout, stderr, err := s.manager.Stream(ctx, sessionID, req)
	if err != nil {
		return nil, nil, fmt.Errorf("stream command: %w", err)
	}

	return stdout, stderr, nil
}

// GetSession 获取会话信息
func (s *Service) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	session, err := s.repo.Get(sessionID)
	if err != nil {
		// 尝试从管理器获取
		session, err = s.manager.GetSession(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("get session: %w", err)
		}
	}
	return session, nil
}

// ListSessions 列出会话
func (s *Service) ListSessions(ctx context.Context, filter SessionFilter) ([]*Session, error) {
	sessions, err := s.repo.List(filter)
	if err != nil {
		// 回退到管理器
		sessions, err = s.manager.ListSessions(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("list sessions: %w", err)
		}
	}
	return sessions, nil
}
