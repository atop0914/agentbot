package auth

import (
	"context"
	"errors"
	"time"
)

// contextWithTimeout 是内部超时构造的单一入口。
//
// 抽出来是为了让「所有吊销查询都有界」这件事在一处成立 ——
// 若各处自行 context.Background()，漏掉一处就是一个无界的
// 阻塞点，而它恰好挂在每个请求的认证路径上。
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// wrapRevocationErr 把底层错误归一成 ErrRevocationUnavailable。
//
// 用 errors.Join 而不是 %w 单包一层：上层既能用
// errors.Is(err, ErrRevocationUnavailable) 做分类判定，
// 又能用 errors.As 拿到 *redis.ServerError 之类的具体原因
// （例如「键类型错了」与「连不上」需要不同的运维动作）。
func wrapRevocationErr(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(ErrRevocationUnavailable, err)
}
