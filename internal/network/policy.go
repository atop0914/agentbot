package network

import (
	"context"
	"strings"
	"sync"
	"time"
)

// scope 是策略条目的作用域，用于表达「Agent 维度优先于全局维度」。
const (
	scopeAgent  = "agent"
	scopeGlobal = "global"
)

// MemoryPolicyStore 是出口策略的内存实现。
//
// 之所以把策略存储与管理器分开：判定是每次出站都要走的热路径，
// 需要稳定的排序结果与快照语义；而增删改是低频运维动作。分开后
// 判定可以在读锁下基于**已排序快照**完成，不依赖 map 迭代顺序。
type MemoryPolicyStore struct {
	mu    sync.RWMutex
	rules map[string]*PolicyRule
}

// NewMemoryPolicyStore 创建内存策略存储。
func NewMemoryPolicyStore() *MemoryPolicyStore {
	return &MemoryPolicyStore{rules: make(map[string]*PolicyRule)}
}

// Add 写入一条策略，返回存储后的副本。
func (s *MemoryPolicyStore) Add(_ context.Context, rule PolicyRule) (*PolicyRule, error) {
	cp := cloneRule(rule)

	s.mu.Lock()
	defer s.mu.Unlock()
	if cp.ID == "" {
		cp.ID = newID("egress-rule")
	}
	if _, exists := s.rules[cp.ID]; exists {
		// ID 冲突不覆盖：静默覆盖会让「改错了哪条」无从追溯。
		cp.ID = newID("egress-rule")
	}
	s.rules[cp.ID] = cp
	return cloneRule(*cp), nil
}

// List 返回全部策略，排序规则见 sortRules。
func (s *MemoryPolicyStore) List(_ context.Context) ([]*PolicyRule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*PolicyRule, 0, len(s.rules))
	for _, r := range s.rules {
		if r == nil {
			continue
		}
		out = append(out, cloneRule(*r))
	}
	sortRules(out)
	return out, nil
}

// Delete 删除一条策略。
func (s *MemoryPolicyStore) Delete(_ context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return ErrRuleNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rules[id]; !ok {
		return ErrRuleNotFound
	}
	delete(s.rules, id)
	return nil
}

// Evaluate 基于当前策略快照做一次判定。
//
// 判定顺序（这是本包的安全核心，改动必须同步更新注释与测试）：
//
//  1. Agent 维度条目优先于全局条目 —— 为某个 Agent 单独收紧出口，
//     不能被一条宽泛的全局 allow 抵消；
//  2. 同作用域内「具体度」高的优先：精确域名 > 最长通配后缀 > 更短后缀 > "*"；
//  3. 具体度相同时 Deny 优先（fail-closed），
//     Priority 数值小的先判定，同 Priority 下 Deny 先于 Allow；
//  4. 方法不匹配的条目跳过（条目 methods 为空表示不限方法）；
//  5. 一条都没有命中 → **默认拒绝**。
func (s *MemoryPolicyStore) Evaluate(_ context.Context, agentID, method, url string) (Decision, error) {
	domain := DomainOf(url)
	if domain == "" {
		return Decision{Allowed: false, Reason: "unable to determine the destination domain"}, nil
	}

	s.mu.RLock()
	rules := make([]*PolicyRule, 0, len(s.rules))
	for _, r := range s.rules {
		if r == nil {
			continue
		}
		rules = append(rules, cloneRule(*r))
	}
	s.mu.RUnlock()

	sortRules(rules)

	// 两轮扫描：先 Agent 维度，再全局维度。这样「Agent 维度的 deny」
	// 永远压得住「全局的 allow」，而「Agent 维度的 allow」也能在全局
	// 默认收紧的部署里为单个 Agent 开口子。
	for _, scope := range []string{scopeAgent, scopeGlobal} {
		if scope == scopeAgent && agentID == "" {
			continue
		}
		var best *PolicyRule
		bestSpec := -1
		for _, r := range rules {
			if r.AgentID != "" && r.AgentID != agentID {
				continue
			}
			if (r.AgentID != "") != (scope == scopeAgent) {
				continue
			}
			if !effectMatchesMethod(r, method) {
				continue
			}
			spec := matchSpecificity(r.Target, domain)
			if spec < 0 {
				continue
			}
			if best == nil || spec > bestSpec || (spec == bestSpec && r.Effect == EffectDeny) {
				best = r
				bestSpec = spec
			}
		}
		if best != nil {
			allowed := best.Effect == EffectAllow
			decision := Decision{
				Allowed:        allowed,
				RuleID:         best.ID,
				MatchedPattern: best.Target,
				Scope:          scope,
				Reason:         decisionReason(best, domain),
			}
			return decision, nil
		}
	}

	return Decision{
		Allowed:        false,
		Scope:          scopeGlobal,
		Reason:         DefaultDenyReason,
		MatchedPattern: "",
	}, nil
}

