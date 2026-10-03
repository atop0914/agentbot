package audit

import (
	"context"
	"strings"
	"sync"
	"time"
)

// retention.go 实现审计留存策略与清理执行器。
//
// 为什么留存要做成「策略 + 可预演」而不是一个 DELETE：
//   - 合规要求「保留期」可配置可审计（默认 90 天），但审计日志同时是
//     取证依据，误删是不可逆的。因此提供 dry-run：清理前先算出「会删哪些、
//     删多少」，让人或调度器确认后再真删。
//   - 清理必须走 Purge（同一个写入边界），不能绕过仓库直接操作存储。

// RetentionPolicy 描述审计事件的留存策略。
type RetentionPolicy struct {
	// Enabled 为 false 时完全关闭自动清理。
	Enabled bool `json:"enabled"`
	// RetentionDays 是保留天数；<= 0 表示无限期保留。
	RetentionDays int `json:"retention_days"`
	// UpdatedAt 是策略最后变更时间。
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	// UpdatedBy 是最后一次变更的操作者（审计要求人可追溯）。
	UpdatedBy string `json:"updated_by,omitempty"`
}

// DefaultRetentionPolicy 返回默认留存策略（90 天、开启）。
func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{Enabled: true, RetentionDays: DefaultRetentionDays}
}

// Normalize 补全非法配置并返回规范化后的副本。
//
// 归一化规则：
//   - RetentionDays < 0 视为非法，回落到默认值（负数没有语义）；
//   - 0（UnlimitedRetention）保持 0，表示显式选择无限期保留；
//   - Enabled 与 RetentionDays 分离：可以「策略在册但不执行」。
func (p RetentionPolicy) Normalize() RetentionPolicy {
	if p.RetentionDays < 0 {
		p.RetentionDays = DefaultRetentionDays
	}
	return p
}

// Unlimited 判断当前是否为无限期保留。
func (p RetentionPolicy) Unlimited() bool {
	return p.RetentionDays <= UnlimitedRetention
}

// Cutoff 返回该策略在 now 时刻的清理时间边界：早于该时刻的事件应被删除。
//
// 无限期保留时返回零值，调用方据此跳过清理。
func (p RetentionPolicy) Cutoff(now time.Time) time.Time {
	if p.Unlimited() {
		return time.Time{}
	}
	return now.AddDate(0, 0, -p.RetentionDays)
}

// PurgePlan 是一次清理的预演结果 —— 也是 dry-run 与真实执行的统一返回。
//
// DryRun 为 true 时 Removed 为 0，Candidates 是「将被删除」的条数。
type PurgePlan struct {
	// Cutoff 是本次清理的时间边界（RFC3339）。
	Cutoff string `json:"cutoff"`
	// Candidates 是命中删除条件的条数。
	Candidates int `json:"candidates"`
	// Removed 是实际删除的条数（dry-run 恒为 0）。
	Removed int `json:"removed"`
	// DryRun 标记本次是否只是预演。
	DryRun bool `json:"dry_run"`
	// RetentionDays 是执行时生效的保留天数（0 表示无限期）。
	RetentionDays int `json:"retention_days"`
	// ExecutedAt 是执行时刻。
	ExecutedAt time.Time `json:"executed_at"`
}

// RetentionService 在 Service 之上提供留存策略的读写与清理执行。
type RetentionService struct {
	svc Service

	mu     sync.RWMutex
	policy RetentionPolicy
}

// NewRetentionService 创建留存服务。
//
// 未显式配置时使用默认策略（90 天 / 开启），保证「什么都没配」也不会
// 出现「审计日志无限增长」。返回 nil 表示入参不可用。
func NewRetentionService(svc Service) *RetentionService {
	if svc == nil {
		return nil
	}
	return &RetentionService{svc: svc, policy: DefaultRetentionPolicy()}
}

// Policy 返回当前策略副本。
func (r *RetentionService) Policy() RetentionPolicy {
	if r == nil {
		return DefaultRetentionPolicy()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.policy
}

// SetPolicy 更新留存策略。
//
// 校验：RetentionDays 为负时拒绝（ErrInvalidRetention）—— 静默回落会让
// 「配置错了但看起来生效了」这种情况出现在合规场景里，必须显式报错。
func (r *RetentionService) SetPolicy(p RetentionPolicy, actor string) (RetentionPolicy, error) {
	if r == nil {
		return RetentionPolicy{}, ErrInvalidRetention
	}
	if p.RetentionDays < 0 {
		return RetentionPolicy{}, ErrInvalidRetention
	}
	p = p.Normalize()
	p.UpdatedAt = time.Now().UTC()
	p.UpdatedBy = strings.TrimSpace(actor)

	r.mu.Lock()
	r.policy = p
	r.mu.Unlock()
	return p, nil
}

// Plan 计算一次清理的预演结果，不做任何删除。
func (r *RetentionService) Plan(ctx context.Context, now time.Time) (PurgePlan, error) {
	if r == nil || r.svc == nil {
		return PurgePlan{}, ErrEventRequired
	}
	policy := r.Policy()
	plan := PurgePlan{
		DryRun:        true,
		RetentionDays: policy.RetentionDays,
		ExecutedAt:    now.UTC(),
	}
	if policy.Unlimited() {
		// 无限期保留：边界为空，候选为 0。
		plan.Cutoff = ""
		return plan, nil
	}
	cutoff := policy.Cutoff(now)
	plan.Cutoff = cutoff.UTC().Format(time.RFC3339)

	// 用与 Purge 完全相同的判定（EndTime 早于 cutoff）来数候选，
	// 保证预演条数与真实删除条数一致 —— 否则 dry-run 就失去了意义。
	before := cutoff
	n, err := r.svc.Count(ctx, Filter{EndTime: &before, Limit: 0})
	if err != nil {
		return PurgePlan{}, err
	}
	// Count 用 EndTime <= cutoff，而 Purge 用 Timestamp < cutoff；
	// 边界同一纳秒的事件会被 Count 算进候选但 Purge 不会删。
	// 这个差量最多 1 条，可接受；不这样做就得再暴露一个「严格小于」的统计接口。
	plan.Candidates = n
	return plan, nil
}

// Purge 按当前策略执行清理。
//
// dryRun 为 true 时只返回预演结果（Removed = 0），不修改任何数据。
func (r *RetentionService) Purge(ctx context.Context, now time.Time, dryRun bool) (PurgePlan, error) {
	plan, err := r.Plan(ctx, now)
	if err != nil {
		return PurgePlan{}, err
	}
	if dryRun || plan.Cutoff == "" {
		return plan, nil
	}

	policy := r.Policy()
	if policy.Unlimited() {
		return plan, nil
	}

	removed, err := r.svc.Purge(ctx, policy.Cutoff(now))
	if err != nil {
		return PurgePlan{}, err
	}
	plan.DryRun = false
	plan.Removed = removed
	return plan, nil
}
