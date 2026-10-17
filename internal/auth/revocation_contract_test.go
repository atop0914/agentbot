package auth

import (
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/redis"
)

// 本文件是吊销表的**契约测试**：同一组断言跑在全部实现上
// （内存 + Redis）。这样做的价值在 3-4 那轮已经验证过 ——
// 断言也跑在**已有**的那套实现上，而此前从没有任何东西把两者
// 放在一起比较，于是已上线实现里的缺陷会当场浮出来。
//
// 两套实现必须语义一致，否则「换个后端」会让已经写下的吊销记录
// 全部失配，而服务不报任何错。

// revocationImpl 是一个被测实现的描述。
type revocationImpl struct {
	name string
	// newStore 返回一个全新的空表。
	newStore func(t *testing.T) RevocationStore
	// shared 为真表示同一份底层存储可以被两个 Store 实例共享
	// （用于验证跨副本可见性）。
	shared bool
	// newShared 返回两个共享同一底层存储的 Store。
	newShared func(t *testing.T) (RevocationStore, RevocationStore)
}

// revocationImpls 登记**全部**实现。
//
// 刻意不遗漏内存实现：它仍被生产回退路径使用，
// 不覆盖它就等于「回退路径只在出事那天才第一次被运行」。
func revocationImpls(t *testing.T) []revocationImpl {
	t.Helper()
	impls := []revocationImpl{
		{
			name:      "memory",
			newStore:  func(t *testing.T) RevocationStore { return NewMemoryRevocationStore() },
			shared:    false, // 内存实现无法跨实例共享 —— 这正是它的局限
			newShared: nil,
		},
	}

	if addr := redisTestAddr(); addr != "" {
		client, err := redis.New(redis.Config{Addr: addr, DialTimeout: time.Second})
		if err == nil {
			t.Cleanup(func() { _ = client.Close() })
			impls = append(impls, revocationImpl{
				name: "redis",
				newStore: func(t *testing.T) RevocationStore {
					// 每次用独立的键前缀，避免实现之间互相污染。
					return NewRedisRevocationStore(
						redis.NewRevocationStore(client, "agentbot:test:contract:"+t.Name()))
				},
				shared: true,
				newShared: func(t *testing.T) (RevocationStore, RevocationStore) {
					// 两个 Store 实例共享同一个 client —— 模拟两个副本
					// 连同一个 Redis。用共享 client 而不是各建一个：
					// 这样断言的是「状态在 Redis 里共享」，
					// 而不是「两个客户端恰好都连上了」。
					prefix := "agentbot:test:contract-shared:" +
						t.Name() + ":" + strconv.FormatInt(time.Now().UnixNano(), 10)
					return NewRedisRevocationStore(redis.NewRevocationStore(client, prefix)),
						NewRedisRevocationStore(redis.NewRevocationStore(client, prefix))
				},
			})
		} else {
			t.Logf("redis unavailable at %s, contract will only cover memory: %v", addr, err)
		}
	}
	return impls
}

// redisTestAddr 返回可用的 Redis 地址；空串表示不可用。
func redisTestAddr() string {
	addr := os.Getenv("AGENTBOT_TEST_REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	c, err := redis.New(redis.Config{Addr: addr, DialTimeout: 500 * time.Millisecond})
	if err != nil {
		return ""
	}
	_ = c.Close()
	return addr
}

// TestRevocationContract_NotRevokedByDefault 验证初始状态为未吊销。
//
// 反向断言同样要进契约：只断言正向路径（吊销后为真）会让
// 「初始就报已吊销」这种实现通过 —— 那会把所有用户全部登出。
func TestRevocationContract_NotRevokedByDefault(t *testing.T) {
	for _, impl := range revocationImpls(t) {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.newStore(t)
			revoked, err := s.IsRevoked("user-1", Fingerprint("some-token"))
			if err != nil {
				t.Fatalf("IsRevoked failed: %v", err)
			}
			if revoked {
				t.Fatal("a fresh store must not report anything as revoked")
			}
		})
	}
}

// TestRevocationContract_RevokeThenIsRevoked 验证吊销往返。
func TestRevocationContract_RevokeThenIsRevoked(t *testing.T) {
	for _, impl := range revocationImpls(t) {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.newStore(t)
			fp := Fingerprint("token-" + strconv.FormatInt(time.Now().UnixNano(), 10))

			if err := s.Revoke("user-1", fp, time.Hour); err != nil {
				t.Fatalf("Revoke failed: %v", err)
			}
			revoked, err := s.IsRevoked("user-1", fp)
			if err != nil {
				t.Fatalf("IsRevoked failed: %v", err)
			}
			if !revoked {
				t.Fatal("credential must be revoked after Revoke")
			}
		})
	}
}