// Rules 返回排序后的策略快照（便于测试与后台直接断言）。
func (s *MemoryPolicyStore) Rules() []*PolicyRule {
	rules, _ := s.List(context.Background())
	return rules
}

func decisionReason(rule *PolicyRule, domain string) string {
	if rule == nil {
		return DefaultDenyReason
	}
	verb := "allowed by"
	if rule.Effect == EffectDeny {
		verb = "denied by"
	}
	scope := "global"
	if rule.AgentID != "" {
		scope = "agent:" + rule.AgentID
	}
	reason := verb + " policy " + rule.ID + " (" + scope + ", pattern=" + rule.Target + ")"
	if rule.Description != "" {
		reason += ": " + rule.Description
	}
	return reason
}

// effectMatchesMethod 判断条目是否适用于该 HTTP 方法。
func effectMatchesMethod(rule *PolicyRule, method string) bool {
	if rule == nil || len(rule.Methods) == 0 {
		return true
	}
	if method == "" {
		// 未声明方法时按通配对待：「这个方法是否被允许」交给上层，
		// 但**不能**因为缺方法就绕过整条 deny 规则。
		return true
	}
	for _, m := range rule.Methods {
		if strings.EqualFold(strings.TrimSpace(m), method) {
			return true
		}
	}
	return false
}

// sortRules 给策略排序，保证判定与列表输出在任何 map 迭代顺序下都一致。
//
// 排序键：Agent 维度优先 → Priority 升序 → Deny 优先 → 具体度降序 → ID 升序。
func sortRules(rules []*PolicyRule) {
	sortSlice(rules, func(a, b *PolicyRule) bool {
		aAgent := a.AgentID != ""
		bAgent := b.AgentID != ""
		if aAgent != bAgent {
			return aAgent
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		aDeny := a.Effect == EffectDeny
		bDeny := b.Effect == EffectDeny
		if aDeny != bDeny {
			return aDeny
		}
		aSpec := patternSpecificity(a.Target)
		bSpec := patternSpecificity(b.Target)
		if aSpec != bSpec {
			return aSpec > bSpec
		}
		return a.ID < b.ID
	})
}

// patternSpecificity 给模式本身的「具体程度」打分（与目标域名无关）。
func patternSpecificity(pattern string) int {
	if pattern == "*" {
		return 0
	}
	if strings.HasPrefix(pattern, "*.") {
		return len(pattern) - 2
	}
	return 100000 + len(pattern)
}

// cloneRule 返回策略条目的深拷贝（Methods 切片必须复制）。
func cloneRule(r PolicyRule) *PolicyRule {
	cp := r
	cp.Methods = append([]string(nil), r.Methods...)
	return &cp
}

// NormalizeRule 校验并规整一条策略条目。
//
// 校验点：
//   - AgentID / Target 去空白；
//   - Target 必须能通过 ValidateTarget（拒绝 URL、端口、中间通配）；
//   - Effect 必须是 allow / deny，空值时沿用 deny（安全默认，不是 allow）；
//   - Methods 规整为大写去空白，去重。
//
// 刻意**不**允许 effect 缺省为 allow：一条「忘了写 effect」的规则如果
// 默认放行，就是一个静默的口子，而默认拒绝只会让人发现配置写错了。
func NormalizeRule(rule PolicyRule) (PolicyRule, error) {
	out := rule
	out.AgentID = strings.TrimSpace(out.AgentID)
	out.ID = strings.TrimSpace(out.ID)
	out.Description = strings.TrimSpace(out.Description)

	target, err := ValidateTarget(out.Target)
	if err != nil {
		return PolicyRule{}, err
	}
	out.Target = target

	if out.Effect == "" {
		out.Effect = EffectDeny
	}
	if !out.Effect.Valid() {
		return PolicyRule{}, ErrInvalidEffect
	}

	methods := make([]string, 0, len(out.Methods))
	seen := make(map[string]struct{}, len(out.Methods))
	for _, m := range out.Methods {
		mm := strings.ToUpper(strings.TrimSpace(m))
		if mm == "" {
			continue
		}
		if _, dup := seen[mm]; dup {
			continue
		}
		seen[mm] = struct{}{}
		methods = append(methods, mm)
	}
	if len(methods) == 0 {
		methods = nil
	}
	out.Methods = methods

	now := time.Now().UTC()
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	out.UpdatedAt = now
	return out, nil
}

// sortSlice 是一个泛型插入排序的薄包装。
//
// 策略条目数量是「人工维护」级别（几十条），插入排序在这里比 sort.Slice
// 更省分配，也避免了为 nil 元素单独处理比较函数。
func sortSlice[T any](items []T, less func(a, b T) bool) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && less(items[j], items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}
