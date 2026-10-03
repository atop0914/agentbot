package audit

import (
	"errors"
	"time"
)

// 审计模块的哨兵错误，便于 handler 层用 errors.Is 判定后映射 HTTP 状态码。
var (
	// ErrActionRequired 表示事件缺少 action 字段。
	ErrActionRequired = errors.New("audit: action is required")
	// ErrIDRequired 表示查询时未提供事件 ID。
	ErrIDRequired = errors.New("audit: event id is required")
	// ErrEventNotFound 表示指定 ID 的事件不存在。
	ErrEventNotFound = errors.New("audit: event not found")
	// ErrEventRequired 表示未提供事件实体。
	ErrEventRequired = errors.New("audit: event is required")
	// ErrCutoffRequired 表示清理时未提供时间边界。
	ErrCutoffRequired = errors.New("audit: cutoff time is required")
	// ErrUnknownDistinctField 表示请求了不支持的去重维度。
	ErrUnknownDistinctField = errors.New("audit: unknown distinct field")
	// ErrUnsupportedFormat 表示请求了不支持的导出格式。
	ErrUnsupportedFormat = errors.New("audit: unsupported export format")
	// ErrExportRangeTooLarge 表示导出的时间窗跨度超过上限。
	ErrExportRangeTooLarge = errors.New("audit: export time range exceeds limit")
	// ErrNilEvent 表示写入的事件指针为空。
	ErrNilEvent = errors.New("audit: event is nil")
	// ErrInvalidRetention 表示留存策略配置非法（保留期必须为正）。
	ErrInvalidRetention = errors.New("audit: retention must be positive")
)

// 事件状态与操作者类型的取值。
const (
	StatusSuccess = "success"
	StatusFailure = "failure"

	ActorTypeUser  = "user"
	ActorTypeAgent = "agent"
	// ActorTypeSystem 表示由平台自身发起的操作（启动、关停、内部错误等）。
	ActorTypeSystem = "system"
)

// 排序字段与方向。
const (
	SortByTimestamp = "timestamp"
	SortByAction    = "action"

	SortAsc  = "asc"
	SortDesc = "desc"
)

// Distinct 支持的维度。
const (
	DistinctActor     = "actor"
	DistinctActorType = "actor_type"
	DistinctAction    = "action"
	DistinctResource  = "resource"
	DistinctStatus    = "status"
	DistinctIPAddress = "ip_address"
)

// 导出格式。
const (
	FormatJSON = "json"
	FormatCSV  = "csv"
)

// 留存与脱敏相关常量。
const (
	// DefaultRetentionDays 是未配置留存策略时的默认保留期（天）。
	DefaultRetentionDays = 90
	// UnlimitedRetention 表示显式关闭自动清理。
	// 审计日志是取证依据，企业侧有时需要长期保留，因此保留「不清理」这一档，
	// 但必须由调用方显式选择，绝不作为默认值。
	UnlimitedRetention = 0

	// RedactedPlaceholder 是敏感字段脱敏后的占位符。
	RedactedPlaceholder = "[REDACTED]"

	// ExportHashAlgo 是导出摘要使用的算法标识。
	ExportHashAlgo = "sha256"
	// MaxExportRange 是单次按时间窗导出的最大跨度（31 天）。
	MaxExportRange = 31 * 24 * time.Hour
)
