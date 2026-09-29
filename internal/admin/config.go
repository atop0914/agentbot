package admin

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// MaxTitleLen 限制控制台标题长度，避免异常长的标题打爆前端布局。
const MaxTitleLen = 120

// Config 是管理控制台的展示配置。
type Config struct {
	Title   string `json:"title"`
	Version string `json:"version"`
	// FeatureFlags 是控制台功能开关；nil 表示全部关闭。
	FeatureFlags map[string]bool `json:"feature_flags"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// UpdateConfigRequest 是配置更新请求。
//
// 字段使用指针，区分「不修改」与「显式置空」：
//   - nil        → 保持原值
//   - 指向零值   → 覆盖为零值（FeatureFlags 语义见下）
type UpdateConfigRequest struct {
	Title   *string `json:"title,omitempty"`
	Version *string `json:"version,omitempty"`
	// FeatureFlags 为 nil 时保持原开关集合；
	// 非 nil 时整体替换（传入空 map 即清空全部开关）。
	FeatureFlags map[string]bool `json:"feature_flags,omitempty"`
}

// DefaultFeatureFlags 返回控制台默认的开关集合。
//
// 默认值刻意保守：所有「会改变运行行为」的能力关闭，只开只读展示能力，
// 避免开发期误操作真实 Agent。
func DefaultFeatureFlags() map[string]bool {
	return map[string]bool{
		"show_audit":     true,
		"show_monitor":   true,
		"show_templates": true,
		"agent_control":  false,
		"task_control":   false,
		"user_admin":     false,
	}
}

// Config 构造默认配置。
func NewConfig(title, version string) *Config {
	return &Config{
		Title:        title,
		Version:      version,
		FeatureFlags: DefaultFeatureFlags(),
		UpdatedAt:    time.Now().UTC(),
	}
}

// clone 返回配置的深拷贝，防止调用方改动内部状态。
func (c *Config) clone() *Config {
	if c == nil {
		return nil
	}
	cp := *c
	cp.FeatureFlags = make(map[string]bool, len(c.FeatureFlags))
	for k, v := range c.FeatureFlags {
		cp.FeatureFlags[k] = v
	}
	return &cp
}

// ValidateTitle 校验并规整控制台标题。
func ValidateTitle(title string) (string, error) {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return "", fmt.Errorf("admin: title is required")
	}
	if len([]rune(trimmed)) > MaxTitleLen {
		return "", fmt.Errorf("admin: title must not exceed %d characters", MaxTitleLen)
	}
	return trimmed, nil
}

// normalizeFlags 校验开关名并返回排好序的拷贝。
//
// 开关名要求非空且不含空白字符 —— 开关名会被用作前端读写的 key，
// 含空白的名字在 URL / 模板里都容易出问题。
func normalizeFlags(flags map[string]bool) (map[string]bool, error) {
	if flags == nil {
		return nil, nil
	}
	out := make(map[string]bool, len(flags))
	for name, enabled := range flags {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return nil, fmt.Errorf("admin: feature flag name must not be empty")
		}
		if strings.ContainsAny(trimmed, " \t\r\n") {
			return nil, fmt.Errorf("admin: feature flag name %q must not contain whitespace", name)
		}
		out[trimmed] = enabled
	}
	return out, nil
}

// FlagNames 返回按字母序排列的开关名，便于稳定输出。
func FlagNames(flags map[string]bool, enabled bool) []string {
	names := make([]string, 0, len(flags))
	for name, value := range flags {
		if value == enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
