package audit

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Recorder 是 Recorder 接口的一个宽松实现：任何 Recording 失败都不阻断主流程，
// 只把错误返回给调用方，由其决定是否忽略。
var _ Recorder = (*recorder)(nil)

// middleware.go 里放需要在多个模块间复用的审计辅助能力。

// multiRecorder 把事件扇出到多个 Recorder，用于同时落内存/落文件的场景。
type multiRecorder struct {
	mu        sync.RWMutex
	recorders []Recorder
}

// NewMultiRecorder 创建扇出记录器。
func NewMultiRecorder(recorders ...Recorder) Recorder {
	rs := make([]Recorder, 0, len(recorders))
	for _, r := range recorders {
		if r != nil {
			rs = append(rs, r)
		}
	}
	return &multiRecorder{recorders: rs}
}

// Add 追加一个下游记录器，线程安全。
func (m *multiRecorder) Add(r Recorder) {
	if r == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recorders = append(m.recorders, r)
}

// fanout 向所有下游记录器派发，返回聚合错误（不阻断，收集全部失败原因）。
func (m *multiRecorder) fanout(fn func(Recorder) error) error {
	m.mu.RLock()
	rs := append([]Recorder(nil), m.recorders...)
	m.mu.RUnlock()

	var firstErr error
	for _, r := range rs {
		if err := fn(r); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *multiRecorder) RecordAgentEvent(ctx context.Context, t EventType, agentID string, d map[string]interface{}) error {
	return m.fanout(func(r Recorder) error { return r.RecordAgentEvent(ctx, t, agentID, d) })
}

func (m *multiRecorder) RecordTaskEvent(ctx context.Context, t EventType, taskID string, d map[string]interface{}) error {
	return m.fanout(func(r Recorder) error { return r.RecordTaskEvent(ctx, t, taskID, d) })
}

func (m *multiRecorder) RecordUserEvent(ctx context.Context, t EventType, userID string, d map[string]interface{}) error {
	return m.fanout(func(r Recorder) error { return r.RecordUserEvent(ctx, t, userID, d) })
}

func (m *multiRecorder) RecordMessageEvent(ctx context.Context, t EventType, messageID string, d map[string]interface{}) error {
	return m.fanout(func(r Recorder) error { return r.RecordMessageEvent(ctx, t, messageID, d) })
}

func (m *multiRecorder) RecordPermissionEvent(ctx context.Context, t EventType, userID string, d map[string]interface{}) error {
	return m.fanout(func(r Recorder) error { return r.RecordPermissionEvent(ctx, t, userID, d) })
}

func (m *multiRecorder) RecordSystemEvent(ctx context.Context, t EventType, d map[string]interface{}) error {
	return m.fanout(func(r Recorder) error { return r.RecordSystemEvent(ctx, t, d) })
}

// BusinessEvent 是一次业务操作的审计描述，便于在 handler/service 里一行打点。
type BusinessEvent struct {
	Action     string
	Resource   string
	ResourceID string
	Actor      string
	ActorType  string
	Status     string
	Err        error
	IPAddress  string
	UserAgent  string
	Details    map[string]interface{}
}

// ToEvent 把业务事件描述转换为审计事件实体。
func (b BusinessEvent) ToEvent() Event {
	status := b.Status
	if status == "" {
		status = StatusSuccess
		if b.Err != nil {
			status = StatusFailure
		}
	}

	details := b.Details
	if b.Err != nil {
		if details == nil {
			details = make(map[string]interface{}, 1)
		}
		if _, ok := details["error"]; !ok {
			details["error"] = b.Err.Error()
		}
	}

	actorType := b.ActorType
	if actorType == "" {
		actorType = ActorTypeUser
	}

	return Event{
		Timestamp:  nowUTC(),
		Actor:      b.Actor,
		ActorType:  actorType,
		Action:     b.Action,
		Resource:   b.Resource,
		ResourceID: b.ResourceID,
		Details:    details,
		IPAddress:  b.IPAddress,
		UserAgent:  b.UserAgent,
		Status:     status,
		Error:      errString(b.Err),
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// nowUTC 统一审计事件的时间基准，便于测试替换与日志对齐。
func nowUTC() time.Time {
	return time.Now().UTC()
}

// Middleware 返回一个把业务事件写入审计的包装函数。
//
// 用法：
//
//	audited := audit.Middleware(svc, audit.BusinessEvent{Action: "user.login", Resource: audit.ResourceUser})
//	err := audited(ctx, func(ctx context.Context) error { return doLogin(ctx) })
func Middleware(svc Service, base BusinessEvent) func(ctx context.Context, fn func(context.Context) error) error {
	return func(ctx context.Context, fn func(context.Context) error) error {
		err := fn(ctx)
		ev := base
		if err != nil {
			ev.Err = err
		}
		if _, logErr := svc.LogEvent(ctx, ev.ToEvent()); logErr != nil {
			// 审计失败不应影响主流程，附加上下文后返回原始错误。
			return joinAuditErr(err, logErr)
		}
		return err
	}
}

func joinAuditErr(err, logErr error) error {
	if err == nil {
		return fmt.Errorf("audit: failed to write event: %w", logErr)
	}
	return err
}
