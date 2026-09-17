package filesystem

import "context"

// Manager 文件系统管理器接口
type Manager interface {
	// ReadFile 读取文件内容
	ReadFile(ctx context.Context, path string) (*FileContent, error)
	// WriteFile 写入文件内容
	WriteFile(ctx context.Context, path string, content []byte, overwrite bool) (*FileInfo, error)
	// ListDir 列出目录内容
	ListDir(ctx context.Context, path string, recursive, showHidden bool) ([]FileInfo, error)
	// GetFileInfo 获取文件信息
	GetFileInfo(ctx context.Context, path string) (*FileInfo, error)
	// Mkdir 创建目录
	Mkdir(ctx context.Context, path string, recursive bool) error
	// Remove 删除文件或目录
	Remove(ctx context.Context, path string, recursive bool) error
	// Move 移动/重命名
	Move(ctx context.Context, src, dest string) error
	// Copy 复制文件或目录
	Copy(ctx context.Context, src, dest string, overwrite bool) error
	// Search 搜索文件
	Search(ctx context.Context, basePath, pattern, content string, maxSize int64) ([]FileInfo, error)
	// Stat 检查路径是否存在
	Stat(ctx context.Context, path string) (bool, error)
}

// Repository 文件元数据仓库接口（用于缓存/索引）
type Repository interface {
	// Save 保存文件元数据
	Save(ctx context.Context, info *FileInfo) error
	// GetByPath 按路径获取文件元数据
	GetByPath(ctx context.Context, path string) (*FileInfo, error)
	// ListByParent 列出父目录下的文件
	ListByParent(ctx context.Context, parentPath string) ([]FileInfo, error)
	// Delete 删除元数据
	Delete(ctx context.Context, path string) error
	// DeleteByPrefix 按前缀删除（删除目录时用）
	DeleteByPrefix(ctx context.Context, prefix string) error
}
