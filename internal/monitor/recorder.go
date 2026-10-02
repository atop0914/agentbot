package monitor

import (
	"context"
	"time"
)

// DispositionRecorder 是告警处置的审计落地点。
//
// 之所以定义成本包自己的回调类型而不是直接依赖 internal/audit：
//
//  1. monitor 是观测面模块，不该因为「记录处置」而反向依赖审计模块的模型；
//  2. 装配层（internal/app）可以在这一层把处置动作翻译成审计事件
//     （资源类型、动作名、明细字段都由装配层决定）；
//  3. 测试可以注入一个内存实现，断言「处置是否真的留痕」而不必启动审计服务。
type DispositionRecorder interface {
	// RecordDisposition 记录一次处置动作。
	//
	// 返回的错误**不**会回滚状态迁移：处置已经发生，审计失败只意味着
	// 留痕缺失，调用方（服务层）会把它记录到日志而不是拒绝用户操作。
	// 原因：拒绝一次真实的运维操作比丢一条审计记录更危险。
	RecordDisposition(ctx context.Context, alert *Alert, disposition *AlertDisposition) error
}

// DispositionRecorderFunc 让普通函数满足 DispositionRecorder。
type DispositionRecorderFunc func(ctx context.Context, alert *Alert, disposition *AlertDisposition) error

// RecordDisposition 实现 DispositionRecorder。
func (f DispositionRecorderFunc) RecordDisposition(ctx context.Context, alert *Alert, disposition *AlertDisposition) error {
	if f == nil {
		return nil
	}
	return f(ctx, alert, disposition)
}

// timeNow 便于测试替换时间源；生产路径上就是 time.Now().UTC()。
var timeNow = func() time.Time { return time.Now().UTC() }
