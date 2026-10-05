package tenant

import (
	"context"
	"strings"
	"time"
)

// Service 是多租户隔离的业务门面。
//
// 它把「谁能看到什么」收敛成两类调用：
//
//   - 租户/成员的生命周期（只有平台管理员能用）；
//   - 资源归属的判定（每条业务读写路径都要用）。
//
// 判定一律返回 404 语义的 ErrNotFound，绝不返回 403 —— 见包注释。
type Service interface {
	// ===== 租户生命周期 =====

	// Create 创建租户。Slug 重复会被拒绝。
	Create(ctx context.Context, req CreateRequest) (*Tenant, error)
	// List 返回全部租户（平台管理员视角）。
	List(ctx context.Context) ([]*Tenant, error)
	// Get 返回租户详情。
	Get(ctx context.Context, id ID) (*Tenant, error)
	// UpdateStatus 暂停/恢复租户。
	UpdateStatus(ctx context.Context, id ID, status Status) (*Tenant, error)

	// ===== 成员 =====

	// AddMember 把用户加入租户（同租户重复加入是幂等的）。
	AddMember(ctx context.Context, tenantID ID, userID, invitedBy string) (*Member, error)
	// RemoveMember 把用户移出租户。
	RemoveMember(ctx context.Context, tenantID ID, userID string) error
	// ListMembers 返回成员列表。
	ListMembers(ctx context.Context, tenantID ID) ([]*Member, error)
	// TenantsOfUser 返回用户所属租户。
	TenantsOfUser(ctx context.Context, userID string) ([]ID, error)
	// Resolve 解析一次请求的可信租户上下文。
	//
	// 这是隔离的**唯一入口**：tenantID 必须来自服务端签发的凭据，
	// userID 用于校验成员关系。返回的 *Scope 内部自带 fail-closed 检查。
	Resolve(ctx context.Context, tenantID ID, userID string) (*Scope, error)

	// ===== 资源边界 =====

	// Register 登记资源归属（创建资源时调用）。
	Register(ctx context.Context, tenantID ID, rt ResourceType, resID string) (Owner, error)
	// Unregister 移除资源归属（删除资源时调用）。
	Unregister(ctx context.Context, tenantID ID, rt ResourceType, resID string) error
	// Owner 返回资源归属；未登记返回 ErrNotFound。
	Owner(ctx context.Context, rt ResourceType, resID string) (Owner, error)
	// Check 判断租户能否访问资源；不可访问一律 ErrNotFound。
	Check(ctx context.Context, tenantID ID, rt ResourceType, resID string) error
	// CountResources 统计租户某类资源数（配额检查用）。
	CountResources(ctx context.Context, tenantID ID, rt ResourceType) int
	// EnforceQuota 检查是否还能再创建一份该类资源。
	EnforceQuota(ctx context.Context, tenantID ID, rt ResourceType) error

	// Stats 返回租户汇总（供管理后台）。
	Stats(ctx context.Context) (*Stats, error)
	// StatsFor 返回单租户视角的汇总。
	StatsFor(ctx context.Context, tenantID ID) (*Stats, error)
}

// CreateRequest 是创建租户的请求。
type CreateRequest struct {
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Plan    Plan   `json:"plan"`
	OwnerID string `json:"owner_id"`
}

// scope 是 Service 的默认实现。
type scopeService struct {
	store Store
	// clock 允许测试注入时间（保留策略与时间相关断言需要确定性）。
	clock func() time.Time
}

// NewService 创建租户服务。
func NewService(store Store) Service {
	if store == nil {
		store = NewMemoryStore()
	}
	return &scopeService{store: store, clock: func() time.Time { return time.Now().UTC() }}
}

// SetClock 注入时钟（仅测试使用）。
func SetClock(s Service, clock func() time.Time) {
	if ss, ok := s.(*scopeService); ok && clock != nil {
		ss.clock = clock
	}
}

