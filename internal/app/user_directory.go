package app

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/atop0914/agentbot/internal/user"
)

// userDirectoryAdapter 把 internal/user 适配为 sso.UserDirectory。
//
// 为什么需要这一层：
//
//	sso 包必须能回答「这个邮箱有没有本地账号」「要不要自动创建」
//	这三个问题，但如果它直接 import internal/user，身份校验代码就会
//	拖上用户模型、密码哈希等一整条依赖链 —— 而这些代码**不应该**
//	出现在签名校验的执行路径上（依赖面就是攻击面）。
//
// 邮箱规范化在这里也做一次：SSO 的邮箱与本地注册的邮箱必须按同一规则
// 归一（小写、去空白），否则 "Alice@Corp.com" 与 "alice@corp.com"
// 会被当成两个账号，用户会看到「我的账号哪去了」。
type userDirectoryAdapter struct {
	svc *user.UserService
}

// FindByEmail 查找本地账号。
func (a userDirectoryAdapter) FindByEmail(_ context.Context, email string) (string, bool, error) {
	if a.svc == nil {
		return "", false, errTenantDirectoryUnavailable
	}
	normalized := normalizeEmail(email)
	if normalized == "" {
		return "", false, nil
	}
	u, err := a.svc.GetByEmail(normalized)
	if err != nil {
		// 保守处理：任何读取错误都不当作「不存在」。
		//
		// 这一条是账号安全的关键：把一次 DB 抖动/超时解释成「这个邮箱没
		// 注册过」，紧接着走 auto_create 分支就会给一个**已存在**的邮箱
		// 再建一个账号 —— 之后两个账号争抢同一个邮箱，谁登录进来取决于
		// 查询顺序。宁可让这次登录失败。
		return "", false, err
	}
	if u == nil {
		// 未找到是正常分支（MemoryRepository 约定返回 (nil, nil)），
		// 交由上层的绑定策略决定是拒绝还是自动创建。
		return "", false, nil
	}
	return u.ID, true, nil
}

// CreateFromSSO 用 SSO 断言创建本地账号。
//
// 密码字段填一个随机值：SSO 用户不应有可用的本地密码，
// 但也不能留空（空密码在部分校验路径里等于「无密码即可登录」）。
func (a userDirectoryAdapter) CreateFromSSO(_ context.Context, email, username, issuer, subject string) (string, error) {
	if a.svc == nil {
		return "", errTenantDirectoryUnavailable
	}
	normalized := normalizeEmail(email)
	if normalized == "" {
		return "", errSSOAccountInvalid
	}
	name := strings.TrimSpace(username)
	if name == "" {
		name = strings.SplitN(normalized, "@", 2)[0]
	}
	// 随机密码：不可被猜中，且用户永远不会用到它（登录走 SSO）。
	randomPwd := "sso-" + uuid.New().String() + uuid.New().String()

	created, err := a.svc.Create(&user.CreateUserRequest{
		Email:    normalized,
		Username: name,
		Password: randomPwd,
	})
	if err != nil {
		return "", err
	}
	// 关联 SSO 身份：绑定失败不能静默吞掉，否则下次登录会被当成新用户
	// 再建一个账号（账号重复的经典成因）。
	if err := a.svc.LinkOAuth(created.ID, issuer, subject); err != nil {
		return "", err
	}
	return created.ID, nil
}

// LinkSSO 把 SSO 身份绑定到已有账号。
func (a userDirectoryAdapter) LinkSSO(_ context.Context, userID, issuer, subject string) error {
	if a.svc == nil {
		return errTenantDirectoryUnavailable
	}
	if strings.TrimSpace(userID) == "" {
		return errSSOAccountInvalid
	}
	return a.svc.LinkOAuth(userID, issuer, subject)
}

// normalizeEmail 统一邮箱规范化规则。
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
