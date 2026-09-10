package cloud

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryRepository_Create(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	env := &Environment{
		ID:      "env-1",
		AgentID: "agent-1",
		State:   EnvStateRunning,
	}

	if err := repo.Create(ctx, env); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// 重复创建应该失败
	if err := repo.Create(ctx, env); err == nil {
		t.Error("expected error for duplicate create")
	}
}

func TestMemoryRepository_GetByID(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	env := &Environment{
		ID:      "env-1",
		AgentID: "agent-1",
		State:   EnvStateRunning,
	}
	repo.Create(ctx, env)

	// 获取存在的环境
	got, err := repo.GetByID(ctx, "env-1")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got.ID != "env-1" {
		t.Errorf("expected ID 'env-1', got '%s'", got.ID)
	}

	// 获取不存在的环境
	_, err = repo.GetByID(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent environment")
	}
}

func TestMemoryRepository_Update(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	env := &Environment{
		ID:    "env-1",
		State: EnvStateRunning,
	}
	repo.Create(ctx, env)

	env.State = EnvStateStopped
	if err := repo.Update(ctx, env); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	got, _ := repo.GetByID(ctx, "env-1")
	if got.State != EnvStateStopped {
		t.Errorf("expected stopped, got %s", got.State)
	}
}

func TestMemoryRepository_Delete(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	env := &Environment{ID: "env-1"}
	repo.Create(ctx, env)

	if err := repo.Delete(ctx, "env-1"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	_, err := repo.GetByID(ctx, "env-1")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestMemoryRepository_List(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		env := &Environment{
			ID:      "env-" + string(rune('a'+i)),
			AgentID: "agent-1",
		}
		repo.Create(ctx, env)
	}

	envs, err := repo.List(ctx, "agent-1")
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(envs) != 5 {
		t.Errorf("expected 5 environments, got %d", len(envs))
	}
}

func TestLocalManager_Create(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewLocalManager(tmpDir)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	ctx := context.Background()

	env, err := mgr.Create(ctx, "agent-1", EnvironmentConfig{
		Type:      EnvTypeContainer,
		Image:     "ubuntu:22.04",
		Resources: Resources{CPU: "1", Memory: "512Mi"},
	})
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}

	if env.ID == "" {
		t.Error("expected non-empty ID")
	}
	if env.AgentID != "agent-1" {
		t.Errorf("expected agent-1, got %s", env.AgentID)
	}
	if env.State != EnvStateRunning {
		t.Errorf("expected running, got %s", env.State)
	}
}

func TestLocalManager_Destroy(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewLocalManager(tmpDir)
	ctx := context.Background()

	env, _ := mgr.Create(ctx, "agent-1", EnvironmentConfig{})

	if err := mgr.Destroy(ctx, env.ID); err != nil {
		t.Fatalf("destroy: %v", err)
	}

	// 验证环境已被删除
	_, err := mgr.Get(ctx, env.ID)
	if err == nil {
		t.Error("expected error after destroy")
	}

	// 验证工作目录已被清理
	envPath := filepath.Join(tmpDir, env.ID)
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Error("expected work directory to be removed")
	}
}

func TestLocalManager_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewLocalManager(tmpDir)
	ctx := context.Background()

	env, _ := mgr.Create(ctx, "agent-1", EnvironmentConfig{})

	// 初始状态应该是 running
	if env.State != EnvStateRunning {
		t.Fatalf("expected running, got %s", env.State)
	}

	// Stop
	if err := mgr.Stop(ctx, env.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}

	env, _ = mgr.Get(ctx, env.ID)
	if env.State != EnvStateStopped {
		t.Errorf("expected stopped, got %s", env.State)
	}

	// Start
	if err := mgr.Start(ctx, env.ID); err != nil {
		t.Fatalf("start: %v", err)
	}

	env, _ = mgr.Get(ctx, env.ID)
	if env.State != EnvStateRunning {
		t.Errorf("expected running, got %s", env.State)
	}

	// Pause
	if err := mgr.Pause(ctx, env.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}

	env, _ = mgr.Get(ctx, env.ID)
	if env.State != EnvStatePaused {
		t.Errorf("expected paused, got %s", env.State)
	}

	// Resume
	if err := mgr.Resume(ctx, env.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}

	env, _ = mgr.Get(ctx, env.ID)
	if env.State != EnvStateRunning {
		t.Errorf("expected running, got %s", env.State)
	}
}

func TestLocalManager_ExecuteCommand(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewLocalManager(tmpDir)
	ctx := context.Background()

	env, _ := mgr.Create(ctx, "agent-1", EnvironmentConfig{})

	// 执行 echo 命令
	result, err := mgr.ExecuteCommand(ctx, env.ID, "echo hello")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Stdout != "hello\n" {
		t.Errorf("expected 'hello\\n', got '%s'", result.Stdout)
	}
}

func TestLocalManager_ExecuteCommand_NotRunning(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewLocalManager(tmpDir)
	ctx := context.Background()

	env, _ := mgr.Create(ctx, "agent-1", EnvironmentConfig{})
	mgr.Stop(ctx, env.ID)

	_, err := mgr.ExecuteCommand(ctx, env.ID, "echo hello")
	if err == nil {
		t.Error("expected error when executing in stopped environment")
	}
}

