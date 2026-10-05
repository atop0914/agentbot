package tenant

import (
	"context"
	"errors"
	"net/http"
)

// scopeKeyType 是本包在 context 中的键类型。
//
// 用未导出空结构体而不是字符串：不同包定义的字符串键即使字面量相同也会
// 互相覆盖（context key 是 interface 比较），空结构体键在本包内唯一。
type scopeKeyType struct{}

var scopeKey scopeKeyType

// Scope 是一次请求的租户作用域。
//
// 所有资源访问都必须经过它，原因很直接：**只要有一处直接读 store，
// 那处就绕过了租户边界**。把判定收进一个类型，能让「漏加过滤」在编译期
// 就变得别扭（得先拿到 store），而不是靠 review 时肉眼检查。
type Scope struct {
	tenant *Tenant
	userID string
	svc    *scopeService
}

// TenantID 返回租户 ID。
func (s *Scope) TenantID() ID {
	if s == nil || s.tenant == nil {
		return None
	}
	return s.tenant.ID
}

// Tenant 返回租户快照。
func (s *Scope) Tenant() *Tenant {
	if s == nil {
		return nil
	}
	cp := *s.tenant
	return &cp
}

// UserID 返回作用域内的用户。
func (s *Scope) UserID() string {
	if s == nil {
		return ""
	}
	return s.userID
}

// Allows 判断该作用域能否访问某个资源。
//
// 无作用域（nil）与空租户一律返回 false —— 这是隔离的最后一层兜底：
// 即使某条路径忘了做中间件检查，只要它用 Scope 判定就拿不到放行。
func (s *Scope) Allows(ctx context.Context, rt ResourceType, resID string) bool {
	if s == nil || s.svc == nil {
		return false
	}
	return s.svc.Check(ctx, s.TenantID(), rt, resID) == nil
}

// Owner 返回受作用域约束的资源归属；跨租户与不存在同样返回 ErrNotFound。
func (s *Scope) Owner(ctx context.Context, rt ResourceType, resID string) (Owner, error) {
	if s == nil || s.svc == nil {
		return Owner{}, ErrUnavailable
	}
	if err := s.svc.Check(ctx, s.TenantID(), rt, resID); err != nil {
		return Owner{}, err
	}
	return s.svc.Owner(ctx, rt, resID)
}

// Filter 按作用域过滤一批资源 ID，只保留本租户可见的那些。
//
// 之所以提供批量过滤而不是让调用方循环 Check：**列表接口是最容易漏加
// 租户条件的地方**（单条读取有明确的 resID 可校验，列表往往直接返回全量）。
// 统一走这里，至少让列表路径有一个可被测试覆盖的收口。
func (s *Scope) Filter(ctx context.Context, rt ResourceType, resIDs []string) []string {
	out := make([]string, 0, len(resIDs))
	if s == nil {
		return out
	}
	for _, id := range resIDs {
		if s.Allows(ctx, rt, id) {
			out = append(out, id)
		}
	}
	return out
}

// Register 在本租户下登记资源归属。
func (s *Scope) Register(ctx context.Context, rt ResourceType, resID string) (Owner, error) {
	if s == nil || s.svc == nil {
		return Owner{}, ErrUnavailable
	}
	return s.svc.Register(ctx, s.TenantID(), rt, resID)
}

// EnforceQuota 检查本租户配额。
func (s *Scope) EnforceQuota(ctx context.Context, rt ResourceType) error {
	if s == nil || s.svc == nil {
		return ErrUnavailable
	}
	return s.svc.EnforceQuota(ctx, s.TenantID(), rt)
}

// WithScope 把作用域写入 context。
func WithScope(ctx context.Context, s *Scope) context.Context {
	return context.WithValue(ctx, scopeKey, s)
}

// ScopeFromContext 从 context 取出作用域；不存在返回 nil。
func ScopeFromContext(ctx context.Context) *Scope {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(scopeKey).(*Scope)
	return s
}

// ===== HTTP 中间件 =====

