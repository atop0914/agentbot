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
	// Suffix 为 true 时把 Pattern 当作**路径后缀**匹配。
	//
	// 用于 /api/v1/users/{id}/status 这类「中间带 ID、尾部是固定子资源名」的
	// 路由：前缀匹配无法表达，因为 ID 段长度不定。
	Suffix bool
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
	// 再后缀（最长的后缀优先）。必须在通配前缀之前判定，否则
	// /api/v1/users/{id}/status 会被 /api/v1/users/ 前缀规则抢走。
	var bestSuffix RouteRule
	bestSuffixLen := -1
	for _, r := range t.rules {
		if !r.Suffix {
			continue
		}
		if !strings.HasSuffix(path, r.Pattern) || !methodMatches(r.Method, method) {
			continue
		}
		if len(r.Pattern) > bestSuffixLen {
			bestSuffix = r
			bestSuffixLen = len(r.Pattern)
		}
	}
	if bestSuffixLen >= 0 {
		return bestSuffix, true
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
		// Agent 生命周期动作（start/stop/pause/resume）走 POST 子路径。
		{Method: http.MethodPost, Pattern: "/api/v1/agents/", Action: role.PermAgentControl, TargetType: "agent"},

		// Cloud environment
		{Method: http.MethodGet, Pattern: "/api/v1/cloud/", Action: role.PermEnvRead},
		{Method: http.MethodPost, Pattern: "/api/v1/cloud/", Action: role.PermEnvExecute},
		{Method: http.MethodGet, Pattern: "/api/v1/environments", Action: role.PermEnvRead},
		{Method: http.MethodPost, Pattern: "/api/v1/environments", Action: role.PermEnvCreate},
		{Method: http.MethodGet, Pattern: "/api/v1/environments/", Action: role.PermEnvRead},
		{Method: http.MethodPost, Pattern: "/api/v1/environments/", Action: role.PermEnvExecute},
		{Method: http.MethodDelete, Pattern: "/api/v1/environments/", Action: role.PermEnvDestroy},

		// Task
		{Method: http.MethodGet, Pattern: "/api/v1/tasks", Action: role.PermTaskRead},
		{Method: http.MethodPost, Pattern: "/api/v1/tasks", Action: role.PermTaskCreate},
		{Method: http.MethodGet, Pattern: "/api/v1/tasks/", Action: role.PermTaskRead},
		{Method: http.MethodDelete, Pattern: "/api/v1/tasks/", Action: role.PermTaskCancel},

		// Terminal / filesystem（Agent 能力面里最危险的两块）
		//
		// 注意路径是复数 /terminals 与 /filesystem/<op>，与 handler 注册的前缀
		// 一致；写错过一次（单数 /terminal）会导致整组路由落进默认拒绝。
		{Method: http.MethodGet, Pattern: "/api/v1/terminals", Action: role.PermEnvRead},
		{Method: http.MethodPost, Pattern: "/api/v1/terminals", Action: role.PermEnvExecute},
		{Method: http.MethodGet, Pattern: "/api/v1/terminals/", Action: role.PermEnvRead},
		{Method: http.MethodPost, Pattern: "/api/v1/terminals/", Action: role.PermEnvExecute},
		{Method: http.MethodDelete, Pattern: "/api/v1/terminals/", Action: role.PermEnvDestroy},
		{Method: http.MethodGet, Pattern: "/api/v1/filesystem/", Action: role.PermFileRead},
		{Method: http.MethodPost, Pattern: "/api/v1/filesystem/", Action: role.PermFileWrite},
		{Method: http.MethodDelete, Pattern: "/api/v1/filesystem/", Action: role.PermFileDelete},

		// Browser / memory
		{Method: http.MethodGet, Pattern: "/api/v1/browser", Action: role.PermBrowserUse},
		{Method: http.MethodGet, Pattern: "/api/v1/browser/", Action: role.PermBrowserUse},
		{Method: http.MethodPost, Pattern: "/api/v1/browser/", Action: role.PermBrowserUse},
		{Method: http.MethodDelete, Pattern: "/api/v1/browser/", Action: role.PermBrowserUse},
		{Method: http.MethodGet, Pattern: "/api/v1/browsers", Action: role.PermBrowserUse},
		{Method: http.MethodPost, Pattern: "/api/v1/browsers", Action: role.PermBrowserUse},
		{Method: http.MethodGet, Pattern: "/api/v1/browsers/", Action: role.PermBrowserUse},
		{Method: http.MethodDelete, Pattern: "/api/v1/browsers/", Action: role.PermBrowserUse},
		{Method: http.MethodGet, Pattern: "/api/v1/memory", Action: role.PermMemoryRead},
		{Method: http.MethodPost, Pattern: "/api/v1/memory", Action: role.PermMemoryWrite},
		{Method: http.MethodGet, Pattern: "/api/v1/memory/", Action: role.PermMemoryRead},
		{Method: http.MethodPost, Pattern: "/api/v1/memory/", Action: role.PermMemoryWrite},
		{Method: http.MethodDelete, Pattern: "/api/v1/memory/", Action: role.PermMemoryWrite},

		// 应用适配器：连接外部系统（邮件/日历），按写权限对待。
		{Method: http.MethodGet, Pattern: "/api/v1/adapters", Action: role.PermMemoryRead},
		{Method: http.MethodGet, Pattern: "/api/v1/adapters/", Action: role.PermMemoryRead},
		{Method: http.MethodPost, Pattern: "/api/v1/adapters", Action: role.PermMemoryWrite},
		{Method: http.MethodPost, Pattern: "/api/v1/adapters/", Action: role.PermMemoryWrite},
		{Method: http.MethodDelete, Pattern: "/api/v1/adapters/", Action: role.PermMemoryWrite},

		// 任务拆解：只读推理，不落盘。
		{Method: http.MethodPost, Pattern: "/api/v1/decompose", Action: role.PermTaskCreate},

		// 多 Agent 通信
		{Method: http.MethodGet, Pattern: "/api/v1/messages", Action: role.PermMessageRead},
		{Method: http.MethodPost, Pattern: "/api/v1/messages", Action: role.PermMessageSend},
		{Method: http.MethodPost, Pattern: "/api/v1/messages/", Action: role.PermMessageSend},
		{Method: http.MethodGet, Pattern: "/api/v1/messages/", Action: role.PermMessageRead},
		{Method: http.MethodGet, Pattern: "/api/v1/groups", Action: role.PermMessageRead},
		{Method: http.MethodPost, Pattern: "/api/v1/groups", Action: role.PermMessageBroadcast},
		{Method: http.MethodPost, Pattern: "/api/v1/groups/", Action: role.PermMessageBroadcast},
		{Method: http.MethodGet, Pattern: "/api/v1/groups/", Action: role.PermMessageRead},

		// 工作流录制 → 模板生成
		{Method: http.MethodPost, Pattern: "/api/v1/recordings/start", Action: role.PermMemoryWrite},
		{Method: http.MethodPost, Pattern: "/api/v1/recordings/step", Action: role.PermMemoryWrite},
		{Method: http.MethodPost, Pattern: "/api/v1/recordings/stop", Action: role.PermMemoryWrite},

		// 模板与模板市场
		{Method: http.MethodGet, Pattern: "/api/v1/templates", Action: role.PermMemoryRead},
		{Method: http.MethodGet, Pattern: "/api/v1/templates/", Action: role.PermMemoryRead},
		{Method: http.MethodPost, Pattern: "/api/v1/templates", Action: role.PermMemoryWrite},
		{Method: http.MethodPost, Pattern: "/api/v1/templates/", Action: role.PermMemoryWrite},
		{Method: http.MethodDelete, Pattern: "/api/v1/templates/", Action: role.PermMemoryWrite},
		{Method: http.MethodGet, Pattern: "/api/v1/marketplace", Action: role.PermMemoryRead},
		{Method: http.MethodGet, Pattern: "/api/v1/marketplace/", Action: role.PermMemoryRead},
		{Method: http.MethodPost, Pattern: "/api/v1/marketplace/", Action: role.PermMemoryWrite},

		// Agent 健康监控：观测面只读。
		//
		// 注意：/monitor/ 前缀规则覆盖不到集合路径（无尾斜杠），
		// Day 24 新增的 /monitor/task-outcomes 与 /monitor/alert-dispositions
		// 必须单独登记，否则落进默认拒绝。
		{Method: http.MethodGet, Pattern: "/api/v1/monitor/", Action: role.PermAgentRead},
		{Method: http.MethodGet, Pattern: "/api/v1/monitor/alert-dispositions", Action: role.PermAgentRead},
		// 任务结果上报属于「写」：它是观测数据的写入面，按 agent:control 对待。
		{Method: http.MethodPost, Pattern: "/api/v1/monitor/task-outcomes", Action: role.PermAgentControl},
		// 指标上报：POST /monitor/agents 与 POST /monitor/agents/{id}/metrics。
		// 两者都是「Agent 上报自身观测数据」，但集合路径没有尾斜杠，
		// 会被前缀规则漏掉，因此显式登记。
		{Method: http.MethodPost, Pattern: "/api/v1/monitor/agents", Action: role.PermAgentControl},
		{Method: http.MethodPost, Pattern: "/api/v1/monitor/agents/", Action: role.PermAgentControl},
		// 告警处置改变告警状态，属于控制类操作而非只读观测。
		{Method: http.MethodPost, Pattern: "/api/v1/monitor/alerts", Action: role.PermAgentControl},
		{Method: http.MethodPost, Pattern: "/api/v1/monitor/alerts/", Action: role.PermAgentControl},

		// 审计日志：读审计需要 role:manage（属敏感数据）。
		// 写入（/audit/events）由业务模块经内部 recorder 产生，HTTP 直写同样
		// 要求 role:manage，避免任何人伪造审计记录。
		{Method: http.MethodGet, Pattern: "/api/v1/audit/", Action: role.PermRoleManage},
		{Method: http.MethodPost, Pattern: "/api/v1/audit/events", Action: role.PermRoleManage},
		{Method: http.MethodPost, Pattern: "/api/v1/audit/purge", Action: role.PermRoleManage},
		// 留存策略与清理：改保留期等价于决定「证据能留多久」，
		// 与删除历史数据同级敏感，一律 role:manage。
		// 注意 /retention、/retention/purge、/export/verify 都是
		// 无尾斜杠的集合路径 / 子资源，前缀规则覆盖不到，必须显式登记。
		{Method: http.MethodGet, Pattern: "/api/v1/audit/retention", Action: role.PermRoleManage},
		{Method: http.MethodPut, Pattern: "/api/v1/audit/retention", Action: role.PermRoleManage},
		{Method: http.MethodPost, Pattern: "/api/v1/audit/retention/purge", Action: role.PermRoleManage},
		{Method: http.MethodPost, Pattern: "/api/v1/audit/export/verify", Action: role.PermRoleManage},

		// 网络出口路由（Day 26）：策略管理 + 网关代理 + 出站流量审计。
		//
		// 注意这一组全部是**无尾斜杠的集合路径 / 子资源**，前缀规则
		// /api/v1/network/ 覆盖不到（/rules/ 只能覆盖到带 ID 的删除），
		// 必须逐条显式登记，否则会落进默认拒绝返回 403。
		//
		// 权限映射的取舍：
		//   - 改出口策略 = 决定 Agent 能连什么，属于元级安全配置 → role:manage；
		//   - 走网关出站 = Agent 用网络能力 → env:execute（与终端同级）；
		//   - 读流量/统计 = 观测面 → agent:read（与监控口径一致）。
		{Method: http.MethodGet, Pattern: "/api/v1/network/rules", Action: role.PermRoleManage},
		{Method: http.MethodPost, Pattern: "/api/v1/network/rules", Action: role.PermRoleManage},
		{Method: http.MethodDelete, Pattern: "/api/v1/network/rules/", Action: role.PermRoleManage},
		{Method: http.MethodPost, Pattern: "/api/v1/network/evaluate", Action: role.PermEnvExecute},
		{Method: http.MethodPost, Pattern: "/api/v1/network/proxy", Action: role.PermEnvExecute},
		{Method: http.MethodGet, Pattern: "/api/v1/network/traffic", Action: role.PermAgentRead},
		{Method: http.MethodPost, Pattern: "/api/v1/network/traffic/purge", Action: role.PermRoleManage},
		{Method: http.MethodGet, Pattern: "/api/v1/network/stats", Action: role.PermAgentRead},
		{Method: http.MethodGet, Pattern: "/api/v1/network/summary", Action: role.PermAgentRead},

		// 多租户（Day 27）：租户生命周期 + 成员 + 资源边界自查。
		//
		// 权限映射的取舍：
		//   - 建租户 / 暂停租户 = 决定「谁能拥有资源」，是平台级元权限
		//     → role:manage（与角色矩阵同级，因为它们都能改变权限边界）；
		//   - 成员增删 = 改变租户的可见范围 → user:update（与用户管理同级）；
		//   - 读租户/成员/资源清单 = 观测面 → user:read。
		//
		// 集合路径 /api/v1/tenants 无尾斜杠，前缀规则覆盖不到；
		// /{id}/status、/{id}/members、/{id}/resources、/current 同理，
		// 全部必须显式登记 —— 这正是 Day 24 踩过的坑。
		{Method: http.MethodGet, Pattern: "/api/v1/tenants", Action: role.PermUserRead},
		{Method: http.MethodPost, Pattern: "/api/v1/tenants", Action: role.PermRoleManage},
		{Method: http.MethodGet, Pattern: "/api/v1/tenants/", Action: role.PermUserRead},
		{Method: http.MethodPost, Pattern: "/api/v1/tenants/status", Action: role.PermRoleManage, Suffix: true},
		{Method: http.MethodGet, Pattern: "/api/v1/tenants/current", Action: role.PermUserRead, Suffix: true},
		{Method: http.MethodGet, Pattern: "/api/v1/tenants/members", Action: role.PermUserRead, Suffix: true},
		{Method: http.MethodPost, Pattern: "/api/v1/tenants/members", Action: role.PermUserUpdate, Suffix: true},
		{Method: http.MethodDelete, Pattern: "/api/v1/tenants/members/", Action: role.PermUserUpdate},
		{Method: http.MethodGet, Pattern: "/api/v1/tenants/resources", Action: role.PermUserRead, Suffix: true},

		// 企业 SSO（Day 27）：登录入口本身是公开的。
		//
		// 这三条必须显式登记为 Public —— 中间件对**未登记**路径的默认行为
		// 是 403，而不是「公开」。忘记登记会让登录入口变成 403，
		// 表现为「SSO 按钮点了没反应」，最难排查的一类故障。
		// 保护它们的是协议本身（state / nonce / PKCE）与「未配置即 503」。
		{Method: http.MethodPost, Pattern: "/api/v1/sso/authorize", Public: true},
		{Method: http.MethodGet, Pattern: "/api/v1/sso/callback", Public: true},
		{Method: http.MethodPost, Pattern: "/api/v1/sso/callback", Public: true},
		{Method: http.MethodGet, Pattern: "/api/v1/sso/status", Public: true},

		// 角色与权限矩阵（元权限）
		{Method: http.MethodGet, Pattern: "/api/v1/roles", Action: role.PermRoleManage},
		{Method: http.MethodGet, Pattern: "/api/v1/roles/", Action: role.PermRoleManage},
		{Method: http.MethodPost, Pattern: "/api/v1/roles", Action: role.PermRoleManage},
		{Method: http.MethodPut, Pattern: "/api/v1/roles/", Action: role.PermRoleManage},
		{Method: http.MethodDelete, Pattern: "/api/v1/roles/", Action: role.PermRoleManage},
		{Method: http.MethodGet, Pattern: "/api/v1/role-assignments/", Action: role.PermRoleManage},
		{Method: http.MethodGet, Pattern: "/api/v1/permissions", Action: role.PermRoleManage},

		// 管理后台：用户与权限管理属于 user_admin（默认关闭的功能开关）
		//
		// 注意：具体权限（user:read 等）与 role:manage 有重叠语义，这里刻意用
		// user:* 系列，保证「能看用户」不需要「能改角色」。
		{Method: http.MethodGet, Pattern: "/api/v1/users", Action: role.PermUserRead},
		{Method: http.MethodGet, Pattern: "/api/v1/users/", Action: role.PermUserRead, TargetType: "user"},
		{Method: http.MethodPut, Pattern: "/api/v1/users/", Action: role.PermUserUpdate, TargetType: "user"},
		{Method: http.MethodDelete, Pattern: "/api/v1/users/", Action: role.PermUserDelete, TargetType: "user"},
		// 用户子资源：启用/停用比「改用户」更敏感，单列权限，避免拿到
		// user:update 就能停用他人账号。角色分配读写同样单列。
		// 注意：这些规则必须带方法，否则会覆盖上面的 GET 详情规则。
		{Method: http.MethodGet, Pattern: "/status", Suffix: true, Action: role.PermUserRead, TargetType: "user"},
		{Method: http.MethodPut, Pattern: "/status", Suffix: true, Action: role.PermUserActivate, TargetType: "user"},
		{Method: http.MethodPost, Pattern: "/status", Suffix: true, Action: role.PermUserActivate, TargetType: "user"},
		{Method: http.MethodGet, Pattern: "/roles", Suffix: true, Action: role.PermUserRead, TargetType: "user"},
		{Method: http.MethodPost, Pattern: "/roles", Suffix: true, Action: role.PermRoleAssign, TargetType: "user"},
		{Method: http.MethodDelete, Pattern: "/roles", Suffix: true, Action: role.PermRoleRevoke, TargetType: "user"},
		{Method: http.MethodGet, Pattern: "/api/v1/authorizations", Action: role.PermRoleManage},
		{Method: http.MethodPost, Pattern: "/api/v1/authorizations", Action: role.PermRoleAssign},
		{Method: http.MethodDelete, Pattern: "/api/v1/authorizations", Action: role.PermRoleRevoke},
		{Method: http.MethodGet, Pattern: "/api/v1/authorizations/me", Action: "", Public: true},
		{Method: http.MethodGet, Pattern: "/api/v1/authorizations/", Action: role.PermRoleManage},
		{Method: http.MethodGet, Pattern: "/api/v1/authorization-audit", Action: role.PermRoleManage},
		{Method: http.MethodGet, Pattern: "/api/v1/authorization-stats", Action: role.PermRoleManage},
		{Method: http.MethodGet, Pattern: "/api/v1/permission-routes", Action: role.PermRoleManage},

		// 健康检查与 CORS 预检：无需身份，且不能因为中间件而变成 403。
		{Method: http.MethodGet, Pattern: "/health", Public: true},
		{Method: http.MethodHead, Pattern: "/health", Public: true},
		{Method: http.MethodOptions, Pattern: "/", Public: true},

		// 认证相关路由自身不需要权限（登录/注册/刷新/登出）。
		{Pattern: "/api/v1/auth/", Public: true},

		// WebSocket：升级走 query token（权限在连接建立时校验），状态查询为只读。
		// 注意 /ws/status 若不加规则会落进默认拒绝，前端探活会 403。
		{Pattern: "/api/v1/ws/status", Public: true},
		{Pattern: "/api/v1/ws", Public: true},

		// 管理后台聚合视图与静态资源（控制台自身的读取能力）。
		{Pattern: "/api/v1/admin/", Action: role.PermRoleManage},
		{Method: http.MethodGet, Pattern: "/admin", Public: true},
		{Method: http.MethodGet, Pattern: "/admin/", Public: true},
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
	if pattern == "" {
		return ""
	}
	if !strings.HasSuffix(pattern, "/") {
		// 后缀规则：ID 是后缀之前的那一段。
		// 例：pattern="/status", path="/api/v1/users/u1/status" -> "u1"
		trimmed := strings.TrimSuffix(path, pattern)
		trimmed = strings.Trim(trimmed, "/")
		if trimmed == "" {
			return ""
		}
		if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
			return trimmed[idx+1:]
		}
		return trimmed
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
