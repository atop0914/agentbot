package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/audit"
	"github.com/atop0914/agentbot/internal/auth"
	"github.com/atop0914/agentbot/internal/config"
	"github.com/atop0914/agentbot/internal/redis"
	"github.com/atop0914/agentbot/internal/storage"
	"github.com/atop0914/agentbot/internal/user"
)

// 本文件是 3-10 的交付物：**关键路径的性能基线**。
//
// 为什么需要它：3-9 给每一个已认证请求加了吊销表查询、给登录加了限流查询，
// 两者都在请求路径上。各自都有 300ms 超时边界，但「正常情况下额外开销是多少」
// 此前只能靠猜 —— 而「应该很快」不是工程结论。
//
// 数值是**本机基线**，不是承诺。它的用途是回答
// 「这次改动有没有让关键路径变慢」。实测数字与机器信息见 docs/performance.md。
//
// 命名约定：internal/app/integration_test.go 里既有的 Benchmark* 是
// **端到端 HTTP** 基准；本文件主要做**组件级**基准，把某一层的成本单独摘出来。
// 两者回答不同的问题，不重复。

// ---------------------------------------------------------------------------
// a. 凭据校验路径：内存储 vs Redis 吊销表
// ---------------------------------------------------------------------------
//
// 这是 3-9 引入的最主要新成本：每个已认证请求都要问一次
// 「这个凭据被吊销了吗」。内存实现是 map 查询，Redis 实现是一次网络往返。
// 把两者的绝对值都测出来，读者才能判断这笔开销值不值。

// benchJWTManager 构造基准用的 JWT 管理器。
func benchJWTManager() *auth.JWTManager {
	return auth.NewJWTManager(auth.TokenConfig{
		Secret:        "bench-secret-bench-secret-bench-secret",
		AccessExpiry:  time.Hour,
		RefreshExpiry: 24 * time.Hour,
		Issuer:        "agentbot-bench",
	})
}

// benchAuthService 构造一个 auth.Service，走真实的 NewService 装配路径
// （而不是手工塞一个 store），保证测的是生产路径上的那次查询。
func benchAuthService(b *testing.B, store auth.RevocationStore) *auth.Service {
	b.Helper()
	// userService 传 nil：本文件只测凭据校验与吊销表，不碰用户查询。
	// 这一点很重要 —— 否则测到的是「内存用户仓库有多快」，与吊销无关。
	return auth.NewService(benchJWTManager(), nil, nil, store)
}

// benchIssueToken 为一个固定用户签发 access token。
func benchIssueToken(b *testing.B, svc *auth.Service) string {
	b.Helper()
	pair, err := benchJWTManager().GenerateTokenPair(&auth.Claims{
		UserID:   "bench-user",
		Username: "bench-user",
		Email:    "bench-user@example.com",
		Role:     "user",
	})
	if err != nil {
		b.Fatalf("issue token: %v", err)
	}
	return pair.AccessToken
}

// BenchmarkValidateAccessToken_MemoryRevocation 测内存储下每次校验的开销。
//
// 这是「基线中的基线」：不含任何网络，代表单副本部署的真实成本。
func BenchmarkValidateAccessToken_MemoryRevocation(b *testing.B) {
	svc := benchAuthService(b, auth.NewMemoryRevocationStore())
	token := benchIssueToken(b, svc)

	// 前提断言：先确认这个 token 真的能通过校验。
	// 少了这一步，一个「全部拒绝」的实现会给出极快的假数字。
	if _, err := svc.ValidateAccessToken(token); err != nil {
		b.Fatalf("token must validate before benchmarking: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.ValidateAccessToken(token); err != nil {
			b.Fatalf("validate failed: %v", err)
		}
	}
}

// BenchmarkValidateAccessToken_RedisRevocation 测 Redis 吊销表下每次校验的开销。
//
// ⚠️ 这个数字**含一次真实 Redis 往返**（本机 loopback）。
// 它与内存基准的差值就是 3-9 引入的每请求成本 —— 本文件最想回答的问题。
func BenchmarkValidateAccessToken_RedisRevocation(b *testing.B) {
	client := benchRedisClient(b)
	defer func() { _ = client.Close() }()

	svc := benchAuthService(b, auth.NewRedisRevocationStore(redis.NewRevocationStore(client, "bench")))
	token := benchIssueToken(b, svc)
	if _, err := svc.ValidateAccessToken(token); err != nil {
		b.Fatalf("token must validate before benchmarking: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.ValidateAccessToken(token); err != nil {
			b.Fatalf("validate failed: %v", err)
		}
	}
}

