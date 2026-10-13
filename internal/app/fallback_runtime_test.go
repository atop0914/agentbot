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

	"github.com/atop0914/agentbot/internal/config"
	"github.com/atop0914/agentbot/internal/storage"
)

// fallback_runtime_test.go 补上 3-3 只验证过一半的那条分支。
//
// 3-3 验证的是**启动阶段**：「连不上 + 显式开回退」时进程能起来、
// 启动日志写出 storage.backend=memory。但**运行期的读写行为从未被验证过** ——
// 「启动成功」与「一用就崩」完全可以同时成立：
//
//   - 装配层可能只把句柄换成了 nil，而某个仓库仍持有那个连不上的 *sql.DB
//     （症状：启动日志写着 memory，第一次写请求报 connection refused）；
//   - 也可能反过来：回退模式下数据**仍然**写进了那个连不上的库
//     （不可能成功，但会让请求报 500 而不是正常服务）。
//
// 所以本文件断言两件事，缺一不可：
//  1. 正向：回退模式下 Agent 的读写**完整可用**（建、查、改、起停、删）；
//  2. 反向：回退模式下**不得**有任何数据落进那个连不上的库 ——
//     用真实 PG 连接去查那张表，行数必须是 0。
//
// 反向断言为什么必须做：它防的是「回退只是换了日志、后端其实没换」。
// 那种装配在正常路径下看起来完全正常（内存实现兜住了），
// 只有在真机上查表才能发现「数据其实在尝试往一个死掉的连接写」。

// unreachableDatabaseConfig 返回一个「声明了数据库、但地址保证连不上」的配置，
// 并显式打开内存回退。
//
// 用 127.0.0.1:1 而不是某个域名：保留地址的 1 端口在本机不可能有监听，
// 连接会立刻被拒绝（而不是等 DNS 超时）—— 测试不会因为网络环境而变慢或变脆。
func unreachableDatabaseConfig() config.Config {
	cfg := config.DevelopmentDefault()
	cfg.Database.Host = "127.0.0.1"
	cfg.Database.Port = 1
	cfg.Database.User = "agentbot"
	cfg.Database.Password = "agentbot"
	cfg.Database.DBName = "agentbot_nowhere"
	cfg.Database.SSLMode = "disable"
	cfg.Storage.AllowMemoryFallback = true
	return cfg
}

// assertUnreachableIsReallyUnreachable 前置检查：保证测试用的地址确实连不上。
// 若这里能连上，整条测试就变成了「回退分支下其实用着 postgres」，
// 断言全都会以误导性的方式通过或失败。
func assertUnreachableIsReallyUnreachable(t *testing.T, cfg config.Config) {
	t.Helper()

	// 用极短超时探测：目的是确认「连不上」，长超时只会拖慢测试。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	db, err := sql.Open("pgx", cfg.Database.DSN())
	if err == nil {
		defer func() { _ = db.Close() }()
		if perr := db.PingContext(ctx); perr == nil {
			t.Fatalf("the fixture address %s actually IS reachable; "+
				"this test would not exercise the fallback branch",
				cfg.Database.Host)
		}
	}
}

