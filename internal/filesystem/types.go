package filesystem

import "time"

// FileType 文件类型
type FileType string

const (
	FileTypeFile      FileType = "file"
	FileTypeDirectory FileType = "directory"
	FileTypeSymlink   FileType = "symlink"
)

// FileInfo 文件信息
type FileInfo struct {
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	Type         FileType  `json:"type"`
	Size         int64     `json:"size"`
	Mode         string    `json:"mode"`
	ModTime      time.Time `json:"mod_time"`
	IsHidden     bool      `json:"is_hidden"`
	MimeType     string    `json:"mime_type,omitempty"`
	Checksum     string    `json:"checksum,omitempty"`
}

// FileContent 文件内容
type FileContent struct {
	Info     FileInfo `json:"info"`
	Content  []byte   `json:"content,omitempty"`
	Encoding string   `json:"encoding"` // "raw", "base64"
}

// UploadRequest 上传请求
type UploadRequest struct {
	Path     string `json:"path" binding:"required"`
	Content  []byte `json:"content" binding:"required"`
	Encoding string `json:"encoding"` // "raw", "base64"; default "raw"
	Overwrite bool  `json:"overwrite"`
}

// DownloadRequest 下载请求
type DownloadRequest struct {
	Path     string `json:"path" binding:"required"`
	Encoding string `json:"encoding"` // "raw", "base64"; default "raw"
}

// ListRequest 目录列表请求
type ListRequest struct {
	Path      string `json:"path" binding:"required"`
	Recursive bool   `json:"recursive"`
	Hidden    bool   `json:"hidden"` // 是否显示隐藏文件
}

// MkdirRequest 创建目录请求
type MkdirRequest struct {
	Path      string `json:"path" binding:"required"`
	Recursive bool   `json:"recursive"` // 创建父目录
}

// MoveRequest 移动/重命名请求
type MoveRequest struct {
	Src  string `json:"src" binding:"required"`
	Dest string `json:"dest" binding:"required"`
}

// CopyRequest 复制请求
type CopyRequest struct {
	Src       string `json:"src" binding:"required"`
	Dest      string `json:"dest" binding:"required"`
	Overwrite bool   `json:"overwrite"`
}

// SearchRequest 搜索请求
type SearchRequest struct {
	Path    string `json:"path" binding:"required"`
	Pattern string `json:"pattern" binding:"required"` // 文件名 glob 模式
	Content string `json:"content,omitempty"`           // 内容关键字
	MaxSize int64  `json:"max_size,omitempty"`          // 最大文件大小
}

// SearchResult 搜索结果
type SearchResult struct {
	Files []FileInfo `json:"files"`
	Total int        `json:"total"`
}
