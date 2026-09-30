// Package authz: HTTP 层的权限判定中间件与路由表。
//
// 这一层解决的是「权限模型写了但没接进请求链路」的问题 —— 2026 年 TanStack
// 事件中 `pull_request_target` 的教训正是：**权限上下文配置正确，但被错误地
// 用在了不可信输入上**。因此这里做两件事：
//
//  1. RouteTable：把「路径 + 方法 → 所需权限」显式登记，没有登记的写操作一律拒绝；
//  2. Middleware：在 handler 之前完成判定，未授权返回 403，授权服务故障返回 503
//     （fail-closed，绝不因为「查不到」而放行）。
package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/atop0914/agentbot/internal/auth"
	"github.com/atop0914/agentbot/internal/role"
)

// RouteRule 描述一条路由的权限要求。
type RouteRule struct {
	// Method HTTP 方法；"" 表示匹配任意方法。
	Method string
	// Pattern 路径模式；以 "/" 结尾表示前缀匹配，否则精确匹配。
	Pattern string
	// Action 该路由所需的权限。
	Action role.Permission
	// TargetType 目标类型；非空时会把路径最后一段作为目标 ID 做存在性校验。
	TargetType string
	// Public 为 true 表示无需权限（如查询自己的授权信息）。
	Public bool
}

// RouteTable 是路由权限表的查询结构。
type RouteTable struct {
	rules []RouteRule
}

// NewRouteTable 创建路由权限表。
func NewRouteTable(rules []RouteRule) *RouteTable {
	cp := make([]RouteRule, len(rules))
	copy(cp, rules)
	return &RouteTable{rules: cp}
}

// Lookup 查找匹配的路由规则。
//
// 精确匹配优先于前缀匹配；同一优先级下按登记顺序取第一条。
// 返回 false 表示**没有登记** —— 调用方应据此拒绝（默认拒绝）。
func (t *RouteTable) Lookup(method, path string) (RouteRule, bool) {
	if t == nil {
		return RouteRule{}, false
	}
	// 先精确
	for _, r := range t.rules {
		if r.Pattern == "" || strings.HasSuffix(r.Pattern, "/") {
			continue
		}
		if r.Pattern == path && methodMatches(r.Method, method) {
			return r, true
		}
	}
	// 再前缀（最长的前缀优先，保证 /api/v1/users/x/roles 不会被 /api/v1/users/ 抢走）
	var best RouteRule
	bestLen := -1
	for _, r := range t.rules {
		if !strings.HasSuffix(r.Pattern, "/") {
			continue
		}
		if !strings.HasPrefix(path, r.Pattern) || !methodMatches(r.Method, method) {
			continue
		}
		if len(r.Pattern) > bestLen {
			best = r
			bestLen = len(r.Pattern)
		}
	}
	if bestLen >= 0 {
		return best, true
	}
	return RouteRule{}, false
}

// Rules 返回表中全部规则（按路径排序，便于后台展示与测试断言）。
func (t *RouteTable) Rules() []RouteRule {
	if t == nil {
		return nil
	}
	out := make([]RouteRule, len(t.rules))
	copy(out, t.rules)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pattern != out[j].Pattern {
			return out[i].Pattern < out[j].Pattern
		}
		return out[i].Method < out[j].Method
	})
	return out
}

func methodMatches(ruleMethod, actual string) bool {
	if ruleMethod == "" {
		return true
	}
	return strings.EqualFold(ruleMethod, actual)
}

