package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/atop0914/agentbot/internal/network"
)

// ===== Day 26：网络出口路由的端到端联通验证 =====
//
// 这些测试走真实路由（NewRouter，含 authz 中间件），验证：
//  1. 新增路由已被权限表登记（否则会落进默认拒绝返回 403）；
//  2. 策略写入 → 预检判定 → 网关阻断 / 放行，这条链路真的通；
//  3. 未授权目标被阻断时，流量记录与审计日志都留下了证据。
//
// 注意本机没有外网：放行路径通过替换 App 内部网关的 RoundTripper 来验证，
// 阻断路径则完全不发包（这正是它要证明的性质）。

type networkSummaryResponse struct {
	TotalRules      int `json:"total_rules"`
	AllowRules      int `json:"allow_rules"`
	DenyRules       int `json:"deny_rules"`
	TotalRequests   int `json:"total_requests"`
	BlockedRequests int `json:"blocked_requests"`
	DistinctDomains int `json:"distinct_domains"`
	TopDomains      []struct {
		Domain   string `json:"domain"`
		Requests int    `json:"requests"`
		Blocked  int    `json:"blocked"`
	} `json:"top_domains"`
	SuspiciousDomains []struct {
		Domain  string   `json:"domain"`
		Reasons []string `json:"reasons"`
	} `json:"suspicious_domains"`
}

type networkRulesResponse struct {
	Count int                  `json:"count"`
	Rules []network.PolicyRule `json:"rules"`
}

type networkEvaluateResponse struct {
	Decision network.Decision `json:"decision"`
}

type networkTrafficResponse struct {
	Count   int                     `json:"count"`
	Records []network.TrafficRecord `json:"records"`
}

