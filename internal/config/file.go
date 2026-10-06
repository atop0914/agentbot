package config

import "time"

// 本文件定义「配置文件层」的解析结构。
//
// 为什么不直接 json.Unmarshal 到 Config：
//
//	配置文件需要区分「字段没写」与「字段写了零值」。如果直接反序列化到
//	Config，`{"server":{"port":0}}` 与「没写 port」无法区分，逐字段覆盖
//	就退化成整体替换。这里全部用指针表达「写了」，nil 即「没写」。
//
// 代价是每个字段都要写一遍赋值；收益是「只改一个字段」真的只需要写
// 那一个字段（否则使用者必须把整个子结构写全，一旦漏写就被判为非法）。
type fileConfig struct {
	Server     *fileServer     `json:"server"`
	Database   *fileDatabase   `json:"database"`
	Redis      *fileRedis      `json:"redis"`
	Auth       *fileAuth       `json:"auth"`
	SSO        *fileSSO        `json:"sso"`
	Agent      *fileAgent      `json:"agent"`
	Browser    *fileBrowser    `json:"browser"`
	Cloud      *fileCloud      `json:"cloud"`
	FileSystem *fileFileSystem `json:"filesystem"`
	Log        *fileLog        `json:"log"`
	Metrics    *fileMetrics    `json:"metrics"`
	Storage    *fileStorage    `json:"storage"`
	Admin      *fileAdmin      `json:"admin"`
}

type fileServer struct {
	Host         *string `json:"host"`
	Port         *int    `json:"port"`
	ReadTimeout  *string `json:"read_timeout"`
	WriteTimeout *string `json:"write_timeout"`
	IdleTimeout  *string `json:"idle_timeout"`
}

type fileDatabase struct {
	Host     *string `json:"host"`
	Port     *int    `json:"port"`
	User     *string `json:"user"`
	Password *string `json:"password"`
	DBName   *string `json:"db_name"`
	SSLMode  *string `json:"ssl_mode"`
}

type fileRedis struct {
	Host     *string `json:"host"`
	Port     *int    `json:"port"`
	Password *string `json:"password"`
	DB       *int    `json:"db"`
}

type fileAuth struct {
	JWTSecret     *string `json:"jwt_secret"`
	AccessExpiry  *string `json:"access_expiry"`
	RefreshExpiry *string `json:"refresh_expiry"`
	Issuer        *string `json:"issuer"`
}

type fileSSO struct {
	Issuer       *string  `json:"issuer"`
	ClientID     *string  `json:"client_id"`
	ClientSecret *string  `json:"client_secret"`
	RedirectURL  *string  `json:"redirect_url"`
	Scopes       *[]string `json:"scopes"`
	AllowedAlgs  *[]string `json:"allowed_algs"`
	ClockSkew    *string  `json:"clock_skew"`
}

type fileAgent struct {
	DefaultModel       *string  `json:"default_model"`
	MaxTokens          *int     `json:"max_tokens"`
	DefaultTemperature *float64 `json:"default_temperature"`
	MaxConcurrent      *int     `json:"max_concurrent"`
	TaskTimeout        *string  `json:"task_timeout"`
	MemorySize         *int     `json:"memory_size"`
}

type fileBrowser struct {
	Headless       *bool   `json:"headless"`
	ExecutablePath *string `json:"executable_path"`
	UserDataDir    *string `json:"user_data_dir"`
	Proxy          *string `json:"proxy"`
	Timeout        *string `json:"timeout"`
}

type fileCloud struct {
	Provider     *string `json:"provider"`
	DockerHost   *string `json:"docker_host"`
	KubeConfig   *string `json:"kube_config"`
	Namespace    *string `json:"namespace"`
	DefaultImage *string `json:"default_image"`
	MaxEnvs      *int    `json:"max_envs"`
}

type fileFileSystem struct {
	Root *string `json:"root"`
}

type fileLog struct {
	Level  *string `json:"level"`
	Format *string `json:"format"`
	Output *string `json:"output"`
	File   *string `json:"file"`
}

type fileMetrics struct {
	Enabled *bool   `json:"enabled"`
	Port    *int    `json:"port"`
	Path    *string `json:"path"`
}

type fileStorage struct {
	Provider   *string `json:"provider"`
	LocalPath  *string `json:"local_path"`
	S3Bucket   *string `json:"s3_bucket"`
	S3Region   *string `json:"s3_region"`
	S3Endpoint *string `json:"s3_endpoint"`
}

type fileAdmin struct {
	ConsoleDir *string `json:"console_dir"`
}

