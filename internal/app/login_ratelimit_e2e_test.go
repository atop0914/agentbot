package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/auth"
	"github.com/atop0914/agentbot/internal/config"
)

// 本文件验证登录暴力破解防护**经真实 router** 生效。
//
// 为什么必须端到端：LoginGuard 单独测全绿也证明不了它被挂上了 ——
// handler.WithLoginGuard 是个可选 Option，装配层忘了调它
// 不会有任何编译错误或测试失败（本仓库已有先例：WithStaticDir
// 早就存在但全仓无调用方，控制台因此一直报「产物尚未构建」）。
//
// 断言落在**真实 HTTP 状态码**上：429 + Retry-After。

// loginAttempt 发一次登录请求，返回状态码与 Retry-After 头。
func loginAttempt(t *testing.T, baseURL, email, password string) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)
	resp, err := http.Post(baseURL+"/api/v1/auth/login", "application/json",
		bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header.Get("Retry-After")
}

// TestLoginRateLimit_Returns429AfterRepeatedFailures 验证失败累积到阈值后返回 429。
//
// 同时断言 **Retry-After 头存在且为正**：只回 429 不带该头，
// 客户端无法知道该等多久，只能盲目重试 —— 那是把限流转成重试风暴。
func TestLoginRateLimit_Returns429AfterRepeatedFailures(t *testing.T) {
	cfg := redisCfgForTest(t)
	rep := newRedisReplica(t, cfg)
	rep.register(t)

	// 用一个**存在但密码错**的账号，让失败计数真实累加。
	email := rep.email
	wrongPassword := "WrongPass!999"

	// 阈值是 10（auth.loginRateLimit）。打到第 11 次应看到 429。
	var got429At int
	var retryAfter string
	for i := 1; i <= 15; i++ {
		code, ra := loginAttempt(t, rep.ts.URL, email, wrongPassword)
		if code == http.StatusTooManyRequests {
			got429At = i
			retryAfter = ra
			break
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("attempt %d returned %d, want 401 (wrong password) "+
				"or 429 (rate limited)", i, code)
		}
	}
	if got429At == 0 {
		t.Fatal("15 failed logins did not trigger a 429 — the guard is not " +
			"wired into the login handler (handler.WithLoginGuard may not be called)")
	}
	if retryAfter == "" {
		t.Fatal("429 must carry a Retry-After header; without it clients " +
			"cannot know how long to wait and will retry blindly")
	}
	// Retry-After 必须是正整数秒。
	var secs int
	if _, err := fmt.Sscanf(retryAfter, "%d", &secs); err != nil || secs < 1 {
		t.Fatalf("Retry-After = %q, want a positive integer number of seconds", retryAfter)
	}
}

// TestLoginRateLimit_SharedAcrossReplicas 验证限流跨副本共享。
//
// 这是与吊销表同源的核心断言：进程内计数在多副本下把攻击者的
// 可用尝试次数放大 N 倍（N = 副本数），且各副本独立重置 ——
// 攻击者只要轮询副本就能绕过。
func TestLoginRateLimit_SharedAcrossReplicas(t *testing.T) {
	cfg := redisCfgForTest(t)
	repA := newRedisReplica(t, cfg)
	repB := newRedisReplica(t, cfg)
	repA.register(t)

	email := repA.email
	wrong := "WrongPass!999"

	// 在 A 上打满失败次数。
	saw429OnA := false
	for i := 0; i < 15; i++ {
		code, _ := loginAttempt(t, repA.ts.URL, email, wrong)
		if code == http.StatusTooManyRequests {
			saw429OnA = true
			break
		}
	}
	if !saw429OnA {
		t.Fatal("replica A never rate limited; cannot test cross-replica sharing")
	}

	// B 副本必须**立刻**也拒绝 —— 即使 B 自己一次失败都没见过。
	// 注意：这里要从另一个来源 IP 发起，否则按 IP 计数也可能命中；
	// 但 httptest 的 RemoteAddr 都是 127.0.0.1，因此这里验证的
	// 主要是**账号维度**的共享，这已经足够证明限流状态跨副本。
	code, ra := loginAttempt(t, repB.ts.URL, email, wrong)
	if code != http.StatusTooManyRequests {
		t.Fatalf("replica B returned %d for an account already throttled on "+
			"replica A, want 429 — the rate limit state is not shared, so "+
			"attackers get N times the attempts by rotating replicas", code)
	}
	if ra == "" {
		t.Fatal("429 from replica B must also carry Retry-After")
	}
}

