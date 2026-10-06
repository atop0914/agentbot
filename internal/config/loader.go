package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// 哨兵错误。调用方（cmd/agentbot）用 errors.Is 区分「配置缺失」
// 与「I/O 失败」，前者要打印可操作的修复提示。
var (
	// ErrInvalidConfig 表示配置语义不合法（必填缺失 / 值越界）。
	ErrInvalidConfig = errors.New("config: invalid configuration")
	// ErrConfigFile 表示配置文件存在但无法读取或解析。
	ErrConfigFile = errors.New("config: cannot read configuration file")
)

// MinJWTSecretLength 是 JWT 签名密钥的最小长度。
//
// 32 字节是 HMAC-SHA256 的摘要长度：短于摘要长度的密钥会把有效强度
// 拉低到密钥长度，属于可离线暴力破解的量级。
const MinJWTSecretLength = 32

// placeholderMarkers 是「看起来配了、实际是占位符」的特征串。
//
// 保留这份清单的原因：样例配置里写 `REPLACE_VIA_ENV_...` 比留空更危险 ——
// 留空会在启动期报「缺失」，而占位符会被当成真值一路跑起来。
var placeholderMarkers = []string{
	"replace", "changeme", "change-me", "change_me",
	"your-", "xxx", "todo", "placeholder", "example.com/secret",
}

// isPlaceholderSecret 判断一个机密值是否缺失或仍是占位符。
func isPlaceholderSecret(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return true
	}
	lower := strings.ToLower(v)
	for _, m := range placeholderMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// Load 按 默认值 → 配置文件 → 环境变量 的顺序装配配置，并在末尾校验。
//
// configPath 为空时跳过文件层（纯环境变量部署，k8s 的常见形态）。
// 文件不存在**不**报错：compose/k8s 里配置常常完全由环境变量提供，
// 此时没有文件是正常状态；文件存在但解析失败才报错。
func Load(configPath string) (Config, error) {
	cfg := Default()

	path := strings.TrimSpace(configPath)
	if path == "" {
		path = strings.TrimSpace(os.Getenv(EnvPrefix + "CONFIG_FILE"))
	}
	if path != "" {
		if err := applyFile(&cfg, path); err != nil {
			return Config{}, err
		}
	}

	if err := applyEnv(&cfg, os.LookupEnv); err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applyFile 用配置文件覆盖默认值。
//
// 覆盖方式是「逐字段赋值」而不是把整个子结构替换掉：如果 1:1 替换，
// 配置文件里只写了 `{"server":{"port":9000}}` 会把 Default 里的
// read_timeout 一起清零，最终被 Validate 判为非法 —— 使用者会以为
// 「只改端口」是允许的，实际必须写全。逐字段覆盖才符合直觉。
func applyFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 显式指定的文件不存在视为错误（用户以为在用但没生效），
			// 而自动探测（AGENTBOT_CONFIG_FILE 未设）时不会走到这里。
			return fmt.Errorf("%w: %s: %v", ErrConfigFile, path, err)
		}
		return fmt.Errorf("%w: %s: %v", ErrConfigFile, path, err)
	}

	// 未知字段直接报错：配置文件里的拼写错误（如 `jwt_secrets`）如果静默
	// 忽略，结果就是「我明明配了但它说没配」—— 最难查的一类问题。
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()

	var file fileConfig
	if err := dec.Decode(&file); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrConfigFile, path, err)
	}
	file.applyTo(cfg)
	return nil
}

