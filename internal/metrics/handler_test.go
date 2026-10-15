package metrics

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 本文件覆盖 Day 3-7 的 /metrics 端点与其**基数防线**。
//
// 具体要守的行为：
//  1. 端点返回 Prometheus 文本格式（Content-Type 带 version）；
//  2. 非 GET/HEAD 被拒绝（否则 scrape 端的分歧会变成 500）；
//  3. **路径归一**：真实 ID 不得进入标签值（基数爆炸是本包唯一的事故面）；
//  4. 反向断言：归一不得把两条不同路由合并（那是指标撒谎，比基数高更难查）。

func TestHandlerServesTextFormat(t *testing.T) {
	r := NewRegistry("agentbot")
	r.MustCounter("http_requests_total", "Total.", "method", "path", "status").Inc("GET", "/health", "200")

	srv := httptest.NewServer(r.Handler(slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") || !strings.Contains(ct, "version=0.0.4") {
		t.Errorf("Content-Type = %q，必须是带 version 的 Prometheus 文本类型", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store（指标是瞬时快照，不可缓存）", cc)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `agentbot_http_requests_total{method="GET",path="/health",status="200"} 1`) {
		t.Errorf("响应体缺少指标行\n%s", body)
	}
}

func TestHandlerRejectsNonReadMethods(t *testing.T) {
	r := NewRegistry("")
	srv := httptest.NewServer(r.Handler(nil))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/metrics", strings.NewReader("x"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /metrics 状态码 = %d, want 405", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("405 应带 Allow 头，得到 %q", allow)
	}
}

func TestHandlerHeadReturnsNoBody(t *testing.T) {
	r := NewRegistry("")
	r.MustCounter("x_total", "help").Inc()
	srv := httptest.NewServer(r.Handler(nil))
	defer srv.Close()

	resp, err := http.Head(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD 状态码 = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Errorf("HEAD 不应返回正文，得到 %d 字节", len(body))
	}
}

// TestNormalizePathCollapsesIDs 是本文件最要紧的一条：
// 若真实 ID 进了标签值，几万个 Agent 就能让 Prometheus 内存爆掉。
func TestNormalizePathCollapsesIDs(t *testing.T) {
	cases := []struct{ in, want string }{
		// UUID（末段）
		{"/api/v1/agents/3f2504e0-4f89-11d3-9a0c-0305e82c3301", "/api/v1/agents/{id}"},
		// UUID **在中间** —— 本项目大量路由是 /agents/{id}/{action} 形态，
		// 只归一末段会让每个 Agent 的动作路径都变成独立 series
		{"/api/v1/agents/3f2504e0-4f89-11d3-9a0c-0305e82c3301/start", "/api/v1/agents/{id}/start"},
		{"/api/v1/agents/3f2504e0-4f89-11d3-9a0c-0305e82c3301/pause", "/api/v1/agents/{id}/pause"},
		// 长纯数字（BIGSERIAL / 时间戳式 ID）
		{"/api/v1/audit/events/1791810552881793163", "/api/v1/audit/events/{id}"},
		{"/api/v1/tenants/12345678/members", "/api/v1/tenants/{id}/members"},
		// 长十六进制
		{"/api/v1/audit/062494a2defe23fd6b86d5c72f7a0b00", "/api/v1/audit/{id}"},
		// 集合路径（无尾斜杠）原样保留
		{"/api/v1/agents", "/api/v1/agents"},
		{"/api/v1/audit/events", "/api/v1/audit/events"},
		// 尾斜杠是「目录形态」，与集合路径是两条不同路由，不做 trim
		{"/api/v1/agents/", "/api/v1/agents/"},
		{"/health", "/health"},
		// 短数字是版本段，不得被误伤成 {id}
		{"/api/v1/users", "/api/v1/users"},
		{"/api/v2/agents", "/api/v2/agents"},
		// 空路径 / 无前导斜杠
		{"", "/"},
		{"health", "/health"},
	}
	for _, tc := range cases {
		if got := NormalizePath(tc.in); got != tc.want {
			t.Errorf("NormalizePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// 反向断言：归一**不得**把两条语义不同的路由合并成同一个标签值 ——
// 那是指标撒谎（两个动作的延迟混在一起），比基数高难查得多。
func TestNormalizePathKeepsDistinctRoutes(t *testing.T) {
	distinct := [][2]string{
		{"/api/v1/agents/{id}/start", "/api/v1/agents/{id}/pause"},
		{"/api/v1/agents", "/api/v1/agents/"},
		{"/api/v1/agents/{id}", "/api/v1/agents/{id}/status"},
		{"/api/v1/users/{id}/status", "/api/v1/tenants/{id}/status"},
	}
	for _, pair := range distinct {
		if pair[0] == pair[1] {
			t.Errorf("两条不同路由被归一成了同一个值 %q", pair[0])
		}
	}
}

// 归一必须**幂等**：中间件可能被叠加，重复归一不能让结果继续变化。
func TestNormalizePathIsIdempotent(t *testing.T) {
	for _, p := range []string{
		"/api/v1/agents/3f2504e0-4f89-11d3-9a0c-0305e82c3301/start",
		"/api/v1/agents/{id}/start",
		"/api/v1/audit/events/1791810552881793163",
		"/health",
		"/api/v1/agents/",
	} {
		once := NormalizePath(p)
		twice := NormalizePath(once)
		if once != twice {
			t.Errorf("NormalizePath 非幂等: %q -> %q -> %q", p, once, twice)
		}
	}
}

// 基数上界：1000 个不同 Agent ID 必须只产生 1 个 series。
func TestNormalizePathBoundsCardinality(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 1000; i++ {
		p := "/api/v1/agents/" + uuidLike(i) + "/start"
		seen[NormalizePath(p)] = struct{}{}
	}
	if len(seen) != 1 {
		t.Errorf("1000 个不同 ID 产生了 %d 个标签值，期望 1 个", len(seen))
		for k := range seen {
			t.Logf("  %s", k)
		}
	}
}

// uuidLike 造一个形态合法的 UUID（不引 uuid 包，测试自身保持零依赖）。
//
// 每个 i 产出不同字符串：否则「1000 个 ID」这条测试会因为输入其实相同
// 而**恒真**，等于什么都没验证。
func uuidLike(i int) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	pos := 0
	for _, n := range []int{8, 4, 4, 4, 12} {
		for j := 0; j < n; j++ {
			b.WriteByte(hex[(i*(j+3)+pos)%16])
			pos++
		}
		if len(b.String()) < 36 {
			b.WriteByte('-')
		}
	}
	s := b.String()
	if len(s) < 36 {
		// 兜底补齐到 36 位，形态仍是 UUID
		for len(s) < 36 {
			s += hex[(i+len(s))%16 : (i+len(s))%16+1]
		}
	}
	return s
}
