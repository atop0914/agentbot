// Package config 负责 AgentBot 的运行时配置：默认值 → 配置文件 → 环境变量
// 三层覆盖，并在加载末尾对**机密**做 fail-fast 校验。
//
// 设计取舍（Day 28 部署配置分层）：
//
//  1. 全项目只有这一处读取环境变量。业务模块一律不直接 os.Getenv ——
//     散落的读取会让「这个部署实际用了哪个值」无法回答，也无法在启动期
//     统一校验。
//  2. 机密（JWT secret / SSO client secret / DB 密码）**没有可用默认值**。
//     缺失时 Load 返回错误、进程拒绝启动，而不是用一个公开的 dev 值静默
//     跑起来。启动失败是响亮的，静默降级是无声的。
package config

import (
	"fmt"
	"strings"
	"time"
)

// EnvPrefix 是所有环境变量的统一前缀。
const EnvPrefix = "AGENTBOT_"

// Config 是应用的完整运行时配置。
//
// 分层刻意保持扁平：每个子结构对应一个业务关注点，字段名与环境变量
// 段名一一对应（Server.Port ↔ AGENTBOT_SERVER_PORT），避免出现
// 「配置文件叫 port、环境变量叫 http_port」这种需要额外记忆的映射。
type Config struct {
	Server     ServerConfig     `json:"server"`
	Database   DatabaseConfig   `json:"database"`
	Redis      RedisConfig      `json:"redis"`
	Auth       AuthConfig       `json:"auth"`
	SSO        SSOConfig        `json:"sso"`
	Agent      AgentConfig      `json:"agent"`
	Browser    BrowserConfig    `json:"browser"`
	Cloud      CloudConfig      `json:"cloud"`
	FileSystem FileSystemConfig `json:"filesystem"`
	Log        LogConfig        `json:"log"`
	Metrics    MetricsConfig    `json:"metrics"`
	Storage    StorageConfig    `json:"storage"`
	Admin      AdminConfig      `json:"admin"`
}

// ServerConfig 是 HTTP 服务端配置。
type ServerConfig struct {
	Host         string        `json:"host"`
	Port         int           `json:"port"`
	ReadTimeout  time.Duration `json:"read_timeout"`
	WriteTimeout time.Duration `json:"write_timeout"`
	IdleTimeout  time.Duration `json:"idle_timeout"`
}

// Addr 返回可直接传给 net/http 的监听地址。
func (c ServerConfig) Addr() string {
	host := c.Host
	if host == "" {
		host = "0.0.0.0"
	}
	return fmt.Sprintf("%s:%d", host, c.Port)
}

