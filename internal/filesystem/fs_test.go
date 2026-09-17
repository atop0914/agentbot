package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestManager(t *testing.T) (*LocalManager, string) {
	t.Helper()
	tmpDir := t.TempDir()
	mgr, err := NewLocalManager(tmpDir)
	if err != nil {
		t.Fatalf("NewLocalManager: %v", err)
	}
	return mgr, tmpDir
}

func TestLocalManager_WriteAndReadFile(t *testing.T) {
	mgr, _ := setupTestManager(t)
	ctx := context.Background()

	// 写入文件
	info, err := mgr.WriteFile(ctx, "test.txt", []byte("hello world"), false)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if info.Name != "test.txt" {
		t.Errorf("Name = %q, want %q", info.Name, "test.txt")
	}
	if info.Type != FileTypeFile {
		t.Errorf("Type = %q, want %q", info.Type, FileTypeFile)
	}

	// 读取文件
	content, err := mgr.ReadFile(ctx, "test.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(content.Content) != "hello world" {
		t.Errorf("Content = %q, want %q", string(content.Content), "hello world")
	}
	if content.Encoding != "raw" {
		t.Errorf("Encoding = %q, want %q", content.Encoding, "raw")
	}
}

func TestLocalManager_WriteNoOverwrite(t *testing.T) {
	mgr, _ := setupTestManager(t)
	ctx := context.Background()

	_, err := mgr.WriteFile(ctx, "test.txt", []byte("first"), false)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// 不覆盖应报错
	_, err = mgr.WriteFile(ctx, "test.txt", []byte("second"), false)
	if err == nil {
		t.Fatal("expected error for overwrite=false, got nil")
	}

	// 覆盖应成功
	info, err := mgr.WriteFile(ctx, "test.txt", []byte("second"), true)
	if err != nil {
		t.Fatalf("WriteFile overwrite: %v", err)
	}
	if info.Size != 6 {
		t.Errorf("Size = %d, want 6", info.Size)
	}
}

func TestLocalManager_Mkdir(t *testing.T) {
	mgr, base := setupTestManager(t)
	ctx := context.Background()

	// 非递归创建
	if err := mgr.Mkdir(ctx, "subdir", false); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "subdir")); err != nil {
		t.Fatalf("dir not created: %v", err)
	}

	// 递归创建
	if err := mgr.Mkdir(ctx, "a/b/c", true); err != nil {
		t.Fatalf("Mkdir recursive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "a/b/c")); err != nil {
		t.Fatalf("nested dir not created: %v", err)
	}
}

func TestLocalManager_ListDir(t *testing.T) {
	mgr, _ := setupTestManager(t)
	ctx := context.Background()

	mgr.WriteFile(ctx, "a.txt", []byte("a"), false)
	mgr.WriteFile(ctx, "b.txt", []byte("b"), false)
	mgr.WriteFile(ctx, ".hidden", []byte("h"), false)
	mgr.Mkdir(ctx, "subdir", false)

	// 不显示隐藏文件
	files, err := mgr.ListDir(ctx, ".", false, false)
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(files) != 3 { // a.txt, b.txt, subdir
		t.Errorf("len = %d, want 3", len(files))
	}

	// 显示隐藏文件
	files, err = mgr.ListDir(ctx, ".", false, true)
	if err != nil {
		t.Fatalf("ListDir hidden: %v", err)
	}
	if len(files) != 4 {
		t.Errorf("len = %d, want 4", len(files))
	}
}