// DefaultRouteTable 返回 AgentBot 的默认路由权限表。
//
// 覆盖设计原则：
//   - 读操作要求 *:read；
//   - 控制类操作（启停/暂停/执行）要求 *:control 或 *:execute；
//   - 破坏类操作（删除/销毁）要求 *:delete 或 *:destroy；
//   - 未登记的路径不在此表中 → 中间件默认拒绝（默认拒绝原则）。
func DefaultRouteTable() *RouteTable {
	return NewRouteTable([]RouteRule{
		// Agent
		{Method: http.MethodGet, Pattern: "/api/v1/agents", Action: role.PermAgentRead},
		{Method: http.MethodPost, Pattern: "/api/v1/agents", Action: role.PermAgentCreate},
		{Method: http.MethodGet, Pattern: "/api/v1/agents/", Action: role.PermAgentRead, TargetType: "agent"},
		{Method: http.MethodPut, Pattern: "/api/v1/agents/", Action: role.PermAgentUpdate, TargetType: "agent"},
		{Method: http.MethodDelete, Pattern: "/api/v1/agents/", Action: role.PermAgentDelete, TargetType: "agent"},

		// Cloud environment
		{Method: http.MethodGet, Pattern: "/api/v1/cloud/", Action: role.PermEnvRead},
		{Method: http.MethodPost, Pattern: "/api/v1/cloud/", Action: role.PermEnvExecute},

		// Task
		{Method: http.MethodGet, Pattern: "/api/v1/tasks", Action: role.PermTaskRead},
		{Method: http.MethodPost, Pattern: "/api/v1/tasks", Action: role.PermTaskCreate},
		{Method: http.MethodDelete, Pattern: "/api/v1/tasks/", Action: role.PermTaskCancel},

		// Terminal / filesystem（Agent 能力面里最危险的两块）
		{Method: http.MethodGet, Pattern: "/api/v1/terminal", Action: role.PermEnvRead},
		{Method: http.MethodPost, Pattern: "/api/v1/terminal/", Action: role.PermEnvExecute},
		{Method: http.MethodGet, Pattern: "/api/v1/filesystem", Action: role.PermFileRead},
		{Method: http.MethodPost, Pattern: "/api/v1/filesystem/", Action: role.PermFileWrite},
		{Method: http.MethodDelete, Pattern: "/api/v1/filesystem/", Action: role.PermFileDelete},

		// Browser / memory
		{Method: http.MethodGet, Pattern: "/api/v1/browser", Action: role.PermBrowserUse},
		{Method: http.MethodPost, Pattern: "/api/v1/browser/", Action: role.PermBrowserUse},
		{Method: http.MethodGet, Pattern: "/api/v1/memory", Action: role.PermMemoryRead},
		{Method: http.MethodPost, Pattern: "/api/v1/memory", Action: role.PermMemoryWrite},

		// 管理后台：用户与权限管理属于 user_admin（默认关闭的功能开关）
		{Method: http.MethodGet, Pattern: "/api/v1/users", Action: role.PermRoleManage},
		{Method: http.MethodGet, Pattern: "/api/v1/users/", Action: role.PermRoleManage},
		{Method: http.MethodPost, Pattern: "/api/v1/users/", Action: role.PermRoleManage, TargetType: "user"},
		{Method: http.MethodPut, Pattern: "/api/v1/users/", Action: role.PermRoleManage, TargetType: "user"},
		{Method: http.MethodDelete, Pattern: "/api/v1/users/", Action: role.PermRoleManage, TargetType: "user"},
		{Method: http.MethodPost, Pattern: "/api/v1/authorizations", Action: role.PermRoleAssign},
		{Method: http.MethodDelete, Pattern: "/api/v1/authorizations", Action: role.PermRoleRevoke},
		{Method: http.MethodGet, Pattern: "/api/v1/authorizations/me", Action: "", Public: true},
		{Method: http.MethodGet, Pattern: "/api/v1/authorizations/", Action: role.PermRoleManage},
		{Method: http.MethodGet, Pattern: "/api/v1/permission-routes", Action: role.PermRoleManage},
	})
}

// Middleware 在 HTTP 层执行权限判定。
type Middleware struct {
	svc   Service
	table *RouteTable
	// ResolveSubject 把请求映射为授权主体；缺省从 auth.Claims 取用户身份。
	ResolveSubject func(r *http.Request) Subject
}

// NewMiddleware 创建权限判定中间件。
func NewMiddleware(svc Service, table *RouteTable) *Middleware {
	if table == nil {
		table = DefaultRouteTable()
	}
	return &Middleware{
		svc:            svc,
		table:          table,
		ResolveSubject: SubjectFromClaims,
	}
}

// SubjectFromClaims 从认证中间件写入的 JWT claims 解析用户主体。
//
// 平台内部动作（Agent 以自身身份调用）由调用方用显式 header 声明，
// 但**始终以 JWT 的 user_id 为准**，不接受客户端自称的主体，避免越权提升。
func SubjectFromClaims(r *http.Request) Subject {
	claims := auth.GetClaimsFromContext(r.Context())
	if claims == nil || claims.UserID == "" {
		return Subject{}
	}
	return Subject{Type: SubjectUser, ID: claims.UserID}
}

