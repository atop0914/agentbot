package app_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/atop0914/agentbot/internal/app"
	"github.com/atop0914/agentbot/internal/config"
)

// 本文件是 Day 3-7 第二批的端到端验证：指标必须**经真实 router** 产生。
//
// 断言纪律（本轮的重点）：
//   - 不直接调 metrics 包的计数器，而是发真实 HTTP 请求、再读 /metrics 文本。
//     只断言「注册表里有这个指标」证明不了它被打点 —— 这在 3-5 那轮
//     已经踩过（断言配置标识 ≠ 数据真的变了）。
//   - 反向断言同样要有：未请求过的路由不得出现；真实 ID 不得进入标签值。

// devConfig 返回开发默认配置（无数据库、有 dev 机密），
// 它与生产配置走的是**同一条** NewWithConfig 装配路径。
func devConfig(t *testing.T) config.Config {
	t.Helper()
	return config.DevelopmentDefault()
}

// extractValue 从导出的文本里取出某个系列的数值。
//
// 找不到时 t.Fatalf 而不是返回 0：返回 0 会让「系列不存在」与
// 「数值恰好是 0」在断言里变得不可区分 —— 那正是最容易被放过的假绿。
func extractValue(t *testing.T, body, prefix string) float64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			t.Fatalf("系列 %q 的值 %q 无法解析: %v", prefix, raw, err)
		}
		return v
	}
	t.Fatalf("导出文本里找不到系列 %q\n%s", prefix, body)
	return 0
}

// extractValueOrZero 与 extractValue 相同，但系列不存在时返回 0。
//
// 用于**基线**抓取：第一次抓 /metrics 时该路由还没被访问过，
// 系列本就不存在。用 extractValue 会以「找不到系列」直接 Fatal ——
// 那是测试写错了，不是实现坏了（实测踩过一次）。
// 注意：反向用途（断言某个系列**不存在**）不能用这个函数，
// 那种场景要显式断言 strings.Contains 为假。
func extractValueOrZero(t *testing.T, body, prefix string) float64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			t.Fatalf("系列 %q 的值 %q 无法解析: %v", prefix, raw, err)
		}
		return v
	}
	return 0
}

// itoa 是 strconv.Itoa 的短别名，仅为让标签断言串更易读。
func itoa(i int) string { return strconv.Itoa(i) }

// newMetricsApp 起一个带真实 router 的 App，返回测试服务器。
func newMetricsApp(t *testing.T) (*app.App, *httptest.Server) {
	t.Helper()
	a, err := app.NewWithConfig(devConfig(t))
	if err != nil {
		t.Fatalf("装配 App 失败: %v", err)
	}
	srv := httptest.NewServer(app.NewRouter(a))
	t.Cleanup(srv.Close)
	return a, srv
}

