package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// RateLimitResult 是一次限流判定的结果。
type RateLimitResult struct {
	// Allowed 表示本次请求是否放行。
	Allowed bool
	// Remaining 是当前窗口内剩余额度（不小于 0）。
	Remaining int
	// RetryAfter 是建议的等待时长；Allowed 为真时为 0。
	//
	// 对外变现为 Retry-After 头。它必须来自**服务端已知的窗口剩余时间**，
	// 而不是简单地回填整个窗口长度 —— 后者会让客户端在窗口即将结束时
	// 白白多等一整个周期。
	RetryAfter time.Duration
	// Count 是当前窗口内已计数的请求数（用于日志/指标）。
	Count int
}

// ErrRateLimiterClosed 表示限流器已关闭。
var ErrRateLimiterClosed = errors.New("redis: rate limiter closed")

// RateLimiter 是跨副本共享的固定窗口限流器。
//
// 为什么是固定窗口而不是令牌桶/滑动窗口：
// 固定窗口只需 INCR + EXPIRE 两条命令，在 Redis 上是原子的，
// 且实现可以手写而无需 Lua。它的已知代价是「窗口边界突发」——
// 窗口交界处允许最多 2 倍额度通过（前窗末尾打满 + 新窗开头打满）。
//
// 对本平台的真实用途（登录暴力破解防护、Agent 启动频率限制）来说，
// 2 倍突发是可接受的：防护目标是「把无限次尝试压到几十次」，
// 不是精确整形。滑动窗口需要 Lua 脚本或 ZSET 才能做对，
// 那会把「一个防暴力破解的计数器」变成一段需要单独测试和部署的逻辑。
// 这个取舍写在这里，是为了将来有人要换成滑动窗口时知道代价是什么。
type RateLimiter struct {
	client *Client
	prefix string
}

// NewRateLimiter 创建限流器。
//
// prefix 用于隔离用途（如 "login"、"agentstart"），
// 不同用途的计数绝不能共用键 —— 否则登录失败会消耗 Agent 启动的额度。
func NewRateLimiter(c *Client, prefix string) *RateLimiter {
	return &RateLimiter{client: c, prefix: prefix}
}

// key 生成窗口键。
//
// 键里含窗口序号（unix 秒 / 窗口秒）—— 这是固定窗口的核心：
// 窗口一过，键名自然变化，旧键靠 TTL 回收，不需要任何清理逻辑。
// 用「窗口序号」而不是「窗口起始时间戳」，是为了让键长度恒定且可读。
func (l *RateLimiter) key(id string, window time.Duration) string {
	slot := time.Now().Unix() / int64(window/time.Second)
	return fmt.Sprintf("%s:rl:%s:%d", l.prefix, id, slot)
}

// Allow 判定 id 在 window 内是否还有额度；有则消耗 1 次。
//
// limit <= 0 视为「不限制」，直接放行且不写 Redis ——
// 若把 0 当成「一次都不许」，一个未配置的字段会变成全站 429，
// 那是比不限制严重得多的故障。
func (l *RateLimiter) Allow(ctx context.Context, id string, limit int, window time.Duration) (RateLimitResult, error) {
	if limit <= 0 {
		return RateLimitResult{Allowed: true}, nil
	}
	if window <= 0 {
		return RateLimitResult{}, errors.New("redis: rate limit window must be positive")
	}
	if err := ctx.Err(); err != nil {
		return RateLimitResult{}, err
	}

	k := l.key(id, window)

	// INCR 先自增再判定：这样「计数」与「判定」基于同一个原子操作，
	// 不需要先 GET 再 SET（那会有 TOCTOU 窗口，多副本下必然超发）。
	count, err := l.client.Do("INCR", k)
	if err != nil {
		return RateLimitResult{}, err
	}
	n, ok := count.(int64)
	if !ok {
		return RateLimitResult{}, fmt.Errorf("redis: INCR returned %T, want int64", count)
	}

	// 只在**第一次**计数时设 TTL。
	//
	// 每次都设 TTL 会让键永不过期：一个持续被访问的限流键每请求
	// 刷新一次过期时间，窗口就永远不滚动 —— 计数只增不减，
	// 表现为「用户被永久限流」。必须在 n == 1 时设。
	if n == 1 {
		// 窗口长度 + 1 秒余量：让键活到窗口结束后再回收，
		// 避免「键先过期、窗口还没结束」导致计数被提前清零。
		if _, err := l.client.Do("EXPIRE", k, int(window/time.Second)+1); err != nil {
			return RateLimitResult{}, err
		}
	}

	remaining := limit - int(n)
	if remaining < 0 {
		remaining = 0
	}
	if n <= int64(limit) {
		return RateLimitResult{Allowed: true, Remaining: remaining, Count: int(n)}, nil
	}

	// 超限：算出该窗口还剩多久，作为 Retry-After。
	retry := l.remainingTTL(k, window)
	return RateLimitResult{Allowed: false, Remaining: 0, RetryAfter: retry, Count: int(n)}, nil
}

// remainingTTL 返回键的剩余存活时间，作为客户端应等待的时长。
//
// 取值失败时退回整个窗口长度：这是一个**保守**的兜底 ——
// 让客户端多等一会儿，比返回 0（等于「立刻重试」，
// 会让被限流的客户端变成压测工具）安全。
func (l *RateLimiter) remainingTTL(k string, window time.Duration) time.Duration {
	v, err := l.client.Do("TTL", k)
	if err != nil {
		return window
	}
	sec, ok := v.(int64)
	if !ok || sec < 1 {
		// -1（无 TTL）与 -2（键不存在）都退回窗口长度。
		return window
	}
	if d := time.Duration(sec) * time.Second; d < window {
		return d
	}
	return window
}

// Peek 只读取当前窗口计数，不消耗额度。
//
// 用于「已限流后再请求」的观测路径：若用 Allow 去探测，
// 探测本身会继续推高计数，让日志里的数字失真。
func (l *RateLimiter) Peek(ctx context.Context, id string, window time.Duration) (int, error) {
	if window <= 0 {
		return 0, errors.New("redis: rate limit window must be positive")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	v, err := l.client.Do("GET", l.key(id, window))
	if errors.Is(err, ErrNil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	s, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("redis: GET returned %T, want string", v)
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("redis: rate limit counter is not an integer: %q", s)
	}
	return n, nil
}

// Reset 清除 id 当前窗口的计数。
//
// 用于「登录成功后清零失败计数」这样语义：用户证明了自己是本人，
// 继续按失败次数惩罚他没有任何意义。
func (l *RateLimiter) Reset(ctx context.Context, id string, window time.Duration) error {
	if window <= 0 {
		return errors.New("redis: rate limit window must be positive")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := l.client.Do("DEL", l.key(id, window))
	return err
}
