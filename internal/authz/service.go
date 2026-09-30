package authz

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/atop0914/agentbot/internal/role"
)

// maxAuditEntries 限制内存中的判定记录数量，避免长时间运行后无界增长。
// 达到上限后丢弃最旧的记录（Day 25 会把判定记录接入持久化审计模块）。
const maxAuditEntries = 2000

// defaultAuditLimit 是查询判定记录时的默认条数。
const defaultAuditLimit = 50

// service 是 Service 接口的实现。
type service struct {
	store   Store
	roles   role.Service
	targets TargetRegistry
	now     func() time.Time // 便于测试注入时钟

	mu       sync.RWMutex
	audit    []AuditEntry
	seq      int64
	decided  int
	denied   int
	degraded int
	subjects map[string]Subject // subjectKey -> Subject，供 Stats 汇总主体维度
}

// Option 用于覆盖服务的默认依赖。
type Option func(*service)

// WithClock 注入时钟（测试用）。
func WithClock(now func() time.Time) Option {
	return func(s *service) { s.now = now }
}

// NewService 创建授权链服务。
//
// roles 用于校验角色真实存在并解析权限；targets 可为 nil（此时目标一律不校验），
// 便于在没有业务模块的单元测试里单独使用。
func NewService(store Store, roles role.Service, targets TargetRegistry, opts ...Option) Service {
	if store == nil {
		store = NewMemoryStore()
	}
	if targets == nil {
		targets = NewTargetRegistry()
	}
	s := &service{
		store:    store,
		roles:    roles,
		targets:  targets,
		now:      nowUTC,
		subjects: make(map[string]Subject),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *service) Assign(ctx context.Context, subject Subject, roleID, grantedBy string) (*Assignment, error) {
	if subject.IsZero() {
		return nil, wrap(ErrInvalidSubject, "subject type and id are required")
	}
	if strings.TrimSpace(roleID) == "" {
		return nil, wrap(ErrUnknownRole, "role_id is required")
	}
	if s.roles == nil {
		return nil, wrap(ErrUnavailable, "role service is not configured")
	}

	r, err := s.roles.GetRole(ctx, roleID)
	if err != nil {
		return nil, wrap(ErrUnknownRole, "role %s: %v", roleID, err)
	}

	assignment := &Assignment{
		ID:        "as-" + subject.Key() + "-" + roleID,
		Subject:   subject,
		RoleID:    r.ID,
		RoleName:  r.Name,
		GrantedBy: grantedBy,
		GrantedAt: s.now(),
	}
	if err := s.store.Assign(ctx, assignment); err != nil {
		return nil, err
	}
	s.trackSubject(subject)
	// 返回值与存储内容解耦，避免调用方改动影响已存记录。
	cp := *assignment
	return &cp, nil
}

func (s *service) Revoke(ctx context.Context, subject Subject, roleID string) error {
	if subject.IsZero() {
		return wrap(ErrInvalidSubject, "subject type and id are required")
	}
	if strings.TrimSpace(roleID) == "" {
		return wrap(ErrUnknownRole, "role_id is required")
	}
	if err := s.store.Revoke(ctx, subject, roleID); err != nil {
		return err
	}
	s.trackSubject(subject)
	return nil
}

func (s *service) ListAssignments(ctx context.Context, subject Subject) ([]Assignment, error) {
	if subject.IsZero() {
		return nil, wrap(ErrInvalidSubject, "subject type and id are required")
	}
	s.trackSubject(subject)
	return s.store.ListAssignments(ctx, subject)
}

// Authorize 解析主体当前生效的权限链。
//
// 权限是**所有未过期角色权限的并集**，且按权限名排序 —— 输出必须稳定，
// 否则前端展示与测试断言都会随机漂移。
func (s *service) Authorize(ctx context.Context, subject Subject) (*Authorization, error) {
	if subject.IsZero() {
		return nil, wrap(ErrInvalidSubject, "subject type and id are required")
	}
	if s.roles == nil {
		return nil, wrap(ErrUnavailable, "role service is not configured")
	}
	s.trackSubject(subject)

	assignments, err := s.store.ListAssignments(ctx, subject)
	if err != nil {
		return nil, err
	}

	now := s.now()
	auth := &Authorization{
		Subject:      subject,
		Assignments:  make([]Assignment, 0, len(assignments)),
		Roles:        make([]*role.Role, 0, len(assignments)),
		Permissions:  []role.Permission{},
		ByPermission: map[string][]string{},
		UpdatedAt:    now,
	}

	seenPerm := make(map[role.Permission]bool)
	for i := range assignments {
		a := assignments[i]
		// 过期授予仍然列在 assignments 里（便于排障），但不贡献权限。
		auth.Assignments = append(auth.Assignments, a)
		if a.Expired(now) {
			continue
		}

		r, err := s.roles.GetRole(ctx, a.RoleID)
		if err != nil || r == nil {
			// 角色已被删除：不影响其余角色，仅跳过。
			continue
		}
		auth.Roles = append(auth.Roles, r)
		for _, p := range r.Permissions {
			if !seenPerm[p] {
				seenPerm[p] = true
				auth.Permissions = append(auth.Permissions, p)
			}
			auth.ByPermission[string(p)] = append(auth.ByPermission[string(p)], r.Name)
		}
	}

	sort.Slice(auth.Permissions, func(i, j int) bool { return auth.Permissions[i] < auth.Permissions[j] })
	for perm, names := range auth.ByPermission {
		sort.Strings(names)
		auth.ByPermission[perm] = names
	}
	sort.Slice(auth.Roles, func(i, j int) bool { return auth.Roles[i].ID < auth.Roles[j].ID })

	return auth, nil
}

// Decide 判定主体是否可以对 target 执行 action。
//
// 判定顺序刻意如此：
//  1. 参数与动作合法性 — 非法动作直接拒绝（不因为有角色就放行未知动作）；
//  2. 目标存在性 — 拒绝不存在的目标，避免「权限对了但对象错了」的越权探测；
//  3. 权限解析 — 任一环节故障都按 fail-closed 拒绝并标记 Degraded。
func (s *service) Decide(ctx context.Context, subject Subject, action role.Permission, target Target) (*Decision, error) {
	decision := &Decision{
		Subject:   subject,
		Action:    action,
		Target:    target,
		DecidedAt: s.now(),
	}

	if subject.IsZero() {
		decision.Reason = "invalid subject: type and id are required"
		s.record(decision)
		return decision, wrap(ErrInvalidSubject, "subject type and id are required")
	}
	s.trackSubject(subject)

	if !role.ValidatePermission(action) {
		decision.Reason = "unknown permission: " + string(action)
		s.record(decision)
		return decision, wrap(ErrForbidden, "unknown permission %q", action)
	}

	if err := s.targets.Validate(ctx, target); err != nil {
		decision.Reason = "target validation failed: " + err.Error()
		s.record(decision)
		return decision, err
	}

	auth, err := s.Authorize(ctx, subject)
	if err != nil {
		// fail-closed：授权链不可用时绝不能放行。
		decision.Allowed = false
		decision.Degraded = true
		decision.Reason = "authorization chain unavailable: " + err.Error()
		s.record(decision)
		return decision, wrap(ErrUnavailable, "%s", decision.Reason)
	}

	decision.GrantedBy = auth.ByPermission[string(action)]
	decision.Allowed = len(decision.GrantedBy) > 0
	if decision.Allowed {
		decision.Reason = "permission granted by role(s)"
	} else {
		decision.Reason = "no assigned role provides " + string(action)
		decision.GrantedBy = nil
	}

	s.record(decision)
	if !decision.Allowed {
		return decision, wrap(ErrForbidden, "%s is not allowed to %s %s", subject.Key(), action, describeTarget(target))
	}
	return decision, nil
}

// Require 是 Decide 的便利封装，只返回错误。
func (s *service) Require(ctx context.Context, subject Subject, action role.Permission, target Target) error {
	_, err := s.Decide(ctx, subject, action, target)
	return err
}

func (s *service) Audit(_ context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = defaultAuditLimit
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]AuditEntry, 0, limit)
	// audit 按追加顺序保存，倒序取即「最近优先」。
	for i := len(s.audit) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.audit[i])
	}
	return out, nil
}