// DatabaseConfig 是关系型数据库配置。
//
// Password 是机密：没有默认值，缺失时拒绝启动。
type DatabaseConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"db_name"`
	SSLMode  string `json:"ssl_mode"`
}

// DSN 返回 PostgreSQL 连接串。
//
// 注意：把所有字段都拼进来（包括 ssl_mode），避免出现「配置文件写了
// ssl_mode=require 但代码没读」这种看起来加密实际裸奔的情况。
func (c DatabaseConfig) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		c.User, c.Password, c.Host, c.Port, c.DBName, c.SSLMode)
}

// RedisConfig 是 Redis 配置。
type RedisConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Password string `json:"password"`
	DB       int    `json:"db"`
}

// Addr 返回 host:port。
func (c RedisConfig) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// AuthConfig 是认证与令牌配置。
type AuthConfig struct {
	// JWTSecret 是令牌签名密钥，**机密**：无默认值，长度下限 32 字节。
	JWTSecret     string        `json:"jwt_secret"`
	AccessExpiry  time.Duration `json:"access_expiry"`
	RefreshExpiry time.Duration `json:"refresh_expiry"`
	Issuer        string        `json:"issuer"`
}

// SSOConfig 是企业 OIDC 接入配置。
//
// 与 internal/sso.Config 刻意分开：这里只管「从环境/文件读到了什么」，
// 由装配层转换成 sso.Config 并调用其 Normalize 做语义校验。
// 两处都保留校验不是冗余 —— 配置层拦「没给」，业务层拦「给错了」。
type SSOConfig struct {
	Issuer       string        `json:"issuer"`
	ClientID     string        `json:"client_id"`
	ClientSecret string        `json:"client_secret"`
	RedirectURL  string        `json:"redirect_url"`
	Scopes       []string      `json:"scopes,omitempty"`
	AllowedAlgs  []string      `json:"allowed_algs,omitempty"`
	ClockSkew    time.Duration `json:"clock_skew"`
}

// Configured 表示该部署是否启用了 SSO。
//
// 三个必填项缺任一即视为未启用：未启用的部署所有 SSO 入口返回 503，
// 而**不是**放行 —— 这是从 authz 包延续下来的默认拒绝基线。
func (c SSOConfig) Configured() bool {
	return strings.TrimSpace(c.Issuer) != "" &&
		strings.TrimSpace(c.ClientID) != "" &&
		strings.TrimSpace(c.RedirectURL) != ""
}

// AgentConfig 是 Agent 运行时默认参数。
type AgentConfig struct {
	DefaultModel       string        `json:"default_model"`
	MaxTokens          int           `json:"max_tokens"`
	DefaultTemperature float64       `json:"default_temperature"`
	MaxConcurrent      int           `json:"max_concurrent"`
	TaskTimeout        time.Duration `json:"task_timeout"`
	MemorySize         int           `json:"memory_size"`
}

// BrowserConfig 是浏览器自动化配置。
type BrowserConfig struct {
	Headless       bool          `json:"headless"`
	ExecutablePath string        `json:"executable_path,omitempty"`
	UserDataDir    string        `json:"user_data_dir,omitempty"`
	Proxy          string        `json:"proxy,omitempty"`
	Timeout        time.Duration `json:"timeout"`
}

// CloudConfig 是云环境（容器）配置。
type CloudConfig struct {
	Provider     string `json:"provider"`
	DockerHost   string `json:"docker_host,omitempty"`
	KubeConfig   string `json:"kube_config,omitempty"`
	Namespace    string `json:"namespace,omitempty"`
	DefaultImage string `json:"default_image"`
	MaxEnvs      int    `json:"max_envs"`
}

// FileSystemConfig 是 Agent 文件系统的沙箱根目录。
type FileSystemConfig struct {
	Root string `json:"root"`
}

// LogConfig 是日志配置。
type LogConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
	Output string `json:"output"`
	File   string `json:"file,omitempty"`
}

// MetricsConfig 是 /metrics 端点配置。
type MetricsConfig struct {
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port"`
	Path    string `json:"path"`
}

// StorageConfig 是对象/文件存储配置。
type StorageConfig struct {
	Provider   string `json:"provider"`
	LocalPath  string `json:"local_path,omitempty"`
	S3Bucket   string `json:"s3_bucket,omitempty"`
	S3Region   string `json:"s3_region,omitempty"`
	S3Endpoint string `json:"s3_endpoint,omitempty"`
}

// AdminConfig 是管理控制台配置。
type AdminConfig struct {
	// ConsoleDir 是前端构建产物目录；目录不存在时后台返回占位页。
	ConsoleDir string `json:"console_dir"`
}