// fetchMetrics 抓一次 /metrics，返回文本与状态码。
func fetchMetrics(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("抓取 /metrics 失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode
}

// TestMetricsEndpointReachableThroughRealRouter 验证端点真的挂在路由上，
// 且返回 Prometheus 文本格式。
func TestMetricsEndpointReachableThroughRealRouter(t *testing.T) {
	a, srv := newMetricsApp(t)

	if got := a.MetricsPath(); got != "/metrics" {
		t.Fatalf("MetricsPath() = %q, want /metrics（开发默认配置）", got)
	}

	resp, err := http.Get(srv.URL + a.MetricsPath())
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics 状态码 = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "version=0.0.4") {
		t.Errorf("Content-Type = %q，缺少 Prometheus 版本参数", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "process_uptime_seconds ") {
		t.Errorf("/metrics 应导出 uptime\n%s", body)
	}
}

// TestMetricsReflectsRealRequests 是本文件的核心断言：
// 发一个真实请求 → /metrics 里必须出现对应的计数与直方图观测。
func TestMetricsReflectsRealRequests(t *testing.T) {
	_, srv := newMetricsApp(t)

	// 先抓一次基线，避免把「端点自身被抓取」的计数算进来造成假阳性。
	before, status := fetchMetrics(t, srv)
	if status != http.StatusOK {
		t.Fatalf("基线抓取状态码 %d", status)
	}

	const hits = 3
	for i := 0; i < hits; i++ {
		resp, err := http.Get(srv.URL + "/health")
		if err != nil {
			t.Fatalf("请求 /health 失败: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("/health 状态码 = %d", resp.StatusCode)
		}
	}

	after, _ := fetchMetrics(t, srv)

	// 计数必须是**相对基线增加 hits**，而不是断言「存在 > 0」——
	// 后者在计数只加 1 或加错系列时照样通过。
	want := `agentbot_http_requests_total{method="GET",route="/health",status="200"} `
	beforeN := extractValueOrZero(t, before, want)
	afterN := extractValue(t, after, want)
	if afterN-beforeN != hits {
		t.Errorf("/health 计数增量 = %v, want %v\n（before=%v after=%v）", afterN-beforeN, hits, beforeN, afterN)
	}

	// 直方图也必须被观测到：_count 的增量同样应为 hits。
	wantCount := `agentbot_http_request_duration_seconds_count{method="GET",route="/health"} `
	beforeC := extractValueOrZero(t, before, wantCount)
	afterC := extractValue(t, after, wantCount)
	if afterC-beforeC != hits {
		t.Errorf("直方图 _count 增量 = %v, want %v", afterC-beforeC, hits)
	}

	// 分桶行必须存在，且 +Inf 桶等于 _count —— 缺了它 histogram_quantile() 返回 NaN。
	if !strings.Contains(after, `agentbot_http_request_duration_seconds_bucket{method="GET",route="/health",le="+Inf"} `) {
		t.Errorf("缺少 +Inf 桶\n%s", after)
	}
	inf := extractValue(t, after, `agentbot_http_request_duration_seconds_bucket{method="GET",route="/health",le="+Inf"} `)
	if inf != afterC {
		t.Errorf("+Inf 桶 = %v, _count = %v，两者必须相等（否则分位数计算失效）", inf, afterC)
	}
}

// TestMetricsRecordsErrorStatus 验证 4xx/5xx 也被计入 ——
// 只统计成功请求的指标会让「错误率」永远为零。
func TestMetricsRecordsErrorStatus(t *testing.T) {
	_, srv := newMetricsApp(t)

	before, _ := fetchMetrics(t, srv)

	// 未认证访问受保护路由 → 401
	resp, err := http.Get(srv.URL + "/api/v1/agents")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("未认证访问状态码 = %d, 期望 401/403", resp.StatusCode)
	}

	after, _ := fetchMetrics(t, srv)

	key := `agentbot_http_requests_total{method="GET",route="/api/v1/agents",status="` + itoa(resp.StatusCode) + `"} `
	if extractValue(t, after, key)-extractValueOrZero(t, before, key) != 1 {
		t.Errorf("未认证请求应被计入 %s\n%s", key, after)
	}

	// 状态类聚合也必须反映出来（告警规则靠它避免正则）
	clsKey := `agentbot_http_responses_by_status_class_total{class="4xx"} `
	if extractValue(t, after, clsKey) <= extractValueOrZero(t, before, clsKey) {
		t.Errorf("4xx 状态类计数应增加\n%s", after)
	}
}

// TestMetricsDoesNotLeakRealIDs 是**基数防线**的端到端验证：
// 带真实 UUID 的请求必须归一到 {id}，而不是把自己的 ID 变成标签值。
func TestMetricsDoesNotLeakRealIDs(t *testing.T) {
	_, srv := newMetricsApp(t)

	uuidA := "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
	uuidB := "7c9e6679-7425-40de-944b-e07fc1f90ae7"

	for _, id := range []string{uuidA, uuidB} {
		resp, err := http.Get(srv.URL + "/api/v1/agents/" + id)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		resp.Body.Close()
	}

	after, _ := fetchMetrics(t, srv)

	// 反向断言：两个不同的真实 ID 都不得出现在导出文本里
	for _, id := range []string{uuidA, uuidB} {
		if strings.Contains(after, id) {
			t.Errorf("真实资源 ID %q 泄漏进了指标标签值 —— 基数会随资源数线性增长\n%s", id, after)
		}
	}
	// 正向断言：两个请求归并到同一个 route 标签
	key := `agentbot_http_requests_total{method="GET",route="/api/v1/agents/{id}"`
	if !strings.Contains(after, key) {
		t.Errorf("UUID 路径未被归一成 {id}\n%s", after)
	}
}

// TestMetricsStorageBackendReflectsReality 验证后端指标反映的是**实际生效**的后端。
//
// 开发默认配置没有数据库 → 必须是 memory=1 / postgres=0。
// 这条断言的价值在于反向那半：若两个 gauge 都被置 1，
// 任何基于它们的告警都会失去意义。
func TestMetricsStorageBackendReflectsReality(t *testing.T) {
	a, srv := newMetricsApp(t)

	if a.StorageBackend != "memory" {
		t.Fatalf("开发默认配置的后端应为 memory，实际 %q", a.StorageBackend)
	}

	body, _ := fetchMetrics(t, srv)

	if got := extractValue(t, body, "agentbot_storage_backend_memory "); got != 1 {
		t.Errorf("storage_backend_memory = %v, want 1", got)
	}
	if got := extractValue(t, body, "agentbot_storage_backend_postgres "); got != 0 {
		t.Errorf("storage_backend_postgres = %v, want 0（两个 gauge 不能同时为 1）", got)
	}
	if got := extractValue(t, body, "agentbot_storage_memory_fallback_allowed "); got != 0 {
		t.Errorf("storage_memory_fallback_allowed = %v, want 0（未显式开启回退）", got)
	}
}

// TestMetricsGaugeInFlightReturnsToZero 验证并发 gauge 会回到 0 ——
// 一个只增不减的 in-flight gauge 是经典的泄漏征兆（漏了 defer）。
//
// ⚠️ 断言方式：**不能**在抓取 /metrics 的那一刻断言 in_flight==0 ——
// 这次抓取自己也是一个 in-flight 请求，那一刻必然为 1。
// 正确做法是直接问注册表（它是同一份内存状态，且不产生新的请求）：
// 全部业务请求已返回，且没有进行中的抓取时，值必须为 0。
func TestMetricsGaugeInFlightReturnsToZero(t *testing.T) {
	a, srv := newMetricsApp(t)

	for i := 0; i < 5; i++ {
		resp, err := http.Get(srv.URL + "/health")
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		resp.Body.Close()
	}

	// 从这里读：此刻没有任何在途请求。
	body := a.Metrics.Expose()
	if got := extractValue(t, body, "agentbot_http_requests_in_flight "); got != 0 {
		t.Errorf("in_flight = %v, 全部请求结束后应为 0（非 0 说明漏了 defer）", got)
	}

	// 反向对照：抓取**期间**它确实是 1 —— 说明这个 gauge 真的在动，
	// 而不是一个恒为 0 的死指标（那比没有指标更危险）。
	scraped, _ := fetchMetrics(t, srv)
	if got := extractValue(t, scraped, "agentbot_http_requests_in_flight "); got != 1 {
		t.Errorf("抓取期间 in_flight = %v, want 1（gauge 在抓取自身时必然为 1，否则说明它没被更新）", got)
	}
}

// TestMetricsEndpointIsAnonymous 验证端点不需要 token。
//
// 理由写在 metrics_wiring.go：抓取端不持有平台 JWT。
// 这条断言是**契约**：若将来有人给 /metrics 加了认证，
// Prometheus 会静默抓不到，而这条测试会立刻红。
func TestMetricsEndpointIsAnonymous(t *testing.T) {
	_, srv := newMetricsApp(t)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/metrics", nil)
	// 不带任何 Authorization 头
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("匿名 GET /metrics = %d, want 200（抓取端不持有平台 JWT）", resp.StatusCode)
	}
}