// IdentityResolver 从请求中解析**可信**的租户身份。
//
// 返回的 tenantID 必须来自服务端签发的凭据（JWT claims），
// 实现里**绝不能**读取请求体、query 或可被客户端改写的 header ——
// 那等于把租户边界交给攻击者决定。
type IdentityResolver func(r *http.Request) (tenantID ID, userID string, ok bool)

// Middleware 把租户隔离接入请求链路。
//
// 行为（fail-closed）：
//   - 解析不出可信身份 → 401；
//   - 租户不存在 / 已暂停 / 用户非成员 → 404（与「资源不存在」同形，
//     不泄漏租户是否存在）；
//   - 存储不可用 → 503，绝不降级为放行。
type Middleware struct {
	svc      Service
	resolve  IdentityResolver
	required bool
}

// NewMiddleware 创建租户中间件。
//
// required 为 true 时，所有经过它的请求都必须携带租户身份；
// 为 false 时无身份的请求按「无租户作用域」放行（由下层决定是否需要），
// 用于公开路由（登录、健康检查）与尚未迁移的旧路由。
func NewMiddleware(svc Service, resolve IdentityResolver, required bool) *Middleware {
	return &Middleware{svc: svc, resolve: resolve, required: required}
}

// Handler 包装一个 handler，注入租户作用域。
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m == nil || m.svc == nil {
			writeTenantError(w, ErrUnavailable)
			return
		}
		var (
			tenantID ID
			userID   string
			ok       bool
		)
		if m.resolve != nil {
			tenantID, userID, ok = m.resolve(r)
		}
		if !ok {
			if !m.required {
				next.ServeHTTP(w, r)
				return
			}
			writeTenantError(w, ErrIdentityRequired)
			return
		}
		scope, err := m.svc.Resolve(r.Context(), tenantID, userID)
		if err != nil {
			writeTenantError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithScope(r.Context(), scope)))
	})
}

// RequireScope 返回一个装饰器，强制处理函数在**租户作用域内**运行。
//
// 用于那些绝不能在没有租户上下文时执行的业务入口（例如资源创建）：
// 漏挂中间件的后果是 503 而不是「悄悄按无租户写入」。
func RequireScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ScopeFromContext(r.Context()) == nil {
			writeTenantError(w, ErrIdentityRequired)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeTenantError 把租户错误映射为 HTTP 状态码。
//
// 注意 404 的用法：跨租户访问与资源不存在**必须**返回同一个状态码，
// 否则攻击者可以用「403 还是 404」枚举出别人的资源 ID。
func writeTenantError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case isErr(err, ErrIdentityRequired):
		status = http.StatusUnauthorized
	case isErr(err, ErrSuspended), isErr(err, ErrNotFound):
		status = http.StatusNotFound
	case isErr(err, ErrUnavailable):
		status = http.StatusServiceUnavailable
	case isErr(err, ErrQuotaExceeded):
		status = http.StatusConflict
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := "{\"error\":\"" + messageFor(err) + "\"}\n"
	_, _ = w.Write([]byte(body))
}

// messageFor 给出对外的稳定错误文案。
//
// 对 404 一律用同一句话：文案差异也是一种信息泄漏。
func messageFor(err error) string {
	switch {
	case isErr(err, ErrIdentityRequired):
		return "authenticated tenant identity required"
	case isErr(err, ErrSuspended), isErr(err, ErrNotFound):
		return "resource not found"
	case isErr(err, ErrUnavailable):
		return "tenant store unavailable"
	case isErr(err, ErrQuotaExceeded):
		return "tenant quota exceeded"
	default:
		return "internal error"
	}
}

// isErr 用 errors.Is 做哨兵错误判定。
//
// 必须走 errors.Is 而不是字符串比较：本包的哨兵错误将来可能被包装
// （例如加上租户 ID 便于排障），字符串比较会在那一刻静默失效 ——
// 而失效的方向是「错误分类错 → 404 变成 500」，或者更糟，
// 「越权错误被当成未知错误而放行」。
func isErr(err, target error) bool {
	return errors.Is(err, target)
}
