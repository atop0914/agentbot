package authz

import (
	"errors"
	"fmt"
)

// 授权链的错误分类。
//
// 调用方（HTTP 层、中间件、管理后台）需要区分这几类：
//   - ErrForbidden：主体确实没有权限 → 403，且这是**正常业务结果**，不是故障；
//   - ErrUnavailable：权限服务不可用 → 503，且必须 fail-closed（拒绝而非放行）；
//   - ErrConflict / ErrValidation：请求本身有问题 → 400/409。
var (
	// ErrForbidden 表示主体未获得所需权限（fail-closed 的默认结果）。
	ErrForbidden = errors.New("authz: permission denied")
	// ErrUnavailable 表示授权链的依赖不可用，按 fail-closed 处理。
	ErrUnavailable = errors.New("authz: authorization service unavailable")
	// ErrConflict 表示重复授予。
	ErrConflict = errors.New("authz: assignment already exists")
	// ErrInvalidSubject 表示主体类型或 ID 非法。
	ErrInvalidSubject = errors.New("authz: invalid subject")
	// ErrUnknownRole 表示角色不存在。
	ErrUnknownRole = errors.New("authz: unknown role")
	// ErrUnknownTargetType 表示没有为该类型注册目标校验器。
	ErrUnknownTargetType = errors.New("authz: unknown target type")
	// ErrTargetNotFound 表示目标实体不存在。
	ErrTargetNotFound = errors.New("authz: target not found")
)

// IsForbidden 判断错误是否为「权限不足」。
func IsForbidden(err error) bool { return errors.Is(err, ErrForbidden) }

// IsUnavailable 判断错误是否为「授权服务不可用」。
func IsUnavailable(err error) bool { return errors.Is(err, ErrUnavailable) }

// wrap 给错误加上下文，同时保留 errors.Is 可判定性。
func wrap(base error, format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", base, fmt.Sprintf(format, args...))
}
