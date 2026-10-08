package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// validSecret 是一段足够长且不含占位符特征的随机串。
//
// 用拼接构造而不是字面量：某些写入路径会截断特别长的连续字符串，
// 拼接能保证测试里的值就是文件中真实的值。
func validSecret() string {
	return "jwt-" + strings.Repeat("a7f3", 12) + "-tail"
}

// validDBPassword 同理，避免被当作占位符。
func validDBPassword() string {
	return "pg-" + strings.Repeat("9c1d", 6)
}

// baseEnv 返回一组使配置校验通过的环境变量。
func baseEnv() map[string]string {
	return map[string]string{
		EnvPrefix + "AUTH_JWT_SECRET":   validSecret(),
		EnvPrefix + "DATABASE_PASSWORD": validDBPassword(),
	}
}

// lookupOf 把 map 包装成 LookupEnv 形状。
func lookupOf(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

// ---------------------------------------------------------------------------
// 机密 fail-fast
// ---------------------------------------------------------------------------

func TestLoad_RejectsMissingJWTSecret(t *testing.T) {
	env := baseEnv()
	delete(env, EnvPrefix+"AUTH_JWT_SECRET")

	cfg := Default()
	if err := applyEnv(&cfg, lookupOf(env)); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	err := cfg.Validate()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
	if !strings.Contains(err.Error(), "jwt_secret") {
		t.Errorf("error should name the offending field, got: %v", err)
	}
}

func TestLoad_RejectsDefaultPlaceholderSecret(t *testing.T) {
	// 这是本次改动的核心断言：源码里曾经硬编码的那个值必须在启动期被拒。
	cfg := Default()
	cfg.Auth.JWTSecret = "dev-secret-change-in-production"
	cfg.Database.Password = validDBPassword()

	err := cfg.Validate()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("the old hard-coded secret must be rejected, got %v", err)
	}
}

func TestLoad_RejectsShortJWTSecret(t *testing.T) {
	cfg := Default()
	cfg.Auth.JWTSecret = "short-key"
	cfg.Database.Password = validDBPassword()

	err := cfg.Validate()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Errorf("expected a length complaint, got: %v", err)
	}
}

func TestLoad_RejectsMissingDatabasePassword(t *testing.T) {
	cfg := Default()
	cfg.Auth.JWTSecret = validSecret()

	err := cfg.Validate()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
	if !strings.Contains(err.Error(), "database.password") {
		t.Errorf("expected database.password complaint, got: %v", err)
	}
}

// SSO 未配置时不要求 client secret —— 「没配 SSO」不该让整个进程起不来。
func TestValidate_SSOUnconfiguredDoesNotRequireClientSecret(t *testing.T) {
	cfg := Default()
	cfg.Auth.JWTSecret = validSecret()
	cfg.Database.Password = validDBPassword()

	if cfg.SSO.Configured() {
		t.Fatal("default SSO config must not report itself as configured")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unconfigured SSO must not block startup: %v", err)
	}
}