// Authorize 是 http.Handler 包装器，按路由表判定权限。
func (m *Middleware) Authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rule, ok := m.table.Lookup(r.Method, r.URL.Path)
		if !ok {
			// 默认拒绝：未登记的写操作绝不放行；只读请求也要求显式登记，
			// 否则新增接口时会"忘记加权限"而静默裸奔。
			writeAuthzError(w, http.StatusForbidden, "no permission rule registered for this route")
			return
		}
		if rule.Public {
			next.ServeHTTP(w, r)
			return
		}

		subject := m.ResolveSubject(r)
		if subject.IsZero() {
			writeAuthzError(w, http.StatusUnauthorized, "authenticated identity required")
			return
		}

		target := m.targetFromRequest(rule, r)
		decision, err := m.svc.Decide(r.Context(), subject, rule.Action, target)
		if err == nil && decision != nil && decision.Allowed {
			next.ServeHTTP(w, r)
			return
		}

		switch {
		case IsUnavailable(err) || (decision != nil && decision.Degraded):
			// fail-closed：授权链故障时拒绝，并明确告知是"服务不可用"而不是"无权限"。
			writeAuthzError(w, http.StatusServiceUnavailable, "authorization service unavailable")
		case IsForbidden(err):
			writeAuthzError(w, http.StatusForbidden, "permission denied: "+string(rule.Action))
		default:
			writeAuthzError(w, http.StatusForbidden, "permission denied")
		}
	})
}

// targetFromRequest 依据规则推断权限动作的目标。
func (m *Middleware) targetFromRequest(rule RouteRule, r *http.Request) Target {
	if rule.TargetType == "" {
		return Target{}
	}
	id := targetIDFromPath(rule.Pattern, r.URL.Path)
	if id == "" {
		// 没有具体目标 ID（如集合路径）→ 视为平台级动作。
		return Target{}
	}
	return Target{Type: rule.TargetType, ID: id}
}

// targetIDFromPath 从路径中取出目标 ID。
//
// 对 /api/v1/users/{id}/roles 这类路径，{id} 是 id 之后紧跟的一段，
// 而不是最后一段，因此需要按 pattern 的段数裁剪。
func targetIDFromPath(pattern, path string) string {
	if pattern == "" || !strings.HasSuffix(pattern, "/") {
		return ""
	}
	rest := strings.TrimPrefix(path, pattern)
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return ""
	}
	if idx := strings.Index(rest, "/"); idx >= 0 {
		return rest[:idx]
	}
	return rest
}

func writeAuthzError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// RequireAction 是一个便利中间件：要求固定权限（不做路由表查询）。
//
// 用于无法从前缀推断权限的场景（例如同一路径不同语义的 handler）。
func (m *Middleware) RequireAction(action role.Permission, targetType string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			subject := m.ResolveSubject(r)
			if subject.IsZero() {
				writeAuthzError(w, http.StatusUnauthorized, "authenticated identity required")
				return
			}
			var target Target
			if targetType != "" {
				id := lastPathSegment(r.URL.Path)
				if id != "" {
					target = Target{Type: targetType, ID: id}
				}
			}
			decision, err := m.svc.Decide(r.Context(), subject, action, target)
			if err == nil && decision != nil && decision.Allowed {
				next.ServeHTTP(w, r)
				return
			}
			if IsUnavailable(err) {
				writeAuthzError(w, http.StatusServiceUnavailable, "authorization service unavailable")
				return
			}
			writeAuthzError(w, http.StatusForbidden, "permission denied: "+string(action))
		})
	}
}

func lastPathSegment(path string) string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, "/")
	return parts[len(parts)-1]
}

// DescribeRoute 生成路由规则的可读描述（后台展示权限矩阵用）。
func DescribeRoute(r RouteRule) string {
	method := r.Method
	if method == "" {
		method = "*"
	}
	if r.Public {
		return fmt.Sprintf("%s %s -> (public)", method, r.Pattern)
	}
	return fmt.Sprintf("%s %s -> %s", method, r.Pattern, r.Action)
}

// contextWithTarget 便于测试与内部调用传递目标（保留扩展点）。
func contextWithTarget(ctx context.Context, target Target) context.Context {
	return context.WithValue(ctx, targetContextKey{}, target)
}

type targetContextKey struct{}

// TargetFromContext 取出由中间件解析的目标。
func TargetFromContext(ctx context.Context) (Target, bool) {
	t, ok := ctx.Value(targetContextKey{}).(Target)
	return t, ok
}
