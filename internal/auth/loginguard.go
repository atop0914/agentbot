package auth

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// 本文件是登录暴力破解防护。
//
// 为什么要做：全仓此前没有任何限流 —— `grep -rn "429|StatusTooManyRequests"
// internal/` 零命中。攻击者可以对 /api/v1/auth/login 无限次尝试，
// 而平台的账号主要是邮箱 + 密码。
//
// 为什么用跨副本限流而不是进程内计数：
// 进程内计数在多副本下把攻击者的可用尝试次数放大 N 倍（N = 副本数），
// 而且各副本独立重置 —— 攻击者只要轮询副本就能绕过。
// 这正是本任务引入 Redis 的第二个真实用途。

// loginRateLimit 是登录失败次数的上限。
//
// 取 10：正常用户极少在一个窗口内输错超过 10 次密码
// （通常第 1-2 次就成功，或直接去走重置流程），
// 而 10 次对在线暴力破解来说远远不够枚举常见弱口令。
const loginRateLimit = 10

// loginRateWindow 是计数窗口。
//
// 取 15 分钟：足够短，让「自己输错几次被锁」的用户等一会儿就能恢复；
// 足够长，让在线爆破的吞吐被压到无意义。
const loginRateWindow = 15 * time.Minute

// RateLimiter 是 LoginGuard 需要的限流能力。
//
// 定义在 auth 包而不是直接用 redis.RateLimiter：
// auth 包（及其 handler）不该依赖具体的限流后端。
// 刻意只声明需要的这两个方法 —— LoginGuard 不该知道限流器
// 还能 Peek 之类的其它能力（多一个方法就多一处将来会分歧的地方）。
type RateLimiter interface {
	// Increment 记一次失败并返回当前窗口的计数。
	Increment(id string, window time.Duration) (count int, err error)
	// Exceeded 只读判定：当前计数是否已达/超过 limit。
	// 不改变计数 —— 见 Check 的注释说明为什么检查不该消耗额度。
	Exceeded(id string, limit int, window time.Duration) (blocked bool, retryAfter time.Duration, err error)
	// Reset 清零某键的计数。
	Reset(id string, window time.Duration) error
}

// LoginGuard 实现登录的失败次数限制。
//
// 设计要点：**只对失败计数**，不对成功计数。
//
// 若对所有尝试计数，一个共享出口 IP 的办公室（NAT）在正常使用时
// 也会被锁 —— 那会把一个安全功能变成可用性事故。
// 失败计数则精确对应「有人在猜密码」这个信号。
type LoginGuard struct {
	// limiter 为 nil 表示不限流（未配置 Redis）。
	limiter RateLimiter
	// limit 与 window 允许测试收窄。
	limit  int
	window time.Duration
}

// NewLoginGuard 创建登录防护；limiter 为 nil 时不限流。
func NewLoginGuard(limiter RateLimiter) *LoginGuard {
	return &LoginGuard{limiter: limiter, limit: loginRateLimit, window: loginRateWindow}
}

// WithLimit 覆盖阈值与窗口（测试用）。
func (g *LoginGuard) WithLimit(limit int, window time.Duration) *LoginGuard {
	if limit > 0 {
		g.limit = limit
	}
	if window > 0 {
		g.window = window
	}
	return g
}

// Check 判定该请求是否允许尝试登录。
//
// 返回 retryAfter > 0 表示应拒绝并告知客户端等待多久。
//
// **Check 不消耗额度**：它只读当前计数。真正计数的只有
// RecordFailure（登录失败时调用）。若 Check 也自增，一次
// 「检查后因字段缺失而提前返回」的请求也会消耗额度，
// 那让计数语义变成「尝试次数」而不是「失败次数」——
// 而后者才对应「有人在猜密码」这个信号。
//
// **失败开放（fail-open）**：限流器本身故障时放行。
// 理由：限流是「降低暴力破解速度」的措施，不是认证的必要条件。
// 让它 fail-closed 会让一次 Redis 故障变成「全站无法登录」——
// 而攻击者恰恰可以靠打垮 Redis 来实现拒绝服务。
// 注意这与吊销表的 fail-closed 是**相反**的选择，因为两者的
// 失败后果不同：吊销失效 = 安全问题；限流失效 = 速度变慢。
func (g *LoginGuard) Check(email string, r *http.Request) (retryAfter time.Duration, err error) {
	if g == nil || g.limiter == nil {
		return 0, nil
	}

	// 两个维度各自判定，任一超限即拒绝：
	//
	//   按账号（email）—— 防止针对某个账号的定向爆破。
	//   按来源 IP     —— 防止用一个弱口令去撞大量账号（credential stuffing）。
	//
	// 只按其中一个都不够：只按账号时可换账号绕过，只按 IP 时可换代理绕过。
	for _, key := range loginKeys(email, r) {
		blocked, retry, aerr := g.limiter.Exceeded(key, g.limit, g.window)
		if aerr != nil {
			// fail-open，但把错误回传给调用方记日志 ——
			// 静默放行会让「限流一直没生效」这件事无人知晓。
			return 0, aerr
		}
		if blocked {
			return retry, nil
		}
	}
	return 0, nil
}