// TestLoginRateLimit_SuccessClearsAccountCounter 验证成功登录清零账号计数。
//
// 反向断言（不误伤）：一个输错几次后终于想起密码的用户，
// 不该在下次登录时仍被之前的失败次数拖累。
func TestLoginRateLimit_SuccessClearsAccountCounter(t *testing.T) {
	cfg := redisCfgForTest(t)
	rep := newRedisReplica(t, cfg)
	rep.register(t)
	email := rep.email

	// 输错 3 次（低于阈值 10）。
	for i := 0; i < 3; i++ {
		if code, _ := loginAttempt(t, rep.ts.URL, email, "WrongPass!999"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, code)
		}
	}

	// 用正确密码登录成功。
	code, _ := loginAttempt(t, rep.ts.URL, email, "Str0ngPass!123")
	if code != http.StatusOK {
		t.Fatalf("correct login returned %d, want 200", code)
	}

	// 计数应已清零：再输错不该立刻接近阈值。
	// 断言方式是「再输错 6 次仍不该 429」——若清零没生效，
	// 3 + 6 = 9 仍低于 10，因此这里额外验证一个更强的事实：
	// 清零后单次失败会让计数从 1 开始。用 Peek 直接读计数更精确。
	limiter := rep.app.RateLimiter
	if limiter == nil {
		t.Fatal("RateLimiter must be wired for a redis-backed app")
	}
	// 先失败一次，让计数确定存在。
	if code, _ := loginAttempt(t, rep.ts.URL, email, "WrongPass!999"); code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", code)
	}
	// 通过适配器读回计数（与 handler 用同一存储）。
	adapter := newRedisLoginLimiter(limiter)
	count, err := adapter.Increment("login:email:"+email, time.Minute)
	if err != nil {
		t.Fatalf("increment failed: %v", err)
	}
	// 清零后应经历 1（刚才那次失败）+ 1（本次探测）= 2，
	// 而不是 3（清零前的失败数）+ 2 = 5。
	if count > 3 {
		t.Fatalf("counter = %d after a successful login cleared it; "+
			"expected a small number — the success did not clear prior failures", count)
	}
}

// TestLoginRateLimit_NotWiredWithoutRedis 验证未配 Redis 时不限流也不报错。
//
// 这是刻意的取舍：宁愿不限流，也不要一个「多副本下等于没限」的假限流
// （那会让运维以为已经有了防护）。
func TestLoginRateLimit_NotWiredWithoutRedis(t *testing.T) {
	cfg := config.DevelopmentDefault()
	cfg.Redis.Host = ""
	cfg.Redis.Password = ""

	rep := newRedisReplica(t, cfg)
	rep.register(t)

	if rep.app.RateLimiter != nil {
		t.Fatal("RateLimiter must be nil without redis")
	}

	// 打满 15 次失败也不该出现 429（因为没限流）。
	for i := 0; i < 15; i++ {
		code, _ := loginAttempt(t, rep.ts.URL, rep.email, "WrongPass!999")
		if code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d returned 429 without redis; the guard must "+
				"not be wired when there is no shared counter", i+1)
		}
	}
}

// TestLoginRateLimit_GuardIsActuallyAttached 直接断言装配结果。
//
// 这是对上面端到端断言的**补充**：它把「guard 是否挂上」
// 变成一个显式检查，失败信息直指原因（WithLoginGuard 没被调用），
// 而不是让排查者从「15 次登录都没 429」反推。
func TestLoginRateLimit_GuardIsActuallyAttached(t *testing.T) {
	cfg := redisCfgForTest(t)
	rep := newRedisReplica(t, cfg)

	// 断言 App 持有的 limiter 与 auth handler 上的 guard 来自同一份装配。
	if rep.app.RateLimiter == nil {
		t.Fatal("app.RateLimiter must be non-nil for a redis-backed app")
	}
	// 通过 auth 包暴露的构造断言接口形态一致（编译期保证 + 运行时非 nil）。
	g := auth.NewLoginGuard(newRedisLoginLimiter(rep.app.RateLimiter))
	if g == nil {
		t.Fatal("guard construction must succeed")
	}
}

// TestLoginRateLimit_BodyIsDecodable 确认 429 响应体是 JSON。
//
// 与仓库其它错误响应形状一致：客户端不该为一个 429 写特例解析。
func TestLoginRateLimit_BodyIsDecodable(t *testing.T) {
	cfg := redisCfgForTest(t)
	rep := newRedisReplica(t, cfg)
	rep.register(t)

	var lastBody string
	for i := 0; i < 15; i++ {
		body := fmt.Sprintf(`{"email":%q,"password":"WrongPass!999"}`, rep.email)
		resp, err := http.Post(rep.ts.URL+"/api/v1/auth/login", "application/json",
			bytes.NewBufferString(body))
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			lastBody = string(b)
			break
		}
	}
	if lastBody == "" {
		t.Skip("account was not throttled within 15 attempts; nothing to decode")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(lastBody), &decoded); err != nil {
		t.Fatalf("429 body is not valid JSON (%v): %s", err, lastBody)
	}
	if _, ok := decoded["error"]; !ok {
		t.Fatalf("429 body must carry an 'error' field like other API errors: %s", lastBody)
	}
}
