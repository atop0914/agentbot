package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeLimiter 是 auth.RateLimiter 的可控实现。
//
// 用它而不是真 Redis：本文件的目的是验证 LoginGuard 的**语义**
// （只对失败计数、双维度、fail-open），而不是 Redis 的行为 ——
// 后者已由 redis 包的测试覆盖。端到端那层再用真 Redis。
type fakeLimiter struct {
	counts map[string]int
	// failWith 非 nil 时所有调用返回该错误（模拟限流器故障）。
	failWith error
	resets   []string
}

func newFakeLimiter() *fakeLimiter {
	return &fakeLimiter{counts: make(map[string]int)}
}

func (f *fakeLimiter) Increment(id string, _ time.Duration) (int, error) {
	if f.failWith != nil {
		return 0, f.failWith
	}
	f.counts[id]++
	return f.counts[id], nil
}

// Exceeded 是纯读：不改变计数。与真实适配器的 Peek 语义一致。
func (f *fakeLimiter) Exceeded(id string, limit int, _ time.Duration) (bool, time.Duration, error) {
	if f.failWith != nil {
		return false, 0, f.failWith
	}
	if f.counts[id] >= limit {
		// 返回一个具体的等待时长，让 handler 能写出 Retry-After。
		return true, 42 * time.Second, nil
	}
	return false, 0, nil
}

func (f *fakeLimiter) Reset(id string, _ time.Duration) error {
	if f.failWith != nil {
		return f.failWith
	}
	f.resets = append(f.resets, id)
	delete(f.counts, id)
	return nil
}

// reqFrom 造一个带指定来源 IP 的请求。
func reqFrom(ip string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	r.RemoteAddr = ip + ":54321"
	return r
}

// TestLoginGuard_BlocksAfterLimit 验证超限后被拦。
func TestLoginGuard_BlocksAfterLimit(t *testing.T) {
	f := newFakeLimiter()
	g := NewLoginGuard(f).WithLimit(3, time.Minute)

	// 前 3 次失败：每次检查都应放行（计数尚未达上限），随后计数 +1。
	for i := 1; i <= 3; i++ {
		retry, err := g.Check("user@example.com", reqFrom("10.0.0.1"))
		if err != nil {
			t.Fatalf("check #%d failed: %v", i, err)
		}
		if retry > 0 {
			t.Fatalf("check #%d blocked too early (retry=%v)", i, retry)
		}
		if err := g.RecordFailure("user@example.com", reqFrom("10.0.0.1")); err != nil {
			t.Fatalf("record #%d failed: %v", i, err)
		}
	}

	// 第 4 次必须被拦，且带非零等待时长。
	retry, err := g.Check("user@example.com", reqFrom("10.0.0.1"))
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if retry <= 0 {
		t.Fatal("must be blocked after exceeding the limit")
	}
}

// TestLoginGuard_FailOpenOnLimiterError 验证限流器故障时放行。
//
// 这是与吊销表**相反**的选择，理由写在 loginguard.go：
// 限流失效只是「速度变慢」，而让登录整体不可用是更大的故障 ——
// 攻击者甚至可以靠打垮 Redis 来实现拒绝服务。
func TestLoginGuard_FailOpenOnLimiterError(t *testing.T) {
	f := newFakeLimiter()
	f.failWith = errors.New("redis down")
	g := NewLoginGuard(f).WithLimit(1, time.Minute)

	retry, err := g.Check("user@example.com", reqFrom("10.0.0.1"))
	if err == nil {
		t.Fatal("limiter failure must be reported so the caller can log it")
	}
	if retry > 0 {
		t.Fatal("a limiter failure must NOT block the login (fail-open)")
	}
}

// TestLoginGuard_NilLimiterMeansUnlimited 验证 nil = 不限流。
func TestLoginGuard_NilLimiterMeansUnlimited(t *testing.T) {
	g := NewLoginGuard(nil)
	for i := 0; i < 100; i++ {
		retry, err := g.Check("user@example.com", reqFrom("10.0.0.1"))
		if err != nil || retry > 0 {
			t.Fatalf("nil limiter must never block: retry=%v err=%v", retry, err)
		}
	}
	// 裸 nil 也必须安全（不给「忘记初始化」留下 panic 的机会）。
	var nilGuard *LoginGuard
	retry, err := nilGuard.Check("user@example.com", reqFrom("10.0.0.1"))
	if err != nil || retry > 0 {
		t.Fatalf("nil guard must be safe: retry=%v err=%v", retry, err)
	}
	nilGuard.RecordFailure("a@b.c", reqFrom("10.0.0.1"))
	nilGuard.ClearFailures("a@b.c")
}

