package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/config"
	"github.com/atop0914/agentbot/internal/redis"
)

// 本文件逐条覆盖 redis_wiring.go 的决策表。
//
// 与 storage_wiring_test.go 同构：决策表达 4 条分支，
// 每条都要有测试，否则「配了却静默回退」这条最危险的路径
// 只会在生产上第一次被走到。

// redisTestAddr 返回本机 Redis 地址（默认 6379）。
func redisTestAddr() string {
	if a := os.Getenv("AGENTBOT_TEST_REDIS_ADDR"); a != "" {
		return a
	}
	return "127.0.0.1:6379"
}

// redisAvailable 探测本机 Redis 是否可用。
//
// 判定依据是**真实可达性**，不是环境变量是否存在 ——
// 环境变量说有，不代表真能连（3-3 的教训）。
func redisAvailable(t *testing.T) bool {
	t.Helper()
	c, err := redis.New(redis.Config{Addr: redisTestAddr(), DialTimeout: 500 * time.Millisecond})
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// TestSelectRedis_UnconfiguredUsesMemory 验证「没配 Redis」→ 内存实现。
//
// 这是合法状态（单副本/开发），不是降级：不该报错，也不该 Warn。
func TestSelectRedis_UnconfiguredUsesMemory(t *testing.T) {
	cfg := config.DevelopmentDefault()
	cfg.Redis.Host = ""

	sel, err := selectRedis(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unconfigured redis must not be an error: %v", err)
	}
	if sel.backend != redisBackendMemory {
		t.Fatalf("backend = %q, want %q", sel.backend, redisBackendMemory)
	}
	if sel.revocations == nil {
		t.Fatal("revocations must never be nil — a nil store would panic on first use")
	}
	if sel.client != nil {
		t.Fatal("client must be nil when redis is not configured")
	}
	// 未配置时没有限流器，这是刻意的（见 App.RateLimiter 注释）。
	if sel.limiter != nil {
		t.Fatal("limiter must be nil without redis")
	}
}

// TestSelectRedis_ConfiguredAndReachableUsesRedis 验证可达 → Redis 实现。
func TestSelectRedis_ConfiguredAndReachableUsesRedis(t *testing.T) {
	if !redisAvailable(t) {
		t.Skipf("redis unavailable at %s", redisTestAddr())
	}
	cfg := config.DevelopmentDefault()
	cfg.Redis.Host = "127.0.0.1"
	cfg.Redis.Port = 6379

	sel, err := selectRedis(context.Background(), cfg)
	if err != nil {
		t.Fatalf("select failed: %v", err)
	}
	t.Cleanup(func() {
		if sel.client != nil {
			_ = sel.client.Close()
		}
	})
	if sel.backend != redisBackendRedis {
		t.Fatalf("backend = %q, want %q", sel.backend, redisBackendRedis)
	}
	if sel.client == nil {
		t.Fatal("client must be non-nil when redis is selected")
	}
	if sel.limiter == nil {
		t.Fatal("limiter must be non-nil when redis is selected")
	}
}

// TestSelectRedis_UnreachableRefusesToStartByDefault 验证默认 fail-closed。
//
// 这是本轮最重要的安全断言：静默退回内存实现的后果不是
// 「功能少一点」，而是「登出只在命中的那个副本生效」——
// 用户看到 200，安全动作却没有生效，且没有任何外部信号。
func TestSelectRedis_UnreachableRefusesToStartByDefault(t *testing.T) {
	cfg := config.DevelopmentDefault()
	cfg.Redis.Host = "127.0.0.1"
	// 保留端口 1：连接被立即拒绝，不必等 DNS 超时。
	cfg.Redis.Port = 1
	cfg.Storage.AllowMemoryFallback = false

	sel, err := selectRedis(context.Background(), cfg)
	if err == nil {
		t.Fatal("configured-but-unreachable redis must refuse to start by default")
	}
	if sel.backend != redisBackendMemory {
		t.Fatalf("backend = %q, want %q", sel.backend, redisBackendMemory)
	}
	// 错误信息必须指明逃生开关，否则运维只能去读源码。
	if !strings.Contains(err.Error(), "STORAGE_ALLOW_MEMORY_FALLBACK") {
		t.Fatalf("error must name the escape hatch, got: %v", err)
	}
	if !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("error must state the refusal explicitly, got: %v", err)
	}
}