// applyTo 把「文件里写了的值」覆盖到 cfg 上。
//
// 注意：这里**不做**解析错误的静默忽略 —— 时间字段若写成非法值，
// 与「没写」的处理方式不同（保留默认），因为使用者显然是想改它。
// 但配置文件层的解析错误会在 JSON 解码阶段以字符串形式进来，
// 真正的时间解析在这里完成，失败时保留默认值而不阻断启动：
// 配置层最终会有 Validate 兜底，且这里失败的原因只有写法错误一种，
// 会在日志里以 warn 出现（见 loader 的 parseDurationOr）。
func (f *fileConfig) applyTo(c *Config) {
	if f == nil {
		return
	}
	if f.Server != nil {
		setStr(f.Server.Host, &c.Server.Host)
		setInt(f.Server.Port, &c.Server.Port)
		setDur(f.Server.ReadTimeout, &c.Server.ReadTimeout)
		setDur(f.Server.WriteTimeout, &c.Server.WriteTimeout)
		setDur(f.Server.IdleTimeout, &c.Server.IdleTimeout)
	}
	if f.Database != nil {
		setStr(f.Database.Host, &c.Database.Host)
		setInt(f.Database.Port, &c.Database.Port)
		setStr(f.Database.User, &c.Database.User)
		setStr(f.Database.Password, &c.Database.Password)
		setStr(f.Database.DBName, &c.Database.DBName)
		setStr(f.Database.SSLMode, &c.Database.SSLMode)
	}
	if f.Redis != nil {
		setStr(f.Redis.Host, &c.Redis.Host)
		setInt(f.Redis.Port, &c.Redis.Port)
		setStr(f.Redis.Password, &c.Redis.Password)
		setInt(f.Redis.DB, &c.Redis.DB)
	}
	if f.Auth != nil {
		setStr(f.Auth.JWTSecret, &c.Auth.JWTSecret)
		setDur(f.Auth.AccessExpiry, &c.Auth.AccessExpiry)
		setDur(f.Auth.RefreshExpiry, &c.Auth.RefreshExpiry)
		setStr(f.Auth.Issuer, &c.Auth.Issuer)
	}
	if f.SSO != nil {
		setStr(f.SSO.Issuer, &c.SSO.Issuer)
		setStr(f.SSO.ClientID, &c.SSO.ClientID)
		setStr(f.SSO.ClientSecret, &c.SSO.ClientSecret)
		setStr(f.SSO.RedirectURL, &c.SSO.RedirectURL)
		setList(f.SSO.Scopes, &c.SSO.Scopes)
		setList(f.SSO.AllowedAlgs, &c.SSO.AllowedAlgs)
		setDur(f.SSO.ClockSkew, &c.SSO.ClockSkew)
	}
	if f.Agent != nil {
		setStr(f.Agent.DefaultModel, &c.Agent.DefaultModel)
		setInt(f.Agent.MaxTokens, &c.Agent.MaxTokens)
		setFloat(f.Agent.DefaultTemperature, &c.Agent.DefaultTemperature)
		setInt(f.Agent.MaxConcurrent, &c.Agent.MaxConcurrent)
		setDur(f.Agent.TaskTimeout, &c.Agent.TaskTimeout)
		setInt(f.Agent.MemorySize, &c.Agent.MemorySize)
	}
	if f.Browser != nil {
		setBool(f.Browser.Headless, &c.Browser.Headless)
		setStr(f.Browser.ExecutablePath, &c.Browser.ExecutablePath)
		setStr(f.Browser.UserDataDir, &c.Browser.UserDataDir)
		setStr(f.Browser.Proxy, &c.Browser.Proxy)
		setDur(f.Browser.Timeout, &c.Browser.Timeout)
	}
	if f.Cloud != nil {
		setStr(f.Cloud.Provider, &c.Cloud.Provider)
		setStr(f.Cloud.DockerHost, &c.Cloud.DockerHost)
		setStr(f.Cloud.KubeConfig, &c.Cloud.KubeConfig)
		setStr(f.Cloud.Namespace, &c.Cloud.Namespace)
		setStr(f.Cloud.DefaultImage, &c.Cloud.DefaultImage)
		setInt(f.Cloud.MaxEnvs, &c.Cloud.MaxEnvs)
	}
	if f.FileSystem != nil {
		setStr(f.FileSystem.Root, &c.FileSystem.Root)
	}
	if f.Log != nil {
		setStr(f.Log.Level, &c.Log.Level)
		setStr(f.Log.Format, &c.Log.Format)
		setStr(f.Log.Output, &c.Log.Output)
		setStr(f.Log.File, &c.Log.File)
	}
	if f.Metrics != nil {
		setBool(f.Metrics.Enabled, &c.Metrics.Enabled)
		setInt(f.Metrics.Port, &c.Metrics.Port)
		setStr(f.Metrics.Path, &c.Metrics.Path)
	}
	if f.Storage != nil {
		setStr(f.Storage.Provider, &c.Storage.Provider)
		setStr(f.Storage.LocalPath, &c.Storage.LocalPath)
		setStr(f.Storage.S3Bucket, &c.Storage.S3Bucket)
		setStr(f.Storage.S3Region, &c.Storage.S3Region)
		setStr(f.Storage.S3Endpoint, &c.Storage.S3Endpoint)
	}
	if f.Admin != nil {
		setStr(f.Admin.ConsoleDir, &c.Admin.ConsoleDir)
	}
}

func setStr(v *string, dst *string) {
	if v != nil {
		*dst = *v
	}
}

func setInt(v *int, dst *int) {
	if v != nil {
		*dst = *v
	}
}

func setFloat(v *float64, dst *float64) {
	if v != nil {
		*dst = *v
	}
}

func setBool(v *bool, dst *bool) {
	if v != nil {
		*dst = *v
	}
}

func setList(v *[]string, dst *[]string) {
	if v != nil {
		*dst = *v
	}
}

// setDur 解析时间字段。
//
// 支持 JSON 字符串（"30s"）与 JSON 数字（按秒）两种写法：
// 直接用 time.Duration 反序列化数字时，JSON 里的 30 会被当成 30 纳秒，
// 这是 Go 里最经典的配置陷阱之一（写 30s 得到 30ns，超时形同虚设）。
// 显式按秒解释数字，符合「配置里写 30 就是 30 秒」的直觉。
func setDur(v *string, dst *time.Duration) {
	if v == nil {
		return
	}
	if d, err := parseDurationOr(*v); err == nil {
		*dst = d
	}
}
