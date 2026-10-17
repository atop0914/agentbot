package app

import (
	"context"
	"time"

	"github.com/atop0914/agentbot/internal/auth"
	"github.com/atop0914/agentbot/internal/redis"
)

// redisLoginLimiter 把 redis.RateLimiter 适配成 auth.RateLimiter。
//
// 为什么需要适配而不是让 auth 直接用 redis.RateLimiter：
//   - redis 版需要 context，而 HTTP handler 在这一层拿不到一个
//     带有合适超时的 ctx（handler 的 ctx 是请求级的，但限流
//     查询应该有自己的上限，不该被客户端断连影响）；
//   - 更重要的是**不让 auth 包依赖 redis 包**：auth 是业务包，
//     不该知道 Redis 的存在。这样将来换限流后端（例如 PostgreSQL
//     计数表）只需换一个适配器。
//
// 语义映射：auth.RateLimiter 的 Increment / Exceeded 对应
// redis.RateLimiter 的 Allow / Peek。
// Allow 同时自增并判定，因此 Increment 用它（忽略判定结果），
// Exceeded 用 Peek（纯读）—— 这样「检查」不会消耗额度，
// 与 auth.LoginGuard.Check 的约定一致。
type redisLoginLimiter struct {
	limiter *redis.RateLimiter
	// timeout 是单次限流查询的上限。
	//
	// 挂在登录路径上：没有超时约定时，一次 Redis 卡顿会让
	// 「登录」这个最关键的入口变慢。注意限流整体是 fail-open 的
	// （见 auth.LoginGuard.Check），所以超时只会让它放弃限流，
	// 不会阻止登录。
	timeout time.Duration
}

// loginLimiterTimeout 是限流查询超时。
//
// 比吊销查询的超时（300ms）同样取 300ms：两者都在请求路径上。
// 限流是 fail-open，超时的后果只是「这次没限」。
const loginLimiterTimeout = 300 * time.Millisecond

// newRedisLoginLimiter 创建适配器；l 为 nil 时返回 nil（= 不限流）。
func newRedisLoginLimiter(l *redis.RateLimiter) auth.RateLimiter {
	if l == nil {
		return nil
	}
	return &redisLoginLimiter{limiter: l, timeout: loginLimiterTimeout}
}

// Increment 记一次失败，返回当前窗口计数。
func (a *redisLoginLimiter) Increment(id string, window time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()

	// 传一个足够大的 limit，让 Allow 只负责自增、不产生「超限」
	// 的额外语义。计数由返回值给出。
	res, err := a.limiter.Allow(ctx, id, maxInt, window)
	if err != nil {
		return 0, err
	}
	return res.Count, nil
}

// Exceeded 只读判定当前计数是否已达/超过 limit。
func (a *redisLoginLimiter) Exceeded(id string, limit int, window time.Duration) (bool, time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()

	count, err := a.limiter.Peek(ctx, id, window)
	if err != nil {
		return false, 0, err
	}
	if count < limit {
		return false, 0, nil
	}
	// 已超限：给出该窗口剩余的存活时间作为 Retry-After。
	// Peek 不返回 TTL，因此这里退回窗口长度 —— 保守方向
	// （让客户端多等一会儿，而不是返回 0 让它立刻重试）。
	return true, window, nil
}

// Reset 清零某键的计数。
func (a *redisLoginLimiter) Reset(id string, window time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()
	return a.limiter.Reset(ctx, id, window)
}

// maxInt 用于「只自增、不判定」的调用。
//
// 取一个不可能达到的值而不是 0：redis.RateLimiter 把 limit <= 0
// 解释为「不限制」，那会让 Allow 直接返回而**完全没有自增** ——
// 一个静默失效（限流键永远不存在）。
const maxInt = 1 << 30
