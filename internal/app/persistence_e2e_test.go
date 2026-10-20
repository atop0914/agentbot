package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/atop0914/agentbot/internal/authz"
	"github.com/atop0914/agentbot/internal/config"
	"github.com/atop0914/agentbot/internal/storage"
)

// authzSubject 构造授权主体，与 integration_test.go 里的写法一致。
func authzSubject(userID string) authz.Subject {
	return authz.Subject{Type: authz.SubjectUser, ID: userID}
}

// persistence_e2e_test.go 补上 3-3 缺失的那一环。
//
// 3-3 交付了「后端选择」：数据库连上了、迁移跑了、启动日志写着
// storage.backend=postgres，单测也全绿 —— 但真机验证里
// 「注册用户 → 201」之后 users 表行数是 0，因为当时没有任何仓库
// 真的改用 Postgres。
//
// 那次教训是：**断言配置或标识，证明不了数据落在哪里**。
// 本文件的断言刻意全部落在「真实 HTTP 请求产生的副作用」上：
//
//  1. 经真实 router 注册用户 → 用 SQL 直接查那一行（不是查服务、不是查标识）
//  2. 重启（重建 App 与连接）→ 同一账号仍能登录（证明真的持久化了，
//     而不是活在进程的内存里）
//  3. 审计事件同样落库：经真实请求产生的事件能在 audit_events 里查到
//
// 这三条都不看 a.StorageBackend 这类字段 —— 那正是 3-3 犯过的错。

// requirePersistedDB 打开一个指向真机 PG 的连接并清空相关表。
// 连不上则跳过（CI 常态），连上了清理失败则 fatal（真问题）。
func requirePersistedDB(t *testing.T) *sql.DB {
	t.Helper()

	cfg := withDatabase(t) // 内部已做真实可达性探测，不可达时 skip

	handle, err := storage.Open(context.Background(), storage.Config{DSN: cfg.Database.DSN()})
	if err != nil {
		t.Skipf("skipping: PostgreSQL is not reachable: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	if err := handle.Migrate(context.Background()); err != nil {
		t.Fatalf("migrations failed against a reachable PostgreSQL: %v", err)
	}
	return handle.SQL()
}

// newPersistentApp 用「配置了数据库」的配置装配一个 App，
// 并返回它对应的 httptest 服务器。
//
// 刻意不复用 newTestEnv：那个用的是 New()（无数据库的内存装配），
// 拿它测持久化等于什么都没测 —— 这正是要避免的那类「测试看起来跑过了」。
func newPersistentApp(t *testing.T, cfg config.Config) (*httptest.Server, *App) {
	t.Helper()

	a, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig() error = %v", err)
	}
	// App 没有 Close()：连接池由 App.Storage 持有，调用方负责关闭。
	t.Cleanup(func() {
		if a.Storage != nil {
			_ = a.Storage.Close()
		}
	})

	// 反向断言：带数据库的配置**必须**真的选上 postgres 后端。
	// 若这里退化成 memory，后面的「数据落库」断言会以晦涩的方式失败，
	// 不如在入口处就响亮地说清楚原因。
	if a.StorageBackend != storage.BackendPostgres {
		t.Fatalf("StorageBackend = %q, want %q (the fixture claims a database)",
			a.StorageBackend, storage.BackendPostgres)
	}

	ts := httptest.NewServer(NewRouter(a))
	t.Cleanup(ts.Close)
	// 必须把 App 一并返回：测试要往**同一个** App 上授予角色。
	// 另建一个 App 去授权是无效的 —— authz 是进程内状态，
	// 服务请求的那个 App 不会看到另一个实例的授权。
	return ts, a
}

// registerUser 经真实注册路由建一个用户，返回用户 ID 与 token。
func registerUser(t *testing.T, baseURL string, email string) (string, string) {
	t.Helper()

	body := fmt.Sprintf(
		`{"email":%q,"username":%q,"password":"Str0ngPass!123"}`,
		email, "u"+fmt.Sprint(time.Now().UnixNano()))
	resp, err := http.Post(baseURL+"/api/v1/auth/register", "application/json",
		bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d, want 201", resp.StatusCode)
	}

	var reg struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if reg.User.ID == "" {
		t.Fatal("register response has no user id")
	}
	return reg.User.ID, reg.AccessToken
}