// TestRevocationContract_EmptyFingerprintIsNotRevoked 验证空指纹的语义。
//
// 空指纹必须返回「未吊销」且不报错。它出现在「请求没带 token」的
// 场景里，让它报错会把一个正常的未认证请求变成 500。
// 反向断言：空指纹也**不得**被当成「吊销了一切」。
func TestRevocationContract_EmptyFingerprintIsNotRevoked(t *testing.T) {
	for _, impl := range revocationImpls(t) {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.newStore(t)

			revoked, err := s.IsRevoked("user-1", "")
			if err != nil {
				t.Fatalf("empty fingerprint must not error: %v", err)
			}
			if revoked {
				t.Fatal("empty fingerprint must not be reported as revoked")
			}

			// 空指纹也不能被写进表里。
			if err := s.Revoke("user-1", "", time.Hour); err == nil {
				t.Fatal("Revoke with empty fingerprint must be rejected")
			}
		})
	}
}

// TestRevocationContract_RejectsNonPositiveTTL 验证无 TTL 被拒。
//
// 永不过期的吊销记录会永久占用存储，而它保护的凭据早已过期。
func TestRevocationContract_RejectsNonPositiveTTL(t *testing.T) {
	for _, impl := range revocationImpls(t) {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.newStore(t)
			fp := Fingerprint("ttl-token")
			for _, ttl := range []time.Duration{0, -time.Second} {
				if err := s.Revoke("user-1", fp, ttl); err == nil {
					t.Fatalf("ttl %v must be rejected", ttl)
				}
			}
			// 被拒绝的写入不得留下任何痕迹。
			revoked, err := s.IsRevoked("user-1", fp)
			if err != nil {
				t.Fatalf("IsRevoked failed: %v", err)
			}
			if revoked {
				t.Fatal("a rejected Revoke must not have revoked anything")
			}
		})
	}
}

// TestRevocationContract_DifferentUsersDoNotCollide 验证用户间不串。
//
// 反向断言（别人的凭据不受影响）比正向断言更重要：串了会让
// 一个人登出把另一个人的会话也干掉。
func TestRevocationContract_DifferentUsersDoNotCollide(t *testing.T) {
	for _, impl := range revocationImpls(t) {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.newStore(t)
			fp := Fingerprint("shared-fingerprint")

			if err := s.Revoke("user-mine", fp, time.Hour); err != nil {
				t.Fatalf("Revoke failed: %v", err)
			}
			revoked, err := s.IsRevoked("user-other", fp)
			if err != nil {
				t.Fatalf("IsRevoked failed: %v", err)
			}
			if revoked {
				t.Fatal("revoking my credential must not revoke another user's credential")
			}
		})
	}
}

// TestRevocationContract_DifferentCredentialsDoNotCollide 验证凭据间不串。
//
// 这是「按凭据指纹而非按用户吊销」的核心：同一个用户的旧 token
// 被吊销，不得影响他刚签发的新 token。
func TestRevocationContract_DifferentCredentialsDoNotCollide(t *testing.T) {
	for _, impl := range revocationImpls(t) {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.newStore(t)
			oldFP := Fingerprint("old-token")
			newFP := Fingerprint("new-token")

			if err := s.Revoke("user-1", oldFP, time.Hour); err != nil {
				t.Fatalf("Revoke failed: %v", err)
			}
			revoked, err := s.IsRevoked("user-1", newFP)
			if err != nil {
				t.Fatalf("IsRevoked failed: %v", err)
			}
			if revoked {
				t.Fatal("revoking the old token must not revoke a freshly issued token " +
					"(otherwise multi-device login breaks)")
			}
		})
	}
}

// TestRevocationContract_SharedAcrossInstances 验证跨副本可见。
//
// 只对声明了 shared 的实现运行。内存实现**无法**通过这一条 ——
// 那不是测试缺陷，而是它作为多副本方案的固有限制：
// 本测试的存在就是为了把这一点写成可执行的断言，
// 而不是留一句注释。
func TestRevocationContract_SharedAcrossInstances(t *testing.T) {
	for _, impl := range revocationImpls(t) {
		if !impl.shared {
			t.Logf("impl %s does not support cross-instance sharing "+
				"(it is process-local by definition) — skipping", impl.name)
			continue
		}
		t.Run(impl.name, func(t *testing.T) {
			a, b := impl.newShared(t)
			fp := Fingerprint("cross-replica-token")

			if err := a.Revoke("user-1", fp, time.Hour); err != nil {
				t.Fatalf("Revoke on A failed: %v", err)
			}
			revoked, err := b.IsRevoked("user-1", fp)
			if err != nil {
				t.Fatalf("IsRevoked on B failed: %v", err)
			}
			if !revoked {
				t.Fatal("replica B must observe the revocation made on replica A")
			}
		})
	}
}