// applyEnv 用环境变量覆盖。
//
// lookup 参数化是为了可测：测试注入自造的 map，避免污染进程环境。
func applyEnv(cfg *Config, lookup func(string) (string, bool)) error {
	s := setter{lookup: lookup}

	// server
	s.str(EnvPrefix+"SERVER_HOST", &cfg.Server.Host)
	s.port(EnvPrefix+"SERVER_PORT", &cfg.Server.Port)
	s.dur(EnvPrefix+"SERVER_READ_TIMEOUT", &cfg.Server.ReadTimeout)
	s.dur(EnvPrefix+"SERVER_WRITE_TIMEOUT", &cfg.Server.WriteTimeout)
	s.dur(EnvPrefix+"SERVER_IDLE_TIMEOUT", &cfg.Server.IdleTimeout)

	// database
	s.str(EnvPrefix+"DATABASE_HOST", &cfg.Database.Host)
	s.port(EnvPrefix+"DATABASE_PORT", &cfg.Database.Port)
	s.str(EnvPrefix+"DATABASE_USER", &cfg.Database.User)
	s.str(EnvPrefix+"DATABASE_PASSWORD", &cfg.Database.Password)
	s.str(EnvPrefix+"DATABASE_NAME", &cfg.Database.DBName)
	s.str(EnvPrefix+"DATABASE_SSL_MODE", &cfg.Database.SSLMode)

	// redis
	s.str(EnvPrefix+"REDIS_HOST", &cfg.Redis.Host)
	s.port(EnvPrefix+"REDIS_PORT", &cfg.Redis.Port)
	s.str(EnvPrefix+"REDIS_PASSWORD", &cfg.Redis.Password)
	s.int(EnvPrefix+"REDIS_DB", &cfg.Redis.DB)

	// auth
	s.str(EnvPrefix+"AUTH_JWT_SECRET", &cfg.Auth.JWTSecret)
	s.dur(EnvPrefix+"AUTH_ACCESS_EXPIRY", &cfg.Auth.AccessExpiry)
	s.dur(EnvPrefix+"AUTH_REFRESH_EXPIRY", &cfg.Auth.RefreshExpiry)
	s.str(EnvPrefix+"AUTH_ISSUER", &cfg.Auth.Issuer)

	// sso
	s.str(EnvPrefix+"SSO_ISSUER", &cfg.SSO.Issuer)
	s.str(EnvPrefix+"SSO_CLIENT_ID", &cfg.SSO.ClientID)
	s.str(EnvPrefix+"SSO_CLIENT_SECRET", &cfg.SSO.ClientSecret)
	s.str(EnvPrefix+"SSO_REDIRECT_URL", &cfg.SSO.RedirectURL)
	s.list(EnvPrefix+"SSO_SCOPES", &cfg.SSO.Scopes)
	s.list(EnvPrefix+"SSO_ALLOWED_ALGS", &cfg.SSO.AllowedAlgs)
	s.dur(EnvPrefix+"SSO_CLOCK_SKEW", &cfg.SSO.ClockSkew)

	// agent
	s.str(EnvPrefix+"AGENT_DEFAULT_MODEL", &cfg.Agent.DefaultModel)
	s.int(EnvPrefix+"AGENT_MAX_TOKENS", &cfg.Agent.MaxTokens)
	s.float(EnvPrefix+"AGENT_DEFAULT_TEMPERATURE", &cfg.Agent.DefaultTemperature)
	s.int(EnvPrefix+"AGENT_MAX_CONCURRENT", &cfg.Agent.MaxConcurrent)
	s.dur(EnvPrefix+"AGENT_TASK_TIMEOUT", &cfg.Agent.TaskTimeout)
	s.int(EnvPrefix+"AGENT_MEMORY_SIZE", &cfg.Agent.MemorySize)

	// browser
	s.bool(EnvPrefix+"BROWSER_HEADLESS", &cfg.Browser.Headless)
	s.str(EnvPrefix+"BROWSER_EXECUTABLE_PATH", &cfg.Browser.ExecutablePath)
	s.str(EnvPrefix+"BROWSER_USER_DATA_DIR", &cfg.Browser.UserDataDir)
	s.str(EnvPrefix+"BROWSER_PROXY", &cfg.Browser.Proxy)
	s.dur(EnvPrefix+"BROWSER_TIMEOUT", &cfg.Browser.Timeout)

	// cloud
	s.str(EnvPrefix+"CLOUD_PROVIDER", &cfg.Cloud.Provider)
	s.str(EnvPrefix+"CLOUD_DOCKER_HOST", &cfg.Cloud.DockerHost)
	s.str(EnvPrefix+"CLOUD_KUBE_CONFIG", &cfg.Cloud.KubeConfig)
	s.str(EnvPrefix+"CLOUD_NAMESPACE", &cfg.Cloud.Namespace)
	s.str(EnvPrefix+"CLOUD_DEFAULT_IMAGE", &cfg.Cloud.DefaultImage)
	s.int(EnvPrefix+"CLOUD_MAX_ENVS", &cfg.Cloud.MaxEnvs)

	// filesystem
	s.str(EnvPrefix+"FILESYSTEM_ROOT", &cfg.FileSystem.Root)

	// log
	s.str(EnvPrefix+"LOG_LEVEL", &cfg.Log.Level)
	s.str(EnvPrefix+"LOG_FORMAT", &cfg.Log.Format)
	s.str(EnvPrefix+"LOG_OUTPUT", &cfg.Log.Output)
	s.str(EnvPrefix+"LOG_FILE", &cfg.Log.File)

	// metrics
	s.bool(EnvPrefix+"METRICS_ENABLED", &cfg.Metrics.Enabled)
	s.port(EnvPrefix+"METRICS_PORT", &cfg.Metrics.Port)
	s.str(EnvPrefix+"METRICS_PATH", &cfg.Metrics.Path)

	// storage
	s.str(EnvPrefix+"STORAGE_PROVIDER", &cfg.Storage.Provider)
	s.str(EnvPrefix+"STORAGE_LOCAL_PATH", &cfg.Storage.LocalPath)
	s.str(EnvPrefix+"STORAGE_S3_BUCKET", &cfg.Storage.S3Bucket)
	s.str(EnvPrefix+"STORAGE_S3_REGION", &cfg.Storage.S3Region)
	s.str(EnvPrefix+"STORAGE_S3_ENDPOINT", &cfg.Storage.S3Endpoint)

	// admin
	s.str(EnvPrefix+"ADMIN_CONSOLE_DIR", &cfg.Admin.ConsoleDir)

	return s.err()
}

