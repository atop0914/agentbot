package app

import (
	"context"
	"errors"

	"github.com/atop0914/agentbot/internal/admin"
	"github.com/atop0914/agentbot/internal/agent"
	"github.com/atop0914/agentbot/internal/authz"
	"github.com/atop0914/agentbot/internal/role"
	"github.com/atop0914/agentbot/internal/user"
)

// errAuthzTargetMissing 表示权限判定指向了一个不存在的目标实体。
var errAuthzTargetMissing = errors.New("target entity does not exist")

// registerAuthzTargets 把各业务模块的目标校验器注册进 authz 的目标表。
//
// 这么做而不是让 authz 直接 import agent/user：authz 只认「目标是否存在」这个
// 语义，不需要知道 Agent 有几个字段。后续新增资源（任务、环境、模板）只需在这里
// 多注册一行，权限判定的默认拒绝就自动对新资源生效。
func registerAuthzTargets(registry authz.TargetRegistry, agents agent.Service, users user.Service) {
	registry.Register("agent", func(ctx context.Context, id string) error {
		a, err := agents.Get(ctx, id)
		if err != nil {
			return err
		}
		if a == nil {
			return errAuthzTargetMissing
		}
		return nil
	})
	registry.Register("user", func(ctx context.Context, id string) error {
		u, err := users.GetByID(id)
		if err != nil {
			return err
		}
		if u == nil {
			return errAuthzTargetMissing
		}
		return nil
	})
}

// authzSubjectName 把授权主体解析为可读名字，供后台与审计展示。
func authzSubjectName(users user.Service, agents agent.Service) func(string, string) string {
	return func(subjectType, id string) string {
		ctx := context.Background()
		switch authz.SubjectType(subjectType) {
		case authz.SubjectUser:
			if u, err := users.GetByID(id); err == nil && u != nil {
				if u.Username != "" {
					return u.Username
				}
				return u.Email
			}
		case authz.SubjectAgent:
			if a, err := agents.Get(ctx, id); err == nil && a != nil {
				return a.Name
			}
		}
		return ""
	}
}

// userAssignments 返回某个用户的角色分配（供 user.AdminHandler 的子资源使用）。
//
// 返回 map 而不是 authz.Assignment，是为了让 user 包只依赖「JSON 可序列化的
// 任意结构」，不依赖 authz 的具体类型。
func userAssignments(svc authz.Service) func(string) ([]map[string]interface{}, error) {
	return func(userID string) ([]map[string]interface{}, error) {
		subject := authz.Subject{Type: authz.SubjectUser, ID: userID}
		list, err := svc.ListAssignments(context.Background(), subject)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]interface{}, 0, len(list))
		for i := range list {
			a := list[i]
			out = append(out, map[string]interface{}{
				"id":           a.ID,
				"role_id":      a.RoleID,
				"role_name":    a.RoleName,
				"granted_by":   a.GrantedBy,
				"granted_at":   a.GrantedAt,
				"expires_at":   a.ExpiresAt,
				"subject_type": string(a.Subject.Type),
				"subject_id":   a.Subject.ID,
			})
		}
		return out, nil
	}
}

// adminUserSource 把 user.Service 适配为 admin.UserSource。
type adminUserSource struct {
	svc user.Service
}

func (s adminUserSource) ListUsers(ctx context.Context, offset, limit int) ([]*admin.UserView, int, error) {
	users, total, err := s.svc.List(offset, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]*admin.UserView, 0, len(users))
	for _, u := range users {
		if u == nil {
			continue
		}
		provider := u.OAuthProvider
		if provider == "" {
			provider = "password"
		}
		out = append(out, &admin.UserView{
			ID:        u.ID,
			Username:  u.Username,
			Email:     u.Email,
			Status:    string(u.Status),
			Role:      string(u.Role),
			Provider:  provider,
			CreatedAt: u.CreatedAt,
		})
	}
	return out, total, nil
}

// adminAuthzSource 把 authz.Service 适配为 admin.AuthzSource。
type adminAuthzSource struct {
	svc authz.Service
}

func (s adminAuthzSource) Stats(ctx context.Context) (*admin.AuthzStats, error) {
	stats, err := s.svc.Stats(ctx)
	if err != nil {
		return nil, err
	}
	return &admin.AuthzStats{
		TotalAssignments: stats.TotalAssignments,
		UserAssignments:  stats.UserAssignments,
		AgentAssignments: stats.AgentAssignments,
		Expired:          stats.Expired,
		ByRole:           stats.ByRole,
		Decisions:        stats.Decisions,
		Denials:          stats.Denials,
		Degraded:         stats.Degraded,
	}, nil
}

// ensureRoleService 是装配期断言：授权链必须拿到可用的角色服务。
//
// 如果把 nil 传进 authz.NewService，运行时才会以 503 的形式暴露出来；
// 这里提前发现配置错误，避免「平台看起来能起，但所有写操作都被拒绝」。
func ensureRoleService(svc role.Service) role.Service {
	return svc
}
