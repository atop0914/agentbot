package audit

import (
	"context"
	"sort"
	"sync"
	"time"
)

// EventRecord 是写入审计仓库的完整事件记录。
// 与 Event 的区别：Event 是调用方提交的输入，EventRecord 是仓库保存后
// 补全了 ID / 时间戳 / 创建序号的可持久化实体。
type EventRecord struct {
	Event
	// Seq 是单调递增的写入序号，用于同毫秒内的事件排序，保证列表输出稳定。
	Seq int64 `json:"seq"`
}

// Repository 定义审计事件的持久化接口。
type Repository interface {
	// Append 追加一条审计事件并返回补全 ID/时间戳后的记录。
	Append(ctx context.Context, event Event) (*EventRecord, error)
	// List 按过滤条件查询事件，返回结果已排序。
	List(ctx context.Context, filter Filter) ([]*EventRecord, error)
	// Count 统计符合条件的事件数量。
	Count(ctx context.Context, filter Filter) (int, error)
	// Get 按 ID 获取单条事件。
	Get(ctx context.Context, id string) (*EventRecord, error)
	// Distinct 返回指定维度（actor / actor_type / action / resource / status / ip_address）
	// 的去重取值列表，用于筛选器下拉与统计聚合。
	Distinct(ctx context.Context, field string) ([]string, error)
	// Purge 删除早于 cutoff 的事件，返回删除条数。
	Purge(ctx context.Context, cutoff time.Time) (int, error)
}

// memoryRepository 是基于内存的 Repository 实现，全部方法线程安全。
type memoryRepository struct {
	mu     sync.RWMutex
	events []*EventRecord
	byID   map[string]*EventRecord
	seq    int64
}

// NewMemoryRepository 创建一个内存审计仓库。
func NewMemoryRepository() Repository {
	return &memoryRepository{
		events: make([]*EventRecord, 0, 64),
		byID:   make(map[string]*EventRecord),
	}
}

func (r *memoryRepository) Append(_ context.Context, event Event) (*EventRecord, error) {
	if event.Action == "" {
		return nil, ErrActionRequired
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	rec := &EventRecord{Event: event}
	if rec.ID == "" {
		rec.ID = "aud_" + time.Now().UTC().Format("20060102150405.000000000")
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}
	if rec.Status == "" {
		rec.Status = StatusSuccess
	}
	if rec.ActorType == "" {
		rec.ActorType = ActorTypeUser
	}
	if rec.Details != nil {
		// 存副本，避免调用方后续修改污染已保存的事件。
		cp := make(map[string]interface{}, len(rec.Details))
		for k, v := range rec.Details {
			cp[k] = v
		}
		rec.Details = cp
	}
	r.seq++
	rec.Seq = r.seq

	r.events = append(r.events, rec)
	r.byID[rec.ID] = rec

	out := *rec
	return &out, nil
}

// match 判断事件是否命中过滤条件（调用方需持有读锁）。
func (r *memoryRepository) match(rec *EventRecord, f Filter) bool {
	if f.StartTime != nil && rec.Timestamp.Before(*f.StartTime) {
		return false
	}
	if f.EndTime != nil && rec.Timestamp.After(*f.EndTime) {
		return false
	}
	if f.Actor != "" && rec.Actor != f.Actor {
		return false
	}
	if f.ActorType != "" && rec.ActorType != f.ActorType {
		return false
	}
	if f.EventType != "" && rec.Action != f.EventType {
		return false
	}
	if f.Resource != "" && rec.Resource != f.Resource {
		return false
	}
	if f.ResourceID != "" && rec.ResourceID != f.ResourceID {
		return false
	}
	if f.Status != "" && rec.Status != f.Status {
		return false
	}
	if f.IPAddress != "" && rec.IPAddress != f.IPAddress {
		return false
	}
	return true
}

func (r *memoryRepository) List(_ context.Context, filter Filter) ([]*EventRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	matched := make([]*EventRecord, 0, len(r.events))
	for _, rec := range r.events {
		if r.match(rec, filter) {
			matched = append(matched, rec)
		}
	}

	asc := filter.SortOrder == SortAsc
	sort.SliceStable(matched, func(i, j int) bool {
		var less bool
		switch filter.SortBy {
		case SortByAction:
			if matched[i].Action != matched[j].Action {
				less = matched[i].Action < matched[j].Action
			} else {
				less = matched[i].Seq < matched[j].Seq
			}
		default: // 默认按时间排序，同时间用 Seq 保证稳定
			if !matched[i].Timestamp.Equal(matched[j].Timestamp) {
				less = matched[i].Timestamp.Before(matched[j].Timestamp)
			} else {
				less = matched[i].Seq < matched[j].Seq
			}
		}
		if asc {
			return less
		}
		return !less
	})

	out := paginate(matched, filter.Limit, filter.Offset)
	for i := range out {
		cp := *out[i]
		out[i] = &cp
	}
	return out, nil
}

func (r *memoryRepository) Count(_ context.Context, filter Filter) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Count 忽略 Limit/Offset，统计的是命中条件的全量数量。
	countFilter := filter
	countFilter.Limit = 0
	countFilter.Offset = 0

	n := 0
	for _, rec := range r.events {
		if r.match(rec, countFilter) {
			n++
		}
	}
	return n, nil
}

func (r *memoryRepository) Get(_ context.Context, id string) (*EventRecord, error) {
	if id == "" {
		return nil, ErrIDRequired
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	rec, ok := r.byID[id]
	if !ok {
		return nil, ErrEventNotFound
	}
	cp := *rec
	return &cp, nil
}

func (r *memoryRepository) Distinct(_ context.Context, field string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]struct{})
	for _, rec := range r.events {
		var v string
		switch field {
		case DistinctActor:
			v = rec.Actor
		case DistinctActorType:
			v = rec.ActorType
		case DistinctAction:
			v = rec.Action
		case DistinctResource:
			v = rec.Resource
		case DistinctStatus:
			v = rec.Status
		case DistinctIPAddress:
			v = rec.IPAddress
		default:
			return nil, ErrUnknownDistinctField
		}
		if v != "" {
			seen[v] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	// 排序保证接口输出稳定。
	sort.Strings(out)
	return out, nil
}

func (r *memoryRepository) Purge(_ context.Context, cutoff time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if cutoff.IsZero() {
		return 0, ErrCutoffRequired
	}

	kept := r.events[:0:0]
	removed := 0
	for _, rec := range r.events {
		if rec.Timestamp.Before(cutoff) {
			delete(r.byID, rec.ID)
			removed++
			continue
		}
		kept = append(kept, rec)
	}
	r.events = kept
	return removed, nil
}

// paginate 对已排序结果应用 Offset/Limit，Limit <= 0 表示不限制。
func paginate(records []*EventRecord, limit, offset int) []*EventRecord {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(records) {
		return []*EventRecord{}
	}
	start := offset
	end := len(records)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return records[start:end]
}

// Len 返回仓库中当前保存的事件总数，主要用于测试与容量观测。
func (r *memoryRepository) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.events)
}
