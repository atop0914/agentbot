package audit

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 默认参数。
const (
	// defaultLimit 是查询未指定 Limit 时返回的最大条数。
	defaultLimit = 50
	// maxLimit 是单次查询允许的最大条数，防止一次拉爆内存。
	maxLimit = 1000
)

// service 是 Service 接口的实现：在仓库之上补充校验、默认值与导出能力。
type service struct {
	repo Repository
}

// NewService 创建审计服务。
func NewService(repo Repository) Service {
	return &service{repo: repo}
}

// NewRecorder 创建基于服务的事件记录器，供其他模块直接打点。
func NewRecorder(svc Service) Recorder {
	return &recorder{svc: svc}
}

func (s *service) Log(ctx context.Context, event Event) error {
	if s.repo == nil {
		return fmt.Errorf("audit: repository is not configured")
	}
	_, err := s.repo.Append(ctx, event)
	return err
}

// LogEvent 校验并写入一条事件，返回写入后的完整记录。
func (s *service) LogEvent(ctx context.Context, event Event) (*EventRecord, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("audit: repository is not configured")
	}
	if strings.TrimSpace(event.Action) == "" {
		return nil, ErrActionRequired
	}
	if event.Actor != "" && event.ActorType == "" {
		// 未显式声明操作者类型时按 user 处理，agent ID 由调用方显式标注。
		event.ActorType = ActorTypeUser
	}
	return s.repo.Append(ctx, event)
}

func (s *service) Query(ctx context.Context, filter Filter) ([]*EventRecord, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("audit: repository is not configured")
	}
	f := normalizeFilter(filter)
	return s.repo.List(ctx, f)
}

func (s *service) GetEvent(ctx context.Context, id string) (*EventRecord, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("audit: repository is not configured")
	}
	return s.repo.Get(ctx, id)
}

func (s *service) Count(ctx context.Context, filter Filter) (int, error) {
	if s.repo == nil {
		return 0, fmt.Errorf("audit: repository is not configured")
	}
	return s.repo.Count(ctx, normalizeFilter(filter))
}

// Export 导出符合条件的事件。支持 json 与 csv 两种格式。
func (s *service) Export(ctx context.Context, filter Filter, format string) ([]byte, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("audit: repository is not configured")
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = FormatJSON
	}
	if format != FormatJSON && format != FormatCSV {
		return nil, ErrUnsupportedFormat
	}

	// 导出时忽略分页，最多导出 maxLimit 条，避免全量落盘。
	f := normalizeFilter(filter)
	f.Offset = 0
	f.Limit = maxLimit

	records, err := s.repo.List(ctx, f)
	if err != nil {
		return nil, err
	}
	if format == FormatCSV {
		return exportCSV(records), nil
	}
	return exportJSON(records)
}

// Stats 按维度聚合事件数量，用于审计看板。dimension 取值见 Distinct* 常量。
func (s *service) Stats(ctx context.Context, filter Filter, dimension string) (map[string]int, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("audit: repository is not configured")
	}
	switch dimension {
	case DistinctActor, DistinctActorType, DistinctAction,
		DistinctResource, DistinctStatus, DistinctIPAddress:
	default:
		return nil, ErrUnknownDistinctField
	}

	// 统计需要全量遍历，故清空分页参数。
	f := normalizeFilter(filter)
	f.Limit = 0
	f.Offset = 0

	records, err := s.repo.List(ctx, f)
	if err != nil {
		return nil, err
	}

	stats := make(map[string]int)
	for _, rec := range records {
		var key string
		switch dimension {
		case DistinctActor:
			key = rec.Actor
		case DistinctActorType:
			key = rec.ActorType
		case DistinctAction:
			key = rec.Action
		case DistinctResource:
			key = rec.Resource
		case DistinctStatus:
			key = rec.Status
		case DistinctIPAddress:
			key = rec.IPAddress
		}
		if key == "" {
			key = "unknown"
		}
		stats[key]++
	}
	return stats, nil
}

// Distinct 返回指定维度的去重取值，供前端筛选器使用。
func (s *service) Distinct(ctx context.Context, field string) ([]string, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("audit: repository is not configured")
	}
	return s.repo.Distinct(ctx, field)
}

