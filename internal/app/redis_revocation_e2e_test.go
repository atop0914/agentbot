package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/auth"
	"github.com/atop0914/agentbot/internal/config"
	"github.com/atop0914/agentbot/internal/redis"
)

// 本文件是 3-9 的**决定性验收**：跨副本的凭据吊销。
//
// 为什么必须是端到端 + 两个真实 App 实例：
//   - 单测直接调 store.Revoke 只能证明「存储能共享」，
//     证明不了「装配层真的把共享存储接给了 auth.Service」——
//     本仓库已有先例（registerTenantOwnership 写了但从未被调用，
//     单测永远发现不了）。
//   - 一个 App 实例内部测不出跨副本问题：那正是进程内 map
//     曾经「看起来能用」的原因。
//
// 断言落在**真实 HTTP 请求的状态码**上，不看任何配置标识。

// replicaEnv 是一个「副本」：独立配置、独立 App、独立 HTTP 服务器。
type replicaEnv struct {
	ts    *httptest.Server
	app   *App
	token string
	email string
}

// probePath 是用于判定「凭据是否仍被接受」的端点。
//
// 选它的理由（实测得到，不是猜的）：
//   - 它在权限表里登记为 **Public 且无 Action**，因此一个刚注册、
//     没有任何角色的用户也能通过授权层；
//   - 但它**仍经过认证中间件**：不带/带了无效 token 会得到 401。
//
// 反例：用 /api/v1/agents 做探针会得到 **403**（无角色），
// 于是「登出后变 401」的断言无法与「本来就没权限」区分开来 ——
// 第一版就是这么写的，跑出来是红的，错的是测试而不是实现。
const probePath = "/api/v1/authorizations/me"

// newRedisReplica 起一个使用 Redis 后端的副本。
//
// 两个副本共用同一份 cfg（同一个 Redis 地址/DB），
// 因此它们共享吊销表 —— 这正是被测的部署形态。
func newRedisReplica(t *testing.T, cfg config.Config) *replicaEnv {
	t.Helper()
	a, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	ts := httptest.NewServer(NewRouter(a))
	t.Cleanup(func() {
		ts.Close()
		if a.Redis != nil {
			_ = a.Redis.Close()
		}
	})
	return &replicaEnv{ts: ts, app: a}
}

// register 在副本上注册一个用户并返回 access token。
func (r *replicaEnv) register(t *testing.T) {
	t.Helper()
	r.email = fmt.Sprintf("replica-%d@example.com", time.Now().UnixNano())
	body := fmt.Sprintf(`{"email":%q,"username":"replicauser","password":"Str0ngPass!123"}`,
		r.email)
	resp, err := http.Post(r.ts.URL+"/api/v1/auth/register", "application/json",
		bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("register status = %d, want 201: %s", resp.StatusCode, b)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode register: %v", err)
	}
	if out.AccessToken == "" {
		t.Fatal("register response has no access_token")
	}
	r.token = out.AccessToken
}