// SSO 一旦声明启用，client secret 就变成必需项。
func TestValidate_ConfiguredSSORequiresClientSecret(t *testing.T) {
	cfg := Default()
	cfg.Auth.JWTSecret = validSecret()
	cfg.Database.Password = validDBPassword()
	cfg.SSO.Issuer = "https://idp.example.test"
	cfg.SSO.ClientID = "agentbot"
	cfg.SSO.RedirectURL = "https://bot.example.test/api/v1/sso/callback"

	if !cfg.SSO.Configured() {
		t.Fatal("SSO should be considered configured")
	}
	err := cfg.Validate()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
	if !strings.Contains(err.Error(), "sso.client_secret") {
		t.Errorf("expected sso.client_secret complaint, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 覆盖链：默认值 → 文件 → 环境变量
// ---------------------------------------------------------------------------

func TestLoad_EnvOverridesDefaults(t *testing.T) {
	env := baseEnv()
	env[EnvPrefix+"SERVER_PORT"] = "9000"
	env[EnvPrefix+"LOG_LEVEL"] = "debug"
	env[EnvPrefix+"AGENT_MAX_CONCURRENT"] = "42"

	cfg := Default()
	if err := applyEnv(&cfg, lookupOf(env)); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if cfg.Server.Port != 9000 {
		t.Errorf("server.port = %d, want 9000", cfg.Server.Port)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("log.level = %q, want debug", cfg.Log.Level)
	}
	if cfg.Agent.MaxConcurrent != 42 {
		t.Errorf("agent.max_concurrent = %d, want 42", cfg.Agent.MaxConcurrent)
	}
	// 未被覆盖的字段必须保留默认值：整体替换会把整份配置清零。
	if cfg.Server.ReadTimeout != 30*time.Second {
		t.Errorf("server.read_timeout = %v, want default 30s", cfg.Server.ReadTimeout)
	}
}

func TestApplyFile_PartialConfigKeepsOtherDefaults(t *testing.T) {
	cfg := Default()
	file := &fileConfig{}
	port := 9100
	file.Server = &fileServer{Port: &port}
	file.applyTo(&cfg)

	if cfg.Server.Port != 9100 {
		t.Errorf("server.port = %d, want 9100", cfg.Server.Port)
	}
	// 「只改一个字段」必须真的只改一个字段。
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("server.host = %q, want the default to survive", cfg.Server.Host)
	}
	if cfg.Server.ReadTimeout != 30*time.Second {
		t.Errorf("read_timeout was clobbered: %v", cfg.Server.ReadTimeout)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("top-level section was clobbered: %q", cfg.Log.Level)
	}
}

func TestLoad_FileThenEnvPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentbot.json")
	body := `{
	  "server": {"port": 9100},
	  "log": {"level": "warn"},
	  "auth": {"jwt_secret": "` + validSecret() + `"},
	  "database": {"password": "` + validDBPassword() + `"}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// 文件层
	cfg := Default()
	if err := applyFile(&cfg, path); err != nil {
		t.Fatalf("applyFile: %v", err)
	}
	if cfg.Server.Port != 9100 {
		t.Fatalf("file layer not applied: %d", cfg.Server.Port)
	}

	// 环境层必须能覆盖文件层
	env := lookupOf(map[string]string{EnvPrefix + "SERVER_PORT": "9200"})
	if err := applyEnv(&cfg, env); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	if cfg.Server.Port != 9200 {
		t.Errorf("env must win over file: port = %d, want 9200", cfg.Server.Port)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("file value must survive when env does not set it: %q", cfg.Log.Level)
	}
}

func TestApplyFile_RejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	// jwt_secrets 是拼写错误：静默忽略会让使用者看到「我明明配了但说没配」。
	body := `{"auth": {"jwt_secrets": "oops"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	def := Default()
	err := applyFile(&def, path)
	if !errors.Is(err, ErrConfigFile) {
		t.Fatalf("expected ErrConfigFile for unknown field, got %v", err)
	}
}

func TestApplyFile_MissingExplicitFileIsAnError(t *testing.T) {
	def := Default()
	err := applyFile(&def, filepath.Join(t.TempDir(), "does-not-exist.json"))
	if !errors.Is(err, ErrConfigFile) {
		t.Fatalf("expected ErrConfigFile, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 解析器行为
// ---------------------------------------------------------------------------

func TestSetDur_AcceptsDurationLiteralAndSeconds(t *testing.T) {
	// 关键点：JSON 里的数字 30 若直接喂给 time.Duration 会变成 30 纳秒。
	// 这里统一按秒解释，消除「配了 30 秒实际 30 纳秒」的静默失效。
	thirty := "30"
	d := 0 * time.Second
	setDur(&thirty, &d)
	if d != 30*time.Second {
		t.Errorf("bare integer must mean seconds: got %v", d)
	}

	literal := "1h30m"
	d = 0
	setDur(&literal, &d)
	if d != 90*time.Minute {
		t.Errorf("duration literal parsed wrong: %v", d)
	}

	// 非法写法保留原值（不做 clamp 成 0）
	bad := "not-a-duration"
	d = 7 * time.Second
	setDur(&bad, &d)
	if d != 7*time.Second {
		t.Errorf("invalid value must leave the previous value intact: %v", d)
	}

	// 空字符串保留原值
	empty := ""
	d = 7 * time.Second
	setDur(&empty, &d)
	if d != 7*time.Second {
		t.Errorf("empty value must leave the previous value intact: %v", d)
	}
}

func TestParseBool_AcceptsCommonLiterals(t *testing.T) {
	cases := map[string]bool{
		"true": true, "TRUE": true, "1": true, "yes": true, "on": true,
		"false": false, "0": false, "no": false, "off": false, "Off": false,
	}
	for in, want := range cases {
		got, err := ParseBool(in)
		if err != nil {
			t.Errorf("ParseBool(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseBool(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseBool("maybe"); err == nil {
		t.Error("ParseBool must reject unrecognised values rather than guessing")
	}
}

func TestApplyEnv_ReportsBadValuesInsteadOfIgnoring(t *testing.T) {
	env := baseEnv()
	env[EnvPrefix+"SERVER_PORT"] = "eight-thousand"
	env[EnvPrefix+"SERVER_READ_TIMEOUT"] = "soon"

	cfg := Default()
	err := applyEnv(&cfg, lookupOf(env))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
	// 两个错误都要在，否则修一个报一个，来回多轮。
	if !strings.Contains(err.Error(), "SERVER_PORT") || !strings.Contains(err.Error(), "SERVER_READ_TIMEOUT") {
		t.Errorf("all parse errors should be reported together, got: %v", err)
	}
}

func TestApplyEnv_EmptyListClearsToNil(t *testing.T) {
	// 显式置空与「没写」必须可区分：nil 让业务层补默认值，
	// 空切片代表使用者明确要求「没有 scope」。
	env := baseEnv()
	env[EnvPrefix+"SSO_SCOPES"] = ""

	cfg := Default()
	cfg.SSO.Scopes = []string{"openid"}
	if err := applyEnv(&cfg, lookupOf(env)); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	if cfg.SSO.Scopes != nil {
		t.Errorf("explicit empty list must become nil, got %#v", cfg.SSO.Scopes)
	}
}

// ---------------------------------------------------------------------------
// Validate 的普通配置校验
// ---------------------------------------------------------------------------

func validConfig() Config {
	cfg := Default()
	cfg.Auth.JWTSecret = validSecret()
	cfg.Database.Password = validDBPassword()
	return cfg
}

func TestValidate_RejectsNilSliceSafety(t *testing.T) {
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validConfig must pass, got %v", err)
	}
}

func TestValidate_RejectsMetricsSamePortAsServer(t *testing.T) {
	cfg := validConfig()
	cfg.Metrics.Port = cfg.Server.Port

	err := cfg.Validate()
	if err == nil {
		t.Fatal("metrics on the same port as the server can never bind: expected an error")
	}
	if !strings.Contains(err.Error(), "metrics.port") {
		t.Errorf("expected metrics.port complaint, got: %v", err)
	}
}

func TestValidate_RejectsRefreshShorterThanAccess(t *testing.T) {
	cfg := validConfig()
	cfg.Auth.AccessExpiry = 48 * time.Hour
	cfg.Auth.RefreshExpiry = 1 * time.Hour

	if err := cfg.Validate(); err == nil {
		t.Fatal("a refresh token that expires before the access token is a configuration error")
	}
}

func TestValidate_RejectsZeroTimeouts(t *testing.T) {
	// <=0 超时意味着「永不超时」，是可被利用的挂起面。
	for name, mutate := range map[string]func(*Config){
		"server.read_timeout": func(c *Config) { c.Server.ReadTimeout = 0 },
		"server.write_timeout": func(c *Config) {
			c.Server.WriteTimeout = -1
		},
		"agent.task_timeout": func(c *Config) { c.Agent.TaskTimeout = 0 },
	} {
		cfg := validConfig()
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: zero/negative timeout must be rejected", name)
		}
	}
}

func TestValidate_RejectsBadSSLModeAndLogLevel(t *testing.T) {
	cfg := validConfig()
	cfg.Database.SSLMode = "sort-of"
	cfg.Log.Level = "verbose"
	cfg.Log.Format = "xml"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"ssl_mode", "log.level", "log.format"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected %s in the report, got: %v", want, err)
		}
	}
}

func TestValidate_RejectsUnsafeQuotaValues(t *testing.T) {
	cfg := validConfig()
	cfg.Agent.MaxConcurrent = 0
	cfg.Cloud.MaxEnvs = -5

	if err := cfg.Validate(); err == nil {
		t.Fatal("zero/negative quotas must be rejected")
	}
}

// ---------------------------------------------------------------------------
// 开发默认配置
// ---------------------------------------------------------------------------

func TestDevelopmentDefault_IsValidAndMarkedDevelopment(t *testing.T) {
	cfg := DevelopmentDefault()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("development default must be usable without injection: %v", err)
	}
	// 开发机密必须一眼可辨：isPlaceholderSecret 会拒绝把它们当成生产值，
	// 但 DevelopmentDefault 是显式调用，不走那条路径。
	prod := Default()
	if prod.Auth.JWTSecret == cfg.Auth.JWTSecret {
		t.Error("Default() must not carry the development secret")
	}
	if prod.Database.Password != "" {
		t.Errorf("Default() must not carry a database password, got %q", prod.Database.Password)
	}
}

func TestServerAddrAndDSN(t *testing.T) {
	cfg := Default()
	cfg.Server.Host = ""
	if got := cfg.Server.Addr(); got != "0.0.0.0:8080" {
		t.Errorf("Addr() = %q, want 0.0.0.0:8080", got)
	}
	cfg.Database.SSLMode = "require"
	dsn := cfg.Database.DSN()
	if !strings.Contains(dsn, "sslmode=require") {
		// ssl_mode 没进 DSN 的话，配置写了 require 实际却是明文连接。
		t.Errorf("DSN must carry ssl_mode, got %q", dsn)
	}
}