// TestNetworkRoutesAreRegisteredInPermissionTable 验证这一组「无尾斜杠集合路径」
// 都被显式登记了权限。Day 24 的教训：/network/ 前缀规则覆盖不到集合路径，
// 漏一条就是 403，而且只有端到端测试能抓到。
//
// 关键：403 在这组路由上有**两种**含义 —— 权限表漏登记（中间件默认拒绝）
// 与 出口策略拒绝（业务默认拒绝）。两者必须区分，否则测试会把
// 「策略正确拦下未授权目标」误判成「路由没登记」，反之亦然。
// 判据：权限中间件返回 {"error":"no permission rule registered for this route"}，
// 而策略拒绝返回带 decision 的 body。
func TestNetworkRoutesAreRegisteredInPermissionTable(t *testing.T) {
	env := newTestEnv(t)

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/network/rules", ""},
		{http.MethodGet, "/api/v1/network/traffic", ""},
		{http.MethodGet, "/api/v1/network/stats", ""},
		{http.MethodGet, "/api/v1/network/summary", ""},
		{http.MethodPost, "/api/v1/network/evaluate", `{"agent_id":"a1","url":"https://api.example.com"}`},
		{http.MethodPost, "/api/v1/network/proxy", `{"agent_id":"a1","url":"https://api.example.com"}`},
		{http.MethodPost, "/api/v1/network/traffic/purge", `{"before":"2000-01-01T00:00:00Z"}`},
	}
	for _, tc := range cases {
		resp := env.do(tc.method, tc.path, tc.body)
		var body struct {
			Error    string          `json:"error"`
			Decision json.RawMessage `json:"decision"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			continue
		}
		if strings.Contains(body.Error, "no permission rule registered") {
			t.Errorf("%s %s = 403 default-deny — route is not registered in the permission table", tc.method, tc.path)
			continue
		}
		// 403 但不是「未登记」：必须是策略拒绝，且必须可解释。
		if len(body.Decision) == 0 {
			t.Errorf("%s %s = 403 without a decision body — cannot tell policy-deny from an unregistered route", tc.method, tc.path)
		}
	}
}

// TestNetworkBlockedEgressLeavesEvidence 验证「阻断 + 留痕」这条主线：
// 默认拒绝挡住未授权目标，并且阻断本身在流量记录与审计日志里都可查。
func TestNetworkBlockedEgressLeavesEvidence(t *testing.T) {
	env := newTestEnv(t)

	// 1. 对未授权目标发起出站：必须 403（策略拒绝是正常业务结果，不是 5xx）。
	resp := env.doJSONPost("/api/v1/network/proxy", `{"agent_id":"agent-x","url":"https://c2.evil.xyz/beacon"}`)
	var blocked map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&blocked); err != nil {
		t.Fatalf("decode blocked response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("blocked egress status = %d, want 403 (body=%v)", resp.StatusCode, blocked)
	}
	if _, ok := blocked["decision"]; !ok {
		t.Error("blocked response must carry the decision so the reason is diagnosable")
	}
	if _, ok := blocked["record"]; !ok {
		t.Error("blocked response must carry the traffic record (evidence chain)")
	}

	// 2. 流量记录里必须留下这条被阻断的外联。
	resp = env.do(http.MethodGet, "/api/v1/network/traffic?agent_id=agent-x&allowed=false", "")
	var traffic networkTrafficResponse
	if err := json.NewDecoder(resp.Body).Decode(&traffic); err != nil {
		t.Fatalf("decode traffic: %v", err)
	}
	resp.Body.Close()
	if traffic.Count != 1 {
		t.Fatalf("blocked traffic records = %d, want 1", traffic.Count)
	}
	if traffic.Records[0].Domain != "c2.evil.xyz" {
		t.Errorf("recorded domain = %q, want c2.evil.xyz", traffic.Records[0].Domain)
	}
	if !strings.Contains(traffic.Records[0].Reason, "default-deny") {
		t.Errorf("recorded reason = %q, want it to mention default-deny", traffic.Records[0].Reason)
	}

	// 3. 审计日志里必须有一条 egress.blocked 事件（以 Agent 为主体）。
	//
	// 过滤参数用文档化的 event_type（/api/v1/audit/events 的契约），
	// 而不是 action：action 只是兼容别名，测试应该用主键名。
	resp = env.do(http.MethodGet, "/api/v1/audit/events?event_type=egress.blocked&resource_id=agent-x", "")
	var auditResp struct {
		Events []struct {
			Action    string                 `json:"action"`
			ActorType string                 `json:"actor_type"`
			Details   map[string]interface{} `json:"details"`
		} `json:"events"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&auditResp); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	resp.Body.Close()
	if auditResp.Total == 0 || len(auditResp.Events) == 0 {
		t.Fatal("blocked egress must leave an audit event — that is where attack attempts show up")
	}
	if auditResp.Events[0].Action != "egress.blocked" {
		t.Errorf("audit action = %q, want egress.blocked", auditResp.Events[0].Action)
	}
	domain, _ := auditResp.Events[0].Details["domain"].(string)
	if domain != "c2.evil.xyz" {
		t.Errorf("audit details domain = %q, want c2.evil.xyz", domain)
	}
	if allowed, ok := auditResp.Events[0].Details["allowed"].(bool); !ok || allowed {
		t.Errorf("audit details allowed = %v, want false", auditResp.Events[0].Details["allowed"])
	}
}

