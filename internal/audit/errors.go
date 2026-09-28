package audit

import "errors"

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
