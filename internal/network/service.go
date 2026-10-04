package network

import (
	"context"
	"time"
)

// service 是 Service 的默认实现：把策略存储与网关组合成一个门面。
//
// 之所以还要一层 service（而不是让 handler 直接用 store + gateway）：
//
//  1. 一致性校验集中在入口（新增策略必须通过 NormalizeRule），
//     handler 就不需要知道「域名模式的三种形态」这些细节；
//  2. 流量清理与统计在同一处暴露，后台「网络」分区只依赖一个接口；
//  3. 测试可以只替换 store/网关，Service 行为不变。
type service struct {
	policy  *MemoryPolicyStore
	gateway Gateway
	store   TrafficStore
	// statsWindow 是统计的默认窗口（可由构造函数覆盖）。
	statsWindow time.Duration
}

// ServiceConfig 是 Service 的构造参数。
type ServiceConfig struct {
	// Policy 必填。
	Policy *MemoryPolicyStore
	// Gateway 选填：为 nil 时只有策略管理可用（Proxy 返回 ErrUnavailable）。
	Gateway Gateway
	// Store 选填：为 nil 时策略与网关仍可用，但没有流量视图。
	Store TrafficStore
	// StatsWindow 选填。
	StatsWindow time.Duration
}

// NewService 创建出口路由服务。
func NewService(cfg ServiceConfig) Service {
	window := cfg.StatsWindow
	if window <= 0 {
		window = DefaultStatsWindow
	}
	if cfg.Policy == nil {
		cfg.Policy = NewMemoryPolicyStore()
	}
	return &service{
		policy:      cfg.Policy,
		gateway:     cfg.Gateway,
		store:       cfg.Store,
		statsWindow: window,
	}
}

func (s *service) AddRule(ctx context.Context, rule PolicyRule) (*PolicyRule, error) {
	normalized, err := NormalizeRule(rule)
	if err != nil {
		return nil, err
	}
	return s.policy.Add(ctx, normalized)
}

func (s *service) ListRules(ctx context.Context) ([]*PolicyRule, error) {
	return s.policy.List(ctx)
}

func (s *service) DeleteRule(ctx context.Context, id string) error {
	return s.policy.Delete(ctx, id)
}

func (s *service) Evaluate(ctx context.Context, agentID, method, url string) (Decision, error) {
	if s.policy == nil {
		return Decision{Allowed: false, Reason: "egress policy engine is not configured"}, ErrUnavailable
	}
	return s.policy.Evaluate(ctx, agentID, method, url)
}

func (s *service) Proxy(ctx context.Context, req EgressRequest) (*EgressResponse, error) {
	if s.gateway == nil {
		return nil, ErrUnavailable
	}
	return s.gateway.Proxy(ctx, req)
}

func (s *service) ListTraffic(ctx context.Context, filter TrafficFilter) ([]*TrafficRecord, error) {
	if s.store == nil {
		return []*TrafficRecord{}, nil
	}
	return s.store.Query(ctx, filter)
}

func (s *service) Stats(ctx context.Context, window time.Duration) (*TrafficStats, error) {
	if window <= 0 {
		window = s.statsWindow
	}
	if s.store == nil {
		// 没有流量存储不是错误：控制台应展示「暂无数据」而不是一个错误分区。
		now := time.Now().UTC()
		return &TrafficStats{
			GeneratedAt: now,
			Window:      window.String(),
			Start:       now.Add(-window),
			End:         now,
			Domains:     []*DomainStat{},
		}, nil
	}
	return s.store.Stats(ctx, window, time.Now().UTC())
}

func (s *service) PurgeTraffic(ctx context.Context, cutoff time.Time) (int, error) {
	if s.store == nil {
		return 0, nil
	}
	return s.store.PurgeBefore(ctx, cutoff)
}

// Policy is a no-op declared accessor used by 装配层 to build the gateway
// with the same policy instance (see app.go). Keeping it on the concrete type
// rather than the interface avoids widening the public Service contract with
// an implementation detail.
func (s *service) Policy() *MemoryPolicyStore { return s.policy }