// TestNetworkAgentScopedRuleOverridesGlobal 验证策略写入 → 判定的真实链路：
// 为某个 Agent 单独收紧出口后，它被挡住而其他 Agent 不受影响。
func TestNetworkAgentScopedRuleOverridesGlobal(t *testing.T) {
	env := newTestEnv(t)

	// 全局放行（故意用一条最不具体的规则，验证具体度排序真的生效）。
	resp := env.doJSONPost("/api/v1/network/rules", `{"id":"g-allow","target":"*","effect":"allow","description":"e2e global allow"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create global allow status = %d, want 201", resp.StatusCode)
	}

	// 只对 agent-locked 收紧。
	resp = env.doJSONPost("/api/v1/network/rules", `{"id":"locked-deny","agent_id":"agent-locked","target":"api.example.com","effect":"deny"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create agent deny status = %d, want 201", resp.StatusCode)
	}

	// 预检 agent-locked：拒绝，且必须指出是被哪条规则拒的。
	resp = env.doJSONPost("/api/v1/network/evaluate", `{"agent_id":"agent-locked","method":"GET","url":"https://api.example.com/v1/x"}`)
	var locked networkEvaluateResponse
	if err := json.NewDecoder(resp.Body).Decode(&locked); err != nil {
		t.Fatalf("decode evaluate (locked): %v", err)
	}
	resp.Body.Close()
	if locked.Decision.Allowed {
		t.Fatalf("agent-locked decision = %+v, want denied", locked.Decision)
	}
	if locked.Decision.RuleID != "locked-deny" {
		t.Errorf("rule_id = %q, want locked-deny", locked.Decision.RuleID)
	}
	if locked.Decision.Scope != "agent" {
		t.Errorf("scope = %q, want agent", locked.Decision.Scope)
	}

	// 预检 agent-free：同一个目标必须放行（全局 allow 生效）。
	resp = env.doJSONPost("/api/v1/network/evaluate", `{"agent_id":"agent-free","method":"GET","url":"https://api.example.com/v1/x"}`)
	var free networkEvaluateResponse
	if err := json.NewDecoder(resp.Body).Decode(&free); err != nil {
		t.Fatalf("decode evaluate (free): %v", err)
	}
	resp.Body.Close()
	if !free.Decision.Allowed {
		t.Fatalf("agent-free decision = %+v, want allowed", free.Decision)
	}

	// 策略列表要能看到两条（含默认种的 deny 条目）。
	resp = env.do(http.MethodGet, "/api/v1/network/rules", "")
	var rules networkRulesResponse
	if err := json.NewDecoder(resp.Body).Decode(&rules); err != nil {
		t.Fatalf("decode rules: %v", err)
	}
	resp.Body.Close()
	if rules.Count < 2 {
		t.Errorf("rules = %d, want at least the two we created", rules.Count)
	}
}

// TestNetworkDefaultDenySeedRulesBlockMetadataService 验证启动时种的 deny 条目生效：
// 云元数据地址必须开箱即被挡住（这是最容易造成整账号失陷的一条外联）。
func TestNetworkDefaultDenySeedRulesBlockMetadataService(t *testing.T) {
	env := newTestEnv(t)

	for _, url := range []string{
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/",
		"http://localhost:8080/internal/admin/snapshot",
		"http://db.internal:5432/",
	} {
		resp := env.doJSONPost("/api/v1/network/evaluate",
			fmt.Sprintf(`{"agent_id":"a1","method":"GET","url":%q}`, url))
		var got networkEvaluateResponse
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode evaluate (%s): %v", url, err)
		}
		resp.Body.Close()
		if got.Decision.Allowed {
			t.Errorf("%s = allowed, want denied by a seeded rule", url)
			continue
		}
		if got.Decision.RuleID == "" {
			t.Errorf("%s denied without a rule id — seeded rules should give a readable reason", url)
		}
	}
}

