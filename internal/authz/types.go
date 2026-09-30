// Package authz 实现 AgentBot 的授权链：用户 → 角色 → 权限。
//
// 设计动机（详见 plan/research.md）：
// 2025-2026 年多起供应链与 Agent 平台事件（TanStack/Mini Shai-Hulud 的 pull_request_target
// "Pwn Request"、FakeGit 的 AgentBaiting）都源于同一种失败——**运行时的权限上下文与实际
// 身份不匹配**。AgentBot 的 Agent 能碰终端、文件系统、云环境，一旦某个低权限主体拿到了
// 高权限动作，后果不可逆。因此把「谁能对谁做什么」显式建模、可查询、可拦截、可审计。
//
// 三条设计原则：
//  1. **fail-closed**：权限服务不可用时一律拒绝，绝不因为"查不到"而放行；
//  2. **默认拒绝**：未显式授予的权限即无权限，没有隐含的通配权限；
//  3. **可解释**：每次判定都能回答「这个权限是哪个角色给的」，便于审计与排障。
//
// 本包不依赖 agent/task 等业务模块的模型，目标合法性通过 TargetRegistry 注册的
// 校验函数判断，避免包依赖倒挂。
package authz

import (
	"context"
	"time"

	"github.com/atop0914/agentbot/internal/role"
)

// SubjectType 是授权主体的类型。
//
// 用户与 Agent 共用同一套权限字符串（见 role.Permission），但分开存储，
// 避免用户 ID 与 Agent ID 撞车时出现越权。
type SubjectType string

const (
	SubjectUser  SubjectType = "user"
	SubjectAgent SubjectType = "agent"
)

// Valid 判断主体类型是否合法。
func (t SubjectType) Valid() bool {
	return t == SubjectUser || t == SubjectAgent
}

// Subject 是一次判定中的主体。
type Subject struct {
	Type SubjectType `json:"type"`
	ID   string      `json:"id"`
}

// Key 返回用于存储与去重的稳定键。
func (s Subject) Key() string { return string(s.Type) + ":" + s.ID }

// IsZero 判断主体是否为空。
func (s Subject) IsZero() bool { return s.ID == "" || !s.Type.Valid() }

// Target 是权限动作作用的对象（例如某个 Agent、某个任务）。
//
// 随着 Day 25-27 引入租户与资源边界，Target 会承载更多信息（租户、所有者），
// 因此这里从一开始就用结构体而不是裸字符串。
type Target struct {
	Type string `json:"type"` // agent / task / user / environment / ...
	ID   string `json:"id"`
}

// IsZero 判断目标是否为空（表示「平台级」动作，如查看全局统计）。
func (t Target) IsZero() bool { return t.Type == "" && t.ID == "" }

