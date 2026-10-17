package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/config"
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
func redisCfgForTest(t *testing.T) config.Config {
	t.Helper()
	cfg := config.DevelopmentDefault()
	cfg.Redis.Host = "127.0.0.1"
	cfg.Redis.Port = 6379
	// 用独立的 DB 编号隔离本次测试的键，避免与其他测试/手工验证互相污染。
	// 采用 DB 9（Redis 默认 16 个库，9 不常被占用）。
	cfg.Redis.DB = 9
	if !redisAvailable(t) {
		t.Skipf("redis unavailable at %s", redisTestAddr())
	}
	return cfg
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
	if out.AccessToken == oldToken {
		t.Fatal("re-login returned the same token; the test cannot distinguish " +
			"revocation-by-credential from revocation-by-user")
	}

	// 新 token 必须可用（不是按用户吊销）。
	if code := repA.doAuthed(t, http.MethodGet, probePath, out.AccessToken); code != http.StatusOK {
		t.Fatalf("a freshly issued token must work after logout, got %d — "+
			"revocation must key on the credential, not the user", code)
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