// doAuthed 用一个显式 token 发一次带认证的请求。
//
// 刻意不存 token 字段以外的状态：要把**副本 A 签发的 token**
// 拿到**副本 B** 上使用，才能验证吊销是否跨副本生效。
func (r *replicaEnv) doAuthed(t *testing.T, method, path, token string) int {
	t.Helper()
	req, err := http.NewRequest(method, r.ts.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// redisCfgForTest 返回一个指向本机 Redis 的开发配置。
//
// DB 编号按测试名哈希分配（范围 1..15），并且**在返回前清空该库**。
//
// 为什么必须清空，而不是「靠哈希避免碰撞」：
// 哈希只能分散测试，不能保证互斥 —— 实测就有三个测试
// （Body/NotWired/Config）同时落到 DB 6，于是前一个测试
// 打满的计数让后一个测试**第一次请求就 429**。
// 把「测试从干净状态开始」做成 setup 的一部分，
// 比寄希望于哈希分散可靠得多。
//
// 为什么需要独立的库而不是同一个库 + 唯一键前缀：
// 限流键含 `login:ip:127.0.0.1` 这个与测试无关的固定维度，
// 而 httptest 的 RemoteAddr 恒为 127.0.0.1 —— 同库时它必然冲突。
func redisCfgForTest(t *testing.T) config.Config {
	t.Helper()
	cfg := config.DevelopmentDefault()
	cfg.Redis.Host = "127.0.0.1"
	cfg.Redis.Port = 6379

	// 1..15：Redis 默认 16 个库，避开 0（常被手工验证占用）。
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.Name()))
	cfg.Redis.DB = 1 + int(h.Sum32()%15)

	if !redisAvailable(t) {
		t.Skipf("redis unavailable at %s", redisTestAddr())
	}

	// 清空该库，保证测试从已知状态开始。
	flushRedisDB(t, cfg)
	return cfg
}