// Default 返回内置默认值。
//
// 这里的每一项都必须有**安全的**默认值：默认值会成为 compose / k8s 里
// 没写该字段时的实际行为，所以「默认开着」和「默认关着」要有明确理由。
func Default() Config {
	return Config{
		Server: ServerConfig{
			Host:         "0.0.0.0",
			Port:         8080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  120 * time.Second,
		},
		Database: DatabaseConfig{
			Host:    "localhost",
			Port:    5432,
			User:    "agentbot",
			DBName:  "agentbot",
			SSLMode: "disable",
			// Password 刻意留空：机密无默认值。
		},
		Redis: RedisConfig{
			Host: "localhost",
			Port: 6379,
		},
		Auth: AuthConfig{
			AccessExpiry:  1 * time.Hour,
			RefreshExpiry: 7 * 24 * time.Hour,
			Issuer:        "agentbot",
		},
		SSO: SSOConfig{
			AllowedAlgs: []string{"RS256"},
			ClockSkew:   60 * time.Second,
		},
		Agent: AgentConfig{
			DefaultModel:       "gpt-4o-mini",
			MaxTokens:          4096,
			DefaultTemperature: 0.7,
			MaxConcurrent:      10,
			TaskTimeout:        30 * time.Minute,
			MemorySize:         100,
		},
		Browser: BrowserConfig{
			Headless: true,
			Timeout:  30 * time.Second,
		},
		Cloud: CloudConfig{
			Provider:     "docker",
			Namespace:    "agentbot",
			DefaultImage: "agentbot/sandbox:latest",
			MaxEnvs:      100,
		},
		FileSystem: FileSystemConfig{
			Root: "/var/lib/agentbot/fs",
		},
		Log: LogConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
		Metrics: MetricsConfig{
			Enabled: true,
			Port:    9090,
			Path:    "/metrics",
		},
		Storage: StorageConfig{
			Provider:  "local",
			LocalPath: "/var/lib/agentbot/storage",
		},
		Admin: AdminConfig{
			ConsoleDir: "/srv/agentbot/web",
		},
	}
}

// DevelopmentDefault 返回开发/测试用配置：在 Default 之上补齐**仅供本地**的
// 机密，让 `go test` 与本地 `go run` 不需要额外注入就能跑起来。
//
// ⚠️ 这个函数绝不能被生产入口调用。它与「机密无默认值」的原则并不冲突：
// 生产路径只走 Load，而 Load 不会调用它；这里把 dev 机密集中到一个函数里，
// 是为了让「哪些值是假的」一眼可查，而不是散落在各处。
func DevelopmentDefault() Config {
	cfg := Default()
	cfg.Auth.JWTSecret = "agentbot-development-only-secret-0000000000000000"
	// 数据库在开发期不是必需依赖（当前存储实现是内存仓库），
	// 但装配层统一要求非空，所以给一个明确表述「本地」的值。
	cfg.Database.Password = "agentbot-development-only-db-password"
	cfg.FileSystem.Root = "/tmp/agentbot-fs"
	cfg.Storage.LocalPath = "/tmp/agentbot-storage"
	cfg.Admin.ConsoleDir = "web/admin/dist"
	return cfg
}

