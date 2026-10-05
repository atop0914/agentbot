// Package tenant 实现 AgentBot 的多租户隔离（Day 27）。
//
// 为什么租户边界必须是平台的一等公民：
//
//	AgentBot 的 Agent 能碰终端、文件系统、云环境与出站网络。这些能力一旦跨
//	组织泄漏，性质和「数据库里多看到几行」完全不同 —— 一个租户的 Agent 能
//	读到另一个租户的终端输出或出口流量，等于把对方的运行环境交出去了。
//
//	2025-2026 年多起平台类事件（跨租户越权读取他人资源、租户标识由客户端
//	自报导致横向移动）反复证明同一件事：**租户身份不能来自请求**，
//	必须来自服务端签发的认证凭据，并且每一条读写路径都要过同一道闸门。
//
// 三条设计原则（与 authz 包一脉相承）：
//
//  1. **租户身份来自 JWT**：不接受请求体 / header 里自报的 tenant_id，
//     否则「改一个字段就换一个租户」；
//  2. **跨租户一律 404**：不用 403 —— 403 等于告诉对方「这个 ID 是存在的」，
//     在 ID 可枚举的场景里它本身就是信息泄漏；
//  3. **读写路径统一过滤**：资源归属判断只走 Owner / Store 两个口子，
//     避免「某处忘了加 where tenant」这种最难通过 review 发现的漏洞。
package tenant

import (
	"strings"
	"time"
)

// ID 是租户标识。
//
// 刻意保持为字符串而不是自增整数：租户 ID 会出现在 URL、审计与导出文件里，
// 可枚举的短 ID 会让「猜一个 ID 看看有没有」变得廉价。
type ID string

// None 表示「无租户」。
//
// 这个值**不**代表「任意租户」或「全局可见」。未绑定租户的资源不会匹配
// 任何租户的查询（见 Owner.Matches），因此读取它只会得到 404。
const None ID = ""

// Valid 判断租户 ID 是否合法（非空且不含路径分隔符等易混淆字符）。
func (id ID) Valid() bool {
	if id == None {
		return false
	}
	s := string(id)
	if strings.ContainsAny(s, "/\\?#%") {
		return false
	}
	return strings.TrimSpace(s) == s && len(s) <= 64
}

// Status 是租户状态。
type Status string

const (
	// StatusActive 正常可用。
	StatusActive Status = "active"
	// StatusSuspended 已暂停：认证通过但资源访问一律拒绝。
	//
	// 与「删除」区分开：暂停是可逆的运营动作，需要保留数据与审计。
	StatusSuspended Status = "suspended"
)

// Valid 判断状态取值是否合法。
func (s Status) Valid() bool {
	return s == StatusActive || s == StatusSuspended
}

// Plan 是租户的套餐等级，用于资源配额与功能开关。
type Plan string

const (
	PlanFree       Plan = "free"
	PlanTeam       Plan = "team"
	PlanEnterprise Plan = "enterprise"
)

// Valid 判断套餐取值是否合法。
func (p Plan) Valid() bool {
	return p == PlanFree || p == PlanTeam || p == PlanEnterprise
}

// Quota 是租户的资源配额。
//
// 配额做成显式结构而不是散落的常量：企业客户的配额是合同条款，
// 需要能被后台查到、能按租户覆盖，而不是藏在代码里的默认值。
type Quota struct {
	// MaxAgents 最大 Agent 数；<= 0 表示不限。
	MaxAgents int `json:"max_agents"`
	// MaxMembers 最大成员数；<= 0 表示不限。
	MaxMembers int `json:"max_members"`
	// MaxEgressRules 最大出口策略条数；<= 0 表示不限。
	MaxEgressRules int `json:"max_egress_rules"`
}

// Unlimited 判断某项配额是否不限。
func (q Quota) Unlimited(field string) bool {
	switch field {
	case "agents":
		return q.MaxAgents <= 0
	case "members":
		return q.MaxMembers <= 0
	case "egress_rules":
		return q.MaxEgressRules <= 0
	}
	return true
}

