package filesystem

import (
	"context"
	"fmt"
	"sync"
)

// MemoryRepository 内存文件元数据仓库
type MemoryRepository struct {
	mu    sync.RWMutex
	files map[string]*FileInfo // key: path
}

// NewMemoryRepository 创建内存文件元数据仓库
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		files: make(map[string]*FileInfo),
	}
}

func (r *MemoryRepository) Save(ctx context.Context, info *FileInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[info.Path] = info
	return nil
}

func (r *MemoryRepository) GetByPath(ctx context.Context, path string) (*FileInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.files[path]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", path)
	}
	return info, nil
}

func (r *MemoryRepository) ListByParent(ctx context.Context, parentPath string) ([]FileInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var results []FileInfo
	for _, info := range r.files {
		if isDirectChild(parentPath, info.Path) {
			results = append(results, *info)
		}
	}
	return results, nil
}

func (r *MemoryRepository) Delete(ctx context.Context, path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.files, path)
	return nil
}

func (r *MemoryRepository) DeleteByPrefix(ctx context.Context, prefix string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.files {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(r.files, k)
		}
	}
	return nil
}

// isDirectChild 判断 childPath 是否是 parentPath 的直接子项
func isDirectChild(parentPath, childPath string) bool {
	if len(childPath) <= len(parentPath) {
		return false
	}
	if childPath[:len(parentPath)] != parentPath {
		return false
	}
	// 检查是否是直接子项（没有多余的路径分隔符）
	rest := childPath[len(parentPath):]
	if len(rest) == 0 {
		return false
	}
	if rest[0] == '/' {
		rest = rest[1:]
	}
	// 不包含路径分隔符说明是直接子项
	for _, c := range rest {
		if c == '/' {
			return false
		}
	}
	return len(rest) > 0
}