// Purge 删除早于 cutoff 的事件，返回删除条数。
func (s *service) Purge(ctx context.Context, cutoff time.Time) (int, error) {
	if s.repo == nil {
		return 0, fmt.Errorf("audit: repository is not configured")
	}
	if cutoff.IsZero() {
		return 0, ErrCutoffRequired
	}
	return s.repo.Purge(ctx, cutoff)
}

// normalizeFilter 补全过滤条件的默认值并做边界裁剪。
func normalizeFilter(f Filter) Filter {
	if f.Limit < 0 {
		f.Limit = 0
	} else if f.Limit == 0 {
		// 显式传 0 表示"不分页"的场景由调用方自行控制，
		// 这里只对默认值（未设置）补默认条数。
		f.Limit = defaultLimit
	}
	if f.Limit > maxLimit {
		f.Limit = maxLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	switch f.SortBy {
	case SortByAction:
	default:
		f.SortBy = SortByTimestamp
	}
	switch f.SortOrder {
	case SortAsc:
	default:
		f.SortOrder = SortDesc
	}
	return f
}

// recorder 是 Recorder 接口的实现，把各模块的打点调用转换为统一的审计事件。
type recorder struct {
	svc Service
}

func (r *recorder) record(ctx context.Context, event Event) error {
	if r.svc == nil {
		return fmt.Errorf("audit: service is not configured")
	}
	_, err := r.svc.LogEvent(ctx, event)
	return err
}

func (r *recorder) RecordAgentEvent(ctx context.Context, eventType EventType, agentID string, details map[string]interface{}) error {
	return r.record(ctx, Event{
		Actor:      agentID,
		ActorType:  ActorTypeAgent,
		Action:     string(eventType),
		Resource:   ResourceAgent,
		ResourceID: agentID,
		Details:    details,
		Status:     StatusSuccess,
	})
}

func (r *recorder) RecordTaskEvent(ctx context.Context, eventType EventType, taskID string, details map[string]interface{}) error {
	status := StatusSuccess
	if eventType == EventTaskFailed || eventType == EventTaskCancelled {
		status = StatusFailure
	}
	return r.record(ctx, Event{
		Actor:      taskID,
		ActorType:  ActorTypeAgent,
		Action:     string(eventType),
		Resource:   ResourceTask,
		ResourceID: taskID,
		Details:    details,
		Status:     status,
	})
}

func (r *recorder) RecordUserEvent(ctx context.Context, eventType EventType, userID string, details map[string]interface{}) error {
	status := StatusSuccess
	if eventType == EventUserDeleted {
		status = StatusSuccess
	}
	return r.record(ctx, Event{
		Actor:      userID,
		ActorType:  ActorTypeUser,
		Action:     string(eventType),
		Resource:   ResourceUser,
		ResourceID: userID,
		Details:    details,
		Status:     status,
	})
}

func (r *recorder) RecordMessageEvent(ctx context.Context, eventType EventType, messageID string, details map[string]interface{}) error {
	return r.record(ctx, Event{
		Actor:      messageID,
		ActorType:  ActorTypeAgent,
		Action:     string(eventType),
		Resource:   ResourceMessage,
		ResourceID: messageID,
		Details:    details,
		Status:     StatusSuccess,
	})
}

func (r *recorder) RecordPermissionEvent(ctx context.Context, eventType EventType, userID string, details map[string]interface{}) error {
	return r.record(ctx, Event{
		Actor:      userID,
		ActorType:  ActorTypeUser,
		Action:     string(eventType),
		Resource:   ResourcePermission,
		ResourceID: userID,
		Details:    details,
		Status:     StatusSuccess,
	})
}

func (r *recorder) RecordSystemEvent(ctx context.Context, eventType EventType, details map[string]interface{}) error {
	status := StatusSuccess
	if eventType == EventSystemError {
		status = StatusFailure
	}
	return r.record(ctx, Event{
		Actor:     "system",
		ActorType: ActorTypeSystem,
		Action:    string(eventType),
		Resource:  ResourceSystem,
		Details:   details,
		Status:    status,
	})
}

// NewEventID 生成一个审计事件 ID，供需要预分配 ID 的调用方使用。
func NewEventID() string {
	return "aud_" + uuid.NewString()
}

// SortActions 返回动作名列表的稳定排序结果，便于测试与展示。
func SortActions(actions []string) []string {
	out := append([]string(nil), actions...)
	sort.Strings(out)
	return out
}
