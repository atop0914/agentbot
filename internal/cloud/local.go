package cloud

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/atop0914/agentbot/internal/sandbox"
)

// LocalManager 基于本地进程的环境管理器
// 用于开发和测试环境，不依赖 Docker
type LocalManager struct {
	mu           sync.RWMutex
	environments map[string]*localEnv
	workDir      string
	// sandbox 为每个环境的命令执行施加资源限制。
	// 为 nil 时退化为无限制执行 —— 那是一个**必须被察觉**的状态，
	// 因此 NewLocalManager 总会初始化它（见构造函数注释）。
	sandbox *sandbox.Manager
	// execTimeout 是单条命令的 wall-clock 上限（0 = 使用 defaultExecTimeout）
	execTimeout time.Duration
}

// localEnv 包装本地环境的额外状态
type localEnv struct {
	*Environment
	process  *os.Process
	workPath string // 环境工作目录
}

// NewLocalManager 创建本地管理器
func NewLocalManager(workDir string) (*LocalManager, error) {
	if workDir == "" {
		workDir = filepath.Join(os.TempDir(), "agentbot-envs")
	}
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}
	return &LocalManager{
		environments: make(map[string]*localEnv),
		workDir:      workDir,
		sandbox:      sandbox.New(sandbox.DefaultConfig()),
	}, nil
}

// SandboxConfig 覆盖沙箱限制配置。
//
// ⚠️ cgroup 的能力探测发生在真正 Apply 的时刻（见 sandbox.New 注释），
// 因此这里不需要（也无法）在装配阶段判断「本机支不支持 cgroup」。
func (m *LocalManager) SandboxConfig(cfg sandbox.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sandbox = sandbox.New(cfg)
}

// defaultExecTimeout 是单条命令的 wall-clock 上限。
//
// 取值理由：本仓库的典型命令是 go build / npm install 这类分钟级操作，
// 60s 会误伤它们。这里取 5 分钟 —— 足够跑完一次完整构建，
// 又不至于让失控命令占用宿主太久。
const defaultExecTimeout = 5 * time.Minute

// ExecTimeout 返回单条命令的 wall-clock 上限。
func (m *LocalManager) ExecTimeout() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.execTimeout <= 0 {
		return defaultExecTimeout
	}
	return m.execTimeout
}

// SetExecTimeout 覆盖单条命令的超时上限（<=0 时恢复默认）。
func (m *LocalManager) SetExecTimeout(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execTimeout = d
}

// Sandbox 返回当前的沙箱管理器（可能为 nil，调用方需自行判空）。
func (m *LocalManager) Sandbox() *sandbox.Manager {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sandbox
}

// Create 创建新的本地环境
func (m *LocalManager) Create(ctx context.Context, agentID string, config EnvironmentConfig) (*Environment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := uuid.New().String()
	now := time.Now().UTC()

	envPath := filepath.Join(m.workDir, id)
	if err := os.MkdirAll(envPath, 0755); err != nil {
		return nil, fmt.Errorf("create env dir: %w", err)
	}

	env := &Environment{
		ID:        id,
		AgentID:   agentID,
		Type:      config.Type,
		State:     EnvStateCreating,
		Resources: config.Resources,
		Network:   config.Network,
		CreatedAt: now,
		UpdatedAt: now,
	}

	local := &localEnv{
		Environment: env,
		workPath:    envPath,
	}

	// 模拟创建过程
	env.State = EnvStateRunning
	env.UpdatedAt = time.Now().UTC()

	m.environments[id] = local

	copy := *env
	return &copy, nil
}

// Destroy 销毁环境
func (m *LocalManager) Destroy(ctx context.Context, envID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	local, ok := m.environments[envID]
	if !ok {
		return fmt.Errorf("environment %s not found", envID)
	}

	// 终止进程（如果有）。process 句柄当前不会被真正填充（本地管理器只模拟
	// 生命周期），Kill 失败也没有补救动作。
	if local.process != nil {
		_ = local.process.Kill()
		local.process = nil
	}

	// 清理工作目录
	os.RemoveAll(local.workPath)

	delete(m.environments, envID)
	return nil
}