func (s *scopeService) Create(ctx context.Context, req CreateRequest) (*Tenant, error) {
	name := strings.TrimSpace(req.Name)
	slug := normalizeSlug(req.Slug)
	if name == "" {
		name = slug
	}
	if slug == "" {
		return nil, ErrNotFound
	}
	plan := req.Plan
	if plan == "" {
		// 默认最低权限套餐：企业能力必须显式开通，不能靠「忘写」拿到。
		plan = PlanFree
	}
	if !plan.Valid() {
		return nil, ErrNotFound
	}

	existing, err := s.store.List(ctx)
	if err != nil {
		return nil, ErrUnavailable
	}
	for _, t := range existing {
		if t.Slug == slug {
			// slug 是租户的对外唯一别名，重复会让 SSO 域与展示名歧义。
			return nil, ErrNotFound
		}
	}

	t, err := s.store.Create(ctx, &Tenant{
		Name:    name,
		Slug:    slug,
		Plan:    plan,
		Status:  StatusActive,
		Quota:   defaultQuota(plan),
		OwnerID: strings.TrimSpace(req.OwnerID),
	})
	if err != nil {
		return nil, err
	}

	// 创建者自动成为成员：否则新租户会立刻处于「没人能访问」的状态，
	// 表现为「建完就 404」，是最容易被当成 bug 的配置问题。
	if t.OwnerID != "" {
		if _, err := s.store.AddMember(ctx, &Member{
			TenantID: t.ID,
			UserID:   t.OwnerID,
			Roles:    []string{"owner"},
		}); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (s *scopeService) List(ctx context.Context) ([]*Tenant, error) {
	return s.store.List(ctx)
}

func (s *scopeService) Get(ctx context.Context, id ID) (*Tenant, error) {
	return s.store.Get(ctx, id)
}

func (s *scopeService) UpdateStatus(ctx context.Context, id ID, status Status) (*Tenant, error) {
	if !status.Valid() {
		return nil, ErrNotFound
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	t.Status = status
	return s.store.Update(ctx, t)
}

func (s *scopeService) AddMember(ctx context.Context, tenantID ID, userID, invitedBy string) (*Member, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrNotFound
	}
	t, err := s.store.Get(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if !t.Quota.Unlimited("members") &&
		s.store.CountMembers(ctx, tenantID) >= t.Quota.MaxMembers {
		return nil, ErrQuotaExceeded
	}
	return s.store.AddMember(ctx, &Member{
		TenantID:  tenantID,
		UserID:    userID,
		InvitedBy: strings.TrimSpace(invitedBy),
	})
}

func (s *scopeService) RemoveMember(ctx context.Context, tenantID ID, userID string) error {
	return s.store.RemoveMember(ctx, tenantID, userID)
}

func (s *scopeService) ListMembers(ctx context.Context, tenantID ID) ([]*Member, error) {
	return s.store.ListMembers(ctx, tenantID)
}

func (s *scopeService) TenantsOfUser(ctx context.Context, userID string) ([]ID, error) {
	return s.store.TenantsOfUser(ctx, userID)
}

// Resolve 把「凭据里的租户 + 用户」解析为一个作用域。
//
// 逐条检查（任何一条不过都返回 404/401，绝不降级为「放行全部」）：
//  1. tenantID 合法（非空、格式正确）—— 空租户不是「全局」；
//  2. 租户存在 —— 不存在的租户视为不可见；
//  3. 租户未暂停；
//  4. 用户确实是该租户成员 —— 防止「拿着 A 租户的 token 看 B 租户的数据」。
func (s *scopeService) Resolve(ctx context.Context, tenantID ID, userID string) (*Scope, error) {
	if !tenantID.Valid() {
		return nil, ErrIdentityRequired
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrIdentityRequired
	}
	t, err := s.store.Get(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if !t.Usable() {
		return nil, ErrSuspended
	}
	if !s.store.MemberOf(ctx, tenantID, userID) {
		// 非成员看到的是 404 而不是 403：不泄漏「这个租户存在」。
		return nil, ErrNotFound
	}
	return &Scope{tenant: t, userID: userID, svc: s}, nil
}

func (s *scopeService) Register(ctx context.Context, tenantID ID, rt ResourceType, resID string) (Owner, error) {
	if !rt.Valid() {
		return Owner{}, ErrNotFound
	}
	resID = strings.TrimSpace(resID)
	if resID == "" || tenantID == None {
		return Owner{}, ErrNotFound
	}
	o := Owner{TenantID: tenantID, Type: rt, ResID: resID}
	if err := s.store.SetOwner(ctx, o); err != nil {
		return Owner{}, err
	}
	return o, nil
}

func (s *scopeService) Unregister(ctx context.Context, tenantID ID, rt ResourceType, resID string) error {
	// 删除也要过租户校验：否则「知道 ID 就能删别人的资源」。
	if err := s.Check(ctx, tenantID, rt, resID); err != nil {
		return err
	}
	return s.store.RemoveOwner(ctx, rt, resID)
}

func (s *scopeService) Owner(ctx context.Context, rt ResourceType, resID string) (Owner, error) {
	return s.store.Owner(ctx, rt, resID)
}

// Check 是跨租户访问的闸门，返回 ErrNotFound 表示「在你的租户里不存在」。
func (s *scopeService) Check(ctx context.Context, tenantID ID, rt ResourceType, resID string) error {
	if tenantID == None {
		return ErrIdentityRequired
	}
	o, err := s.store.Owner(ctx, rt, resID)
	if err != nil {
		return err
	}
	if !o.Matches(tenantID) {
		// 关键：别人家的资源与不存在的资源返回同一个错误。
		return ErrNotFound
	}
	return nil
}

func (s *scopeService) CountResources(ctx context.Context, tenantID ID, rt ResourceType) int {
	return s.store.CountResources(ctx, tenantID, rt)
}

func (s *scopeService) EnforceQuota(ctx context.Context, tenantID ID, rt ResourceType) error {
	t, err := s.store.Get(ctx, tenantID)
	if err != nil {
		return err
	}
	current := s.store.CountResources(ctx, tenantID, rt)
	switch rt {
	case ResourceAgent:
		if !t.Quota.Unlimited("agents") && current >= t.Quota.MaxAgents {
			return ErrQuotaExceeded
		}
	case ResourceEgressRule:
		if !t.Quota.Unlimited("egress_rules") && current >= t.Quota.MaxEgressRules {
			return ErrQuotaExceeded
		}
	}
	return nil
}

func (s *scopeService) Stats(ctx context.Context) (*Stats, error) {
	tenants, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	st := &Stats{
		GeneratedAt: s.clock(),
		ByPlan:      map[string]int{},
		Resources:   map[string]int{},
	}
	st.TotalTenants = len(tenants)
	for _, t := range tenants {
		if t.Status == StatusActive {
			st.ActiveTenants++
		} else {
			st.Suspended++
		}
		st.ByPlan[string(t.Plan)]++
		members, _ := s.store.ListMembers(ctx, t.ID)
		st.TotalMembers += len(members)
		for _, rt := range []ResourceType{ResourceAgent, ResourceTask, ResourceAudit, ResourceEgressRule, ResourceEnvironment} {
			st.Resources[string(rt)] += s.store.CountResources(ctx, t.ID, rt)
		}
	}
	return st, nil
}

func (s *scopeService) StatsFor(ctx context.Context, tenantID ID) (*Stats, error) {
	t, err := s.store.Get(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	members, _ := s.store.ListMembers(ctx, tenantID)
	st := &Stats{
		GeneratedAt:  s.clock(),
		TotalTenants: 1,
		TotalMembers: len(members),
		ByPlan:       map[string]int{string(t.Plan): 1},
		Resources:    map[string]int{},
		Scoped:       true,
		TenantID:     tenantID,
	}
	if t.Status == StatusActive {
		st.ActiveTenants = 1
	} else {
		st.Suspended = 1
	}
	for _, rt := range []ResourceType{ResourceAgent, ResourceTask, ResourceAudit, ResourceEgressRule, ResourceEnvironment} {
		st.Resources[string(rt)] += s.store.CountResources(ctx, tenantID, rt)
	}
	return st, nil
}

// defaultQuota 按套餐给出默认配额。
func defaultQuota(p Plan) Quota {
	switch p {
	case PlanEnterprise:
		return Quota{MaxAgents: 500, MaxMembers: 1000, MaxEgressRules: 500}
	case PlanTeam:
		return Quota{MaxAgents: 50, MaxMembers: 100, MaxEgressRules: 100}
	default:
		return Quota{MaxAgents: 5, MaxMembers: 10, MaxEgressRules: 10}
	}
}

// normalizeSlug 规整租户别名：小写、仅保留字母数字与连字符。
//
// 严格化的理由是 slug 会出现在 SSO 域名与 URL 里，允许任意字符会引入
// 同形字与路径注入两类问题。
func normalizeSlug(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == ' ':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	// 折叠连续连字符，避免 acme--corp 与 acme-corp 看起来像同一个租户。
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return out
}
