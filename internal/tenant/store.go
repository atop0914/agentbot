package tenant

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Store 是租户数据的存储接口。
//
// 接口刻意按租户维度设计（而不是「返回全部再把过滤交给调用方」）：
// 当存储换成 PostgreSQL 时，它的实现必须是 `WHERE tenant_id = $1`，
// 而不是「取回来再在内存里筛」。把过滤写进接口签名，才能在换实现时
// 强制每个方法都考虑租户条件。
type Store interface {
	// Create 创建租户。
	Create(ctx context.Context, t *Tenant) (*Tenant, error)
	// Get 返回租户；不存在返回 ErrNotFound。
	Get(ctx context.Context, id ID) (*Tenant, error)
	// List 返回全部租户（按创建时间升序）。
	List(ctx context.Context) ([]*Tenant, error)
	// Update 更新租户（状态、名称、配额等）。
	Update(ctx context.Context, t *Tenant) (*Tenant, error)

	// AddMember 把用户加入租户。
	AddMember(ctx context.Context, m *Member) (*Member, error)
	// RemoveMember 把用户移出租户。
	RemoveMember(ctx context.Context, tenantID ID, userID string) error
	// ListMembers 返回租户成员（按加入时间升序）。
	ListMembers(ctx context.Context, tenantID ID) ([]*Member, error)
	// TenantsOfUser 返回用户所属的全部租户 ID（升序）。
	TenantsOfUser(ctx context.Context, userID string) ([]ID, error)
	// MemberOf 判断用户是否属于租户。
	MemberOf(ctx context.Context, tenantID ID, userID string) bool
	// CountMembers 统计租户成员数（配额检查用）。
	CountMembers(ctx context.Context, tenantID ID) int

	// SetOwner 登记一个资源的归属。
	SetOwner(ctx context.Context, o Owner) error
	// Owner 返回资源归属；未登记返回 ErrNotFound。
	Owner(ctx context.Context, rt ResourceType, resID string) (Owner, error)
	// RemoveOwner 移除资源归属。
	RemoveOwner(ctx context.Context, rt ResourceType, resID string) error
	// CountResources 统计租户某类资源的数量。
	CountResources(ctx context.Context, tenantID ID, rt ResourceType) int
}

// MemoryStore 是 Store 的内存实现。
//
// 所有读取路径都返回副本：调用方拿到指针后修改结构体会污染已存数据，
// 而租户数据被污染的表现是「某个租户忽然看得到别人的资源」——
// 这类问题在内存实现里静默、在生产实现里不会出现，最容易漏掉。
type MemoryStore struct {
	mu sync.RWMutex

	tenants map[ID]*Tenant
	// members[tenantID][userID]
	members map[ID]map[string]*Member
	// owners[resourceType][resourceID]
	owners map[ResourceType]map[string]Owner
}

// NewMemoryStore 创建内存租户存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tenants: make(map[ID]*Tenant),
		members: make(map[ID]map[string]*Member),
		owners:  make(map[ResourceType]map[string]Owner),
	}
}

func (s *MemoryStore) Create(_ context.Context, t *Tenant) (*Tenant, error) {
	if t == nil {
		return nil, ErrNotFound
	}
	cp := cloneTenant(*t)

	s.mu.Lock()
	defer s.mu.Unlock()
	if cp.ID == None {
		cp.ID = ID(newID("tenant"))
	}
	if _, exists := s.tenants[cp.ID]; exists {
		// ID 冲突不静默覆盖：覆盖会让「刚建的租户变成了别人的」无从追溯。
		cp.ID = ID(newID("tenant"))
	}
	now := time.Now().UTC()
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = now
	}
	cp.UpdatedAt = now
	s.tenants[cp.ID] = &cp
	out := cloneTenant(cp)
	return &out, nil
}