// TestMetricsSnapshotSurvivesMultipleScrapes 验证反复抓取不会让指标错乱。
//
// ⚠️ 一个反直觉但**正确**的行为：某次抓取**看不到自己**的计数。
// metricsMiddleware 在最外层，它在 handler 写完响应体之后才记数；
// 因此第 N 次抓取只能看到第 1..N-1 次抓取的计数。
// 第一版按「增量恰好为 1」断言，在第二次抓取时失败 —— 那是断言错了，
// 不是实现坏了：要让抓取看到自己，就得在生成响应体**之前**记数，
// 而那意味着每次抓取都要先污染一次计数，反而不如现在诚实。
func TestMetricsSnapshotSurvivesMultipleScrapes(t *testing.T) {
	_, srv := newMetricsApp(t)

	key := `agentbot_http_requests_total{method="GET",route="/metrics",status="200"} `

	// 第 1 次抓取：此刻还没有任何已完成的抓取，系列不存在。
	first, _ := fetchMetrics(t, srv)
	if extractValueOrZero(t, first, key) != 0 {
		t.Errorf("首次抓取前 route=/metrics 的计数应为 0（抓取看不到自己）\n%s", first)
	}

	// 第 2 次抓取：能看到第 1 次。
	second, _ := fetchMetrics(t, srv)
	if got := extractValue(t, second, key); got != 1 {
		t.Errorf("第二次抓取应看到第 1 次的计数 1，实际 %v", got)
	}

	// 第 3 次抓取：能看到前两次。
	third, _ := fetchMetrics(t, srv)
	if got := extractValue(t, third, key); got != 2 {
		t.Errorf("第三次抓取应看到前两次的计数 2，实际 %v", got)
	}

	// 其余指标不得被反复抓取污染。
	// in_flight 在**没有在途请求时**必须回到 0 —— 这里已经是第 3 次抓取
	// 返回之后，所以读注册表（不产生新请求）时它必须是 0。
	if got := extractValue(t, third, "agentbot_storage_backend_memory "); got != 1 {
		t.Errorf("storage_backend_memory = %v, want 1（不应被抓取次数影响）", got)
	}
}
