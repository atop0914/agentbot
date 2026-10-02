package monitor

import (
	"context"

	"github.com/atop0914/agentbot/internal/auth"
)

// SubjectIDFromContext 从请求 context 中取出已认证主体的 ID。
//
// 处置记录必须能回答「谁改的」，因此这里复用认证中间件写入的 JWT claims，
// 而不是接受客户端自报的身份。未认证时返回空串，由调用方决定降级策略。
func SubjectIDFromContext(ctx context.Context) string {
	if claims := auth.GetClaimsFromContext(ctx); claims != nil {
		return claims.UserID
	}
	return ""
}