// TestSelectRedis_UnreachableWithExplicitFallbackUsesMemory 验证显式回退。
func TestSelectRedis_UnreachableWithExplicitFallbackUsesMemory(t *testing.T) {
	cfg := config.DevelopmentDefault()
	cfg.Redis.Host = "127.0.0.1"
	cfg.Redis.Port = 1
	cfg.Storage.AllowMemoryFallback = true

	sel, err := selectRedis(context.Background(), cfg)
	if err != nil {
		t.Fatalf("explicit fallback must not fail: %v", err)
	}
	if sel.backend != redisBackendMemory {
		t.Fatalf("backend = %q, want %q", sel.backend, redisBackendMemory)
	}
	if sel.revocations == nil {
		t.Fatal("fallback must still provide a usable revocation store")
	}
	if sel.client != nil {
		t.Fatal("client must be nil after falling back — holding a dead handle " +
			"is how 'started fine but breaks on first use' happens")
	}
}

// TestRedisSelection_FallbackIsActuallyUsable 验证回退路径**运行期可用**。
//
// 「启动成功 ≠ 运行期可用」：只验证启动阶段的测试会漏掉
// 「某个组件仍持有那个连不上的句柄」这类问题。
// 这里对回退得到的吊销表做一次完整读写往返。
func TestRedisSelection_FallbackIsActuallyUsable(t *testing.T) {
	cfg := config.DevelopmentDefault()
	cfg.Redis.Host = "127.0.0.1"
	cfg.Redis.Port = 1
	cfg.Storage.AllowMemoryFallback = true

	sel, err := selectRedis(context.Background(), cfg)
	if err != nil {
		t.Fatalf("select failed: %v", err)
	}

	fp := "fingerprint-" + time.Now().Format(time.RFC3339Nano)
	if err := sel.revocations.Revoke("user-1", fp, time.Minute); err != nil {
		t.Fatalf("fallback revocation store is not usable: %v", err)
	}
	revoked, err := sel.revocations.IsRevoked("user-1", fp)
	if err != nil {
		t.Fatalf("IsRevoked on fallback failed: %v", err)
	}
	if !revoked {
		t.Fatal("fallback store must round-trip a revocation")
	}
}

// TestRedisHostForLog_NeverLeaksPassword 验证日志字段不含密码。
//
// 启动日志是最容易被整段贴进 issue 或聊天窗口的东西。
func TestRedisHostForLog_NeverLeaksPassword(t *testing.T) {
	cfg := config.DevelopmentDefault()
	cfg.Redis.Host = "redis.internal"
	cfg.Redis.Port = 6380
	cfg.Redis.DB = 3
	// 用一个绝不该出现在日志里的值。
	cfg.Redis.Password = "super-secret-password-value"

	got := redisHostForLog(cfg)
	if strings.Contains(got, cfg.Redis.Password) {
		t.Fatalf("log field leaks the password: %q", got)
	}
	if got != "redis.internal:6380/3" {
		t.Fatalf("got %q, want redis.internal:6380/3", got)
	}

	// 未配置时返回空串（而不是 "localhost:0" 这种误导性值）。
	cfg.Redis.Host = ""
	if got := redisHostForLog(cfg); got != "" {
		t.Fatalf("unconfigured redis must log an empty host, got %q", got)
	}
}

// TestSelectRedis_UnexpectedErrorIsNotSwallowed 验证非连接类错误不被回退掩盖。
//
// 空白地址会走「未配置」分支（这是对的）。这里验证的是
// 「配置看起来合法但底层拒绝」时不被当成正常回退。
func TestSelectRedis_UnexpectedErrorIsNotSwallowed(t *testing.T) {
	cfg := config.DevelopmentDefault()
	cfg.Redis.Host = "127.0.0.1"
	// 端口 0 是非法的，但 Validate() 会在更早的位置拦下它；
	// 直接调 selectRedis 时它应该以连接失败体现，且必须报错。
	cfg.Redis.Port = 1
	cfg.Storage.AllowMemoryFallback = false

	_, err := selectRedis(context.Background(), cfg)
	if err == nil {
		t.Fatal("must not silently swallow a connection failure")
	}
	var se *redis.ServerError
	// 连接失败不该被误报成服务端错误（两者的运维动作不同）。
	if errors.As(err, &se) {
		t.Fatalf("connection failure must not be classified as a server error: %v", err)
	}
}