// TestRevocationContract_MemoryIsNotShareable 把内存实现的局限写成断言。
//
// 这是一个**反向**契约：内存实现跨实例**不可见**。
// 写它的理由是让「内存实现在多副本下不够用」这件事成为
// 可执行的文档 —— 将来若有人为了省事在多副本部署里用它，
// 这条测试就是说明为什么不该这么做的地方。
func TestRevocationContract_MemoryIsNotShareable(t *testing.T) {
	a := NewMemoryRevocationStore()
	b := NewMemoryRevocationStore()
	fp := Fingerprint("local-only")

	if err := a.Revoke("user-1", fp, time.Hour); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}
	revoked, err := b.IsRevoked("user-1", fp)
	if err != nil {
		t.Fatalf("IsRevoked failed: %v", err)
	}
	if revoked {
		t.Fatal("memory store must NOT share state across instances; " +
			"if this ever returns true, the semantics changed and " +
			"the Redis implementation's reason for existing is gone")
	}
}

// TestFingerprint_StableAndDistinct 验证指纹函数。
//
// 指纹是唯一入口，它若不稳（同一 token 算出不同值），
// 症状是「吊销写了、校验查不到」—— 一个看起来完全正常的登出。
func TestFingerprint_StableAndDistinct(t *testing.T) {
	a := Fingerprint("token-a")
	if a != Fingerprint("token-a") {
		t.Fatal("Fingerprint must be deterministic")
	}
	if a == Fingerprint("token-b") {
		t.Fatal("different tokens must produce different fingerprints")
	}
	if len(a) != 64 {
		t.Fatalf("sha256 hex must be 64 chars, got %d", len(a))
	}
}

// TestMemoryRevocationStore_ExpiresAndIsPruned 验证内存实现的惰性过期。
//
// 内存实现没有 Redis 的 TTL，必须自己在读路径做过期 ——
// 否则一个长期运行的进程会让吊销表无限增长。
func TestMemoryRevocationStore_ExpiresAndIsPruned(t *testing.T) {
	s := NewMemoryRevocationStore()
	fp := Fingerprint("short-lived")

	// 1ms 的 TTL，随后睡过它。
	if err := s.Revoke("user-1", fp, time.Millisecond); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	revoked, err := s.IsRevoked("user-1", fp)
	if err != nil {
		t.Fatalf("IsRevoked failed: %v", err)
	}
	if revoked {
		t.Fatal("an expired revocation must read as not revoked")
	}

	// 读路径必须顺手清掉它，否则表无限增长。
	s.mu.RLock()
	n := len(s.revoked)
	s.mu.RUnlock()
	if n != 0 {
		t.Fatalf("expired entry must be pruned on read, %d entries remain", n)
	}
}

// TestRedisRevocationStore_UnavailableIsDistinctError 验证存储故障
// 与「未吊销」可区分。
//
// 这是本次改动最重要的一条安全断言：把「查不到」当成「没被吊销」，
// 会让一次存储抖动等于「所有已登出的 token 全部复活」——
// 比拒绝服务更糟，因为它静默地撤销了用户的安全动作。
func TestRedisRevocationStore_UnavailableIsDistinctError(t *testing.T) {
	// 指向一个保留地址：连接被立即拒绝，不必等 DNS 超时。
	client, err := redis.New(redis.Config{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond})
	if err == nil {
		_ = client.Close()
		t.Fatal("expected connecting to 127.0.0.1:1 to fail; " +
			"the test premise (unreachable address) does not hold")
	}

	// 用一条已失效的连接模拟运行期故障：先建连成功后关闭。
	realClient, err := redis.New(redis.Config{Addr: redisTestAddr(), DialTimeout: time.Second})
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	s := NewRedisRevocationStore(redis.NewRevocationStore(realClient, "agentbot:test:unavail"))
	_ = realClient.Close() // 关闭后所有命令都会失败

	revoked, err := s.IsRevoked("user-1", Fingerprint("token"))
	if err == nil {
		t.Fatal("a closed connection must produce an error, not a clean 'not revoked'")
	}
	if revoked {
		t.Fatal("'unknown' must not be reported as 'revoked' either")
	}
	if !errors.Is(err, ErrRevocationUnavailable) {
		t.Fatalf("got %v, want ErrRevocationUnavailable so callers can tell "+
			"'confirmed not revoked' apart from 'could not check'", err)
	}
}