func (s *MemoryStore) Get(_ context.Context, id ID) (*Tenant, error) {
	if id == None {
		return nil, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := cloneTenant(*t)
	return &cp, nil
}

func (s *MemoryStore) List(_ context.Context) ([]*Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Tenant, 0, len(s.tenants))
	for _, t := range s.tenants {
		if t == nil {
			continue
		}
		cp := cloneTenant(*t)
		out = append(out, &cp)
	}
	// 排序是硬要求：map 迭代顺序随机会让接口输出与测试断言不稳定。
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *MemoryStore) Update(_ context.Context, t *Tenant) (*Tenant, error) {
	if t == nil || t.ID == None {
		return nil, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.tenants[t.ID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := cloneTenant(*t)
	// 创建时间不可被外部改写：它参与排序与保留策略。
	cp.CreatedAt = existing.CreatedAt
	cp.UpdatedAt = time.Now().UTC()
	s.tenants[cp.ID] = &cp
	out := cloneTenant(cp)
	return &out, nil
}

func (s *MemoryStore) AddMember(_ context.Context, m *Member) (*Member, error) {
	if m == nil || m.TenantID == None || strings.TrimSpace(m.UserID) == "" {
		return nil, ErrNotFound
	}
	cp := *m
	cp.Roles = append([]string(nil), m.Roles...)

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tenants[cp.TenantID]; !ok {
		return nil, ErrNotFound
	}
	if s.members[cp.TenantID] == nil {
		s.members[cp.TenantID] = make(map[string]*Member)
	}
	if existing, ok := s.members[cp.TenantID][cp.UserID]; ok {
		out := *existing
		out.Roles = append([]string(nil), existing.Roles...)
		return &out, nil
	}
	if cp.JoinedAt.IsZero() {
		cp.JoinedAt = time.Now().UTC()
	}
	s.members[cp.TenantID][cp.UserID] = &cp
	out := cp
	out.Roles = append([]string(nil), cp.Roles...)
	return &out, nil
}

func (s *MemoryStore) RemoveMember(_ context.Context, tenantID ID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	byUser, ok := s.members[tenantID]
	if !ok {
		return ErrNotFound
	}
	if _, ok := byUser[userID]; !ok {
		return ErrNotFound
	}
	delete(byUser, userID)
	return nil
}

func (s *MemoryStore) ListMembers(_ context.Context, tenantID ID) ([]*Member, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byUser, ok := s.members[tenantID]
	if !ok {
		// 空租户返回空切片而不是错误：后台展示「暂无成员」即可。
		return []*Member{}, nil
	}
	out := make([]*Member, 0, len(byUser))
	for _, m := range byUser {
		if m == nil {
			continue
		}
		cp := *m
		cp.Roles = append([]string(nil), m.Roles...)
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].JoinedAt.Equal(out[j].JoinedAt) {
			return out[i].JoinedAt.Before(out[j].JoinedAt)
		}
		return out[i].UserID < out[j].UserID
	})
	return out, nil
}

func (s *MemoryStore) TenantsOfUser(_ context.Context, userID string) ([]ID, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return []ID{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []ID
	for tid, byUser := range s.members {
		if _, ok := byUser[userID]; ok {
			out = append(out, tid)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if out == nil {
		out = []ID{}
	}
	return out, nil
}

func (s *MemoryStore) MemberOf(_ context.Context, tenantID ID, userID string) bool {
	if tenantID == None || userID == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	byUser, ok := s.members[tenantID]
	if !ok {
		return false
	}
	_, ok = byUser[userID]
	return ok
}

// CountMembers 统计租户成员数。
func (s *MemoryStore) CountMembers(_ context.Context, tenantID ID) int {
	if tenantID == None {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.members[tenantID])
}

func (s *MemoryStore) SetOwner(_ context.Context, o Owner) error {
	if !o.Type.Valid() || strings.TrimSpace(o.ResID) == "" {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owners[o.Type] == nil {
		s.owners[o.Type] = make(map[string]Owner)
	}
	s.owners[o.Type][o.ResID] = o
	return nil
}

func (s *MemoryStore) Owner(_ context.Context, rt ResourceType, resID string) (Owner, error) {
	if !rt.Valid() || strings.TrimSpace(resID) == "" {
		return Owner{}, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.owners[rt][resID]
	if !ok {
		return Owner{}, ErrNotFound
	}
	return o, nil
}

func (s *MemoryStore) RemoveOwner(_ context.Context, rt ResourceType, resID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	byRes, ok := s.owners[rt]
	if !ok {
		return ErrNotFound
	}
	if _, ok := byRes[resID]; !ok {
		return ErrNotFound
	}
	delete(byRes, resID)
	return nil
}

func (s *MemoryStore) CountResources(_ context.Context, tenantID ID, rt ResourceType) int {
	if tenantID == None || !rt.Valid() {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, o := range s.owners[rt] {
		if o.TenantID == tenantID {
			n++
		}
	}
	return n
}

// cloneTenant 返回租户的深拷贝（目前字段都是值类型，预留切片字段的复制位）。
func cloneTenant(t Tenant) Tenant {
	cp := t
	return cp
}

// newID 生成带前缀的稳定标识；随机源不可用时回落到时间，
// 绝不返回空串（空 ID 会让租户无法被引用）。
func newID(prefix string) string {
	if id, err := uuid.NewRandom(); err == nil {
		return prefix + "-" + id.String()
	}
	return prefix + "-" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
}