// Validate 校验配置的语义正确性。
//
// 校验分两类：
//   - **机密类**：缺失即错误（fail-fast），没有回退。
//   - **普通类**：值不合法即错误，但不允许静默 clamp（把 0 端口改成 8080
//     会让「我配了但没生效」变成一次无声的调试）。
func (c Config) Validate() error {
	var problems []string

	// --- 机密 ---
	if isPlaceholderSecret(c.Auth.JWTSecret) {
		problems = append(problems,
			"auth.jwt_secret is required (set "+EnvPrefix+"AUTH_JWT_SECRET); it must not be a placeholder")
	} else if len(c.Auth.JWTSecret) < MinJWTSecretLength {
		problems = append(problems, fmt.Sprintf(
			"auth.jwt_secret is too short: %d bytes, need at least %d",
			len(c.Auth.JWTSecret), MinJWTSecretLength))
	}
	if isPlaceholderSecret(c.Database.Password) {
		problems = append(problems,
			"database.password is required (set "+EnvPrefix+"DATABASE_PASSWORD)")
	}
	// SSO 未启用时不要求 client secret：未启用的入口返回 503，
	// 不因为「没配 SSO」而拒绝整个进程启动。
	if c.SSO.Configured() && isPlaceholderSecret(c.SSO.ClientSecret) {
		problems = append(problems,
			"sso.client_secret is required when SSO is configured (set "+EnvPrefix+"SSO_CLIENT_SECRET)")
	}

	// --- 服务端 ---
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		problems = append(problems, fmt.Sprintf("server.port out of range: %d", c.Server.Port))
	}

	// --- 依赖服务 ---
	if c.Database.Host == "" {
		problems = append(problems, "database.host is required")
	}
	if c.Database.DBName == "" {
		problems = append(problems, "database.db_name is required")
	}
	switch c.Database.SSLMode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		problems = append(problems, fmt.Sprintf(
			"database.ssl_mode %q is not a valid PostgreSQL sslmode", c.Database.SSLMode))
	}
	// Redis 目前只是一个**预留的配置面**：没有任何代码路径 import go-redis，
	// 持久化仍是内存实现。因此 redis.host 为空表示「未配置 Redis」，是合法状态；
	// 若把非空校验留在这里，用户显式写 redis.host="" 时反而会被拒绝 ——
	// 那恰好是「我不用 Redis」的正确表达。
	// 仅当**配了** host 时才校验端口是否可用。
	if c.Redis.Host != "" {
		if c.Redis.Port < 1 || c.Redis.Port > 65535 {
			problems = append(problems, fmt.Sprintf("redis.port out of range: %d", c.Redis.Port))
		}
	}

	// --- 日志 / 指标 ---
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, fmt.Sprintf("log.level %q is not one of debug/info/warn/error", c.Log.Level))
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		problems = append(problems, fmt.Sprintf("log.format %q is not one of json/text", c.Log.Format))
	}
	if c.Metrics.Enabled {
		if c.Metrics.Port < 1 || c.Metrics.Port > 65535 {
			problems = append(problems, fmt.Sprintf("metrics.port out of range: %d", c.Metrics.Port))
		}
		if c.Metrics.Port == c.Server.Port {
			problems = append(problems,
				"metrics.port must differ from server.port (same port would silently fail to bind)")
		}
		if !strings.HasPrefix(c.Metrics.Path, "/") {
			problems = append(problems, fmt.Sprintf("metrics.path %q must start with /", c.Metrics.Path))
		}
	}

	// --- 额度 ---
	if c.Agent.MaxConcurrent < 1 {
		problems = append(problems, fmt.Sprintf("agent.max_concurrent must be >= 1, got %d", c.Agent.MaxConcurrent))
	}
	if c.Cloud.MaxEnvs < 1 {
		problems = append(problems, fmt.Sprintf("cloud.max_envs must be >= 1, got %d", c.Cloud.MaxEnvs))
	}
	if c.Agent.MaxTokens < 1 {
		problems = append(problems, fmt.Sprintf("agent.max_tokens must be >= 1, got %d", c.Agent.MaxTokens))
	}
	if c.Agent.MemorySize < 1 {
		problems = append(problems, fmt.Sprintf("agent.memory_size must be >= 1, got %d", c.Agent.MemorySize))
	}
	if c.Agent.DefaultTemperature < 0 || c.Agent.DefaultTemperature > 2 {
		problems = append(problems, fmt.Sprintf(
			"agent.default_temperature out of range [0,2]: %v", c.Agent.DefaultTemperature))
	}

	// --- 超时（<=0 意味着「没有超时」，是可利用的挂起面）---
	if c.Server.ReadTimeout <= 0 {
		problems = append(problems, "server.read_timeout must be > 0")
	}
	if c.Server.WriteTimeout <= 0 {
		problems = append(problems, "server.write_timeout must be > 0")
	}
	if c.Agent.TaskTimeout <= 0 {
		problems = append(problems, "agent.task_timeout must be > 0")
	}
	if c.Auth.AccessExpiry <= 0 {
		problems = append(problems, "auth.access_expiry must be > 0")
	}
	if c.Auth.RefreshExpiry <= 0 {
		problems = append(problems, "auth.refresh_expiry must be > 0")
	}
	if c.Auth.RefreshExpiry <= c.Auth.AccessExpiry {
		problems = append(problems,
			"auth.refresh_expiry must be greater than auth.access_expiry (otherwise refresh loops fail)")
	}

	// --- 路径 ---
	if strings.TrimSpace(c.FileSystem.Root) == "" {
		problems = append(problems, "filesystem.root is required")
	}
	if strings.TrimSpace(c.Storage.LocalPath) == "" && c.Storage.Provider == "local" {
		problems = append(problems, "storage.local_path is required when storage.provider is local")
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(problems, "; "))
	}
	return nil
}
