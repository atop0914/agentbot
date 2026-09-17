package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

// LocalManager 本地文件系统管理器
type LocalManager struct {
	basePath string // 根目录限制，防止路径逃逸
}

// NewLocalManager 创建本地文件系统管理器
// basePath 为操作的根目录，所有路径都会被限制在此目录下
func NewLocalManager(basePath string) (*LocalManager, error) {
	abs, err := filepath.Abs(basePath)
	if err != nil {
		return nil, fmt.Errorf("invalid base path: %w", err)
	}
	// 确保基础目录存在
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, fmt.Errorf("create base dir: %w", err)
	}
	return &LocalManager{basePath: abs}, nil
}

// safePath 将用户路径限制在 basePath 内
func (m *LocalManager) safePath(p string) (string, error) {
	// 清理路径
	cleaned := filepath.Clean(p)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Join(m.basePath, cleaned)
	}
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	// 路径逃逸检查
	if !strings.HasPrefix(abs, m.basePath) {
		return "", fmt.Errorf("path %q is outside base directory", p)
	}
	return abs, nil
}

func (m *LocalManager) ReadFile(ctx context.Context, path string) (*FileContent, error) {
	safePath, err := m.safePath(path)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	info, err := m.buildFileInfo(safePath)
	if err != nil {
		return nil, err
	}
	return &FileContent{
		Info:     *info,
		Content:  data,
		Encoding: "raw",
	}, nil
}

func (m *LocalManager) WriteFile(ctx context.Context, path string, content []byte, overwrite bool) (*FileInfo, error) {
	safePath, err := m.safePath(path)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// 检查是否存在
	if _, err := os.Stat(safePath); err == nil && !overwrite {
		return nil, fmt.Errorf("file already exists: %s", path)
	}

	// 确保父目录存在
	parent := filepath.Dir(safePath)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return nil, fmt.Errorf("create parent dir: %w", err)
	}

	if err := os.WriteFile(safePath, content, 0644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	return m.buildFileInfo(safePath)
}

func (m *LocalManager) ListDir(ctx context.Context, path string, recursive, showHidden bool) ([]FileInfo, error) {
	safePath, err := m.safePath(path)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var results []FileInfo

	if recursive {
		err = filepath.Walk(safePath, func(walkPath string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return nil // 跳过无法访问的文件
			}
			if walkPath == safePath {
				return nil // 跳过根目录
			}
			fi := m.osToFileInfo(walkPath, info)
			if !showHidden && fi.IsHidden {
				return nil
			}
			// 转换为相对路径
			rel, _ := filepath.Rel(m.basePath, walkPath)
			fi.Path = rel
			results = append(results, fi)
			return nil
		})
	} else {
		entries, readErr := os.ReadDir(safePath)
		if readErr != nil {
			return nil, fmt.Errorf("read dir: %w", readErr)
		}
		for _, entry := range entries {
			fullPath := filepath.Join(safePath, entry.Name())
			info, statErr := entry.Info()
			if statErr != nil {
				continue
			}
			fi := m.osToFileInfo(fullPath, info)
			if !showHidden && fi.IsHidden {
				continue
			}
			rel, _ := filepath.Rel(m.basePath, fullPath)
			fi.Path = rel
			results = append(results, fi)
		}
	}

	return results, err
}

func (m *LocalManager) GetFileInfo(ctx context.Context, path string) (*FileInfo, error) {
	safePath, err := m.safePath(path)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return m.buildFileInfo(safePath)
}

func (m *LocalManager) Mkdir(ctx context.Context, path string, recursive bool) error {
	safePath, err := m.safePath(path)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if recursive {
		return os.MkdirAll(safePath, 0755)
	}
	return os.Mkdir(safePath, 0755)
}

func (m *LocalManager) Remove(ctx context.Context, path string, recursive bool) error {
	safePath, err := m.safePath(path)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if safePath == m.basePath {
		return fmt.Errorf("cannot remove base directory")
	}
	if recursive {
		return os.RemoveAll(safePath)
	}
	return os.Remove(safePath)
}

func (m *LocalManager) Move(ctx context.Context, src, dest string) error {
	srcPath, err := m.safePath(src)
	if err != nil {
		return err
	}
	destPath, err := m.safePath(dest)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return os.Rename(srcPath, destPath)
}

func (m *LocalManager) Copy(ctx context.Context, src, dest string, overwrite bool) error {
	srcPath, err := m.safePath(src)
	if err != nil {
		return err
	}
	destPath, err := m.safePath(dest)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("source not found: %w", err)
	}

	if _, err := os.Stat(destPath); err == nil && !overwrite {
		return fmt.Errorf("destination already exists: %s", dest)
	}

	if srcInfo.IsDir() {
		return m.copyDir(srcPath, destPath)
	}
	return m.copyFile(srcPath, destPath)
}

func (m *LocalManager) Search(ctx context.Context, basePath, pattern, content string, maxSize int64) ([]FileInfo, error) {
	safePath, err := m.safePath(basePath)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var results []FileInfo
	err = filepath.Walk(safePath, func(walkPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		// 大小过滤
		if maxSize > 0 && info.Size() > maxSize {
			return nil
		}
		// 文件名匹配
		if pattern != "" {
			matched, _ := filepath.Match(pattern, info.Name())
			if !matched {
				return nil
			}
		}
		// 内容搜索
		if content != "" {
			data, readErr := os.ReadFile(walkPath)
			if readErr != nil {
				return nil
			}
			if !strings.Contains(string(data), content) {
				return nil
			}
		}
		fi := m.osToFileInfo(walkPath, info)
		rel, _ := filepath.Rel(m.basePath, walkPath)
		fi.Path = rel
		results = append(results, fi)
		return nil
	})
	return results, err
}

func (m *LocalManager) Stat(ctx context.Context, path string) (bool, error) {
	safePath, err := m.safePath(path)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(safePath)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// === 辅助方法 ===

func (m *LocalManager) buildFileInfo(fullPath string) (*FileInfo, error) {
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	fi := m.osToFileInfo(fullPath, info)
	rel, _ := filepath.Rel(m.basePath, fullPath)
	fi.Path = rel
	return &fi, nil
}

func (m *LocalManager) osToFileInfo(fullPath string, info os.FileInfo) FileInfo {
	ft := FileTypeFile
	if info.IsDir() {
		ft = FileTypeDirectory
	} else if info.Mode()&os.ModeSymlink != 0 {
		ft = FileTypeSymlink
	}

	mimeType := ""
	if !info.IsDir() {
		mimeType = mime.TypeByExtension(filepath.Ext(info.Name()))
	}

	return FileInfo{
		Name:     info.Name(),
		Path:     fullPath,
		Type:     ft,
		Size:     info.Size(),
		Mode:     info.Mode().String(),
		ModTime:  info.ModTime(),
		IsHidden: strings.HasPrefix(info.Name(), "."),
		MimeType: mimeType,
	}
}

func (m *LocalManager) copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func (m *LocalManager) copyDir(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return m.copyFile(path, target)
	})
}

// FileChecksum 计算文件 SHA256 校验和
func FileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
