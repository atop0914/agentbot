package network

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// ===== 域名模式匹配与校验 =====

func TestValidateTargetAcceptsSupportedForms(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"api.example.com", "api.example.com"},
		{"API.Example.COM", "api.example.com"},
		{"  api.example.com.  ", "api.example.com"},
		{"*.example.com", "*.example.com"},
		{"*.Example.COM", "*.example.com"},
		{"*", "*"},
	}
	for _, tc := range cases {
		got, err := ValidateTarget(tc.in)
		if tc.want == "" {
			if err == nil {
				t.Errorf("ValidateTarget(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ValidateTarget(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ValidateTarget(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateTargetRejectsAmbiguousForms(t *testing.T) {
	// 这些输入要么根本没有匹配语义，要么「看起来像 URL」。
	// 静默接受会造成策略实际匹配不到任何流量的黑盒失效。
	bad := []string{
		"",
		"   ",
		"https://api.example.com",
		"api.example.com/path",
		"a.*.example.com",
		"*.example.*.com",
		"*.",
		"foo*bar",
	}
	for _, in := range bad {
		if got, err := ValidateTarget(in); err == nil {
			t.Errorf("ValidateTarget(%q) = %q, want error", in, got)
		}
	}
}

func TestMatchSpecificityPreciseBeatsWildcard(t *testing.T) {
	// 具体度必须严格分层：精确 > 长通配后缀 > 短通配后缀 > '*'
	pairs := []struct {
		more   string
		lesser string
		domain string
	}{
		{"api.example.com", "*.example.com", "api.example.com"},
		{"*.a.example.com", "*.example.com", "x.a.example.com"},
		{"*.example.com", "*", "anything.example.com"},
		{"169.254.169.254", "*", "169.254.169.254"},
	}
	for _, p := range pairs {
		hi := matchSpecificity(p.more, p.domain)
		lo := matchSpecificity(p.lesser, p.domain)
		if hi < 0 {
			t.Errorf("matchSpecificity(%q, %q) = %d, want >= 0", p.more, p.domain, hi)
			continue
		}
		if lo < 0 {
			t.Errorf("matchSpecificity(%q, %q) = %d, want >= 0", p.lesser, p.domain, lo)
			continue
		}
		if hi <= lo {
			t.Errorf("specificity(%q)=%d should exceed specificity(%q)=%d for %q",
				p.more, hi, p.lesser, lo, p.domain)
		}
	}
}

func TestMatchSpecificityWildcardDoesNotMatchApex(t *testing.T) {
	// *.example.com 的本意是「example.com 下面的子域」，不包括它自己。
	// 把 apex 也算进去会让「只放开子域」的收紧配置静默失效。
	if got := matchSpecificity("*.example.com", "example.com"); got != -1 {
		t.Errorf("matchSpecificity(*.example.com, example.com) = %d, want -1", got)
	}
	if got := matchSpecificity("*.example.com", "a.b.example.com"); got < 0 {
		t.Errorf("matchSpecificity(*.example.com, a.b.example.com) = %d, want >= 0", got)
	}
}

func TestDomainOfExtractsHostFromURL(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com/v1/x?y=1": "api.example.com",
		"http://API.Example.COM:8080/p":    "api.example.com",
		"https://169.254.169.254/latest":   "169.254.169.254",
		"not a url":                        "",
	}
	for in, want := range cases {
		if got := DomainOf(in); got != want {
			t.Errorf("DomainOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// ===== 策略判定 =====

func TestEvaluateDefaultsToDeny(t *testing.T) {
	store := NewMemoryPolicyStore()
	decision, err := store.Evaluate(context.Background(), "a1", http.MethodGet, "https://evil.example.com/x")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision.Allowed {
		t.Fatal("empty policy must deny by default")
	}
	if decision.Reason != DefaultDenyReason {
		t.Errorf("reason = %q, want %q", decision.Reason, DefaultDenyReason)
	}
	if decision.RuleID != "" {
		t.Errorf("rule_id = %q, want empty for default-deny", decision.RuleID)
	}
}

func TestEvaluateAgentScopeOverridesGlobal(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryPolicyStore()

	// 全局放行 example.com……
	if _, err := store.Add(ctx, PolicyRule{ID: "g-allow", Target: "example.com", Effect: EffectAllow}); err != nil {
		t.Fatalf("add global allow: %v", err)
	}
	// ……但为 a1 单独收紧。
	if _, err := store.Add(ctx, PolicyRule{ID: "a1-deny", AgentID: "a1", Target: "example.com", Effect: EffectDeny}); err != nil {
		t.Fatalf("add agent deny: %v", err)
	}

	got, err := store.Evaluate(ctx, "a1", http.MethodGet, "https://example.com/x")
	if err != nil {
		t.Fatalf("Evaluate a1: %v", err)
	}
	if got.Allowed {
		t.Fatalf("agent-scoped deny must win over global allow, got %+v", got)
	}
	if got.RuleID != "a1-deny" {
		t.Errorf("rule_id = %q, want a1-deny", got.RuleID)
	}

	got, err = store.Evaluate(ctx, "a2", http.MethodGet, "https://example.com/x")
	if err != nil {
		t.Fatalf("Evaluate a2: %v", err)
	}
	if !got.Allowed {
		t.Fatalf("a2 should still be allowed by the global rule, got %+v", got)
	}
	if got.Scope != scopeGlobal {
		t.Errorf("scope = %q, want %q", got.Scope, scopeGlobal)
	}
}

func TestEvaluatePreciseDenyBeatsWildcardAllow(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryPolicyStore()
	// 先登记宽泛白名单，再登记精确黑名单：顺序不该影响结果。
	if _, err := store.Add(ctx, PolicyRule{ID: "allow-all-sub", Target: "*.example.com", Effect: EffectAllow}); err != nil {
		t.Fatalf("add wildcard allow: %v", err)
	}
	if _, err := store.Add(ctx, PolicyRule{ID: "deny-metadata", Target: "metadata.example.com", Effect: EffectDeny}); err != nil {
		t.Fatalf("add precise deny: %v", err)
	}

	got, err := store.Evaluate(ctx, "a1", http.MethodGet, "https://metadata.example.com/creds")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got.Allowed {
		t.Fatalf("precise deny must win over wildcard allow, got %+v", got)
	}
	if got.RuleID != "deny-metadata" {
		t.Errorf("rule_id = %q, want deny-metadata", got.RuleID)
	}

	// 同域下的其他子域仍按白名单放行。
	got, err = store.Evaluate(ctx, "a1", http.MethodGet, "https://api.example.com/x")
	if err != nil {
		t.Fatalf("Evaluate api: %v", err)
	}
	if !got.Allowed {
		t.Fatalf("api.example.com should be allowed, got %+v", got)
	}
}

func TestEvaluateDenyWinsOnEqualSpecificity(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryPolicyStore()
	if _, err := store.Add(ctx, PolicyRule{ID: "allow", Target: "api.example.com", Effect: EffectAllow}); err != nil {
		t.Fatalf("add allow: %v", err)
	}
	if _, err := store.Add(ctx, PolicyRule{ID: "deny", Target: "api.example.com", Effect: EffectDeny}); err != nil {
		t.Fatalf("add deny: %v", err)
	}
	got, err := store.Evaluate(ctx, "a1", http.MethodGet, "https://api.example.com/x")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got.Allowed {
		t.Fatalf("deny must win on equal specificity (fail-closed), got %+v", got)
	}
}

func TestEvaluateMethodRestriction(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryPolicyStore()
	if _, err := store.Add(ctx, PolicyRule{
		ID: "get-only", Target: "api.example.com", Effect: EffectAllow,
		Methods: []string{"get"},
	}); err != nil {
		t.Fatalf("add rule: %v", err)
	}

	got, err := store.Evaluate(ctx, "a1", http.MethodGet, "https://api.example.com/x")
	if err != nil {
		t.Fatalf("Evaluate GET: %v", err)
	}
	if !got.Allowed {
		t.Fatalf("GET should be allowed, got %+v", got)
	}

	// POST 不匹配该方法限定 → 落回默认拒绝。
	got, err = store.Evaluate(ctx, "a1", http.MethodPost, "https://api.example.com/x")
	if err != nil {
		t.Fatalf("Evaluate POST: %v", err)
	}
	if got.Allowed {
		t.Fatalf("POST should fall through to default-deny, got %+v", got)
	}
}

func TestEvaluateEmptyAgentStillAppliesGlobalRules(t *testing.T) {
	// 没有 Agent 身份的判定请求（例如平台自身预检）也必须走全局策略，
	// 不能因为「拿不到 agent 维度」就跳过判定。
	ctx := context.Background()
	store := NewMemoryPolicyStore()
	if _, err := store.Add(ctx, PolicyRule{ID: "deny-x", Target: "blocked.example.com", Effect: EffectDeny}); err != nil {
		t.Fatalf("add rule: %v", err)
	}
	got, err := store.Evaluate(ctx, "", http.MethodGet, "https://blocked.example.com/x")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got.Allowed {
		t.Fatalf("global deny should apply without an agent id, got %+v", got)
	}
}

func TestNormalizeRuleRejectsBadEffectAndTarget(t *testing.T) {
	if _, err := NormalizeRule(PolicyRule{Target: "api.example.com", Effect: "maybe"}); !errors.Is(err, ErrInvalidEffect) {
		t.Errorf("invalid effect error = %v, want ErrInvalidEffect", err)
	}
	if _, err := NormalizeRule(PolicyRule{Target: "https://api.example.com", Effect: EffectAllow}); !errors.Is(err, ErrInvalidTarget) {
		t.Errorf("url-shaped target error = %v, want ErrInvalidTarget", err)
	}
}

func TestNormalizeRuleDefaultsEmptyEffectToDeny(t *testing.T) {
	// 缺省 allow 会是一条静默的口子；缺省 deny 只会让人发现配置写错了。
	rule, err := NormalizeRule(PolicyRule{Target: "api.example.com"})
	if err != nil {
		t.Fatalf("NormalizeRule: %v", err)
	}
	if rule.Effect != EffectDeny {
		t.Errorf("effect = %q, want %q", rule.Effect, EffectDeny)
	}
}

func TestNormalizeRuleNormalizesMethods(t *testing.T) {
	rule, err := NormalizeRule(PolicyRule{
		Target:  "api.example.com",
		Effect:  EffectAllow,
		Methods: []string{" get ", "GET", "", "post"},
	})
	if err != nil {
		t.Fatalf("NormalizeRule: %v", err)
	}
	if len(rule.Methods) != 2 || rule.Methods[0] != "GET" || rule.Methods[1] != "POST" {
		t.Errorf("methods = %v, want [GET POST]", rule.Methods)
	}
}

func TestPolicyStoreDeleteMissingRule(t *testing.T) {
	store := NewMemoryPolicyStore()
	if err := store.Delete(context.Background(), "nope"); !errors.Is(err, ErrRuleNotFound) {
		t.Errorf("Delete(missing) error = %v, want ErrRuleNotFound", err)
	}
	if err := store.Delete(context.Background(), ""); !errors.Is(err, ErrRuleNotFound) {
		t.Errorf("Delete(empty) error = %v, want ErrRuleNotFound", err)
	}
}

func TestPolicyStoreListIsStableAndSorted(t *testing.T) {
	// 排序不稳定会让控制台的策略列表每次刷新都换顺序，
	// 运维无法用「第几条」沟通，也看不出有没有被改动。
	ctx := context.Background()
	store := NewMemoryPolicyStore()
	inputs := []PolicyRule{
		{ID: "z-global-deny", Target: "b.example.com", Effect: EffectDeny},
		{ID: "a-agent-deny", AgentID: "a1", Target: "c.example.com", Effect: EffectDeny},
		{ID: "m-wildcard", Target: "*.example.com", Effect: EffectAllow},
		{ID: "b-precise", Target: "api.example.com", Effect: EffectAllow},
	}
	for _, r := range inputs {
		if _, err := store.Add(ctx, r); err != nil {
			t.Fatalf("add %s: %v", r.ID, err)
		}
	}

	first, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List #%d: %v", i, err)
		}
		if len(again) != len(first) {
			t.Fatalf("length changed between reads: %d vs %d", len(again), len(first))
		}
		for j := range first {
			if first[j].ID != again[j].ID {
				t.Fatalf("order changed at %d: %q vs %q", j, first[j].ID, again[j].ID)
			}
		}
	}
	// Agent 维度条目必须排在最前（判定顺序与列表顺序一致，便于排障）。
	if first[0].ID != "a-agent-deny" {
		t.Errorf("first rule = %q, want a-agent-deny (agent scope first)", first[0].ID)
	}
}

func TestPolicyStoreAddCopiesMethodsSlice(t *testing.T) {
	store := NewMemoryPolicyStore()
	methods := []string{"GET"}
	added, err := store.Add(context.Background(), PolicyRule{
		ID: "r1", Target: "api.example.com", Effect: EffectAllow, Methods: methods,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	// 调用方后续修改入参切片不得污染已存策略。
	methods[0] = "DELETE"
	list, _ := store.List(context.Background())
	if list[0].Methods[0] != "GET" {
		t.Errorf("stored methods = %v, want [GET] (must be a copy)", list[0].Methods)
	}
	added.Methods[0] = "PUT"
	list, _ = store.List(context.Background())
	if list[0].Methods[0] != "GET" {
		t.Errorf("stored methods mutated via returned copy: %v", list[0].Methods)
	}
}

// ===== 服务层 =====

func TestServiceAddRuleNormalizes(t *testing.T) {
	svc := NewService(ServiceConfig{})
	rule, err := svc.AddRule(context.Background(), PolicyRule{Target: "API.Example.com", Effect: EffectAllow})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if rule.Target != "api.example.com" {
		t.Errorf("target = %q, want api.example.com", rule.Target)
	}
	if rule.ID == "" {
		t.Error("rule id must be assigned")
	}
	if rule.CreatedAt.IsZero() {
		t.Error("created_at must be set")
	}
}

func TestServiceWithoutGatewayFailsClosed(t *testing.T) {
	// 网关未装配时必须返回 ErrUnavailable（→ 503），
	// 绝不能退化成「没有网关所以放行」。
	svc := NewService(ServiceConfig{})
	if _, err := svc.Proxy(context.Background(), EgressRequest{AgentID: "a1", URL: "https://x.example.com"}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Proxy without gateway error = %v, want ErrUnavailable", err)
	}
}

func TestServiceStatsWithoutStoreReturnsEmptyNotError(t *testing.T) {
	// 没有流量存储不是错误：控制台该显示「暂无数据」而不是一个报错分区。
	svc := NewService(ServiceConfig{})
	stats, err := svc.Stats(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TotalRequests != 0 || stats.Domains == nil {
		t.Errorf("stats = %+v, want empty non-nil domains", stats)
	}
	if stats.Window != time.Hour.String() {
		t.Errorf("window = %q, want %q", stats.Window, time.Hour.String())
	}
}

// ===== 网关 =====

// fakeTransport 是可控的下游：本机没有外网也能完整验证放行/阻断/审计三条路径。
type fakeTransport struct {
	mu     sync.Mutex
	calls  int
	status int
	body   string
	err    error
	// gateway 是在 RoundTrip 期间回调的钩子，用于断言「阻断时根本没发包」。
	onTrip func(*http.Request)
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls++
	status, body, err, hook := f.status, f.body, f.err, f.onTrip
	f.mu.Unlock()

	if hook != nil {
		hook(req)
	}
	if err != nil {
		return nil, err
	}
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       newReadCloser(body),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}, nil
}

// newReadCloser 把字符串包成 io.ReadCloser（响应体需要可关闭）。
func newReadCloser(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

func (f *fakeTransport) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newGatewayFixture(t *testing.T) (*HTTPGateway, *MemoryPolicyStore, *MemoryTrafficStore, *fakeTransport) {
	t.Helper()
	policy := NewMemoryPolicyStore()
	store := NewMemoryTrafficStore()
	transport := &fakeTransport{status: http.StatusOK, body: `{"ok":true}`}
	gw := NewHTTPGateway(GatewayConfig{Policy: policy, Store: store, Transport: transport})
	return gw, policy, store, transport
}

func TestGatewayBlocksBeforeConnecting(t *testing.T) {
	gw, _, store, transport := newGatewayFixture(t)

	resp, err := gw.Proxy(context.Background(), EgressRequest{
		AgentID: "a1", Method: http.MethodGet, URL: "https://evil.example.com/steal",
	})
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
	if resp == nil || resp.Record == nil {
		t.Fatal("blocked response must still carry a record (evidence chain)")
	}
	if transport.Calls() != 0 {
		t.Fatalf("transport calls = %d, want 0 — blocked egress must not open a connection", transport.Calls())
	}

	// 阻断也要落流量记录：只记放行等于给攻击尝试留盲区。
	records, err := store.Query(context.Background(), TrafficFilter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Allowed {
		t.Error("blocked record must have allowed=false")
	}
	if records[0].Domain != "evil.example.com" {
		t.Errorf("domain = %q, want evil.example.com", records[0].Domain)
	}
	if !strings.Contains(records[0].Reason, "default-deny") {
		t.Errorf("reason = %q, want it to mention default-deny", records[0].Reason)
	}
}

func TestGatewayAllowsAndRecordsTraffic(t *testing.T) {
	gw, policy, store, transport := newGatewayFixture(t)
	if _, err := policy.Add(context.Background(), PolicyRule{
		ID: "allow-api", Target: "api.example.com", Effect: EffectAllow,
	}); err != nil {
		t.Fatalf("add rule: %v", err)
	}

	resp, err := gw.Proxy(context.Background(), EgressRequest{
		AgentID: "a1", Method: http.MethodPost, URL: "https://api.example.com/v1/items",
		Body: []byte(`{"x":1}`), Headers: map[string]string{"X-Trace": "t1"},
	})
	if err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if string(resp.Body) != `{"ok":true}` {
		t.Errorf("body = %q", string(resp.Body))
	}
	if transport.Calls() != 1 {
		t.Fatalf("transport calls = %d, want 1", transport.Calls())
	}
	if resp.Record == nil || !resp.Record.Allowed {
		t.Fatalf("record = %+v, want allowed", resp.Record)
	}
	// 出站请求体 + 回传响应体都应计入字节数。
	if resp.Record.Bytes != int64(len(`{"x":1}`)+len(`{"ok":true}`)) {
		t.Errorf("bytes = %d, want request+response size", resp.Record.Bytes)
	}
	if resp.Record.RuleID != "allow-api" {
		t.Errorf("rule_id = %q, want allow-api", resp.Record.RuleID)
	}

	records, _ := store.Query(context.Background(), TrafficFilter{AgentID: "a1"})
	if len(records) != 1 || !records[0].Allowed {
		t.Fatalf("records = %+v, want one allowed record", records)
	}
}

func TestGatewayForwardsHeadersAndMethod(t *testing.T) {
	gw, policy, _, transport := newGatewayFixture(t)
	if _, err := policy.Add(context.Background(), PolicyRule{
		ID: "allow-all", Target: "*", Effect: EffectAllow,
	}); err != nil {
		t.Fatalf("add rule: %v", err)
	}

	var seenMethod, seenHeader string
	transport.onTrip = func(r *http.Request) {
		seenMethod = r.Method
		seenHeader = r.Header.Get("X-Custom")
	}

	if _, err := gw.Proxy(context.Background(), EgressRequest{
		AgentID: "a1", Method: "post", URL: "https://api.example.com/x",
		Headers: map[string]string{"X-Custom": "v"},
	}); err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	if seenMethod != http.MethodPost {
		t.Errorf("forwarded method = %q, want POST (lowercase input must be normalized)", seenMethod)
	}
	if seenHeader != "v" {
		t.Errorf("forwarded header = %q, want v", seenHeader)
	}
}

func TestGatewayRecordsTransportFailure(t *testing.T) {
	gw, policy, store, _ := newGatewayFixture(t)
	if _, err := policy.Add(context.Background(), PolicyRule{
		ID: "allow-all", Target: "*", Effect: EffectAllow,
	}); err != nil {
		t.Fatalf("add rule: %v", err)
	}
	// 用一个必然失败的 URL scheme 让 RoundTrip 报错。
	gw.client.Transport = &fakeTransport{err: errors.New("dial tcp: connection refused")}

	resp, err := gw.Proxy(context.Background(), EgressRequest{
		AgentID: "a1", Method: http.MethodGet, URL: "https://down.example.com/x",
	})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if errors.Is(err, ErrBlocked) {
		t.Fatal("transport failure must not be reported as a policy block")
	}
	if resp == nil || resp.Record == nil || resp.Record.Error == "" {
		t.Fatal("failed egress must still produce a record carrying the error")
	}
	records, _ := store.Query(context.Background(), TrafficFilter{})
	if len(records) != 1 || records[0].Error == "" {
		t.Fatalf("records = %+v, want one failed record", records)
	}
	if !records[0].Allowed {
		t.Error("a transport failure is an allowed-but-failed request, not a policy block")
	}
}

func TestGatewayRequiresAgentAndURL(t *testing.T) {
	gw, _, _, _ := newGatewayFixture(t)
	if _, err := gw.Proxy(context.Background(), EgressRequest{URL: "https://api.example.com"}); !errors.Is(err, ErrAgentRequired) {
		t.Errorf("missing agent error = %v, want ErrAgentRequired", err)
	}
	if _, err := gw.Proxy(context.Background(), EgressRequest{AgentID: "a1"}); !errors.Is(err, ErrURLRequired) {
		t.Errorf("missing url error = %v, want ErrURLRequired", err)
	}
}

func TestGatewayWithoutPolicyFailsClosed(t *testing.T) {
	gw := NewHTTPGateway(GatewayConfig{Transport: &fakeTransport{}})
	resp, err := gw.Proxy(context.Background(), EgressRequest{AgentID: "a1", URL: "https://api.example.com"})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("error = %v, want ErrUnavailable (fail-closed)", err)
	}
	if resp != nil {
		t.Errorf("resp = %+v, want nil", resp)
	}
}

func TestGatewayRecorderSeesBlockedEgress(t *testing.T) {
	// 审计必须能看到被拦下的请求 —— 攻击尝试就在这里面。
	var recorded []*TrafficRecord
	gw := NewHTTPGateway(GatewayConfig{
		Policy:    NewMemoryPolicyStore(),
		Transport: &fakeTransport{},
		Recorder: EgressRecorderFunc(func(_ context.Context, rec *TrafficRecord) error {
			recorded = append(recorded, rec)
			return nil
		}),
	})
	if _, err := gw.Proxy(context.Background(), EgressRequest{
		AgentID: "a1", URL: "https://c2.example.com/beacon",
	}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
	if len(recorded) != 1 {
		t.Fatalf("recorded = %d, want 1", len(recorded))
	}
	if recorded[0].Allowed {
		t.Error("recorded blocked event must have allowed=false")
	}
	if recorded[0].Domain != "c2.example.com" {
		t.Errorf("recorded domain = %q", recorded[0].Domain)
	}
}

func TestRecorderErrorDoesNotChangeGatewayResult(t *testing.T) {
	// 审计写失败只意味着留痕缺失，不该把一次成功的出站变成失败
	// （那会诱发调用方重试，产生重复的副作用）。
	gw := NewHTTPGateway(GatewayConfig{
		Policy:    NewMemoryPolicyStore(),
		Transport: &fakeTransport{status: 200, body: "ok"},
		Recorder: EgressRecorderFunc(func(context.Context, *TrafficRecord) error {
			return errors.New("audit backend down")
		}),
	})
	if _, err := gw.policy.Add(context.Background(), PolicyRule{ID: "a", Target: "*", Effect: EffectAllow}); err != nil {
		t.Fatalf("add rule: %v", err)
	}
	resp, err := gw.Proxy(context.Background(), EgressRequest{AgentID: "a1", URL: "https://api.example.com/x"})
	if err != nil {
		t.Fatalf("audit failure must not fail the egress: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// ===== 流量存储与统计 =====

func TestTrafficStoreQueryIsSortedAndLimited(t *testing.T) {
	store := NewMemoryTrafficStore()
	base := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	// 乱序写入，读取必须按时间升序返回。
	for _, offset := range []int{5, 1, 3, 2, 4, 0} {
		if err := store.Append(ctx, &TrafficRecord{
			ID:        "r" + itoa(offset),
			AgentID:   "a1",
			Domain:    "api.example.com",
			Allowed:   true,
			Timestamp: base.Add(time.Duration(offset) * time.Minute),
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	records, err := store.Query(ctx, TrafficFilter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(records) != 6 {
		t.Fatalf("records = %d, want 6", len(records))
	}
	for i := 1; i < len(records); i++ {
		if records[i].Timestamp.Before(records[i-1].Timestamp) {
			t.Fatalf("records not sorted at %d: %v before %v", i, records[i].Timestamp, records[i-1].Timestamp)
		}
	}

	// 超上限时保留**最近**的：排障看的是刚刚发生了什么。
	limited, err := store.Query(ctx, TrafficFilter{Limit: 2})
	if err != nil {
		t.Fatalf("Query limited: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limited records = %d, want 2", len(limited))
	}
	if limited[1].ID != "r5" {
		t.Errorf("last record = %q, want r5 (most recent kept)", limited[1].ID)
	}
}

func TestTrafficStoreQueryFilters(t *testing.T) {
	store := NewMemoryTrafficStore()
	ctx := context.Background()
	base := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	allowed := true
	blocked := false
	_ = blocked

	seed := []*TrafficRecord{
		{ID: "1", AgentID: "a1", Domain: "api.example.com", Allowed: true, Timestamp: base},
		{ID: "2", AgentID: "a1", Domain: "c2.evil.xyz", Allowed: false, Suspicious: true, Timestamp: base.Add(time.Minute)},
		{ID: "3", AgentID: "a2", Domain: "api.example.com", Allowed: true, Timestamp: base.Add(2 * time.Minute)},
	}
	for _, rec := range seed {
		if err := store.Append(ctx, rec); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	byAgent, _ := store.Query(ctx, TrafficFilter{AgentID: "a1"})
	if len(byAgent) != 2 {
		t.Errorf("by agent = %d, want 2", len(byAgent))
	}

	byAllowed, _ := store.Query(ctx, TrafficFilter{Allowed: &allowed})
	if len(byAllowed) != 2 {
		t.Errorf("by allowed = %d, want 2", len(byAllowed))
	}

	byDomain, _ := store.Query(ctx, TrafficFilter{Domain: "example.com"})
	if len(byDomain) != 2 {
		t.Errorf("by domain (subdomain should match) = %d, want 2", len(byDomain))
	}

	bySuspicious, _ := store.Query(ctx, TrafficFilter{OnlySuspicious: true})
	if len(bySuspicious) != 1 || bySuspicious[0].ID != "2" {
		t.Errorf("by suspicious = %+v, want record 2", bySuspicious)
	}

	since := base.Add(time.Minute)
	bySince, _ := store.Query(ctx, TrafficFilter{Since: &since})
	if len(bySince) != 2 {
		t.Errorf("by since = %d, want 2", len(bySince))
	}
}

func TestTrafficStoreAppendStoresCopy(t *testing.T) {
	store := NewMemoryTrafficStore()
	rec := &TrafficRecord{ID: "r1", AgentID: "a1", Domain: "api.example.com", Allowed: true, Timestamp: time.Now().UTC()}
	if err := store.Append(context.Background(), rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// 网关在写入后仍会引用同一条记录构造响应；改动不得污染历史。
	rec.Domain = "mutated.example.com"
	rec.Allowed = false

	records, _ := store.Query(context.Background(), TrafficFilter{})
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Domain != "api.example.com" || !records[0].Allowed {
		t.Errorf("stored record mutated: %+v", records[0])
	}
}

func TestTrafficStorePurgeBefore(t *testing.T) {
	store := NewMemoryTrafficStore()
	ctx := context.Background()
	base := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		_ = store.Append(ctx, &TrafficRecord{
			ID: "r" + itoa(i), AgentID: "a1", Domain: "api.example.com",
			Allowed: true, Timestamp: base.Add(time.Duration(i) * time.Hour),
		})
	}

	removed, err := store.PurgeBefore(ctx, base.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("PurgeBefore: %v", err)
	}
	if removed != 3 {
		t.Errorf("removed = %d, want 3", removed)
	}
	if store.Len() != 2 {
		t.Errorf("len = %d, want 2", store.Len())
	}

	// 零值 cutoff 是「什么都不删」，绝不能理解为「删全部」。
	removed, err = store.PurgeBefore(ctx, time.Time{})
	if err != nil {
		t.Fatalf("PurgeBefore(zero): %v", err)
	}
	if removed != 0 || store.Len() != 2 {
		t.Errorf("zero cutoff removed %d records, want 0 (len=%d)", removed, store.Len())
	}
}

func TestTrafficStoreStatsAggregatesByDomain(t *testing.T) {
	store := NewMemoryTrafficStore()
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

	seed := []*TrafficRecord{
		{ID: "1", AgentID: "a1", Domain: "api.example.com", Allowed: true, Bytes: 100, DurationMS: 10, Timestamp: now.Add(-time.Minute)},
		{ID: "2", AgentID: "a2", Domain: "api.example.com", Allowed: true, Bytes: 200, DurationMS: 30, Timestamp: now.Add(-2 * time.Minute)},
		{ID: "3", AgentID: "a1", Domain: "c2.evil.xyz", Allowed: false, Bytes: 0, Timestamp: now.Add(-3 * time.Minute)},
		{ID: "4", AgentID: "a1", Domain: "down.example.com", Allowed: true, Error: "timeout", Timestamp: now.Add(-4 * time.Minute)},
		// 窗口之外：不该被计入。
		{ID: "5", AgentID: "a1", Domain: "old.example.com", Allowed: true, Bytes: 9999, Timestamp: now.Add(-48 * time.Hour)},
	}
	for _, rec := range seed {
		if err := store.Append(ctx, rec); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	stats, err := store.Stats(ctx, 24*time.Hour, now)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TotalRequests != 4 {
		t.Errorf("total = %d, want 4 (out-of-window record excluded)", stats.TotalRequests)
	}
	if stats.AllowedRequests != 3 || stats.BlockedRequests != 1 {
		t.Errorf("allowed=%d blocked=%d, want 3/1", stats.AllowedRequests, stats.BlockedRequests)
	}
	if stats.FailedRequests != 1 {
		t.Errorf("failed = %d, want 1", stats.FailedRequests)
	}
	if stats.DistinctDomains != 3 {
		t.Errorf("distinct domains = %d, want 3", stats.DistinctDomains)
	}
	if stats.DistinctAgents != 2 {
		t.Errorf("distinct agents = %d, want 2", stats.DistinctAgents)
	}
	if stats.TotalBytes != 300 {
		t.Errorf("bytes = %d, want 300", stats.TotalBytes)
	}
	if stats.Window != (24 * time.Hour).String() {
		t.Errorf("window = %q, want %q", stats.Window, (24 * time.Hour).String())
	}

	// api.example.com 请求数最多，必须排第一。
	if len(stats.Domains) == 0 || stats.Domains[0].Domain != "api.example.com" {
		t.Fatalf("top domain = %+v, want api.example.com", stats.Domains)
	}
	if stats.Domains[0].AvgDurationMS != 20 {
		t.Errorf("avg duration = %d, want 20", stats.Domains[0].AvgDurationMS)
	}

	// 高风险 TLD 的域名必须进入可疑列表。
	if len(stats.SuspiciousDomains) == 0 {
		t.Fatal("c2.evil.xyz should be flagged as suspicious")
	}
	found := false
	for _, d := range stats.SuspiciousDomains {
		if d.Domain == "c2.evil.xyz" {
			found = true
			if len(d.Reasons) == 0 {
				t.Error("suspicious domain must explain why")
			}
		}
	}
	if !found {
		t.Errorf("suspicious domains = %+v, want c2.evil.xyz", stats.SuspiciousDomains)
	}
}

func TestTrafficStoreStatsOrderIsStable(t *testing.T) {
	// 同请求数的域名顺序必须稳定，否则控制台 Top 列表会「跳动」。
	store := NewMemoryTrafficStore()
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

	for _, domain := range []string{"z.example.com", "a.example.com", "m.example.com"} {
		if err := store.Append(ctx, &TrafficRecord{
			ID: domain, AgentID: "a1", Domain: domain, Allowed: true, Timestamp: now.Add(-time.Minute),
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	var baseline []string
	for i := 0; i < 20; i++ {
		stats, err := store.Stats(ctx, time.Hour, now)
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		got := make([]string, 0, len(stats.Domains))
		for _, d := range stats.Domains {
			got = append(got, d.Domain)
		}
		if i == 0 {
			baseline = got
			continue
		}
		for j := range baseline {
			if baseline[j] != got[j] {
				t.Fatalf("domain order changed at %d: %v vs %v", j, baseline, got)
			}
		}
	}
	if baseline[0] != "a.example.com" {
		t.Errorf("tie-break order = %v, want alphabetical", baseline)
	}
}

func TestTopLevelOfAndIPLiteral(t *testing.T) {
	if tld, ok := topLevelOf("c2.evil.xyz"); !ok || tld != "xyz" {
		t.Errorf("topLevelOf = %q,%v want xyz,true", tld, ok)
	}
	if _, ok := topLevelOf("localhost"); ok {
		t.Error("localhost has no tld; want ok=false")
	}
	if !isIPLiteral("169.254.169.254") {
		t.Error("169.254.169.254 must be detected as an IP literal")
	}
	if isIPLiteral("api.example.com") {
		t.Error("api.example.com must not be an IP literal")
	}
}

func TestSuspiciousReasonsExplainsEachHit(t *testing.T) {
	h := DefaultHeuristics()

	reasons := suspiciousReasons("c2.evil.xyz", 1, h)
	if len(reasons) != 1 || !strings.Contains(reasons[0], "high-risk tld") {
		t.Errorf("reasons = %v, want a high-risk-tld reason", reasons)
	}

	reasons = suspiciousReasons("api.example.com", h.HighFrequencyThreshold+1, h)
	if len(reasons) != 1 || !strings.Contains(reasons[0], "high frequency") {
		t.Errorf("reasons = %v, want a high-frequency reason", reasons)
	}

	reasons = suspiciousReasons("203.0.113.7", 1, h)
	if len(reasons) != 1 || !strings.Contains(reasons[0], "direct ip") {
		t.Errorf("reasons = %v, want a direct-ip reason", reasons)
	}

	// 正常域名不该被标记：误报泛滥会让这个功能被整体忽略。
	if reasons := suspiciousReasons("api.example.com", 3, h); len(reasons) != 0 {
		t.Errorf("reasons = %v, want none for a normal domain", reasons)
	}
}

func TestSuspiciousReasonsThresholdBoundary(t *testing.T) {
	// 阈值是「超过」而不是「达到」：边界值不该触发，否则文档与行为会不一致。
	h := DefaultHeuristics()
	if reasons := suspiciousReasons("api.example.com", h.HighFrequencyThreshold, h); len(reasons) != 0 {
		t.Errorf("reasons at threshold = %v, want none", reasons)
	}
}

// ===== 并发安全 =====

func TestPolicyStoreConcurrentEvaluateAndAdd(t *testing.T) {
	// 判定是每次出站都走的热路径，策略变更来自运维；两者并发是常态。
	store := NewMemoryPolicyStore()
	ctx := context.Background()
	if _, err := store.Add(ctx, PolicyRule{ID: "base", Target: "*.example.com", Effect: EffectAllow}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = store.Evaluate(ctx, "a1", http.MethodGet, "https://api.example.com/x")
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _ = store.Add(ctx, PolicyRule{
					Target: "d" + itoa(n) + itoa(j) + ".example.com", Effect: EffectAllow,
				})
			}
		}(i)
	}
	wg.Wait()

	// 并发写入后判定仍然必须给出确定结论（命中白名单）。
	got, err := store.Evaluate(ctx, "a1", http.MethodGet, "https://api.example.com/x")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !got.Allowed {
		t.Errorf("decision = %+v, want allowed", got)
	}
}

func TestTrafficStoreConcurrentAppendAndQuery(t *testing.T) {
	store := NewMemoryTrafficStore()
	ctx := context.Background()
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = store.Append(ctx, &TrafficRecord{
					ID: "r" + itoa(n) + "-" + itoa(j), AgentID: "a" + itoa(n),
					Domain: "api.example.com", Allowed: true, Timestamp: time.Now().UTC(),
				})
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = store.Query(ctx, TrafficFilter{Limit: 10})
				_, _ = store.Stats(ctx, time.Hour, time.Now().UTC())
			}
		}()
	}
	wg.Wait()

	if store.Len() != 800 {
		t.Errorf("len = %d, want 800", store.Len())
	}
}

func TestMemoryTrafficStoreCapsRecords(t *testing.T) {
	// 出站记录的写入频率远高于人工操作，必须有容量上限，
	// 且超限时丢弃**最旧**的记录，不能静默丢新的。
	store := NewMemoryTrafficStore()
	ctx := context.Background()
	for i := 0; i < maxTrafficRecords+500; i++ {
		_ = store.Append(ctx, &TrafficRecord{
			ID: "r" + itoa(i), AgentID: "a1", Domain: "api.example.com",
			Allowed: true, Timestamp: time.Now().UTC(),
		})
	}
	if store.Len() > maxTrafficRecords {
		t.Errorf("len = %d, want <= %d", store.Len(), maxTrafficRecords)
	}
	if store.Dropped() == 0 {
		t.Error("dropped counter must reflect discarded records (no silent data loss)")
	}
	// 最新写入的记录必须还在。
	records, _ := store.Query(ctx, TrafficFilter{Limit: 1})
	if len(records) != 1 || records[0].ID != "r"+itoa(maxTrafficRecords+499) {
		t.Errorf("newest record not retained: %+v", records)
	}
}