func TestPersistence_RegisteredUserLandsInPostgres(t *testing.T) {
	db := requirePersistedDB(t)
	// users 表必须是空的，否则「查到了那一行」可能只是上一轮的残留。
	// 这条前置检查本身就是断言的一部分：没有它，测试在数据残留时
	// 依然会通过，而那种通过毫无意义。
	if _, err := db.Exec(`TRUNCATE users, audit_events RESTART IDENTITY`); err != nil {
		t.Fatalf("failed to truncate tables: %v", err)
	}

	cfg := withDatabase(t)
	ts, _ := newPersistentApp(t, cfg)

	email := fmt.Sprintf("persist-%d@example.com", time.Now().UnixNano())
	userID, token := registerUser(t, ts.URL, email)
	if token == "" {
		t.Fatal("register response has no access token")
	}

	// 核心断言：用 SQL **直接**查那一行。
	// 不走服务、不走仓库、不读任何标识字段 —— 3-3 的教训就在这里：
	// 只有从表里查到那一行，才能说数据真的落进了 PostgreSQL。
	var (
		gotID     string
		gotEmail  string
		gotStatus string
	)
	err := db.QueryRow(
		`SELECT id, email, status FROM users WHERE id = $1`, userID).
		Scan(&gotID, &gotEmail, &gotStatus)
	if err != nil {
		t.Fatalf("registered user %s is not in the users table: %v", userID, err)
	}
	if gotEmail != email {
		t.Errorf("stored email = %q, want %q", gotEmail, email)
	}
	if gotStatus != "active" {
		t.Errorf("stored status = %q, want active", gotStatus)
	}

	// 密码哈希也要落库，且不能是明文。
	var hash string
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE id = $1`, userID).
		Scan(&hash); err != nil {
		t.Fatalf("failed to read password_hash: %v", err)
	}
	if hash == "" {
		t.Error("password_hash is empty in the database")
	}
	if hash == "Str0ngPass!123" {
		t.Error("password is stored in plaintext")
	}
}

func TestPersistence_SurvivesRestart(t *testing.T) {
	// 本轮最关键的一条证明：重启后同一账号仍能登录。
	//
	// 前一个测试证明「写入落库」，这一条证明「读取也走库」——
	// 两者缺一不可：只测写入的话，一个「写入落库但读取仍查内存」的
	// 畸形装配照样能通过。重新装配 App 会丢掉全部内存状态，
	// 因此登录成功只可能来自数据库。
	db := requirePersistedDB(t)
	if _, err := db.Exec(`TRUNCATE users, audit_events RESTART IDENTITY`); err != nil {
		t.Fatalf("failed to truncate tables: %v", err)
	}

	cfg := withDatabase(t)
	email := fmt.Sprintf("restart-%d@example.com", time.Now().UnixNano())

	// 第一次装配：注册。
	ts1, _ := newPersistentApp(t, cfg)
	userID, _ := registerUser(t, ts1.URL, email)
	ts1.Close() // 关掉服务器与 App：内存状态随之消失

	// 第二次装配：全新的 App 与连接池，登录同一账号。
	ts2, _ := newPersistentApp(t, cfg)
	loginBody := fmt.Sprintf(`{"email":%q,"password":"Str0ngPass!123"}`, email)
	resp, err := http.Post(ts2.URL+"/api/v1/auth/login", "application/json",
		bytes.NewBufferString(loginBody))
	if err != nil {
		t.Fatalf("login after restart: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login after restart status = %d, want 200 "+
			"(the user did not survive the restart: it only ever lived in memory)",
			resp.StatusCode)
	}

	var login struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&login); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if login.AccessToken == "" {
		t.Error("login after restart returned no access token")
	}
	// 身份必须还是同一个人 —— 登录成功但登成了别的账号是更糟的结果。
	if login.User.ID != userID {
		t.Errorf("login after restart returned user %q, want %q", login.User.ID, userID)
	}
}

func TestPersistence_AuditEventsLandInPostgres(t *testing.T) {
	// 审计是「最不能丢」的一类数据：重启后内存里的记录会全部消失，
	// 而它的用途正是事后取证。因此审计的落库同样要有端到端断言。
	//
	// 用**被阻断的出口请求**作为触发路径，而不是注册用户 ——
	// 查过装配层：注册路径根本不产生审计事件（auditRec 只接在
	// 出口网关与告警处置上）。第一版测试断言「注册后 audit_events 有行」，
	// 那是在断言一个不存在的特性，失败是测试写错了而不是实现坏了。
	// 这里改用真实会写审计的路径：未授权外联被 default-deny 拦下，
	// 阻断必须留痕（attack attempts show up in the blocked requests）。
	db := requirePersistedDB(t)
	if _, err := db.Exec(`TRUNCATE users, audit_events RESTART IDENTITY`); err != nil {
		t.Fatalf("failed to truncate tables: %v", err)
	}

	cfg := withDatabase(t)
	ts, app := newPersistentApp(t, cfg)

	// 建一个管理员并授予 coordinator 角色，否则出口接口会被授权层拦掉。
	adminEmail := fmt.Sprintf("audit-admin-%d@example.com", time.Now().UnixNano())
	adminID, token := registerUser(t, ts.URL, adminEmail)

	if _, err := app.AuthzSvc.Assign(context.Background(),
		authzSubject(adminID), "role-coordinator", "test-bootstrap"); err != nil {
		t.Fatalf("assign coordinator role: %v", err)
	}

	// 未授权目标 → default-deny 阻断，并落一条 egress.blocked 审计事件。
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/network/proxy",
		bytes.NewBufferString(`{"agent_id":"agent-audit","url":"https://c2.evil.invalid/beacon"}`))
	if err != nil {
		t.Fatalf("build proxy request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("blocked egress request: %v", err)
	}
	defer resp.Body.Close()
	// 403 是策略拒绝的正常业务结果；这里只要求不是 5xx
	// （500 说明审计写入把请求打挂了，那是另一个问题）。
	if resp.StatusCode >= 500 {
		t.Fatalf("proxy status = %d, want a non-5xx policy decision", resp.StatusCode)
	}

	// 核心断言：直接查 audit_events 表。
	// 轮询而不是固定 sleep：审计写入在部分路径上是旁路调用，
	// 紧贴请求返回断言可能因写入未完成而假失败。
	var count int
	var lastErr error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&count); err != nil {
			lastErr = err
		} else if count > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("failed to count audit_events: %v", lastErr)
	}
	if count == 0 {
		t.Fatal("no audit events reached PostgreSQL after a blocked egress request " +
			"(the audit repository is still writing to memory)")
	}

	// 事件内容也要可查：seq 由 BIGSERIAL 分配，必须是正数
	// （0 说明某处把 seq 当零值写进去了）。
	var (
		action string
		seq    int64
		actor  string
	)
	if err := db.QueryRow(
		`SELECT action, seq, actor_id FROM audit_events ORDER BY seq LIMIT 1`).
		Scan(&action, &seq, &actor); err != nil {
		t.Fatalf("failed to read the first audit event: %v", err)
	}
	if action == "" {
		t.Error("audit event has an empty action")
	}
	if seq <= 0 {
		t.Errorf("audit event seq = %d, want a positive BIGSERIAL value", seq)
	}
	// 以 Agent 为主体落审计：这条让「查这台机器往哪些域名发过流量」可过滤。
	if actor != "agent-audit" {
		t.Errorf("audit actor_id = %q, want agent-audit", actor)
	}
}

func TestPersistence_MemoryBackendWritesNothingToPostgres(t *testing.T) {
	// 反向断言：**没有**配置数据库时，注册用户绝不能悄悄写进 PG。
	//
	// 这条防的是「装配层无论配置如何都连库」的回归 ——
	// 那会让「我用内存后端」这个明确的部署选择失效，而症状
	// （测试环境污染生产库）通常要到很久以后才被发现。
	db := requirePersistedDB(t)
	if _, err := db.Exec(`TRUNCATE users, audit_events RESTART IDENTITY`); err != nil {
		t.Fatalf("failed to truncate tables: %v", err)
	}

	cfg := withoutDatabase()
	a, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig() error = %v", err)
	}
	t.Cleanup(func() {
		if a.Storage != nil {
			_ = a.Storage.Close()
		}
	})

	if a.StorageBackend != storage.BackendMemory {
		t.Fatalf("StorageBackend = %q, want %q for a database-less config",
			a.StorageBackend, storage.BackendMemory)
	}

	ts := httptest.NewServer(NewRouter(a))
	t.Cleanup(ts.Close)

	email := fmt.Sprintf("memory-%d@example.com", time.Now().UnixNano())
	registerUser(t, ts.URL, email)

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("failed to count users: %v", err)
	}
	if count != 0 {
		t.Errorf("users table has %d rows after a memory-backed registration; "+
			"the memory backend must not touch PostgreSQL", count)
	}
}