// loginKeys 返回该次登录尝试应计入的限流键。
//
// 返回**全部**维度而不是第一个命中的：多维度共同计数才能同时
// 覆盖「定向爆破」与「撞库」两种模式。
func loginKeys(email string, r *http.Request) []string {
	keys := make([]string, 0, 2)
	if e := normalizeEmail(email); e != "" {
		keys = append(keys, "login:email:"+e)
	}
	if r != nil {
		if ip := clientIP(r); ip != "" {
			keys = append(keys, "login:ip:"+ip)
		}
	}
	return keys
}

// RecordFailure 记一次登录失败。
//
// 返回的 error 由调用方决定是否记日志：失败不该影响响应，
// 但静默失败会让「限流一直没生效」无人知晓。
func (g *LoginGuard) RecordFailure(email string, r *http.Request) error {
	if g == nil || g.limiter == nil {
		return nil
	}
	var firstErr error
	for _, key := range loginKeys(email, r) {
		if _, err := g.limiter.Increment(key, g.window); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ClearFailures 清除某账号的失败计数。
//
// 只在按**账号**维度清零，不动 IP 维度：
// IP 维度统计的是「这个来源的行为」，一个用户登录成功
// 不能证明同一 IP 上没有别人在撞库。
func (g *LoginGuard) ClearFailures(email string) {
	if g == nil || g.limiter == nil {
		return
	}
	e := normalizeEmail(email)
	if e == "" {
		return
	}
	_ = g.limiter.Reset("login:email:"+e, g.window)
}

// logRateLimiterFailure 记录限流器故障（fail-open 的配套动作）。
//
// 单独一个方法而不是内联一行：它存在的意义就是「fail-open 必须留痕」，
// 将来有人想删掉它时，这个注释就是理由。
func (h *Handler) logRateLimiterFailure(r *http.Request, err error) {
	if h.logger != nil {
		h.logger.Warn("login rate limiter unavailable; allowing attempt (fail-open)",
			"error", err)
	}
	_ = r
}

// normalizeEmail 归一化邮箱，避免大小写/空白造成的键分裂。
//
// 不归一化的话，`A@x.com` 与 `a@x.com` 会得到两份独立额度 ——
// 攻击者只要变换大小写就能把 10 次变成任意次。
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// clientIP 提取客户端 IP。
//
// **刻意不信任 X-Forwarded-For** 作为主要来源：
// 该头由客户端可伪造，用它做限流键等于让攻击者按需换 IP ——
// 每次请求换一个伪造值就完全绕过限流。
//
// 只有在 RemoteAddr 不可用时才退回 XFF 的最后一跳，
// 并只取第一个逗号前的值。部署在可信代理后面时，
// 应由代理改写 RemoteAddr（或后续引入可信代理列表）。
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	// 退化路径：RemoteAddr 不可用（如 httptest 未设）时用 XFF，
	// 但这只是让限流「有比没有好」，不能作为安全边界。
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		return strings.TrimSpace(xff)
	}
	return ""
}

// ErrLoginRateLimited 表示登录尝试次数超限。
var ErrLoginRateLimited = errors.New("auth: too many login attempts")

// RateLimitError 携带建议的等待时长。
type RateLimitError struct {
	RetryAfter time.Duration
}

// Error 实现 error。
func (e *RateLimitError) Error() string {
	return fmt.Sprintf("too many login attempts; retry after %s", e.RetryAfter.Round(time.Second))
}