// TestNetworkProxyForwardsThroughGateway 验证放行路径真的经网关转发。
//
// 本机无外网，因此把 App 内网关的 RoundTripper 换成可控实现：
// 这样验证的是我们自己的代码路径（判定 → 转发 → 记录 → 审计），
// 而不是外网的可用性。
func TestNetworkProxyForwardsThroughGateway(t *testing.T) {
	env := newTestEnv(t)

	// 装一个假下游并放行目标。
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"upstream":"ok"}`))
	}))
	defer downstream.Close()

	// 关键：策略目标必须是**裸域名**（ValidateTarget 刻意拒绝 host:port ——
	// 带端口的输入往往意味着作者以为写的是 URL，那会静默失效）。
	// 因此这里让网关请求一个域名，再由假传输把它改写到本地 httptest 服务。
	const egressHost = "egress-test.example.com"
	installTestTransport(t, env.app, &rewriteTransport{
		to:        downstream.URL,
		transport: network.NewHTTPGatewayTransportForTest(),
	})
	resp := env.doJSONPost("/api/v1/network/rules",
		fmt.Sprintf(`{"id":"e2e-allow","target":"%s","effect":"allow","description":"e2e"}`, egressHost))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create allow rule status = %d, want 201", resp.StatusCode)
	}

	// 经网关出站。
	resp = env.doJSONPost("/api/v1/network/proxy",
		fmt.Sprintf(`{"agent_id":"agent-ok","method":"GET","url":%q}`, "https://"+egressHost+"/v1/ping"))
	var out struct {
		Domain   string                 `json:"domain"`
		Status   int                    `json:"status"`
		Body     string                 `json:"body"`
		Decision network.Decision       `json:"decision"`
		Record   *network.TrafficRecord `json:"record"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode proxy response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %d, want 200 (body=%+v)", resp.StatusCode, out)
	}
	if !out.Decision.Allowed {
		t.Fatalf("decision = %+v, want allowed", out.Decision)
	}
	if out.Status != http.StatusOK {
		t.Errorf("upstream status = %d, want 200", out.Status)
	}
	if !strings.Contains(out.Body, "upstream") {
		t.Errorf("body = %q, want the forwarded upstream payload", out.Body)
	}
	if out.Record == nil || !out.Record.Allowed || out.Record.Domain == "" {
		t.Fatalf("record = %+v, want an allowed record with a domain", out.Record)
	}

	// 放行也要落审计。
	resp = env.do(http.MethodGet, "/api/v1/audit/events?event_type=egress.allowed&resource_id=agent-ok", "")
	var auditResp struct {
		Total int `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&auditResp); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	resp.Body.Close()
	if auditResp.Total == 0 {
		t.Error("allowed egress must also be audited")
	}
}

// TestNetworkSummaryReachableAndReportsRules 验证后台「网络」分区的数据源真的可取。
func TestNetworkSummaryReachableAndReportsRules(t *testing.T) {
	env := newTestEnv(t)

	// 造一条被阻断的外联，让摘要里出现可断言的数据。
	resp := env.doJSONPost("/api/v1/network/proxy", `{"agent_id":"agent-sum","url":"https://c2.evil.xyz/x"}`)
	resp.Body.Close()

	resp = env.do(http.MethodGet, "/api/v1/network/summary", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary status = %d, want 200", resp.StatusCode)
	}
	var summary networkSummaryResponse
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	resp.Body.Close()

	// 默认种的三条 deny 必须出现在计数里 —— 「出口一条策略都没配」
	// 是控制台最需要突出的状态，所以这个数字必须真实。
	if summary.DenyRules < 3 {
		t.Errorf("deny_rules = %d, want >= 3 (seeded defaults)", summary.DenyRules)
	}
	if summary.TotalRules < 3 {
		t.Errorf("total_rules = %d, want >= 3", summary.TotalRules)
	}
	if summary.TotalRequests < 1 || summary.BlockedRequests < 1 {
		t.Errorf("summary totals = %+v, want the blocked request reflected", summary)
	}
	if summary.DistinctDomains < 1 {
		t.Errorf("distinct_domains = %d, want >= 1", summary.DistinctDomains)
	}

	// 高风险 TLD 必须出现在可疑外联里。
	found := false
	for _, d := range summary.SuspiciousDomains {
		if d.Domain == "c2.evil.xyz" {
			found = true
			if len(d.Reasons) == 0 {
				t.Error("suspicious domain must explain why it was flagged")
			}
		}
	}
	if !found {
		t.Errorf("suspicious_domains = %+v, want c2.evil.xyz", summary.SuspiciousDomains)
	}
}

// TestNetworkAdminSnapshotCarriesNetworkSection 验证分区已接入聚合视图，
// 且 admin 分区与我们自己的 /summary 接口给出一致的事实。
func TestNetworkAdminSnapshotCarriesNetworkSection(t *testing.T) {
	env := newTestEnv(t)

	resp := env.doJSONPost("/api/v1/network/proxy", `{"agent_id":"agent-adm","url":"https://c2.evil.xyz/x"}`)
	resp.Body.Close()

	resp = env.do(http.MethodGet, "/api/v1/admin/snapshot?sections=network", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d, want 200", resp.StatusCode)
	}
	var snap struct {
		Network *networkSummaryResponse `json:"network"`
		Errors  map[string]string       `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	resp.Body.Close()

	if snap.Network == nil {
		t.Fatalf("snapshot.network is nil (errors=%v) — the network section is not wired", snap.Errors)
	}
	if _, bad := snap.Errors["network"]; bad {
		t.Errorf("network section degraded: %v", snap.Errors)
	}
	if snap.Network.TotalRules < 3 {
		t.Errorf("snapshot total_rules = %d, want >= 3", snap.Network.TotalRules)
	}
	if snap.Network.BlockedRequests < 1 {
		t.Errorf("snapshot blocked_requests = %d, want >= 1", snap.Network.BlockedRequests)
	}
}