func TestLocalManager_Remove(t *testing.T) {
	mgr, _ := setupTestManager(t)
	ctx := context.Background()

	mgr.WriteFile(ctx, "test.txt", []byte("x"), false)
	exists, _ := mgr.Stat(ctx, "test.txt")
	if !exists {
		t.Fatal("file should exist")
	}

	if err := mgr.Remove(ctx, "test.txt", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	exists, _ = mgr.Stat(ctx, "test.txt")
	if exists {
		t.Error("file should not exist after remove")
	}
}

func TestLocalManager_Move(t *testing.T) {
	mgr, _ := setupTestManager(t)
	ctx := context.Background()

	mgr.WriteFile(ctx, "old.txt", []byte("data"), false)
	if err := mgr.Move(ctx, "old.txt", "new.txt"); err != nil {
		t.Fatalf("Move: %v", err)
	}

	exists, _ := mgr.Stat(ctx, "old.txt")
	if exists {
		t.Error("old path should not exist")
	}
	content, err := mgr.ReadFile(ctx, "new.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(content.Content) != "data" {
		t.Errorf("Content = %q, want %q", string(content.Content), "data")
	}
}

func TestLocalManager_Copy(t *testing.T) {
	mgr, _ := setupTestManager(t)
	ctx := context.Background()

	mgr.WriteFile(ctx, "src.txt", []byte("hello"), false)
	if err := mgr.Copy(ctx, "src.txt", "dest.txt", false); err != nil {
		t.Fatalf("Copy: %v", err)
	}

	content, _ := mgr.ReadFile(ctx, "dest.txt")
	if string(content.Content) != "hello" {
		t.Errorf("Content = %q, want %q", string(content.Content), "hello")
	}
}

func TestLocalManager_Search(t *testing.T) {
	mgr, _ := setupTestManager(t)
	ctx := context.Background()

	mgr.WriteFile(ctx, "readme.md", []byte("# Hello"), false)
	mgr.WriteFile(ctx, "main.go", []byte("package main"), false)
	mgr.WriteFile(ctx, "test.go", []byte("package main\n// Hello"), false)

	// 按文件名搜索
	results, err := mgr.Search(ctx, ".", "*.go", "", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("len = %d, want 2", len(results))
	}

	// 按内容搜索
	results, err = mgr.Search(ctx, ".", "", "Hello", 0)
	if err != nil {
		t.Fatalf("Search content: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("len = %d, want 2", len(results))
	}
}

func TestLocalManager_PathEscape(t *testing.T) {
	mgr, _ := setupTestManager(t)
	ctx := context.Background()

	// 路径逃逸应被拒绝
	_, err := mgr.ReadFile(ctx, "../../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path escape")
	}
	if !strings.Contains(err.Error(), "outside base directory") {
		t.Errorf("error = %q, want contains 'outside base directory'", err.Error())
	}
}

func TestMemoryRepository(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	info := &FileInfo{Name: "test.txt", Path: "test.txt", Type: FileTypeFile}
	if err := repo.Save(ctx, info); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.GetByPath(ctx, "test.txt")
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}
	if got.Name != "test.txt" {
		t.Errorf("Name = %q, want %q", got.Name, "test.txt")
	}

	// 列出父目录
	repo.Save(ctx, &FileInfo{Name: "a.txt", Path: "dir/a.txt"})
	repo.Save(ctx, &FileInfo{Name: "b.txt", Path: "dir/b.txt"})
	repo.Save(ctx, &FileInfo{Name: "c.txt", Path: "dir/sub/c.txt"})
	list, _ := repo.ListByParent(ctx, "dir")
	if len(list) != 2 {
		t.Errorf("len = %d, want 2", len(list))
	}

	// 按前缀删除
	repo.DeleteByPrefix(ctx, "dir/")
	list, _ = repo.ListByParent(ctx, "dir")
	if len(list) != 0 {
		t.Errorf("len = %d, want 0 after prefix delete", len(list))
	}
}

func TestService_WriteAndRead(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewLocalManager(tmpDir)
	repo := NewMemoryRepository()
	svc := NewService(mgr, repo)
	ctx := context.Background()

	// 写入
	req := &UploadRequest{Path: "doc.txt", Content: []byte("content"), Encoding: "raw"}
	info, err := svc.WriteFile(ctx, req)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if info.Name != "doc.txt" {
		t.Errorf("Name = %q", info.Name)
	}

	// 读取
	content, err := svc.ReadFile(ctx, "doc.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(content.Content) != "content" {
		t.Errorf("Content = %q", string(content.Content))
	}
}

func TestService_CopyDir(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewLocalManager(tmpDir)
	repo := NewMemoryRepository()
	svc := NewService(mgr, repo)
	ctx := context.Background()

	svc.Mkdir(ctx, &MkdirRequest{Path: "src", Recursive: true})
	svc.WriteFile(ctx, &UploadRequest{Path: "src/a.txt", Content: []byte("a")})
	svc.WriteFile(ctx, &UploadRequest{Path: "src/b.txt", Content: []byte("b")})

	if err := svc.Copy(ctx, &CopyRequest{Src: "src", Dest: "dst"}); err != nil {
		t.Fatalf("Copy dir: %v", err)
	}

	files, _ := svc.ListDir(ctx, "dst", false, false)
	if len(files) != 2 {
		t.Errorf("len = %d, want 2", len(files))
	}
}

func TestFileChecksum(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.txt")
	os.WriteFile(tmpFile, []byte("hello"), 0644)

	hash, err := FileChecksum(tmpFile)
	if err != nil {
		t.Fatalf("FileChecksum: %v", err)
	}
	if len(hash) != 64 { // SHA256 hex = 64 chars
		t.Errorf("hash length = %d, want 64", len(hash))
	}
}