// Assignment 是一条「主体 → 角色」的授予记录。
type Assignment struct {
	ID        string     `json:"id"`
	Subject   Subject    `json:"subject"`
	RoleID    string     `json:"role_id"`
	RoleName  string     `json:"role_name"`
	GrantedBy string     `json:"granted_by"`
	GrantedAt time.Time  `json:"granted_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Expired 判断授予是否已过期。
func (a *Assignment) Expired(now time.Time) bool {
	if a == nil || a.ExpiresAt == nil {
		return false
	}
	return !now.Before(*a.ExpiresAt)
}

// Authorization 描述一个主体当前生效的完整权限链。
type Authorization struct {
	Subject      Subject             `json:"subject"`
	Assignments  []Assignment        `json:"assignments"`
	Roles        []*role.Role        `json:"roles"`
	Permissions  []role.Permission   `json:"permissions"`
	ByPermission map[string][]string `json:"by_permission,omitempty"` // 权限 → 提供它的角色名（可解释性）
	UpdatedAt    time.Time           `json:"updated_at"`
}

// Decision 是一次权限判定结果。
type Decision struct {
	Allowed bool            `json:"allowed"`
	Subject Subject         `json:"subject"`
	Action  role.Permission `json:"action"`
	Target  Target          `json:"target,omitempty"`
	// Reason 是人类可读的判定理由（命中/未命中/降级/服务不可用）。
	Reason string `json:"reason"`
	// GrantedBy 列出提供了该权限的角色名；未命中时为空。
	GrantedBy []string `json:"granted_by,omitempty"`
	// Degraded 为 true 表示判定过程中出现基础设施故障，按 fail-closed 拒绝。
	Degraded  bool      `json:"degraded"`
	DecidedAt time.Time `json:"decided_at"`
}

// AuditEntry 记录一次判定，供审计与排障使用。
type AuditEntry struct {
	Sequence  int64           `json:"sequence"`
	DecidedAt time.Time       `json:"decided_at"`
	Subject   Subject         `json:"subject"`
	Action    role.Permission `json:"action"`
	Target    Target          `json:"target,omitempty"`
	Allowed   bool            `json:"allowed"`
	Degraded  bool            `json:"degraded"`
	Reason    string          `json:"reason"`
}

// Store 是授权数据的持久化接口。
//
// 全部方法以 subject 为维度操作，避免调用方绕过服务层直接改内部状态。
type Store interface {
	// Assign 新增一条授予记录；同一主体重复授予同一角色会返回错误。
	Assign(ctx context.Context, assignment *Assignment) error
	// Revoke 撤销主体对某个角色的授予。
	Revoke(ctx context.Context, subject Subject, roleID string) error
	// ListAssignments 返回主体的全部授予记录（按角色 ID 升序，保证输出稳定）。
	ListAssignments(ctx context.Context, subject Subject) ([]Assignment, error)
	// CountByRole 统计每个角色被授予的次数。
	CountByRole(ctx context.Context) (map[string]int, error)
}

// Service 是授权链的服务端接口。
type Service interface {
	// Assign 授予主体一个角色。roleSvc 用于校验角色真实存在。
	Assign(ctx context.Context, subject Subject, roleID, grantedBy string) (*Assignment, error)
	// Revoke 撤销主体对某个角色的授予。
	Revoke(ctx context.Context, subject Subject, roleID string) error
	// ListAssignments 返回主体的授予记录。
	ListAssignments(ctx context.Context, subject Subject) ([]Assignment, error)
	// Authorize 解析主体当前生效的权限链。
	Authorize(ctx context.Context, subject Subject) (*Authorization, error)
	// Decide 判定主体是否可以对 target 执行 action。
	Decide(ctx context.Context, subject Subject, action role.Permission, target Target) (*Decision, error)
	// Require 是 Decide 的便利封装：未授权时返回 ErrForbidden。
	Require(ctx context.Context, subject Subject, action role.Permission, target Target) error
	// Audit 返回最近的判定记录（按时间倒序）。
	Audit(ctx context.Context, limit int) ([]AuditEntry, error)
	// Stats 返回授权链的统计信息。
	Stats(ctx context.Context) (*Stats, error)
}

// Stats 是授权链的汇总，供管理后台 users 分区使用。
type Stats struct {
	TotalAssignments int            `json:"total_assignments"`
	UserAssignments  int            `json:"user_assignments"`
	AgentAssignments int            `json:"agent_assignments"`
	Expired          int            `json:"expired"`
	ByRole           map[string]int `json:"by_role"`
	Decisions        int            `json:"decisions"`
	Denials          int            `json:"denials"`
	Degraded         int            `json:"degraded"`
	GeneratedAt      time.Time      `json:"generated_at"`
}

// TargetValidator 校验某个目标是否真实存在。
//
// 由装配层为每种 Target.Type 注册，authz 包本身无需知道 agent/task 的模型，
// 从而避免 authz → agent → ... 的循环依赖。
type TargetValidator func(ctx context.Context, id string) error

// TargetRegistry 是目标校验函数的注册表。
type TargetRegistry interface {
	Register(targetType string, validator TargetValidator)
	Validate(ctx context.Context, target Target) error
}