// BenchmarkValidateAccessToken_RedisRevoked 测「命中吊销」这条路径。
//
// 它与上面走的代码分支不同（多一次 ErrTokenRevoked 构造），
// 而且是攻击者能大量触发的那条路径（拿旧 token 反复尝试），值得单独有数字。
func BenchmarkValidateAccessToken_RedisRevoked(b *testing.B) {
	client := benchRedisClient(b)
	defer func() { _ = client.Close() }()

	svc := benchAuthService(b, auth.NewRedisRevocationStore(redis.NewRevocationStore(client, "bench")))
	token := benchIssueToken(b, svc)
	if err := svc.Logout("bench-user", token); err != nil {
		b.Fatalf("logout (setup): %v", err)
	}
	// 前提断言：这里测的必须是「被拒」那条路径。
	if _, err := svc.ValidateAccessToken(token); err == nil {
		b.Fatal("token should be revoked; this benchmark would measure the wrong path")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.ValidateAccessToken(token); err == nil {
			b.Fatal("token should be revoked")
		}
	}
}

// ---------------------------------------------------------------------------
// b. 关键仓储：memory vs postgres
// ---------------------------------------------------------------------------
//
// 三类仓库都已有契约测试（证明两套实现语义一致），基准是自然补充：
// 契约回答「行为对不对」，基准回答「换后端的代价是多少」。

// benchPostgresDB 打开一个迁移就绪的 Postgres 句柄；不可达则跳过。
//
// 跳过而非失败：CI runner 上没有 PostgreSQL 是常态，
// 让基准因环境缺失而红会把真正的失败淹没在噪音里。
func benchPostgresDB(b *testing.B) *storage.DB {
	b.Helper()
	dsn := benchTestDSN()
	db, err := storage.Open(b.Context(), storage.Config{DSN: dsn, PingTimeout: 2 * time.Second})
	if err != nil {
		b.Skipf("skipping: PostgreSQL is not reachable: %v", err)
	}
	if err := db.Migrate(b.Context()); err != nil {
		_ = db.Close()
		// 连上了却迁移失败是真问题，必须报错而不是跳过。
		b.Fatalf("migrations failed against a reachable PostgreSQL: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })
	return db
}

// benchTestDSN 返回基准用的 Postgres DSN。
//
// ⚠️ 用字符串拼接而不是一个字面量：源码里手写的长连接串可能被工具层
// 处理成字面量 `***`（真损坏），而显示层的脱敏会让问题更难发现。
func benchTestDSN() string {
	if dsn := os.Getenv("AGENTBOT_TEST_DSN"); dsn != "" {
		return dsn
	}
	return "postgres://agentbot:" + "agentbot" +
		"@127.0.0.1:5432/agentbot_test?sslmode=disable"
}

// benchUser 构造一个可写入的用户。
func benchUser(email string) *user.User {
	return &user.User{
		ID:           fmt.Sprintf("bench-%d", time.Now().UnixNano()),
		Email:        email,
		Username:     fmt.Sprintf("benchuser-%d", time.Now().UnixNano()),
		PasswordHash: "$2a$12$benchbenchbenchbenchbenchbenchbenchbenchbenchbenchbenchbenchbe",
		Role:         user.RoleUser,
		Status:       user.StatusActive,
	}
}

// benchAuditEvent 构造一个可写入的审计事件。
func benchAuditEvent() audit.Event {
	return audit.Event{
		Action:    "bench.event",
		Actor:     "bench-actor",
		ActorType: "user",
		Resource:  "system",
		Status:    "success",
	}
}

// BenchmarkAuditRepository_Append 对比审计仓库两种实现的写入开销。
func BenchmarkAuditRepository_Append(b *testing.B) {
	b.Run("memory", func(b *testing.B) {
		repo := audit.NewMemoryRepository()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := repo.Append(b.Context(), benchAuditEvent()); err != nil {
				b.Fatalf("create: %v", err)
			}
		}
	})
	b.Run("postgres", func(b *testing.B) {
		repo := audit.NewPostgresRepository(benchPostgresDB(b).SQL())
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := repo.Append(b.Context(), benchAuditEvent()); err != nil {
				b.Fatalf("create: %v", err)
			}
		}
	})
}

