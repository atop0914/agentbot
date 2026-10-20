package redis

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// TestRateLimiter_AllowsUpToLimitThenBlocks 验证额度语义。
func TestRateLimiter_AllowsUpToLimitThenBlocks(t *testing.T) {
	c := newTestClient(t)
	rl := NewRateLimiter(c, "agentbot:test:"+t.Name())
	ctx := context.Background()
	id := "user-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	window := time.Minute
	t.Cleanup(func() { _ = rl.Reset(ctx, id, window) })

	for i := 1; i <= 3; i++ {
		res, err := rl.Allow(ctx, id, 3, window)
		if err != nil {
			t.Fatalf("allow #%d failed: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("allow #%d: got blocked, want allowed (count=%d)", i, res.Count)
		}
	}

	res, err := rl.Allow(ctx, id, 3, window)
	if err != nil {
		t.Fatalf("4th allow failed: %v", err)
	}
	if res.Allowed {
		t.Fatalf("4th request must be blocked (count=%d)", res.Count)
	}
	// RetryAfter 必须落在 (0, window] 内 —— 返回 0 等于让客户端立刻重试。
	if res.RetryAfter <= 0 || res.RetryAfter > window {
		t.Fatalf("RetryAfter = %v, want in (0, %v]", res.RetryAfter, window)
	}
}

// TestRateLimiter_SharedAcrossInstances 是本次改动的**核心断言**。
//
// 两个独立的 RateLimiter 实例（模拟两个副本，各自持有独立连接）
// 必须共享同一份计数。若实现退化成进程内计数，本测试会红。
//
// 这正是要修的真缺口：进程内限流在多副本下让攻击者获得
// N 倍额度（N = 副本数），而服务本身不报任何错。
func TestRateLimiter_SharedAcrossInstances(t *testing.T) {
	cA := newTestClient(t)
	cB, err := New(Config{Addr: testAddr(), DialTimeout: time.Second})
	if err != nil {
		t.Fatalf("second client failed: %v", err)
	}
	t.Cleanup(func() { _ = cB.Close() })

	prefix := "agentbot:test:" + t.Name()
	rlA := NewRateLimiter(cA, prefix)
	rlB := NewRateLimiter(cB, prefix) // 独立客户端 = 模拟另一副本
	ctx := context.Background()
	id := "shared-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	window := time.Minute
	t.Cleanup(func() { _ = rlA.Reset(ctx, id, window) })

	// 副本 A 消耗 2 次额度。
	for i := 0; i < 2; i++ {
		res, err := rlA.Allow(ctx, id, 3, window)
		if err != nil {
			t.Fatalf("A allow failed: %v", err)
		}
		if !res.Allowed {
			t.Fatalf("A allow #%d unexpectedly blocked", i+1)
		}
	}

	// 副本 B 应看到 A 的计数：第 3 次放行、第 4 次拒绝。
	res, err := rlB.Allow(ctx, id, 3, window)
	if err != nil {
		t.Fatalf("B allow failed: %v", err)
	}
	if !res.Allowed {
		t.Fatal("B's 1st request should be allowed (count from A = 2, limit 3)")
	}
	res, err = rlB.Allow(ctx, id, 3, window)
	if err != nil {
		t.Fatalf("B allow failed: %v", err)
	}
	if res.Allowed {
		t.Fatal("B must see A's count: 4th request across replicas must be blocked")
	}
}

// TestRateLimiter_ZeroLimitMeansUnlimited 验证 0 额度 = 不限制。
//
// 若把 0 当成「一次都不许」，一个未配置的字段会变成全站 429 ——
// 比不限制严重得多。
func TestRateLimiter_ZeroLimitMeansUnlimited(t *testing.T) {
	c := newTestClient(t)
	rl := NewRateLimiter(c, "agentbot:test:"+t.Name())
	ctx := context.Background()

	for _, limit := range []int{0, -1} {
		res, err := rl.Allow(ctx, "anyone", limit, time.Minute)
		if err != nil {
			t.Fatalf("limit %d failed: %v", limit, err)
		}
		if !res.Allowed {
			t.Fatalf("limit %d must mean unlimited, got blocked", limit)
		}
	}
}

// TestRateLimiter_TTLSetOnlyOnce 验证 TTL 只在首次计数时设置。
//
// 若每次请求都设 TTL，键永不过期、计数只增不减 ——
// 症状是「用户被永久限流」，且窗口回溯后仍然限流。
// 这类 bug 在短窗口测试里不会暴露（测试跑不到窗口过期）。
func TestRateLimiter_TTLSetOnlyOnce(t *testing.T) {
	c := newTestClient(t)
	rl := NewRateLimiter(c, "agentbot:test:"+t.Name())
	ctx := context.Background()
	id := "ttl-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	window := time.Minute
	k := rl.key(id, window)
	t.Cleanup(func() { _, _ = c.Do("DEL", k) })

	if _, err := rl.Allow(ctx, id, 100, window); err != nil {
		t.Fatalf("first allow failed: %v", err)
	}
	v, err := c.Do("TTL", k)
	if err != nil {
		t.Fatalf("ttl failed: %v", err)
	}
	first := v.(int64)

	// 连打 20 次，TTL 不应被刷新（应随时间单调不增）。
	for i := 0; i < 20; i++ {
		if _, err := rl.Allow(ctx, id, 100, window); err != nil {
			t.Fatalf("allow #%d failed: %v", i, err)
		}
	}
	v, err = c.Do("TTL", k)
	if err != nil {
		t.Fatalf("ttl failed: %v", err)
	}
	second := v.(int64)
	if second > first {
		t.Fatalf("TTL grew from %d to %d — TTL is being refreshed on every request, "+
			"which makes the key never expire and the window never roll", first, second)
	}
}

// TestRateLimiter_ResetClearsWindow 验证重置。
func TestRateLimiter_ResetClearsWindow(t *testing.T) {
	c := newTestClient(t)
	rl := NewRateLimiter(c, "agentbot:test:"+t.Name())
	ctx := context.Background()
	id := "reset-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	window := time.Minute

	for i := 0; i < 5; i++ {
		if _, err := rl.Allow(ctx, id, 5, window); err != nil {
			t.Fatalf("allow failed: %v", err)
		}
	}
	res, err := rl.Allow(ctx, id, 5, window)
	if err != nil {
		t.Fatalf("allow failed: %v", err)
	}
	if res.Allowed {
		t.Fatal("expected to be blocked before reset")
	}

	if err := rl.Reset(ctx, id, window); err != nil {
		t.Fatalf("reset failed: %v", err)
	}
	res, err = rl.Allow(ctx, id, 5, window)
	if err != nil {
		t.Fatalf("allow after reset failed: %v", err)
	}
	if !res.Allowed {
		t.Fatal("reset must restore the full quota")
	}
}

// TestRateLimiter_PeekDoesNotConsume 验证 Peek 不消耗额度。
//
// 用 Allow 去探测会让观测行为本身推高计数，日志里的数字失真。
func TestRateLimiter_PeekDoesNotConsume(t *testing.T) {
	c := newTestClient(t)
	rl := NewRateLimiter(c, "agentbot:test:"+t.Name())
	ctx := context.Background()
	id := "peek-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	window := time.Minute
	t.Cleanup(func() { _ = rl.Reset(ctx, id, window) })

	if _, err := rl.Allow(ctx, id, 10, window); err != nil {
		t.Fatalf("allow failed: %v", err)
	}
	for i := 0; i < 5; i++ {
		n, err := rl.Peek(ctx, id, window)
		if err != nil {
			t.Fatalf("peek failed: %v", err)
		}
		if n != 1 {
			t.Fatalf("peek = %d, want 1 (peek must not increment)", n)
		}
	}
	// 缺键时 Peek 返回 0 而不是错误。
	n, err := rl.Peek(ctx, "never-seen-"+strconv.FormatInt(time.Now().UnixNano(), 10), window)
	if err != nil {
		t.Fatalf("peek on missing key failed: %v", err)
	}
	if n != 0 {
		t.Fatalf("peek on missing key = %d, want 0", n)
	}
}

// TestRateLimiter_DifferentPrefixesAreIsolated 验证用途之间不互相消耗。
//
// 键若不含用途前缀，登录失败会消耗 Agent 启动的额度 ——
// 一个攻击者可以借此让正常业务被限流。
func TestRateLimiter_DifferentPrefixesAreIsolated(t *testing.T) {
	c := newTestClient(t)
	base := "agentbot:test:" + t.Name()
	login := NewRateLimiter(c, base+":login")
	start := NewRateLimiter(c, base+":start")
	ctx := context.Background()
	id := "iso-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	window := time.Minute
	t.Cleanup(func() {
		_ = login.Reset(ctx, id, window)
		_ = start.Reset(ctx, id, window)
	})

	for i := 0; i < 3; i++ {
		if _, err := login.Allow(ctx, id, 3, window); err != nil {
			t.Fatalf("login allow failed: %v", err)
		}
	}
	res, err := login.Allow(ctx, id, 3, window)
	if err != nil {
		t.Fatalf("login allow failed: %v", err)
	}
	if res.Allowed {
		t.Fatal("login must be exhausted")
	}
	// 另一用途的额度必须完全不受影响。
	res, err = start.Allow(ctx, id, 3, window)
	if err != nil {
		t.Fatalf("start allow failed: %v", err)
	}
	if !res.Allowed {
		t.Fatal("start quota must be independent from login quota")
	}
}

// TestRevocationStore_SharedAcrossInstances 验证吊销跨副本可见。
//
// 这是 tokenBlacklist 那个进程内 map 的真缺口：
// A 副本登出后，B 副本必须立即拒绝同一凭据。
func TestRevocationStore_SharedAcrossInstances(t *testing.T) {
	cA := newTestClient(t)
	cB, err := New(Config{Addr: testAddr(), DialTimeout: time.Second})
	if err != nil {
		t.Fatalf("second client failed: %v", err)
	}
	t.Cleanup(func() { _ = cB.Close() })

	prefix := "agentbot:test:" + t.Name()
	storeA := NewRevocationStore(cA, prefix)
	storeB := NewRevocationStore(cB, prefix)
	ctx := context.Background()
	id := SubjectKey("user-1", strconv.FormatInt(time.Now().UnixNano(), 10))
	t.Cleanup(func() { _, _ = cA.Do("DEL", storeA.key(id)) })

	// 吊销前：两个副本都认为有效。
	for name, s := range map[string]*RevocationStore{"A": storeA, "B": storeB} {
		revoked, err := s.IsRevoked(ctx, id)
		if err != nil {
			t.Fatalf("%s isrevoked failed: %v", name, err)
		}
		if revoked {
			t.Fatalf("%s: must not be revoked before Revoke", name)
		}
	}

	// 只在 A 副本上吊销。
	if err := storeA.Revoke(ctx, id, time.Hour); err != nil {
		t.Fatalf("revoke failed: %v", err)
	}

	// B 副本必须立刻看到 —— 这就是跨副本生效的证据。
	revoked, err := storeB.IsRevoked(ctx, id)
	if err != nil {
		t.Fatalf("B isrevoked failed: %v", err)
	}
	if !revoked {
		t.Fatal("replica B must observe the revocation made on replica A; " +
			"if this fails the store is process-local and logout silently " +
			"does nothing on other replicas")
	}
}

// TestRevocationStore_RequiresPositiveTTL 验证无 TTL 被拒绝。
func TestRevocationStore_RequiresPositiveTTL(t *testing.T) {
	c := newTestClient(t)
	s := NewRevocationStore(c, "agentbot:test:"+t.Name())
	ctx := context.Background()

	for _, ttl := range []time.Duration{0, -time.Second} {
		if err := s.Revoke(ctx, "x", ttl); err == nil {
			t.Fatalf("ttl %v must be rejected (a permanent revocation record "+
				"outlives the credential it protects)", ttl)
		}
	}
	if err := s.Revoke(ctx, "", time.Hour); err == nil {
		t.Fatal("empty id must be rejected")
	}
}

// TestRevocationStore_EmptyIDIsNotRevoked 验证空 id 判定为未吊销。
//
// 返回 false 而不是 error：调用方在无 token 场景会传空串，
// 让它报错会把「未认证」变成「内部错误」。
func TestRevocationStore_EmptyIDIsNotRevoked(t *testing.T) {
	c := newTestClient(t)
	s := NewRevocationStore(c, "agentbot:test:"+t.Name())
	revoked, err := s.IsRevoked(context.Background(), "")
	if err != nil {
		t.Fatalf("empty id must not error: %v", err)
	}
	if revoked {
		t.Fatal("empty id must not be reported as revoked")
	}
}

// TestRevocationStore_DifferentSubjectsDoNotCollide 验证不同用户互不影响。
func TestRevocationStore_DifferentSubjectsDoNotCollide(t *testing.T) {
	c := newTestClient(t)
	s := NewRevocationStore(c, "agentbot:test:"+t.Name())
	ctx := context.Background()
	fp := strconv.FormatInt(time.Now().UnixNano(), 10)
	mine := SubjectKey("user-mine", fp)
	other := SubjectKey("user-other", fp)
	t.Cleanup(func() { _, _ = c.Do("DEL", s.key(mine)) })

	if err := s.Revoke(ctx, mine, time.Hour); err != nil {
		t.Fatalf("revoke failed: %v", err)
	}
	revoked, err := s.IsRevoked(ctx, other)
	if err != nil {
		t.Fatalf("isrevoked failed: %v", err)
	}
	if revoked {
		t.Fatal("revoking my credential must not revoke another user's credential")
	}
}