// Get 获取环境信息
func (m *LocalManager) Get(ctx context.Context, envID string) (*Environment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	local, ok := m.environments[envID]
	if !ok {
		return nil, fmt.Errorf("environment %s not found", envID)
	}

	copy := *local.Environment
	return &copy, nil
}

// List 列出指定 Agent 的所有环境
func (m *LocalManager) List(ctx context.Context, agentID string) ([]*Environment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Environment, 0)
	for _, local := range m.environments {
		if local.AgentID == agentID {
			copy := *local.Environment
			result = append(result, &copy)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	return result, nil
}

// Start 启动环境
func (m *LocalManager) Start(ctx context.Context, envID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	local, ok := m.environments[envID]
	if !ok {
		return fmt.Errorf("environment %s not found", envID)
	}

	if local.State != EnvStateStopped && local.State != EnvStatePaused {
		return fmt.Errorf("cannot start environment in state %s", local.State)
	}

	local.State = EnvStateRunning
	local.UpdatedAt = time.Now().UTC()
	return nil
}

// Stop 停止环境
func (m *LocalManager) Stop(ctx context.Context, envID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	local, ok := m.environments[envID]
	if !ok {
		return fmt.Errorf("environment %s not found", envID)
	}

	if local.State != EnvStateRunning && local.State != EnvStatePaused {
		return fmt.Errorf("cannot stop environment in state %s", local.State)
	}

	// 终止进程（同上：句柄未填充，失败无补救动作）
	if local.process != nil {
		_ = local.process.Kill()
		local.process = nil
	}

	local.State = EnvStateStopped
	local.UpdatedAt = time.Now().UTC()
	return nil
}

// Pause 暂停环境
func (m *LocalManager) Pause(ctx context.Context, envID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	local, ok := m.environments[envID]
	if !ok {
		return fmt.Errorf("environment %s not found", envID)
	}

	if local.State != EnvStateRunning {
		return fmt.Errorf("cannot pause environment in state %s", local.State)
	}

	local.State = EnvStatePaused
	local.UpdatedAt = time.Now().UTC()
	return nil
}

// Resume 恢复环境
func (m *LocalManager) Resume(ctx context.Context, envID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	local, ok := m.environments[envID]
	if !ok {
		return fmt.Errorf("environment %s not found", envID)
	}

	if local.State != EnvStatePaused {
		return fmt.Errorf("cannot resume environment in state %s", local.State)
	}

	local.State = EnvStateRunning
	local.UpdatedAt = time.Now().UTC()
	return nil
}

// ExecuteCommand 在环境中执行命令
func (m *LocalManager) ExecuteCommand(ctx context.Context, envID string, command string) (*ExecResult, error) {
	m.mu.RLock()
	local, ok := m.environments[envID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("environment %s not found", envID)
	}

	if local.State != EnvStateRunning {
		return nil, fmt.Errorf("environment %s is not running (state: %s)", envID, local.State)
	}

	start := time.Now()

	// ⚠️ 这里原先直接 exec.CommandContext("sh", "-c", command)，命令在宿主机上
	// **毫无边界**地运行。改为经 sandbox 施加 cgroup v2 + rlimit 限制。
	//
	// 同时这也修掉一个静默挂死的隐患：直接 cmd.Run() 在「脚本里带后台任务」
	// 时不会返回 —— 后台孙进程持有输出管道的写端，Wait 会一直等管道关闭
	// （实测：即使 sh 已被 SIGKILL，Wait 仍阻塞）。sandbox.Run 用
	// wall-clock 上限兜住这种情形并按进程组回收整棵树。
	sbx := m.Sandbox()
	if sbx == nil {
		// 构造时总会初始化沙箱；走到这里说明装配被绕过，
		// 直接报错而不是悄悄退回无限制执行 —— 「没有边界的沙箱」比明确的失败更危险。
		return nil, fmt.Errorf("cloud: sandbox is not configured for environment %s", envID)
	}

	// ⚠️ 这里必须给一个**明确的** wall-clock 上限，不能传 0 让沙箱取默认值。
	//
	// 原因：`sh -c "cmd & sleep 30"` 这类脚本里，sh 自己会一直等到
	// 后台任务结束才退出（实测证实：即使把后台任务的输出重定向或 disown，
	// sh 仍然在等）。此时 "命令是否执行完" 与 "sh 是否退出" 是两件事，
	// 而调用方需要的是前者 —— 后台任务应当立刻返回。
	//
	// 因此采用「等待主命令的合理时长，超时即按进程组回收整棵树」的策略：
	// 一个正常命令（go build / npm install）需要的时间远小于这个上限；
	// 真超时的命令应当被中断并且不留孤儿。上限本身可经 ExecTimeout 配置。
	res, err := sbx.Run(ctx, local.AgentID, local.workPath, command, m.ExecTimeout())
	if err != nil {
		return nil, fmt.Errorf("execute command in %s: %w", envID, err)
	}
	duration := time.Since(start)

	// 把实际生效的限制暴露出来：调用方（与运维）需要能区分
	// 「命令真的被限制了」与「降级到只有 rlimit」。
	if !res.Applied.AnyEnforced() {
		return nil, fmt.Errorf("cloud: environment %s ran without any resource limit (%s)",
			envID, res.Applied.String())
	}

	return &ExecResult{
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		Duration: duration,
	}, nil
}

// resolveInWorkPath 把请求路径解析到环境工作目录之内，越界即报错。
//
// 这里修的是一个「看起来在做校验、实际什么都没做」的空分支：
// 原实现判断了 `..` 与 `/` 前缀，分支体是空的，于是
// filepath.Join(workPath, "../../etc/passwd") 会**真的**写到工作目录之外；
// 下载与列举路径上更危险 —— 可以直接读到宿主机的文件。
//
// 判定方式改为「解析后仍在 workPath 内」，而不是靠字符串前缀猜：
// 前缀检查漏得掉的形式太多（`a/../../b`、`..`、URL 编码等），
// 只有在拼接完成后用 filepath.Rel 比对才是可靠的。
func resolveInWorkPath(workPath, path string) (string, error) {
	if path == "" {
		return workPath, nil
	}
	cleanPath := filepath.Clean(path)
	if filepath.IsAbs(cleanPath) {
		return "", fmt.Errorf("path %q must be relative to the environment work directory", path)
	}

	fullPath := filepath.Join(workPath, cleanPath)
	rel, err := filepath.Rel(workPath, fullPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the environment work directory", path)
	}
	return fullPath, nil
}

// UploadFile 上传文件到环境
func (m *LocalManager) UploadFile(ctx context.Context, envID string, path string, content []byte) error {
	m.mu.RLock()
	local, ok := m.environments[envID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("environment %s not found", envID)
	}

	fullPath, err := resolveInWorkPath(local.workPath, path)
	if err != nil {
		return err
	}

	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

// DownloadFile 从环境下载文件
func (m *LocalManager) DownloadFile(ctx context.Context, envID string, path string) ([]byte, error) {
	m.mu.RLock()
	local, ok := m.environments[envID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("environment %s not found", envID)
	}

	fullPath, err := resolveInWorkPath(local.workPath, path)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	return content, nil
}

// ListFiles 列出环境中的文件
func (m *LocalManager) ListFiles(ctx context.Context, envID string, path string) ([]FileInfo, error) {
	m.mu.RLock()
	local, ok := m.environments[envID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("environment %s not found", envID)
	}

	fullPath, err := resolveInWorkPath(local.workPath, path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, fmt.Errorf("read directory: %w", err)
	}

	files := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, FileInfo{
			Name:    entry.Name(),
			Path:    filepath.Join(path, entry.Name()),
			Size:    info.Size(),
			IsDir:   entry.IsDir(),
			ModTime: info.ModTime(),
			Mode:    info.Mode().String(),
		})
	}

	return files, nil
}

// GetMetrics 返回资源使用指标（本地模式返回模拟数据）
func (m *LocalManager) GetMetrics(ctx context.Context, envID string) (*Metrics, error) {
	m.mu.RLock()
	_, ok := m.environments[envID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("environment %s not found", envID)
	}

	// 本地模式返回模拟指标
	return &Metrics{
		CPUUsage:    0.0,
		MemoryUsage: 0.0,
		DiskUsage:   0.0,
		NetworkIn:   0,
		NetworkOut:  0,
		Timestamp:   time.Now().UTC(),
	}, nil
}