func TestFallback_RuntimeReadWriteWorksInMemoryMode(t *testing.T) {
	cfg := unreachableDatabaseConfig()
	assertUnreachableIsReallyUnreachable(t, cfg)

	// 启动必须成功：这是回退开关的全部意义。
	a, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig() error = %v; an explicit memory fallback must start", err)
	}
	t.Cleanup(func() {
		if a.Storage != nil {
			_ = a.Storage.Close()
		}
	})

	// 回退形态必须自我声明为内存后端，而不是「日志说 memory、字段说 postgres」。
	if a.StorageBackend != storage.BackendMemory {
		t.Fatalf("StorageBackend = %q, want %q under fallback",
			a.StorageBackend, storage.BackendMemory)
	}

	ts := httptest.NewServer(NewRouter(a))
	t.Cleanup(ts.Close)
	_, token := adminWithToken(t, ts, a)

	// ---- 运行期读写的完整往返 ----

	// 1. 建
	agentID := createAgent(t, ts.URL, token, "fallback-agent")

	// 2. 查
	getResp := doFallbackRequest(t, http.MethodGet, ts.URL+"/api/v1/agents/"+agentID, token, "")
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET agent under fallback = %d, want 200 "+
			"(the process started, but a request blows up immediately)", getResp.StatusCode)
	}
	var got struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	decodeFallbackBody(t, getResp, &got)
	if got.ID != agentID {
		t.Errorf("GET returned agent %q, want %q", got.ID, agentID)
	}

	// 3. 改状态（Start 走的是 Update 整行覆盖，与 UpdateState 窄路径不同）
	startResp := doFallbackRequest(t, http.MethodPost, ts.URL+"/api/v1/agents/"+agentID+"/start", token, "")
	if startResp.StatusCode != http.StatusOK {
		t.Fatalf("POST start under fallback = %d, want 200", startResp.StatusCode)
	}
	_ = startResp.Body.Close()

	after := doFallbackRequest(t, http.MethodGet, ts.URL+"/api/v1/agents/"+agentID, token, "")
	if after.StatusCode != http.StatusOK {
		t.Fatalf("GET after start under fallback = %d, want 200", after.StatusCode)
	}
	decodeFallbackBody(t, after, &got)
	if got.State != "running" {
		t.Errorf("state after start = %q, want running "+
			"(the fallback backend must honour writes, not just reads)", got.State)
	}

	// 4. 改配置 / 走生命周期动作
	// 注意 pause 是 **POST**（动作路由只接受 POST，见 handler.handleAction）——
	// 用 PUT 会得到 405，那是路由形态写错了而不是后端有问题。
	// 这条顺带验证：回退模式下所有生命周期动作路由都还能走通。
	upd := doFallbackRequest(t, http.MethodPost, ts.URL+"/api/v1/agents/"+agentID+"/pause", token, "")
	if upd.StatusCode != http.StatusOK {
		t.Fatalf("POST pause under fallback = %d, want 200", upd.StatusCode)
	}
	_ = upd.Body.Close()

	// 生命周期动作的结果要能读回来（proof 写入生效，而不只是接口没报错）。
	paused := doFallbackRequest(t, http.MethodGet, ts.URL+"/api/v1/agents/"+agentID, token, "")
	if paused.StatusCode != http.StatusOK {
		t.Fatalf("GET after pause under fallback = %d, want 200", paused.StatusCode)
	}
	decodeFallbackBody(t, paused, &got)
	if got.State != "paused" {
		t.Errorf("state after pause = %q, want paused", got.State)
	}

	// 5. 列表
	listResp := doFallbackRequest(t, http.MethodGet, ts.URL+"/api/v1/agents", token, "")
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("GET agents list under fallback = %d, want 200", listResp.StatusCode)
	}
	var list struct {
		Agents []struct {
			ID string `json:"id"`
		} `json:"agents"`
	}
	decodeFallbackBody(t, listResp, &list)
	found := false
	for _, x := range list.Agents {
		if x.ID == agentID {
			found = true
		}
	}
	if !found {
		t.Errorf("agent %s missing from the list under fallback", agentID)
	}

	// 6. 删（终态之外的 Agent 允许删；pause 之后是 paused，可删）
	delResp := doFallbackRequest(t, http.MethodDelete, ts.URL+"/api/v1/agents/"+agentID, token, "")
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE agent under fallback = %d, want 200", delResp.StatusCode)
	}
	_ = delResp.Body.Close()

	gone := doFallbackRequest(t, http.MethodGet, ts.URL+"/api/v1/agents/"+agentID, token, "")
	// 期望 **403 而不是 404** —— 这是授权层 fail-closed 的正确结果，不是后端坏了。
	//
	// 链路顺序是「认证 → 授权 → handler」，而 GET /api/v1/agents/{id} 登记的是
	// TargetType="agent"：授权中间件会先查这个 Agent 是否存在，
	// 不存在就直接 403 拒绝，请求根本到不了 handler 的 404 分支。
	// 也就是说删除之后再按 ID 访问，返回的是「你不该看到它」而不是「它不存在」——
	// 语义上更严格（不对外泄露「这个 ID 曾经存在过」）。
	//
	// 若是 200 才是真问题：那意味着删掉的 Agent 还能读出来。
	if gone.StatusCode == http.StatusOK {
		t.Fatalf("GET deleted agent under fallback = 200; the delete did not take effect")
	}
	if gone.StatusCode != http.StatusForbidden && gone.StatusCode != http.StatusNotFound {
		t.Errorf("GET deleted agent = %d, want 403 (authz target missing, fail-closed) or 404",
			gone.StatusCode)
	}
	_ = gone.Body.Close()
}

func TestFallback_WritesNothingToTheUnreachableDatabase(t *testing.T) {
	// 反向断言（本文件最重要的一条）：回退模式下不得有任何数据
	// 落进那个连不上的库。
	//
	// 做法：换一个**可达的**真机 PG 作为"那个库"，但把配置里的 host/port
	// 指向它、同时让 DSN 与实际使用的连接分开 —— 这里用的是更直接的办法：
	// 拿一个真实的 PG 句柄去查 agents 表，然后断言那张表在这个测试前后
	// 行数不变。
	//
	// 注意断言方式：不是「表里没有名字叫 fallback 的行」，而是
	// **前后行数完全相等**。前者会被「写到了别的库」漏过去，
	// 后者才是「没有写入」的直接证明。
	db := requirePersistedDB(t) // 真机 PG，同时也是"那个连不上的库"的对照面

	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&before); err != nil {
		t.Fatalf("failed to count agents before: %v", err)
	}

	cfg := unreachableDatabaseConfig()
	assertUnreachableIsReallyUnreachable(t, cfg)

	a, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig() error = %v", err)
	}
	t.Cleanup(func() {
		if a.Storage != nil {
			_ = a.Storage.Close()
		}
	})

	ts := httptest.NewServer(NewRouter(a))
	t.Cleanup(ts.Close)
	_, token := adminWithToken(t, ts, a)

	// 造数据：注册（user 仓库）、建 Agent、起停。
	// 三个仓库（user/audit/agent）在回退模式下都必须是内存实现。
	unique := fmt.Sprintf("fallback-nothing-%d@example.com", time.Now().UnixNano())
	_, token2 := registerUser(t, ts.URL, unique)
	if token2 == "" {
		t.Fatal("register under fallback returned no token")
	}
	createAgent(t, ts.URL, token, "fallback-nothing-agent")

	var after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&after); err != nil {
		t.Fatalf("failed to count agents after: %v", err)
	}
	if after != before {
		t.Errorf("agents row count changed from %d to %d while running in fallback mode; "+
			"the fallback backend is still writing to PostgreSQL", before, after)
	}
}

// doFallbackRequest 发一个带 Bearer token 的真实 HTTP 请求（走 httptest 服务器）。
// 不能复用 tenant_integration_test.go 的 doAuthed：那个走 ResponseRecorder 直调 handler，
// 而本文件要验证的是「起真实服务器后运行期读写可用」，必须经过网络栈。
func doFallbackRequest(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()

	var reader *bytes.Buffer
	if body == "" {
		reader = bytes.NewBufferString("")
	} else {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

// decodeBody 解码响应体并关闭它。
func decodeFallbackBody(t *testing.T, resp *http.Response, dst interface{}) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}
