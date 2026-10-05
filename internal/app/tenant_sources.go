package app

import (
	"context"
	"net/http"
	"strings"

	"github.com/atop0914/agentbot/internal/auth"
	"github.com/atop0914/agentbot/internal/sso"
	"github.com/atop0914/agentbot/internal/tenant"
)

// tenantIdentityFromRequest 从**服务端签发的凭据**解析租户身份。
//
// 这是整个多租户隔离的可信边界，因此实现刻意只有一条数据来源：
// 认证中间件写入 context 的 JWT claims（已验签）。
//
// 明确**不**读以下任何一处：
//   - 请求体（POST 的 tenant_id 字段是攻击者完全可控的输入）；
//   - URL query（可被链接分享、可被日志截获）；
//   - 明文 header（X-Tenant-ID 这类自定义头没有任何密码学保证）。
//
// 少了这条约束，多租户就退化成一个「改个字段就能换租户」的 UI 特性。
func tenantIdentityFromRequest(r *http.Request) (tenant.ID, string, bool) {
	claims := auth.GetClaimsFromContext(r.Context())
	if claims == nil {
		return tenant.None, "", false
	}
	userID := strings.TrimSpace(claims.UserID)
	if userID == "" {
		return tenant.None, "", false
	}
	// 租户可为空：这表示「已认证但未按租户绑定」（平台管理员，
	// 或尚未加入任何租户的新用户）。
	//
	// 这种情况下 ok 返回 **false**：让中间件按「无作用域」放行，
	// 而不是拿一个空租户去 Resolve（那一定失败，并把
	// 「平台管理员建租户」这类合法操作挡在 401 外）。
	//
	// 这两条路径的区别是刻意的：
	//   ok=false + TenantID 为空 → 无作用域，业务层用 RequireScope 决定；
	//   ok=true  + TenantID 非空 → 解析并注入租户作用域。
	tenantID := tenant.ID(strings.TrimSpace(claims.TenantID))
	if tenantID == tenant.None {
		return tenant.None, userID, false
	}
	return tenantID, userID, true
}

// tenantUserDirectory 把 internal/user 适配成 sso.UserDirectory。
//
// sso 包不反向依赖 user 包（避免身份校验逻辑拖上业务依赖），
// 这一层薄适配是唯一的耦合点，也是「邮箱冲突策略」被真正执行的地方。
type tenantUserDirectory struct {
	users userDirectoryBackend
}

// userDirectoryBackend 是适配器需要的最小用户能力集。
type userDirectoryBackend interface {
	FindByEmail(ctx context.Context, email string) (userID string, found bool, err error)
	CreateFromSSO(ctx context.Context, email, username, issuer, subject string) (userID string, err error)
	LinkSSO(ctx context.Context, userID, issuer, subject string) error
}

func (d tenantUserDirectory) FindByEmail(ctx context.Context, email string) (string, bool, error) {
	if d.users == nil {
		return "", false, nil
	}
	return d.users.FindByEmail(ctx, email)
}

func (d tenantUserDirectory) CreateFromSSO(ctx context.Context, email, username, issuer, subject string) (string, error) {
	if d.users == nil {
		return "", errTenantDirectoryUnavailable
	}
	return d.users.CreateFromSSO(ctx, email, username, issuer, subject)
}

func (d tenantUserDirectory) LinkSSO(ctx context.Context, userID, issuer, subject string) error {
	if d.users == nil {
		return errTenantDirectoryUnavailable
	}
	return d.users.LinkSSO(ctx, userID, issuer, subject)
}

// ssoSessionIssuer 把 SSO 账号换成平台的 JWT。
//
// 关键点：**租户声明由服务端决定**。SSO 回调里带 tenant_id 时，
// 只有在调用方确实绑定了该租户之后才会写进 claims（见 bindSSOToTenant）。
func ssoSessionIssuer(authSvc *auth.Service, tenantSvc tenant.Service) func(r *http.Request, acct *sso.Account) (interface{}, error) {
	return func(r *http.Request, acct *sso.Account) (interface{}, error) {
		if acct == nil || acct.LocalUserID == "" {
			return nil, errSSOAccountInvalid
		}
		claims := &auth.Claims{
			UserID:   acct.LocalUserID,
			Username: acct.Username,
			Email:    acct.Email,
			Role:     "user",
		}
		// 只有「该用户确实属于这个租户」时才把租户写进令牌。
		// 不能因为 URL 里写了 tenant_id 就照单全收 —— 那等于让调用方
		// 通过选 URL 来选租户。
		if tid := strings.TrimSpace(r.URL.Query().Get("tenant_id")); tid != "" && tenantSvc != nil {
			if _, err := tenantSvc.Resolve(r.Context(), tenant.ID(tid), acct.LocalUserID); err == nil {
				claims.TenantID = tid
			}
		}
		pair, err := authSvc.IssueTokens(claims)
		if err != nil {
			return nil, err
		}
		return pair, nil
	}
}

// bindSSOToTenant 把 SSO 登录的账号加入目标租户。
//
// 这是「SSO 登录即入租户」的入口，因此必须严格：
//   - 租户不存在 / 已暂停 → 拒绝（不静默建一个租户）；
//   - 重复加入 → 幂等成功（用户重试登录不该失败）。
func bindSSOToTenant(ctx context.Context, tenantSvc tenant.Service, userID, tenantID string) error {
	if tenantSvc == nil || userID == "" || strings.TrimSpace(tenantID) == "" {
		return errTenantBindingRejected
	}
	t, err := tenantSvc.Get(ctx, tenant.ID(tenantID))
	if err != nil {
		return errTenantBindingRejected
	}
	if !t.Usable() {
		return errTenantBindingRejected
	}
	if _, err := tenantSvc.AddMember(ctx, tenant.ID(tenantID), userID, "sso"); err != nil {
		return errTenantBindingRejected
	}
	return nil
}

// registerTenantOwnership 把一个已存在的资源登记到租户下。
//
// 装配层用它把「建资源」与「登记归属」绑在一起：调用方拿到 Agent ID 之后
// 必须立刻登记，否则该资源会变成谁都看不见的孤儿（见 tenant 包注释）。
func registerTenantOwnership(ctx context.Context, tenantSvc tenant.Service, tenantID tenant.ID, rt tenant.ResourceType, resID string) error {
	if tenantSvc == nil {
		return errTenantDirectoryUnavailable
	}
	_, err := tenantSvc.Register(ctx, tenantID, rt, resID)
	return err
}