// Tenant 是一个租户。
type Tenant struct {
	ID        ID        `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Plan      Plan      `json:"plan"`
	Status    Status    `json:"status"`
	Quota     Quota     `json:"quota"`
	OwnerID   string    `json:"owner_id"`
	SSORealm  string    `json:"sso_realm,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Usable 判断租户当前是否还能访问资源。
//
// 暂停租户的成员仍然可以认证（否则连「为什么被停」都查不到），
// 但一切资源访问都按「不存在」处理。
func (t *Tenant) Usable() bool {
	return t != nil && t.Status == StatusActive
}

// Member 是租户成员（用户 ↔ 租户 的绑定关系）。
//
// 单独建成员表而不是把 TenantID 塞进 User：一个用户可能属于多个租户
// （咨询顾问、外包协作），把租户写死在用户上会逼出「一个人开两个账号」。
type Member struct {
	TenantID  ID        `json:"tenant_id"`
	UserID    string    `json:"user_id"`
	Roles     []string  `json:"roles,omitempty"`
	InvitedBy string    `json:"invited_by,omitempty"`
	JoinedAt  time.Time `json:"joined_at"`
}

// ResourceType 是受租户边界约束的资源类型。
type ResourceType string

const (
	ResourceAgent       ResourceType = "agent"
	ResourceTask        ResourceType = "task"
	ResourceAudit       ResourceType = "audit"
	ResourceEgressRule  ResourceType = "egress_rule"
	ResourceEnvironment ResourceType = "environment"
)

// Valid 判断资源类型是否受支持。
func (r ResourceType) Valid() bool {
	switch r {
	case ResourceAgent, ResourceTask, ResourceAudit, ResourceEgressRule, ResourceEnvironment:
		return true
	}
	return false
}

// Owner 描述「一个资源属于谁」。
//
// 这是租户边界的**唯一**判定依据：任何读写路径都必须能回答这个问题，
// 回答不了就不该放行。把它做成类型（而不是在每处 `if x.TenantID != t`）
// 的好处是判定逻辑只有一份，能被单测穷举。
type Owner struct {
	TenantID ID           `json:"tenant_id"`
	Type     ResourceType `json:"type"`
	ResID    string       `json:"resource_id"`
}

// Matches 判断某个租户是否可以访问该资源。
//
// 关键语义（每条都有对应测试）：
//   - 租户 ID 为空 → **永不匹配**。未绑定租户的资源不是「公共资源」，
//     它是配置错误，必须表现为不可见而不是所有人可见；
//   - 资源租户 ID 为空 → 永不匹配（同上，永远 fail-closed）；
//   - 两者相等 → 匹配。
func (o Owner) Matches(tenant ID) bool {
	if tenant == None || o.TenantID == None {
		return false
	}
	return o.TenantID == tenant
}

// Stats 是租户维度的汇总，供管理后台展示。
type Stats struct {
	GeneratedAt   time.Time      `json:"generated_at"`
	TotalTenants  int            `json:"total_tenants"`
	ActiveTenants int            `json:"active_tenants"`
	Suspended     int            `json:"suspended"`
	TotalMembers  int            `json:"total_members"`
	ByPlan        map[string]int `json:"by_plan"`
	Resources     map[string]int `json:"resources"`
	Scoped        bool           `json:"scoped"`
	// TenantID 非空时表示这是一份「单租户视角」的统计。
	TenantID ID `json:"tenant_id,omitempty"`
}

// ===== 错误 =====

// ErrNotFound 表示目标在**调用方可见的租户范围内**不存在。
//
// 跨租户访问与真正不存在返回同一个错误：调用方无从区分「没有」和
// 「别人的」，这正是我们想要的（不泄漏 ID 是否存在）。
var ErrNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "tenant: resource not found" }

// ErrUnavailable 表示租户存储不可用；fail-closed，绝不放行。
var ErrUnavailable = errUnavailable{}

type errUnavailable struct{}

func (errUnavailable) Error() string { return "tenant: tenant store unavailable" }

// ErrIdentityRequired 表示请求缺少可信的租户身份。
var ErrIdentityRequired = errIdentityRequired{}

type errIdentityRequired struct{}

func (errIdentityRequired) Error() string { return "tenant: authenticated tenant identity required" }

// ErrSuspended 表示租户已暂停。
var ErrSuspended = errSuspended{}

type errSuspended struct{}

func (errSuspended) Error() string { return "tenant: tenant is suspended" }

// ErrQuotaExceeded 表示配额用尽。
var ErrQuotaExceeded = errQuotaExceeded{}

type errQuotaExceeded struct{}

func (errQuotaExceeded) Error() string { return "tenant: quota exceeded" }