func TestLocalManager_FileOperations(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewLocalManager(tmpDir)
	ctx := context.Background()

	env, _ := mgr.Create(ctx, "agent-1", EnvironmentConfig{})

	// 上传文件
	content := []byte("hello world")
	if err := mgr.UploadFile(ctx, env.ID, "test.txt", content); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// 下载文件
	downloaded, err := mgr.DownloadFile(ctx, env.ID, "test.txt")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(downloaded) != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", string(downloaded))
	}

	// 列出文件
	files, err := mgr.ListFiles(ctx, env.ID, ".")
	if err != nil {
		t.Fatalf("list files: %v", err)
	}

	found := false
	for _, f := range files {
		if f.Name == "test.txt" {
			found = true
			if f.Size != int64(len(content)) {
				t.Errorf("expected size %d, got %d", len(content), f.Size)
			}
		}
	}
	if !found {
		t.Error("expected test.txt in file list")
	}
}

func TestLocalManager_GetMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewLocalManager(tmpDir)
	ctx := context.Background()

	env, _ := mgr.Create(ctx, "agent-1", EnvironmentConfig{})

	metrics, err := mgr.GetMetrics(ctx, env.ID)
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}

	if metrics.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

func TestLocalManager_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewLocalManager(tmpDir)
	ctx := context.Background()

	_, err := mgr.Get(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent environment")
	}

	err = mgr.Destroy(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent environment")
	}

	_, err = mgr.ExecuteCommand(ctx, "nonexistent", "echo hello")
	if err == nil {
		t.Error("expected error for nonexistent environment")
	}
}

func TestApplyEnvDefaults(t *testing.T) {
	config := &EnvironmentConfig{}
	applyEnvDefaults(config)

	if config.Type != EnvTypeContainer {
		t.Errorf("expected container, got %s", config.Type)
	}
	if config.Image != "ubuntu:22.04" {
		t.Errorf("expected ubuntu:22.04, got %s", config.Image)
	}
	if config.Resources.CPU != "1" {
		t.Errorf("expected '1', got '%s'", config.Resources.CPU)
	}
	if config.Resources.Memory != "512Mi" {
		t.Errorf("expected '512Mi', got '%s'", config.Resources.Memory)
	}
	if config.Resources.Disk != "1Gi" {
		t.Errorf("expected '1Gi', got '%s'", config.Resources.Disk)
	}
}

func TestService_CreateEnvironment(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewLocalManager(tmpDir)
	repo := NewMemoryRepository()
	svc := NewService(mgr, repo)
	ctx := context.Background()

	env, err := svc.CreateEnvironment(ctx, "agent-1", CreateEnvRequest{
		Type:  EnvTypeContainer,
		Image: "ubuntu:22.04",
		Resources: Resources{
			CPU:    "2",
			Memory: "1Gi",
			Disk:   "5Gi",
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if env.AgentID != "agent-1" {
		t.Errorf("expected agent-1, got %s", env.AgentID)
	}
	if env.Resources.CPU != "2" {
		t.Errorf("expected CPU '2', got '%s'", env.Resources.CPU)
	}
}

func TestService_DestroyEnvironment(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewLocalManager(tmpDir)
	repo := NewMemoryRepository()
	svc := NewService(mgr, repo)
	ctx := context.Background()

	env, _ := svc.CreateEnvironment(ctx, "agent-1", CreateEnvRequest{})

	if err := svc.DestroyEnvironment(ctx, env.ID); err != nil {
		t.Fatalf("destroy: %v", err)
	}

	_, err := svc.GetEnvironment(ctx, env.ID)
	if err == nil {
		t.Error("expected error after destroy")
	}
}

func TestService_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewLocalManager(tmpDir)
	repo := NewMemoryRepository()
	svc := NewService(mgr, repo)
	ctx := context.Background()

	env, _ := svc.CreateEnvironment(ctx, "agent-1", CreateEnvRequest{})

	// Stop
	if err := svc.StopEnvironment(ctx, env.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Start
	if err := svc.StartEnvironment(ctx, env.ID); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Pause
	if err := svc.PauseEnvironment(ctx, env.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}

	// Resume
	if err := svc.ResumeEnvironment(ctx, env.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
}

func TestService_ExecuteCommand(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewLocalManager(tmpDir)
	repo := NewMemoryRepository()
	svc := NewService(mgr, repo)
	ctx := context.Background()

	env, _ := svc.CreateEnvironment(ctx, "agent-1", CreateEnvRequest{})

	result, err := svc.ExecuteCommand(ctx, env.ID, ExecRequest{
		Command: "echo test",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Stdout != "test\n" {
		t.Errorf("expected 'test\\n', got '%s'", result.Stdout)
	}
}

func TestService_FileOperations(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewLocalManager(tmpDir)
	repo := NewMemoryRepository()
	svc := NewService(mgr, repo)
	ctx := context.Background()

	env, _ := svc.CreateEnvironment(ctx, "agent-1", CreateEnvRequest{})

	// Upload
	if err := svc.UploadFile(ctx, env.ID, "hello.txt", []byte("world")); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// Download
	content, err := svc.DownloadFile(ctx, env.ID, "hello.txt")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(content) != "world" {
		t.Errorf("expected 'world', got '%s'", string(content))
	}

	// List
	files, err := svc.ListFiles(ctx, env.ID, ".")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(files) == 0 {
		t.Error("expected at least one file")
	}
}
