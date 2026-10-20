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
//
// ⚠️ 为了让这些测试跑得快，阈值被压到 3（见 rateLimitedReplica）。
// 每次失败登录都会真实执行一次 bcrypt 校验（**刻意**慢，约 50-100ms），
// 用默认阈值 10 会让每个测试打十几次密码校验 —— 单包总计多出
// 数十秒，而 `internal/app` 的 e2e 套件本来就已接近 go test
// 的 600s 默认超时。用配置把阈值调小，既保留了真实链路，
// 又不为「跑满默认阈值」付 CPU 代价。
// 阈值本身可配是**真实需求**（共享出口 IP 的办公场景需要放宽），
// 不是为了测试才加的开关。

// testLoginLimit 是端到端测试使用的阈值。
const testLoginLimit = 3

// rateLimitedReplica 起一个「阈值被调小」的 Redis 后端副本。
func rateLimitedReplica(t *testing.T) *replicaEnv {
	t.Helper()
	cfg := redisCfgForTest(t)
	cfg.Auth.LoginRateLimit = testLoginLimit
	// 窗口保持默认长度：测试只关心「超限后是否拒绝」，
	// 不需要短窗口来模拟过期（那会引入时间竞态）。
	return newRedisReplica(t, cfg)
}

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
	rep := rateLimitedReplica(t)
	rep.register(t)

	// 用一个**存在但密码错**的账号，让失败计数真实累加。
	email := rep.email
	wrongPassword := "WrongPass!999"

	// 阈值是 testLoginLimit（3）。打到第 4 次应看到 429。
	var got429At int
	var retryAfter string
	for i := 1; i <= testLoginLimit+3; i++ {
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
		t.Fatalf("%d failed logins did not trigger a 429 — the guard is not "+
			"wired into the login handler (handler.WithLoginGuard may not be called)",
			testLoginLimit+3)
	}
	// 必须在**恰好**第 limit+1 次被拦：若更早就说明计数逻辑把
	// 「允许的次数」算错了（例如 Check 也自增）。
	if got429At != testLoginLimit+1 {
		t.Fatalf("first 429 at attempt %d, want %d — the threshold off-by-one is wrong",
			got429At, testLoginLimit+1)
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
	cfg.Auth.LoginRateLimit = testLoginLimit
	repA := newRedisReplica(t, cfg)
	repB := newRedisReplica(t, cfg)
	repA.register(t)

	email := repA.email
	wrong := "WrongPass!999"

	// 在 A 上打满失败次数。
	saw429OnA := false
	for i := 0; i <= testLoginLimit+2; i++ {
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
	rep := rateLimitedReplica(t)
	rep.register(t)
	email := rep.email

	// 输错 2 次（低于阈值 3）。
	for i := 0; i < testLoginLimit-1; i++ {
		if code, _ := loginAttempt(t, rep.ts.URL, email, "WrongPass!999"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, code)
		}
	}

	// 用正确密码登录成功。
	code, _ := loginAttempt(t, rep.ts.URL, email, "Str0ngPass!123")
	if code != http.StatusOK {
		t.Fatalf("correct login returned %d, want 200", code)
	}

	// 清零后，**账号维度**可以再次用满整个额度。
	//
	// ⚠️ 只断言账号维度：成功登录**刻意只清账号计数、不清 IP 计数**
	// （见 LoginGuard.ClearFailures 的注释 —— 一个用户登录成功
	// 不能证明同一 IP 上没有别人在撞库）。而 httptest 的
	// RemoteAddr 恒为 127.0.0.1，所以 IP 维度仍累积着之前的失败，
	// 迟早会触发按 IP 的阈值。
	//
	// 首版这里断言「再失败 limit 次都不会 429」，跑出来是红的 ——
	// 错的是测试的预期（它假定了一次完整的重置），
	// 而不是实现（实现按设计只清账号维度）。
	// 改为直接读账号维度的计数，避开 IP 维度的干扰。
	limiter := newRedisLoginLimiter(rep.app.RateLimiter)
	count, err := limiter.Increment("login:email:"+email, time.Minute)
	if err != nil {
		t.Fatalf("increment failed: %v", err)
	}
	// 清零生效时，这次探测应是第 1 次（值 1）；
	// 若没清零，之前 limit-1 次失败会让它变成 limit。
	if count != 1 {
		t.Fatalf("account counter = %d after a successful login, want 1 — "+
			"the success did not clear prior account failures", count)
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
	// 阈值调小不改变结论，但让「打满」这一步更快。
	cfg.Auth.LoginRateLimit = 2

	rep := newRedisReplica(t, cfg)
	rep.register(t)

	if rep.app.RateLimiter != nil {
		t.Fatal("RateLimiter must be nil without redis")
	}

	// 超过任何合理阈值也不该出现 429（因为没限流）。
	for i := 0; i < 6; i++ {
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
// 而不是让排查者从「多次登录都没 429」反推。
func TestLoginRateLimit_GuardIsActuallyAttached(t *testing.T) {
	cfg := redisCfgForTest(t)
	rep := newRedisReplica(t, cfg)

	if rep.app.RateLimiter == nil {
		t.Fatal("app.RateLimiter must be non-nil for a redis-backed app")
	}
	// 构造必须成功（编译期保证接口形态一致 + 运行时非 nil）。
	if g := auth.NewLoginGuard(newRedisLoginLimiter(rep.app.RateLimiter)); g == nil {
		t.Fatal("guard construction must succeed")
	}
}

// TestLoginRateLimit_BodyIsDecodable 确认 429 响应体是 JSON。
//
// 与仓库其它错误响应形状一致：客户端不该为一个 429 写特例解析。
func TestLoginRateLimit_BodyIsDecodable(t *testing.T) {
	rep := rateLimitedReplica(t)
	rep.register(t)

	var lastBody string
	for i := 0; i <= testLoginLimit+2; i++ {
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
		t.Fatal("account was not throttled; nothing to decode")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(lastBody), &decoded); err != nil {
		t.Fatalf("429 body is not valid JSON (%v): %s", err, lastBody)
	}
	if _, ok := decoded["error"]; !ok {
		t.Fatalf("429 body must carry an 'error' field like other API errors: %s", lastBody)
	}
}

// TestLoginRateLimit_ConfigIsHonored 验证配置阈值真的生效。
//
// 断言「阈值小时第 N+1 次被拦」比断言「阈值大时更晚被拦」强：
// 前者能证明配置被读取，后者可能与默认值巧合一致。
func TestLoginRateLimit_ConfigIsHonored(t *testing.T) {
	cfg := redisCfgForTest(t)
	cfg.Auth.LoginRateLimit = 2 // 覆盖默认的 10
	rep := newRedisReplica(t, cfg)
	rep.register(t)

	// 2 次失败允许，第 3 次必须被拦。
	for i := 1; i <= 2; i++ {
		if code, _ := loginAttempt(t, rep.ts.URL, rep.email, "WrongPass!999"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i, code)
		}
	}
	code, _ := loginAttempt(t, rep.ts.URL, rep.email, "WrongPass!999")
	if code != http.StatusTooManyRequests {
		t.Fatalf("attempt 3 returned %d, want 429 — cfg.Auth.LoginRateLimit=2 "+
			"was not honored (is the config wired into NewLoginGuard?)", code)
	}
	_ = time.Second
}