// flushRedisDB 清空指定逻辑库。
//
// 用一个独立的短连接：调用方（NewWithConfig）会另建自己的连接，
// 复用同一个客户端会在装配前就持有连接池，反而让所有权变复杂。
func flushRedisDB(t *testing.T, cfg config.Config) {
	t.Helper()
	c, err := redis.New(redis.Config{
		Addr:        cfg.Redis.Addr(),
		DB:          cfg.Redis.DB,
		DialTimeout: time.Second,
		ReadTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("redis connect for flush (db %d): %v", cfg.Redis.DB, err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Do("FLUSHDB"); err != nil {
		t.Fatalf("flushdb (db %d): %v", cfg.Redis.DB, err)
	}
	// 再读一次确认真的空了 —— FLUSHDB 若悄悄失败，
	// 测试会以「429 at attempt 1」这种极具误导性的方式失败。
	n, err := c.Do("DBSIZE")
	if err != nil {
		t.Fatalf("dbsize after flush: %v", err)
	}
	if size, ok := n.(int64); !ok || size != 0 {
		t.Fatalf("db %d not empty after FLUSHDB (dbsize=%v) — leftover state "+
			"will make this test fail misleadingly", cfg.Redis.DB, n)
	}
}

// TestRedisRevocation_LogoutOnReplicaAIsEnforcedOnReplicaB 是本轮的核心验收。
//
// 场景：用户在副本 A 登出，随后用同一个 token 访问副本 B。
//
//	期望：401（吊销跨副本生效）
//
// 这正是进程内 map 做不到的事 —— 那个实现下副本 B 会返回 200，
// 用户以为已登出，实际凭据仍然可用直到自然过期（默认 1h）。
func TestRedisRevocation_LogoutOnReplicaAIsEnforcedOnReplicaB(t *testing.T) {
	cfg := redisCfgForTest(t)

	repA := newRedisReplica(t, cfg)
	repB := newRedisReplica(t, cfg)

	if repA.app.RedisBackend != redisBackendRedis || repB.app.RedisBackend != redisBackendRedis {
		t.Fatalf("both replicas must use the redis backend, got A=%q B=%q",
			repA.app.RedisBackend, repB.app.RedisBackend)
	}

	// 在 A 上注册并拿到 token。
	repA.register(t)

	// 前置断言：B 副本此刻**接受**这个 token。
	// 若 B 一开始就拒绝，下面「登出后变 401」就没有证明力 ——
	// 那可能只是 token 本身不被 B 认可。
	if code := repB.doAuthed(t, http.MethodGet, probePath, repA.token); code != http.StatusOK {
		t.Fatalf("precondition failed: replica B rejected a valid token with %d, "+
			"want 200 — the cross-replica assertion below would be meaningless", code)
	}

	// 在 A 副本上登出。
	req, err := http.NewRequest(http.MethodPost, repA.ts.URL+"/api/v1/auth/logout", nil)
	if err != nil {
		t.Fatalf("build logout request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+repA.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d, want 200", resp.StatusCode)
	}

	// 同源断言：A 自己必须拒绝（证明登出至少在自己身上生效）。
	if code := repA.doAuthed(t, http.MethodGet, probePath, repA.token); code != http.StatusUnauthorized {
		t.Fatalf("replica A must reject the revoked token after logout, got %d", code)
	}

	// **核心断言**：B 副本也必须拒绝。
	if code := repB.doAuthed(t, http.MethodGet, probePath, repA.token); code != http.StatusUnauthorized {
		t.Fatalf("replica B returned %d for a token revoked on replica A, want 401 — "+
			"revocation is not shared across replicas, so logout silently does nothing "+
			"on other replicas", code)
	}
}

// TestRedisRevocation_MemoryBackendDoesNotShareAcrossReplicas 是对照组。
//
// 这是「缺口真实存在」的证据：同样的场景下，内存后端**不会**共享吊销，
// 副本 B 返回 200。
//
// 为什么这条测试必须存在：它让「本轮的改动到底修了什么」成为可执行的事实。
// 若没有它，「跨副本吊销」的说法只是一个假设 —— 而本仓库的教训正是
// 「不要断言一个不存在的契约」。有了这条对照，上一条断言才有意义。
func TestRedisRevocation_MemoryBackendDoesNotShareAcrossReplicas(t *testing.T) {
	// 不配 Redis：两个副本各自用进程内吊销表。
	cfg := config.DevelopmentDefault()
	// Default() 里 Redis.Host = "localhost"，DevelopmentDefault 继承它。
	// 要得到「未配置 Redis」的形态必须显式清空 ——
	// 这也是本轮改动的一个副作用：以前没人消费这个字段，
	// 现在它会让 NewWithConfig 真的去连 Redis。
	cfg.Redis.Host = ""
	cfg.Redis.Password = ""
	if cfg.Redis.Configured() {
		t.Fatalf("test premise broken: host still set to %q", cfg.Redis.Host)
	}

	repA := newRedisReplica(t, cfg)
	repB := newRedisReplica(t, cfg)

	if repA.app.RedisBackend != redisBackendMemory {
		t.Fatalf("backend = %q, want memory", repA.app.RedisBackend)
	}

	repA.register(t)

	// 登出前：B 接受。
	if code := repB.doAuthed(t, http.MethodGet, probePath, repA.token); code != http.StatusOK {
		t.Fatalf("precondition: B rejected a fresh token with %d", code)
	}

	// A 登出。
	req, err := http.NewRequest(http.MethodPost, repA.ts.URL+"/api/v1/auth/logout", nil)
	if err != nil {
		t.Fatalf("build logout request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+repA.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	_ = resp.Body.Close()

	// A 拒绝（进程内生效）。
	if code := repA.doAuthed(t, http.MethodGet, probePath, repA.token); code != http.StatusUnauthorized {
		t.Fatalf("replica A must reject after its own logout, got %d", code)
	}

	// **对照断言**：B 仍然接受 —— 这就是进程内实现的安全缺口。
	code := repB.doAuthed(t, http.MethodGet, probePath, repA.token)
	if code != http.StatusOK {
		t.Fatalf("memory backend returned %d on replica B, want 200 — "+
			"if this changed, update the docs/redis.md claim that per-replica "+
			"state makes logout ineffective across replicas", code)
	}
	t.Logf("confirmed: with the memory backend, replica B still returns 200 "+
		"for a token revoked on replica A (code=%d) — the gap this task closes", code)
}

// TestRedisRevocation_SurvivesReplicaRestart 验证吊销记录不随进程消失。
//
// 重启一个副本后，它必须仍然拒绝已被吊销的凭据 ——
// 内存实现在这里必然失败（新进程的新 map 是空的）。
func TestRedisRevocation_SurvivesReplicaRestart(t *testing.T) {
	cfg := redisCfgForTest(t)

	repA := newRedisReplica(t, cfg)
	repA.register(t)
	token := repA.token

	// 登出（写入共享吊销表）。
	req, err := http.NewRequest(http.MethodPost, repA.ts.URL+"/api/v1/auth/logout", nil)
	if err != nil {
		t.Fatalf("build logout request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	_ = resp.Body.Close()

	// 起一个**全新**的副本（模拟重启/新 Pod）。
	repC := newRedisReplica(t, cfg)

	// 用户数据不存在（内存用户仓库）无妨：断言的是「凭据被拒」，
	// 而拒绝发生在认证层，早于任何业务逻辑。
	if code := repC.doAuthed(t, http.MethodGet, probePath, token); code != http.StatusUnauthorized {
		t.Fatalf("a freshly started replica returned %d, want 401 — "+
			"revocation must outlive the process that wrote it", code)
	}
}

// TestRedisRevocation_NewLoginAfterLogoutStillWorks 验证登出不误伤新会话。
//
// 反向断言：吊销是按凭据（token 指纹）而不是按用户。
// 若按用户吊销，登出后重新登录拿到的**新** token 也会被拒 ——
// 那会让用户陷入「登出即永久无法访问」。
func TestRedisRevocation_NewLoginAfterLogoutStillWorks(t *testing.T) {
	cfg := redisCfgForTest(t)

	repA := newRedisReplica(t, cfg)
	repA.register(t)
	oldToken := repA.token
	email := repA.email

	// 登出旧 token。
	req, err := http.NewRequest(http.MethodPost, repA.ts.URL+"/api/v1/auth/logout", nil)
	if err != nil {
		t.Fatalf("build logout request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+oldToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	_ = resp.Body.Close()

	// 旧 token 必须被拒。
	if code := repA.doAuthed(t, http.MethodGet, probePath, oldToken); code != http.StatusUnauthorized {
		t.Fatalf("old token must be revoked, got %d", code)
	}

	// 重新登录拿新 token。
	//
	// ⚠️ **必须等到下一个整秒再登录**，否则两条断言都会失去意义。
	// JWT 的 iat/exp 是秒级精度，且 payload 里没有 jti（见
	// internal/auth/jwt.go 的 GenerateTokenPair），所以同一秒内两次登录
	// 产出的是**字节完全相同**的 token —— 指纹相同，于是这个「新」token
	// 命中的正是旧 token 的吊销记录，请求被 401 拒掉。
	//
	// 实测（本次会话）：同秒两次 GenerateTokenPair 的 access token
	// 逐字节相等，Fingerprint 都是 919179f391de00cd。
	//
	// 这不是实现的 bug：按「凭据指纹」吊销是正确语义，而两个字节相同的
	// token 本来就是同一个凭据。是**测试的前提不成立** ——
	// 它假设「重新登录必然得到新凭据」，而同秒内这个假设为假。
	waitForNextSecond(t)

	body := fmt.Sprintf(`{"email":%q,"password":"Str0ngPass!123"}`, email)
	loginResp, err := http.Post(repA.ts.URL+"/api/v1/auth/login", "application/json",
		bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer func() { _ = loginResp.Body.Close() }()
	if loginResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(loginResp.Body)
		t.Fatalf("re-login status = %d, want 200: %s", loginResp.StatusCode, b)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(loginResp.Body).Decode(&out); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if out.AccessToken == "" {
		t.Fatal("re-login returned no access_token")
	}
	// 前提断言：等过一个整秒之后，这个 token 必须真的是**另一个凭据**。
	// 若它仍与旧 token 逐字节相同，说明等待没生效 ——
	// 那种情况下下面的 200 断言会以「吊销按用户生效」这种完全错误的
	// 结论失败（首版就是这么误导的），所以这里先把前提钉住。
	if out.AccessToken == oldToken {
		t.Fatal("re-login returned a byte-identical token even after waiting " +
			"for the next second — the credential-identity premise does not " +
			"hold, so the assertions below cannot distinguish " +
			"revocation-by-credential from revocation-by-user")
	}

	// 新 token 必须可用（不是按用户吊销）。
	//
	// 上面已经钉住「这是一个不同的凭据」这个前提，所以这里的 200
	// 确实能区分「按凭据吊销」与「按用户吊销」。
	if code := repA.doAuthed(t, http.MethodGet, probePath, out.AccessToken); code != http.StatusOK {
		t.Fatalf("a freshly issued token must work after logout, got %d — "+
			"revocation must key on the credential, not the user", code)
	}
}

// waitForNextSecond 阻塞到下一个整秒（秒值改变）为止，最坏约 1s。
//
// 存在的理由见 TestRedisRevocation_NewLoginAfterLogoutStillWorks：
// 该测试需要「两次登录落在不同的秒」才能拿到不同指纹的凭据。
// 用固定 sleep 也行，但这个写法在毫秒层面是精确的，且不浪费整秒。
func waitForNextSecond(t *testing.T) {
	t.Helper()
	start := time.Now().Unix()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Unix() == start {
		if time.Now().After(deadline) {
			t.Fatal("wall clock did not advance past the current second in 3s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRedisRevocation_RevocationIsPerCredentialNotPerUser 是上一条的
// 服务层补充断言，避开「同秒 token 相同」这个测试噪音。
//
// 直接构造两个不同指纹，验证吊销其一不影响其二。
func TestRedisRevocation_RevocationIsPerCredentialNotPerUser(t *testing.T) {
	cfg := redisCfgForTest(t)

	// 从装配路径取到吊销表（走真实 redis_wiring 决策表，不是手工构造）。
	sel, err := selectRedis(t.Context(), cfg)
	if err != nil {
		t.Fatalf("selectRedis: %v", err)
	}
	if sel.client != nil {
		defer func() { _ = sel.client.Close() }()
	}

	const userID = "user-same"
	oldFP := auth.Fingerprint("old-token-" + t.Name())
	newFP := auth.Fingerprint("new-token-" + t.Name())

	if err := sel.revocations.Revoke(userID, oldFP, time.Hour); err != nil {
		t.Fatalf("revoke failed: %v", err)
	}
	// 旧凭据：已吊销。
	revoked, err := sel.revocations.IsRevoked(userID, oldFP)
	if err != nil {
		t.Fatalf("isrevoked failed: %v", err)
	}
	if !revoked {
		t.Fatal("the revoked credential must read as revoked")
	}
	// 同一用户的新凭据：必须**不受影响**。
	revoked, err = sel.revocations.IsRevoked(userID, newFP)
	if err != nil {
		t.Fatalf("isrevoked failed: %v", err)
	}
	if revoked {
		t.Fatal("revoking one credential revoked another credential of the " +
			"same user — revocation must key on the credential, not the user")
	}
}

// TestRedis_StartupLogsBackend 验证启动日志写出后端标识。
//
// 部署时「吊销状态存在哪」只能从这行日志回答，不能靠猜。
func TestRedis_StartupLogsBackend(t *testing.T) {
	cfg := redisCfgForTest(t)
	// 通过 selectRedis + redisHostForLog 检查启动日志会用到的字段，
	// 避免为了断言日志而改动生产装配路径。
	sel, err := selectRedis(t.Context(), cfg)
	if err != nil {
		t.Fatalf("selectRedis: %v", err)
	}
	if sel.client != nil {
		defer func() { _ = sel.client.Close() }()
	}
	if sel.backend != redisBackendRedis {
		t.Fatalf("backend = %q, want redis", sel.backend)
	}
	// 日志位置字段必须不含密码。
	if got := redisHostForLog(cfg); got != "127.0.0.1:6379/9" {
		t.Fatalf("redisHostForLog = %q", got)
	}
}
