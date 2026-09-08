package config

import (
	"encoding/json"
	"os"
	"time"
)

// Config holds application configuration
type Config struct {
	Server     ServerConfig     `json:"server"`
	Database   DatabaseConfig   `json:"database"`
	Redis      RedisConfig      `json:"redis"`
	Auth       AuthConfig       `json:"auth"`
	Agent      AgentConfig      `json:"agent"`
	Browser    BrowserConfig    `json:"browser"`
	Cloud      CloudConfig      `json:"cloud"`
	Log        LogConfig        `json:"log"`
	Metrics    MetricsConfig    `json:"metrics"`
	Storage    StorageConfig    `json:"storage"`
}

// ServerConfig defines HTTP server configuration
type ServerConfig struct {
	Host         string        `json:"host"`
	Port         int           `json:"port"`
	ReadTimeout  time.Duration `json:"read_timeout"`
	WriteTimeout time.Duration `json:"write_timeout"`
	IdleTimeout  time.Duration `json:"idle_timeout"`
}

// DatabaseConfig defines database configuration
type DatabaseConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"db_name"`
	SSLMode  string `json:"ssl_mode"`
}

// RedisConfig defines Redis configuration
type RedisConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Password string `json:"password"`
	DB       int    `json:"db"`
}

// AuthConfig defines authentication configuration
type AuthConfig struct {
	JWTSecret      string        `json:"jwt_secret"`
	TokenExpiry    time.Duration `json:"token_expiry"`
	RefreshExpiry  time.Duration `json:"refresh_expiry"`
	OAuthProviders []OAuthProvider `json:"oauth_providers,omitempty"`
}

// OAuthProvider defines OAuth provider configuration
type OAuthProvider struct {
	Name         string   `json:"name"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	RedirectURL  string   `json:"redirect_url"`
	Scopes       []string `json:"scopes"`
}

// AgentConfig defines agent configuration
type AgentConfig struct {
	DefaultModel       string        `json:"default_model"`
	MaxTokens          int           `json:"max_tokens"`
	DefaultTemperature float64       `json:"default_temperature"`
	MaxConcurrent      int           `json:"max_concurrent"`
	TaskTimeout        time.Duration `json:"task_timeout"`
	MemorySize         int           `json:"memory_size"`
}

// BrowserConfig defines browser configuration
type BrowserConfig struct {
	Headless     bool   `json:"headless"`
	ExecutablePath string `json:"executable_path,omitempty"`
	UserDataDir  string `json:"user_data_dir,omitempty"`
	Proxy        string `json:"proxy,omitempty"`
	Timeout      time.Duration `json:"timeout"`
}

// CloudConfig defines cloud environment configuration
type CloudConfig struct {
	Provider     string `json:"provider"` // "docker", "kubernetes"
	DockerHost   string `json:"docker_host,omitempty"`
	KubeConfig   string `json:"kube_config,omitempty"`
	Namespace    string `json:"namespace,omitempty"`
	DefaultImage string `json:"default_image"`
	MaxEnvs      int    `json:"max_envs"`
}

// LogConfig defines logging configuration
type LogConfig struct {
	Level  string `json:"level"`  // "debug", "info", "warn", "error"
	Format string `json:"format"` // "json", "text"
	Output string `json:"output"` // "stdout", "file"
	File   string `json:"file,omitempty"`
}

// MetricsConfig defines metrics configuration
type MetricsConfig struct {
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port"`
	Path    string `json:"path"`
}

// StorageConfig defines storage configuration
type StorageConfig struct {
	Provider   string `json:"provider"` // "local", "s3", "gcs"
	LocalPath  string `json:"local_path,omitempty"`
	S3Bucket   string `json:"s3_bucket,omitempty"`
	S3Region   string `json:"s3_region,omitempty"`
	S3Endpoint string `json:"s3_endpoint,omitempty"`
}

// Load loads configuration from file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// Set defaults
	setDefaults(&cfg)

	return &cfg, nil
}

// setDefaults sets default values
func setDefaults(cfg *Config) {
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = 30 * time.Second
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 30 * time.Second
	}
	if cfg.Server.IdleTimeout == 0 {
		cfg.Server.IdleTimeout = 120 * time.Second
	}
	if cfg.Database.Port == 0 {
		cfg.Database.Port = 5432
	}
	if cfg.Database.SSLMode == "" {
		cfg.Database.SSLMode = "disable"
	}
	if cfg.Redis.Port == 0 {
		cfg.Redis.Port = 6379
	}
	if cfg.Auth.TokenExpiry == 0 {
		cfg.Auth.TokenExpiry = 24 * time.Hour
	}
	if cfg.Auth.RefreshExpiry == 0 {
		cfg.Auth.RefreshExpiry = 7 * 24 * time.Hour
	}
	if cfg.Agent.MaxTokens == 0 {
		cfg.Agent.MaxTokens = 4096
	}
	if cfg.Agent.DefaultTemperature == 0 {
		cfg.Agent.DefaultTemperature = 0.7
	}
	if cfg.Agent.MaxConcurrent == 0 {
		cfg.Agent.MaxConcurrent = 10
	}
	if cfg.Agent.TaskTimeout == 0 {
		cfg.Agent.TaskTimeout = 30 * time.Minute
	}
	if cfg.Agent.MemorySize == 0 {
		cfg.Agent.MemorySize = 100
	}
	if cfg.Browser.Timeout == 0 {
		cfg.Browser.Timeout = 30 * time.Second
	}
	if cfg.Cloud.DefaultImage == "" {
		cfg.Cloud.DefaultImage = "agentbot/sandbox:latest"
	}
	if cfg.Cloud.MaxEnvs == 0 {
		cfg.Cloud.MaxEnvs = 100
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Log.Format == "" {
		cfg.Log.Format = "json"
	}
	if cfg.Metrics.Path == "" {
		cfg.Metrics.Path = "/metrics"
	}
}