func (s *service) Stats(ctx context.Context) (*Stats, error) {
	byRole, err := s.store.CountByRole(ctx)
	if err != nil {
		return nil, err
	}

	stats := &Stats{ByRole: byRole, GeneratedAt: s.now()}

	s.mu.RLock()
	subjects := make([]Subject, 0, len(s.subjects))
	for _, sub := range s.subjects {
		subjects = append(subjects, sub)
	}
	s.mu.RUnlock()

	// 主体维度需要逐主体展开（内存 Store 的 CountByRole 只给角色维度）。
	for _, sub := range subjects {
		list, err := s.store.ListAssignments(ctx, sub)
		if err != nil {
			continue
		}
		for _, a := range list {
			stats.TotalAssignments++
			switch a.Subject.Type {
			case SubjectUser:
				stats.UserAssignments++
			case SubjectAgent:
				stats.AgentAssignments++
			}
			if a.Expired(stats.GeneratedAt) {
				stats.Expired++
			}
		}
	}

	s.mu.RLock()
	stats.Decisions = s.decided
	stats.Denials = s.denied
	stats.Degraded = s.degraded
	s.mu.RUnlock()

	return stats, nil
}

// trackSubject 记录出现过的授权主体，供 Stats 汇总。
func (s *service) trackSubject(subject Subject) {
	if subject.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subjects[subject.Key()] = subject
}

// record 追加一条判定记录，并维护计数。
func (s *service) record(d *Decision) {
	if d == nil {
		return
	}
	entry := AuditEntry{
		DecidedAt: d.DecidedAt,
		Subject:   d.Subject,
		Action:    d.Action,
		Target:    d.Target,
		Allowed:   d.Allowed,
		Degraded:  d.Degraded,
		Reason:    d.Reason,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	entry.Sequence = s.seq
	s.audit = append(s.audit, entry)
	if len(s.audit) > maxAuditEntries {
		s.audit = append([]AuditEntry(nil), s.audit[len(s.audit)-maxAuditEntries:]...)
	}
	s.decided++
	if !d.Allowed {
		s.denied++
	}
	if d.Degraded {
		s.degraded++
	}
}

func describeTarget(t Target) string {
	if t.IsZero() {
		return "platform"
	}
	return t.Type + "/" + t.ID
}