// setter 收集环境变量解析错误。
//
// 为什么不在解析失败时直接忽略：`AGENTBOT_SERVER_PORT=eight-thousand`
// 被静默忽略的后果是服务照常起在 8080，而运维以为改过了。集合错误
// 一次性报出，比逐个 fail 更快定位。
type setter struct {
	lookup func(string) (string, bool)
	errs   []string
}

func (s *setter) get(key string) (string, bool) {
	if s.lookup == nil {
		return "", false
	}
	v, ok := s.lookup(key)
	if !ok {
		return "", false
	}
	return v, true
}

func (s *setter) add(key, reason string) {
	s.errs = append(s.errs, fmt.Sprintf("%s: %s", key, reason))
}

// err 把累积的错误合成一个 ErrInvalidConfig。
func (s *setter) err() error {
	if len(s.errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(s.errs, "; "))
}

func (s *setter) str(key string, dst *string) {
	if v, ok := s.get(key); ok {
		*dst = strings.TrimSpace(v)
	}
}

func (s *setter) list(key string, dst *[]string) {
	v, ok := s.get(key)
	if !ok {
		return
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	// 显式清空（如 SSO_SCOPES=""）时用 nil 而不是空切片：
	// 业务层对 nil 会补默认值，对空切片则视为「用户明确要求没有 scope」。
	if len(out) == 0 {
		*dst = nil
		return
	}
	*dst = out
}

func (s *setter) int(key string, dst *int) {
	v, ok := s.get(key)
	if !ok {
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		s.add(key, fmt.Sprintf("%q is not an integer", v))
		return
	}
	*dst = n
}

// port 与 int 分开，是为了让错误信息能指出「端口必须是 1-65535」。
func (s *setter) port(key string, dst *int) {
	before := *dst
	s.int(key, dst)
	if *dst == before {
		return
	}
	if *dst < 1 || *dst > 65535 {
		s.add(key, fmt.Sprintf("port %d out of range 1-65535", *dst))
	}
}

func (s *setter) float(key string, dst *float64) {
	v, ok := s.get(key)
	if !ok {
		return
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		s.add(key, fmt.Sprintf("%q is not a number", v))
		return
	}
	*dst = f
}

func (s *setter) bool(key string, dst *bool) {
	v, ok := s.get(key)
	if !ok {
		return
	}
	b, err := ParseBool(v)
	if err != nil {
		s.add(key, err.Error())
		return
	}
	*dst = b
}

// dur 解析时间字段。
//
// 接受两种写法：Go duration 字面量（30s / 5m / 1h30m）与纯整数（按秒）。
// 兼容纯整数是因为 k8s 的 env 与 helm 模板里习惯写整秒；只接受前者会让
// `AGENTBOT_SERVER_READ_TIMEOUT=30` 报错，而使用者很难猜到需要写 `30s`。
func (s *setter) dur(key string, dst *time.Duration) {
	v, ok := s.get(key)
	if !ok {
		return
	}
	raw := strings.TrimSpace(v)
	if n, err := strconv.Atoi(raw); err == nil {
		*dst = time.Duration(n) * time.Second
		return
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		s.add(key, fmt.Sprintf("%q is not a duration (use 30s / 5m / 1h)", v))
		return
	}
	*dst = d
}

// ParseBool 解析布尔字面量。
//
// 比 strconv.ParseBool 宽松：额外接受 yes/no/on/off（compose 与 k8s 里常见）。
func ParseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a boolean (use true/false/1/0/yes/no/on/off)", v)
}