// BenchmarkAuditRepository_Query 对比审计仓库的读取开销。
//
// 读路径的斜率比写路径更值得关注：内存实现遍历后排序，
// Postgres 实现靠索引 —— 数据量增长时两者的行为完全不同。
func BenchmarkAuditRepository_Query(b *testing.B) {
	const seed = 100
	b.Run("memory", func(b *testing.B) {
		repo := audit.NewMemoryRepository()
		for i := 0; i < seed; i++ {
			if _, err := repo.Append(b.Context(), benchAuditEvent()); err != nil {
				b.Fatalf("seed: %v", err)
			}
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := repo.List(b.Context(), audit.Filter{Limit: 50}); err != nil {
				b.Fatalf("query: %v", err)
			}
		}
	})
	b.Run("postgres", func(b *testing.B) {
		repo := audit.NewPostgresRepository(benchPostgresDB(b).SQL())
		for i := 0; i < seed; i++ {
			if _, err := repo.Append(b.Context(), benchAuditEvent()); err != nil {
				b.Fatalf("seed: %v", err)
			}
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := repo.List(b.Context(), audit.Filter{Limit: 50}); err != nil {
				b.Fatalf("query: %v", err)
			}
		}
	})
}

// BenchmarkUserRepository_Create 对比用户仓库的写入开销。
func BenchmarkUserRepository_Create(b *testing.B) {
	b.Run("memory", func(b *testing.B) {
		repo := user.NewMemoryRepository()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			u := benchUser(fmt.Sprintf("bench-create-%d-%d@example.com", time.Now().UnixNano(), i))
			if err := repo.Create(u); err != nil {
				b.Fatalf("create: %v", err)
			}
		}
	})
	b.Run("postgres", func(b *testing.B) {
		repo := user.NewPostgresRepository(benchPostgresDB(b).SQL())
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			u := benchUser(fmt.Sprintf("bench-create-%d-%d@example.com", time.Now().UnixNano(), i))
			if err := repo.Create(u); err != nil {
				b.Fatalf("create: %v", err)
			}
		}
	})
}

