package authz

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// memoryStore 是 Store 的线程安全内存实现。
//
// 存储结构以 subject key 为一级索引（subject → 角色 ID → 授予记录），
// 天然保证同一主体不会重复授予同一角色，也使撤销是 O(1) 定位。
type memoryStore struct {
	mu          sync.RWMutex
	assignments map[string]map[string]Assignment // subjectKey -> roleID -> assignment
}

// NewMemoryStore 创建内存授权存储。
func NewMemoryStore() Store {
	return &memoryStore{assignments: make(map[string]map[string]Assignment)}
}

func (s *memoryStore) Assign(_ context.Context, assignment *Assignment) error {
	if assignment == nil {
		return wrap(ErrInvalidSubject, "assignment is nil")
	}
	key := assignment.Subject.Key()

	s.mu.Lock()
	defer s.mu.Unlock()

	byRole, ok := s.assignments[key]
	if !ok {
		byRole = make(map[string]Assignment)
		s.assignments[key] = byRole
	}
	if _, exists := byRole[assignment.RoleID]; exists {
		return wrap(ErrConflict, "%s already has role %s", key, assignment.RoleID)
	}
	// 存副本，避免调用方后续修改污染已存数据。
	byRole[assignment.RoleID] = *assignment
	return nil
}

func (s *memoryStore) Revoke(_ context.Context, subject Subject, roleID string) error {
	key := subject.Key()

	s.mu.Lock()
	defer s.mu.Unlock()

	byRole, ok := s.assignments[key]
	if !ok {
		return wrap(ErrForbidden, "%s has no assignments", key)
	}
	if _, exists := byRole[roleID]; !exists {
		return wrap(ErrForbidden, "%s does not have role %s", key, roleID)
	}
	delete(byRole, roleID)
	if len(byRole) == 0 {
		delete(s.assignments, key)
	}
	return nil
}

func (s *memoryStore) ListAssignments(_ context.Context, subject Subject) ([]Assignment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	byRole := s.assignments[subject.Key()]
	out := make([]Assignment, 0, len(byRole))
	for _, a := range byRole {
		out = append(out, a)
	}
	// 遍历 map 必须排序，否则输出顺序不稳定（授予时间相同时按角色 ID 兜底）。
	sort.Slice(out, func(i, j int) bool {
		if !out[i].GrantedAt.Equal(out[j].GrantedAt) {
			return out[i].GrantedAt.Before(out[j].GrantedAt)
		}
		return out[i].RoleID < out[j].RoleID
	})
	return out, nil
}

func (s *memoryStore) CountByRole(_ context.Context) (map[string]int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	counts := make(map[string]int)
	for _, byRole := range s.assignments {
		for roleID := range byRole {
			counts[roleID]++
		}
	}
	return counts, nil
}

// TargetRegistry 的内存实现。
type memoryTargetRegistry struct {
	mu         sync.RWMutex
	validators map[string]TargetValidator
}

// NewTargetRegistry 创建目标校验注册表。
//
// 内置一些不依赖业务模块的校验器（user 视为恒存在，由装配层覆盖为真实校验）。
func NewTargetRegistry() TargetRegistry {
	return &memoryTargetRegistry{validators: make(map[string]TargetValidator)}
}

func (r *memoryTargetRegistry) Register(targetType string, validator TargetValidator) {
	if targetType == "" || validator == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.validators[targetType] = validator
}

func (r *memoryTargetRegistry) Validate(ctx context.Context, target Target) error {
	if target.IsZero() {
		// 平台级动作没有具体目标，视为合法。
		return nil
	}
	r.mu.RLock()
	validator, ok := r.validators[target.Type]
	r.mu.RUnlock()
	if !ok {
		return wrap(ErrUnknownTargetType, "no validator registered for target type %q", target.Type)
	}
	if err := validator(ctx, target.ID); err != nil {
		return fmt.Errorf("%w: %s %s: %v", ErrTargetNotFound, target.Type, target.ID, err)
	}
	return nil
}

// nowUTC 统一时间戳，避免测试与生产出现时区差异。
func nowUTC() time.Time { return time.Now().UTC() }
