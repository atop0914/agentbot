package filesystem

import (
	"context"
	"fmt"
)

// Service 文件系统服务层
type Service struct {
	manager Manager
	repo    Repository
}

// NewService 创建文件系统服务
func NewService(manager Manager, repo Repository) *Service {
	return &Service{
		manager: manager,
		repo:    repo,
	}
}

// ReadFile 读取文件
func (s *Service) ReadFile(ctx context.Context, path string) (*FileContent, error) {
	content, err := s.manager.ReadFile(ctx, path)
	if err != nil {
		return nil, err
	}
	// 缓存文件元数据
	_ = s.repo.Save(ctx, &content.Info)
	return content, nil
}

// WriteFile 写入文件
func (s *Service) WriteFile(ctx context.Context, req *UploadRequest) (*FileInfo, error) {
	encoding := req.Encoding
	if encoding == "" {
		encoding = "raw"
	}

	var data []byte
	switch encoding {
	case "raw":
		data = req.Content
	case "base64":
		decoded, err := decodeBase64(req.Content)
		if err != nil {
			return nil, fmt.Errorf("invalid base64 content: %w", err)
		}
		data = decoded
	default:
		return nil, fmt.Errorf("unsupported encoding: %s", encoding)
	}

	info, err := s.manager.WriteFile(ctx, req.Path, data, req.Overwrite)
	if err != nil {
		return nil, err
	}
	_ = s.repo.Save(ctx, info)
	return info, nil
}

// ListDir 列出目录
func (s *Service) ListDir(ctx context.Context, path string, recursive, showHidden bool) ([]FileInfo, error) {
	return s.manager.ListDir(ctx, path, recursive, showHidden)
}

// GetFileInfo 获取文件信息
func (s *Service) GetFileInfo(ctx context.Context, path string) (*FileInfo, error) {
	return s.manager.GetFileInfo(ctx, path)
}

// Mkdir 创建目录
func (s *Service) Mkdir(ctx context.Context, req *MkdirRequest) error {
	return s.manager.Mkdir(ctx, req.Path, req.Recursive)
}

// Remove 删除文件或目录
func (s *Service) Remove(ctx context.Context, path string, recursive bool) error {
	if err := s.manager.Remove(ctx, path, recursive); err != nil {
		return err
	}
	_ = s.repo.Delete(ctx, path)
	if recursive {
		_ = s.repo.DeleteByPrefix(ctx, path+"/")
	}
	return nil
}

// Move 移动/重命名
func (s *Service) Move(ctx context.Context, req *MoveRequest) error {
	return s.manager.Move(ctx, req.Src, req.Dest)
}

// Copy 复制
func (s *Service) Copy(ctx context.Context, req *CopyRequest) error {
	return s.manager.Copy(ctx, req.Src, req.Dest, req.Overwrite)
}

// Search 搜索文件
func (s *Service) Search(ctx context.Context, req *SearchRequest) (*SearchResult, error) {
	files, err := s.manager.Search(ctx, req.Path, req.Pattern, req.Content, req.MaxSize)
	if err != nil {
		return nil, err
	}
	return &SearchResult{
		Files: files,
		Total: len(files),
	}, nil
}

// Stat 检查文件是否存在
func (s *Service) Stat(ctx context.Context, path string) (bool, error) {
	return s.manager.Stat(ctx, path)
}