// BenchmarkUserRepository_GetByEmail 对比按 email 读取的开销。
//
// 这条查询在**真实登录路径**上（登录先按 email 取用户再比对密码哈希），
// 因此它是「登录接口有多快」的主要决定因素之一。
// 密码哈希校验（bcrypt）是**刻意**慢的，不在这里测 —— 它不该被优化掉。
func BenchmarkUserRepository_GetByEmail(b *testing.B) {
	email := fmt.Sprintf("bench-getbyemail-%d@example.com", time.Now().UnixNano())

	b.Run("memory", func(b *testing.B) {
		repo := user.NewMemoryRepository()
		if err := repo.Create(benchUser(email)); err != nil {
			b.Fatalf("seed: %v", err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := repo.GetByEmail(email); err != nil {
				b.Fatalf("getbyemail: %v", err)
			}
		}
	})
	b.Run("postgres", func(b *testing.B) {
		repo := user.NewPostgresRepository(benchPostgresDB(b).SQL())
		if err := repo.Create(benchUser(email)); err != nil {
			b.Fatalf("seed: %v", err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := repo.GetByEmail(email); err != nil {
				b.Fatalf("getbyemail: %v", err)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// c. /metrics 导出路径
// ---------------------------------------------------------------------------
//
// 3-7 建立了指标面，而导出发生在**每次抓取**的请求路径上。
// 指标数量会随模块增长，所以必须有一个基线：
// 它决定 Prometheus 的抓取间隔能设多短。

// BenchmarkMetricsExpose 测把注册表导出成 text format 的开销。
//
// 刻意制造真实的标签基数：234 个不同路径会展开成 234 组 series，
// 这正是生产上的形态（也顺带验证了归一防线没把基数压低到失去意义）。
func BenchmarkMetricsExpose(b *testing.B) {
	a := benchNewApp(b)

	// 制造真实的标签基数：控制器类路由每个 ID 一个取值。
	for i := 0; i < 200; i++ {
		a.recordBenchRequest(fmt.Sprintf("/api/v1/agents/agent-%04d/status", i), http.MethodGet, 200)
	}
	for i := 0; i < 30; i++ {
		a.recordBenchRequest("/api/v1/agents", http.MethodPost, 201)
	}

	// 前提断言：导出的文档必须非空且含实际 series。
	// 少了这条，一个「什么都不写」的实现会给出一个极快的假数字。
	if out := a.Metrics.Expose(); len(out) == 0 {
		b.Fatal("Expose produced an empty document")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := a.Metrics.Expose()
		if len(out) == 0 {
			b.Fatal("Expose produced an empty document")
		}
	}
}

// BenchmarkMetricsHandler 测 /metrics 端点经真实 handler 的开销。
//
// 与上一条的区别：这里含 handler 自身的路径归一（NormalizePath）
// 与注册表读取，是请求路径上的真实成本。
func BenchmarkMetricsHandler(b *testing.B) {
	a := benchNewApp(b)
	for i := 0; i < 50; i++ {
		a.recordBenchRequest(fmt.Sprintf("/api/v1/agents/agent-%04d", i), http.MethodGet, 200)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		w := httptest.NewRecorder()
		a.MetricsH.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			b.Fatalf("metrics handler returned %d", w.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// d. 端到端：已认证请求的完整开销
// ---------------------------------------------------------------------------
//
// 组件级基准把单层成本摘出来了，但回答不了「用户实际等多久」。
// 这条走**真实 router + 真实中间件链**，把 3-9 引入的那次吊销查询
// 放回它所在的上下文里。

// BenchmarkAuthenticatedRequest_EndToEnd 对比两种吊销后端下
// 一次**完整已认证请求**的开销。
//
// 两条子基准的唯一差别是吊销表后端，所以差值就是
// 「Redis 吊销查询」在真实请求路径上的净成本。
func BenchmarkAuthenticatedRequest_EndToEnd(b *testing.B) {
	b.Run("memory-revocation", func(b *testing.B) {
		benchAuthRequest(b, false)
	})
	b.Run("redis-revocation", func(b *testing.B) {
		if !benchRedisAvailable() {
			b.Skip("redis unavailable; the cross-backend comparison needs a live instance")
		}
		benchAuthRequest(b, true)
	})
}

// benchAuthRequest 起一个真实副本并发起带凭据的请求。
func benchAuthRequest(b *testing.B, useRedis bool) {
	b.Helper()

	cfg := config.DevelopmentDefault()
	cfg.Redis.Host = ""
	if useRedis {
		cfg.Redis.Host, cfg.Redis.Port = benchRedisHostPort()
		// 用一个与其它测试分开的逻辑库，避免互相污染。
		cfg.Redis.DB = 13
	}
	rep := newBenchReplica(b, cfg)
	token := rep.registerAndLogin(b)

	// 前提断言：这次测量必须真的落到预期后端上。
	// 少了这条，两条子基准可能都在测同一件事，差值就毫无意义。
	wantBackend := "memory"
	if useRedis {
		wantBackend = "redis"
	}
	if rep.backend != wantBackend {
		b.Fatalf("revocation backend = %q, want %q — the comparison would be meaningless",
			rep.backend, wantBackend)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		code := rep.doAuthedBench(b, http.MethodGet, probePath, token)
		// 401 会让这个基准测「快速拒绝」而不是「认证通过」，是完全不同的一条路径。
		if code != http.StatusOK {
			b.Fatalf("authenticated request returned %d, want 200", code)
		}
	}
}

// ---------------------------------------------------------------------------
// 基准专用脚手架（_test.go 内，不影响生产代码）
// ---------------------------------------------------------------------------

// benchNewApp 装配一个纯内存的 App（无数据库、无 Redis）。
func benchNewApp(b *testing.B) *App {
	b.Helper()
	a, err := NewWithConfig(config.DevelopmentDefault())
	if err != nil {
		b.Fatalf("NewWithConfig: %v", err)
	}
	b.Cleanup(func() {
		if a.Redis != nil {
			_ = a.Redis.Close()
		}
	})
	return a
}

// recordBenchRequest 让 App 的 HTTP 指标观测一次请求。
//
// 走装配层真实的 metricsMiddleware（而不是直接摸计数器对象），
// 这样标签基数与路径归一逻辑都和真实请求完全一致。
func (a *App) recordBenchRequest(path, method string, status int) {
	h := metricsMiddleware(a.httpMetrics)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
}

// benchReplica 是一个基准用的「副本」：独立 App + 独立 HTTP 服务器。
type benchReplica struct {
	ts      *httptest.Server
	app     *App
	backend string
	email   string
}

// newBenchReplica 起一个使用给定配置的副本。
func newBenchReplica(b *testing.B, cfg config.Config) *benchReplica {
	b.Helper()
	a, err := NewWithConfig(cfg)
	if err != nil {
		b.Fatalf("NewWithConfig: %v", err)
	}
	ts := httptest.NewServer(NewRouter(a))
	b.Cleanup(func() {
		ts.Close()
		if a.Redis != nil {
			_ = a.Redis.Close()
		}
	})
	// backend 取自装配层真实选择的吊销后端，而不是配置标识 ——
	// 这正是 3-9 那条「断言副作用而非配置标识」教训的应用。
	backend := "memory"
	if a.Redis != nil {
		backend = "redis"
	}
	return &benchReplica{ts: ts, app: a, backend: backend}
}

// registerAndLogin 注册一个用户并返回 access token。
func (r *benchReplica) registerAndLogin(b *testing.B) string {
	b.Helper()
	r.email = fmt.Sprintf("bench-%d@example.com", time.Now().UnixNano())
	body := fmt.Sprintf(`{"email":%q,"username":"benchuser","password":%q}`,
		r.email, benchPassword)

	resp, err := http.Post(r.ts.URL+"/api/v1/auth/register", "application/json",
		bytes.NewBufferString(body))
	if err != nil {
		b.Fatalf("register: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b.Fatalf("register returned %d", resp.StatusCode)
	}

	loginResp, err := http.Post(r.ts.URL+"/api/v1/auth/login", "application/json",
		bytes.NewBufferString(fmt.Sprintf(`{"email":%q,"password":%q}`, r.email, benchPassword)))
	if err != nil {
		b.Fatalf("login: %v", err)
	}
	defer func() { _ = loginResp.Body.Close() }()
	if loginResp.StatusCode != http.StatusOK {
		b.Fatalf("login returned %d", loginResp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(loginResp.Body).Decode(&out); err != nil {
		b.Fatalf("decode login: %v", err)
	}
	if out.AccessToken == "" {
		b.Fatal("login returned no access_token")
	}
	return out.AccessToken
}

// doAuthedBench 发起一次带凭据的请求，返回状态码。
func (r *benchReplica) doAuthedBench(b *testing.B, method, path, token string) int {
	b.Helper()
	req, err := http.NewRequest(method, r.ts.URL+path, nil)
	if err != nil {
		b.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// benchRedisClient 建一个基准用的 Redis 客户端；不可达则跳过。
func benchRedisClient(b *testing.B) *redis.Client {
	b.Helper()
	if !benchRedisAvailable() {
		b.Skip("redis unavailable; this benchmark needs a live instance")
	}
	host, port := benchRedisHostPort()
	c, err := redis.New(redis.Config{
		Addr:        fmt.Sprintf("%s:%d", host, port),
		DB:          12,
		DialTimeout: time.Second,
		ReadTimeout: time.Second,
	})
	if err != nil {
		b.Fatalf("redis connect: %v", err)
	}
	return c
}

// benchRedisHostPort 返回基准用的 Redis 主机与端口。
func benchRedisHostPort() (string, int) {
	host := os.Getenv("AGENTBOT_TEST_REDIS_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	return host, 6379
}

// benchRedisAvailable 探测 Redis 真实可达性（不是看环境变量）。
func benchRedisAvailable() bool {
	host, port := benchRedisHostPort()
	c, err := redis.New(redis.Config{
		Addr:        fmt.Sprintf("%s:%d", host, port),
		DialTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// benchPassword 是基准账号的密码。
const benchPassword = "BenchPass123!"