// TestNetworkPurgeDefaultsToDryRun 验证清理接口默认不删数据。
//
// 「忘记传参数」不该造成观测数据被删 —— 真要删必须显式写 dry_run=false。
func TestNetworkPurgeDefaultsToDryRun(t *testing.T) {
	env := newTestEnv(t)

	resp := env.doJSONPost("/api/v1/network/proxy", `{"agent_id":"agent-purge","url":"https://c2.evil.xyz/x"}`)
	resp.Body.Close()

	resp = env.doJSONPost("/api/v1/network/traffic/purge", `{"before":"2999-01-01T00:00:00Z"}`)
	var plan struct {
		DryRun     bool `json:"dry_run"`
		Candidates int  `json:"candidates"`
		Removed    int  `json:"removed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&plan); err != nil {
		t.Fatalf("decode purge plan: %v", err)
	}
	resp.Body.Close()
	if !plan.DryRun {
		t.Error("dry_run must default to true")
	}
	if plan.Removed != 0 {
		t.Errorf("removed = %d, want 0 in dry-run", plan.Removed)
	}
	if plan.Candidates == 0 {
		t.Error("dry-run must report how many records would be removed")
	}

	// 记录仍然在。
	resp = env.do(http.MethodGet, "/api/v1/network/traffic?agent_id=agent-purge", "")
	var traffic networkTrafficResponse
	_ = json.NewDecoder(resp.Body).Decode(&traffic)
	resp.Body.Close()
	if traffic.Count == 0 {
		t.Fatal("dry-run must not delete anything")
	}

	// 显式 dry_run=false 才真删。
	resp = env.doJSONPost("/api/v1/network/traffic/purge", `{"before":"2999-01-01T00:00:00Z","dry_run":false}`)
	if err := json.NewDecoder(resp.Body).Decode(&plan); err != nil {
		t.Fatalf("decode purge result: %v", err)
	}
	resp.Body.Close()
	if plan.DryRun || plan.Removed == 0 {
		t.Errorf("explicit purge result = %+v, want records removed", plan)
	}
}

// TestNetworkRuleValidationRejected 验证非法策略在 HTTP 层就被拒（400）。
func TestNetworkRuleValidationRejected(t *testing.T) {
	env := newTestEnv(t)

	bad := []string{
		`{"target":"https://api.example.com","effect":"allow"}`, // URL 形态
		`{"target":"api.example.com:443","effect":"allow"}`,     // 带端口
		`{"target":"a.*.example.com","effect":"allow"}`,         // 中间通配
		`{"target":"api.example.com","effect":"maybe"}`,         // 非法 effect
		`{"target":"","effect":"allow"}`,                        // 空 target
	}
	for _, body := range bad {
		resp := env.doJSONPost("/api/v1/network/rules", body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("create rule %s status = %d, want 400", body, resp.StatusCode)
		}
	}
}