// TestLoginGuard_EmailCaseInsensitive 验证邮箱大小写不绕过限流。
//
// 不归一化的话，攻击者变换大小写就能把 10 次变成任意次。
func TestLoginGuard_EmailCaseInsensitive(t *testing.T) {
	f := newFakeLimiter()
	g := NewLoginGuard(f).WithLimit(2, time.Minute)

	g.RecordFailure("User@Example.com", reqFrom("10.0.0.1"))
	g.RecordFailure("USER@EXAMPLE.COM", reqFrom("10.0.0.1"))

	// 第三个变体必须被拦 —— 前两个已被视为同一个账号。
	retry, err := g.Check("user@example.com", reqFrom("10.0.0.1"))
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if retry <= 0 {
		t.Fatal("email case variations must not bypass the per-account limit")
	}
}

// TestLoginGuard_TracksBothEmailAndIP 验证两个维度都计数。
//
// 只按账号 → 可换账号绕过（撞库）；只按 IP → 可换代理绕过。
func TestLoginGuard_TracksBothEmailAndIP(t *testing.T) {
	f := newFakeLimiter()
	g := NewLoginGuard(f).WithLimit(5, time.Minute)

	if err := g.RecordFailure("a@example.com", reqFrom("10.0.0.1")); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	if _, ok := f.counts["login:email:a@example.com"]; !ok {
		t.Fatal("per-account key must be counted")
	}
	if _, ok := f.counts["login:ip:10.0.0.1"]; !ok {
		t.Fatal("per-IP key must be counted")
	}

	// Check 本身不得改变任何计数（它是纯读）。
	// 必须用 RecordFailure 触发计数：Check 是纯读，只有登录失败才计数 ——
	// 否则「检查后因字段缺失提前返回」的请求也会消耗额度，
	// 语义就变成「尝试次数」而非「失败次数」。
	before := len(f.counts)
	if _, err := g.Check("a@example.com", reqFrom("10.0.0.1")); err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if len(f.counts) != before {
		t.Fatal("Check must not create or modify counters")
	}
}

// TestLoginGuard_DifferentIPsShareAccountLimit 验证换 IP 不能绕过账号限流。
func TestLoginGuard_DifferentIPsShareAccountLimit(t *testing.T) {
	f := newFakeLimiter()
	g := NewLoginGuard(f).WithLimit(1, time.Minute)

	g.RecordFailure("victim@example.com", reqFrom("10.0.0.1"))

	// 换一个 IP 打同一账号：账号维度已超限，必须被拦。
	retry, err := g.Check("victim@example.com", reqFrom("10.0.0.2"))
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if retry <= 0 {
		t.Fatal("changing source IP must not bypass the per-account limit")
	}
}

// TestLoginGuard_DifferentAccountsShareIPLimit 验证换账号不能绕过 IP 限流。
func TestLoginGuard_DifferentAccountsShareIPLimit(t *testing.T) {
	f := newFakeLimiter()
	g := NewLoginGuard(f).WithLimit(1, time.Minute)

	g.RecordFailure("one@example.com", reqFrom("10.0.0.9"))

	// 同一 IP 换账号撞库：IP 维度已超限。
	retry, err := g.Check("two@example.com", reqFrom("10.0.0.9"))
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if retry <= 0 {
		t.Fatal("changing the account must not bypass the per-IP limit " +
			"(credential stuffing uses many accounts from one source)")
	}
}

// TestLoginGuard_ClearFailuresOnlyClearsAccount 验证清零只动账号维度。
//
// 用户登录成功证明「他是这个账号的主人」，但不能证明
// 同一 IP 上没有别人在撞库 —— 所以 IP 维度必须保留。
func TestLoginGuard_ClearFailuresOnlyClearsAccount(t *testing.T) {
	f := newFakeLimiter()
	g := NewLoginGuard(f).WithLimit(10, time.Minute)

	g.RecordFailure("user@example.com", reqFrom("10.0.0.1"))
	if _, ok := f.counts["login:ip:10.0.0.1"]; !ok {
		t.Fatal("precondition: IP counter must exist")
	}

	g.ClearFailures("user@example.com")

	if _, ok := f.counts["login:email:user@example.com"]; ok {
		t.Fatal("account counter must be cleared on successful login")
	}
	if _, ok := f.counts["login:ip:10.0.0.1"]; !ok {
		t.Fatal("IP counter must NOT be cleared by one account's success")
	}
}

// TestClientIP_PrefersRemoteAddrOverXFF 验证不轻信 X-Forwarded-For。
//
// 若以 XFF 为主，攻击者每次请求换一个伪造值就完全绕过限流。
func TestClientIP_PrefersRemoteAddrOverXFF(t *testing.T) {
	r := reqFrom("10.0.0.1")
	r.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got := clientIP(r); got != "10.0.0.1" {
		t.Fatalf("clientIP = %q, want the real RemoteAddr 10.0.0.1 "+
			"(XFF is client-controlled and must not be the primary source)", got)
	}
}

// TestClientIP_FallsBackToXFFWhenRemoteAddrMissing 验证退化路径。
func TestClientIP_FallsBackToXFFWhenRemoteAddrMissing(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	r.RemoteAddr = ""
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")

	if got := clientIP(r); got != "1.2.3.4" {
		t.Fatalf("clientIP = %q, want 1.2.3.4 (first hop only)", got)
	}
}
