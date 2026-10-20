package metrics

import (
	"strings"
	"testing"
	"time"
)

// 本文件是 Day 3-7 第一批的测试：指标注册表与 Prometheus 文本导出。
//
// 断言纪律（从前面几轮继承）：
//   - 不只断言「导出成功」，要断言**具体的行**（含标签转义、+Inf 桶、累积语义）；
//   - 反向断言同样要写（不存在的指标不得出现、非法名必须被拒绝）；
//   - 凡是「看起来绿」的断言都要能被证伪 —— 例如计数用精确值而非「> 0」。

func TestCounterExportExact(t *testing.T) {
	r := NewRegistry("agentbot")
	c := r.MustCounter("http_requests_total", "Total HTTP requests.", "method", "path", "status")

	c.Inc("GET", "/health", "200")
	c.Inc("GET", "/health", "200")
	c.Inc("POST", "/api/v1/agents", "201")

	out := r.Expose()

	// 精确值断言：只写「包含 agentbot_http_requests_total」等于没测 ——
	// 计数错成 1 或 3 时那条断言照样通过。
	want := `agentbot_http_requests_total{method="GET",path="/health",status="200"} 2`
	if !strings.Contains(out, want) {
		t.Fatalf("导出缺少精确系列 %q\n---\n%s", want, out)
	}
	want2 := `agentbot_http_requests_total{method="POST",path="/api/v1/agents",status="201"} 1`
	if !strings.Contains(out, want2) {
		t.Fatalf("导出缺少精确系列 %q\n---\n%s", want2, out)
	}
	if !strings.Contains(out, "# TYPE agentbot_http_requests_total counter") {
		t.Error("缺少 TYPE 行")
	}
	if !strings.Contains(out, "# HELP agentbot_http_requests_total") {
		t.Error("缺少 HELP 行")
	}
}

func TestExposeIsStableAcrossCalls(t *testing.T) {
	r := NewRegistry("ns")
	c := r.MustCounter("c_total", "help", "a")
	for _, v := range []string{"x", "y", "z", "w", "q", "m", "n", "p"} {
		c.Inc(v)
	}

	// process_uptime_seconds 天然随时间推进，比较前必须剔除 —— 否则这条
	// 测试会以「uptime 变了」这种无关原因红掉，而它想守的是
	// 「指标系列的导出顺序与内容稳定」。
	stable := func() string {
		var kept []string
		for _, line := range strings.Split(r.Expose(), "\n") {
			if strings.HasPrefix(line, "process_uptime_seconds ") {
				continue
			}
			kept = append(kept, line)
		}
		return strings.Join(kept, "\n")
	}

	first := stable()
	for i := 0; i < 20; i++ {
		if got := stable(); got != first {
			t.Fatalf("第 %d 次导出与首次不一致 —— 导出顺序不稳定，抓取结果无法 diff\n---first---\n%s\n---got---\n%s",
				i+1, first, got)
		}
	}
	// 反向断言：uptime 这一行**必须**在，剔除只是比较手法，不是它不存在
	if !strings.Contains(r.Expose(), "process_uptime_seconds ") {
		t.Error("process_uptime_seconds 应始终存在")
	}
}

// 导出顺序由「首次出现顺序」决定，而不是字典序 —— 这样新增系列时
// 已有系列的相对顺序不变（diff 里只多不少），便于按文本做回归对比。
func TestLabelSeriesOrderIsInsertionOrder(t *testing.T) {
	r := NewRegistry("")
	c := r.MustCounter("o_total", "help", "k")
	for _, v := range []string{"zebra", "alpha", "middle"} {
		c.Inc(v)
	}
	out := r.Expose()
	iz := strings.Index(out, `k="zebra"`)
	ia := strings.Index(out, `k="alpha"`)
	im := strings.Index(out, `k="middle"`)
	if iz < 0 || ia < 0 || im < 0 {
		t.Fatalf("三个系列都应导出\n%s", out)
	}
	if !(iz < ia && ia < im) {
		t.Errorf("应按首次出现顺序导出（zebra < alpha < middle），实际 zebra=%d alpha=%d middle=%d\n%s",
			iz, ia, im, out)
	}
}

func TestHistogramCumulativeBucketsAndInf(t *testing.T) {
	r := NewRegistry("agentbot")
	h := r.MustHistogram("http_request_duration_seconds", "Latency.", []float64{0.01, 0.1, 1}, "path")

	h.Observe(0.005, "/health") // <=0.01, <=0.1, <=1
	h.Observe(0.05, "/health")  // <=0.1, <=1
	h.Observe(0.5, "/health")   // <=1
	h.Observe(3.0, "/health")   // 无桶，只进 _count/_sum

	out := r.Expose()

	cases := []struct{ want string }{
		{`agentbot_http_request_duration_seconds_bucket{path="/health",le="0.01"} 1`},
		{`agentbot_http_request_duration_seconds_bucket{path="/health",le="0.1"} 2`},
		{`agentbot_http_request_duration_seconds_bucket{path="/health",le="1"} 3`},
		// +Inf 桶必须等于 _count（4 次观测，含那个超出所有桶的 3.0）。
		// 缺了它 histogram_quantile() 会返回 NaN —— 这是最容易被漏掉的一行。
		{`agentbot_http_request_duration_seconds_bucket{path="/health",le="+Inf"} 4`},
		{`agentbot_http_request_duration_seconds_count{path="/health"} 4`},
	}
	for _, tc := range cases {
		if !strings.Contains(out, tc.want) {
			t.Errorf("导出缺少 %q\n---\n%s", tc.want, out)
		}
	}

	// _sum 用最短往返表示，不能因为格式化丢精度（0.005+0.05+0.5+3 = 3.555）
	if !strings.Contains(out, `agentbot_http_request_duration_seconds_sum{path="/health"} 3.555`) {
		t.Errorf("_sum 数值不正确\n---\n%s", out)
	}
	if !strings.Contains(out, "# TYPE agentbot_http_request_duration_seconds histogram") {
		t.Error("缺少 histogram TYPE 行")
	}
}

func TestObserveDurationUsesSeconds(t *testing.T) {
	r := NewRegistry("")
	h := r.MustHistogram("d_seconds", "help", []float64{0.5, 1.5})
	h.ObserveDuration(1200 * time.Millisecond)

	// 1200ms = 1.2s，应落在 le=1.5 桶内而**不**落在 le=0.5。
	out := r.Expose()
	if !strings.Contains(out, `d_seconds_bucket{le="0.5"} 0`) {
		t.Errorf("1.2s 不该落进 0.5 桶\n%s", out)
	}
	if !strings.Contains(out, `d_seconds_bucket{le="1.5"} 1`) {
		t.Errorf("1.2s 应落进 1.5 桶\n%s", out)
	}
	if !strings.Contains(out, "d_seconds_sum 1.2") {
		t.Errorf("_sum 应为 1.2 秒\n%s", out)
	}
}

// Observed==0 的标签组合不导出任何 _bucket 行 —— 这是刻意的：
// 一条「全 0 的桶」与「从未观测过」语义不同，导出前者的空系列会让
// histogram_quantile 在无数据时返回 0 而不是 NaN。
func TestHistogramUnobservedLabelAbsent(t *testing.T) {
	r := NewRegistry("")
	h := r.MustHistogram("h_seconds", "help", []float64{1}, "route")
	h.Observe(0.5, "/a")

	out := r.Expose()
	if strings.Contains(out, `route="/b"`) {
		t.Errorf("从未观测过的标签组合不该出现在导出里\n%s", out)
	}
	if !strings.Contains(out, `route="/a"`) {
		t.Errorf("观测过的标签组合应出现在导出里\n%s", out)
	}
}

func TestLabelValueEscaping(t *testing.T) {
	r := NewRegistry("")
	c := r.MustCounter("esc_total", "help", "v")
	// 标签值里出现反斜杠、双引号、换行都是合法的（例如原始 request path
	// 带引号）。不转义会产出语法非法的文本 —— 抓取端**整轮**失败，
	// 而不是丢这一行。
	c.Inc("a\\b")
	c.Inc("say \"hi\"")
	c.Inc("line1\nline2")

	out := r.Expose()

	if !strings.Contains(out, `v="a\\b"`) {
		t.Errorf("反斜杠未转义\n%s", out)
	}
	if !strings.Contains(out, `v="say \"hi\""`) {
		t.Errorf("双引号未转义\n%s", out)
	}
	if !strings.Contains(out, `v="line1\nline2"`) {
		t.Errorf("换行未转义\n%s", out)
	}
	// 反向断言：真实的换行符不得出现在文件里（那会切断 series 行）
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.HasPrefix(line, "line2") {
			t.Fatalf("标签值里的换行没有被折叠，导出的文本被切成了两行：%q", line)
		}
	}
}

func TestLabelValuesDoNotCollideAcrossSeparators(t *testing.T) {
	// 标签值里含逗号/等号是合法的（例如 path 的查询串）。
	// 若用逗号拼 key，这两组会撞成同一个 series —— 静默串指标。
	r := NewRegistry("")
	c := r.MustCounter("collide_total", "help", "a", "b")
	c.Inc("x,y", "z")
	c.Inc("x", "y,z")

	if got := c.Value("x,y", "z"); got != 1 {
		t.Errorf("Value(x,y / z) = %v, want 1", got)
	}
	if got := c.Value("x", "y,z"); got != 1 {
		t.Errorf("Value(x / y,z) = %v, want 1", got)
	}

	out := r.Expose()
	if c1 := strings.Count(out, `a="x"`); c1 != 1 {
		t.Errorf("第二组标签应恰好导出一次，实际 %d 次\n%s", c1, out)
	}
	if !strings.Contains(out, `a="x,y",b="z"`) || !strings.Contains(out, `a="x",b="y,z"`) {
		t.Errorf("两组标签应各自独立导出\n%s", out)
	}
}

func TestRejectsInvalidMetricName(t *testing.T) {
	r := NewRegistry("agentbot")
	for _, bad := range []string{"", "has-dash", "has space", "1leading_digit", "has.dot", "中文"} {
		if _, err := r.NewCounter(bad, "help"); err == nil {
			t.Errorf("指标名 %q 应被拒绝", bad)
		}
	}
	// 合法名（含下划线与冒号，冒号是 Prometheus 允许的记录规则字符）
	for _, good := range []string{"ok_total", "a_b_c", "ns:name", "with_0digit"} {
		if _, err := r.NewCounter(good, "help"); err != nil {
			t.Errorf("指标名 %q 应被接受，却报错 %v", good, err)
		}
	}
}

func TestRejectsSameNameDifferentLabels(t *testing.T) {
	r := NewRegistry("n")
	if _, err := r.NewCounter("dup_total", "help", "a"); err != nil {
		t.Fatalf("首次注册失败: %v", err)
	}
	// 同名同标签 → 幂等返回同一个实例（装配层可能重复注册）
	again, err := r.NewCounter("dup_total", "help", "a")
	if err != nil {
		t.Fatalf("幂等注册应成功: %v", err)
	}
	if again.Value() != 0 {
		t.Error("幂等注册应返回同一个实例")
	}
	// 同名**不同**标签 → 必须报错，否则导出文本里同一 series 会出现两种标签集
	if _, err := r.NewCounter("dup_total", "help", "b"); err == nil {
		t.Error("同名不同标签应被拒绝")
	}
}

func TestNilSafety(t *testing.T) {
	// 装配失败时调用方可能拿到 nil 指标指针；热路径上不应 panic。
	var c *Counter
	var g *Gauge
	var h *Histogram
	c.Inc("x")
	g.Set(1)
	h.Observe(1)
	if c.Value("x") != 0 || g.Value() != 0 || h.Count() != 0 {
		t.Error("nil 指标应安静地返回零值")
	}
}

func TestBucketNormalization(t *testing.T) {
	r := NewRegistry("")
	// 上游把边界写反 + 重复：必须排序去重，否则累积计数语义完全错乱
	// （观测到 3 秒会被算进 <=1 的桶里），而导出文本看起来仍然合法。
	h := r.MustHistogram("b_seconds", "help", []float64{5, 1, 2.5, 1})
	h.Observe(3)

	out := r.Expose()
	if !strings.Contains(out, `b_seconds_bucket{le="1"} 0`) {
		t.Errorf("3 秒不该落在 le=1 桶\n%s", out)
	}
	if !strings.Contains(out, `b_seconds_bucket{le="2.5"} 0`) {
		t.Errorf("3 秒不该落在 le=2.5 桶\n%s", out)
	}
	if !strings.Contains(out, `b_seconds_bucket{le="5"} 1`) {
		t.Errorf("3 秒应落在 le=5 桶\n%s", out)
	}
	// 去重：只应有 1 个 le="1" 行
	if n := strings.Count(out, `le="1"`); n != 1 {
		t.Errorf("重复边界应被去重，le=\"1\" 出现 %d 次\n%s", n, out)
	}
}

func TestGaugeAndUptime(t *testing.T) {
	r := NewRegistry("agentbot")
	g := r.MustGauge("storage_backend_memory", "1 when the memory backend is in use.", "reason")
	g.Set(1, "no_database")
	g.Add(1, "no_database")

	out := r.Expose()
	if !strings.Contains(out, `agentbot_storage_backend_memory{reason="no_database"} 2`) {
		t.Errorf("gauge 值不正确\n%s", out)
	}
	if !strings.Contains(out, "# TYPE agentbot_storage_backend_memory gauge") {
		t.Error("gauge 缺少 TYPE 行")
	}
	// 自带的 uptime gauge
	if !strings.Contains(out, "process_uptime_seconds ") {
		t.Errorf("缺少 process_uptime_seconds\n%s", out)
	}
}

func TestNamespacePrefix(t *testing.T) {
	withNS := NewRegistry("agentbot")
	withNS.MustCounter("x_total", "help").Inc()
	if out := withNS.Expose(); !strings.Contains(out, "agentbot_x_total 1") {
		t.Errorf("应带命名空间前缀\n%s", out)
	}

	bare := NewRegistry("")
	bare.MustCounter("x_total", "help").Inc()
	out := bare.Expose()
	if !strings.Contains(out, "\nx_total 1") {
		t.Errorf("空前缀应导出无前缀名\n%s", out)
	}
	if strings.Contains(out, "agentbot_x_total") {
		t.Errorf("空前缀不该出现 agentbot_ 前缀\n%s", out)
	}
}

func TestConcurrentObservationRace(t *testing.T) {
	// -race 下运行本测试才有意义：并发写同一系列的计数不能丢。
	r := NewRegistry("")
	c := r.MustCounter("cc_total", "help", "k")
	h := r.MustHistogram("cc_seconds", "help", []float64{0.1}, "k")

	const goroutines, per = 8, 200
	done := make(chan struct{}, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < per; j++ {
				c.Inc("a")
				h.Observe(0.05, "a")
			}
		}()
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}

	// 精确断言：并发的自增不能丢计数（丢了说明锁的临界区没覆盖读改写）
	if got, want := c.Value("a"), float64(goroutines*per); got != want {
		t.Errorf("并发计数 = %v, want %v", got, want)
	}
	if got, want := h.Count("a"), uint64(goroutines*per); got != want {
		t.Errorf("并发观测次数 = %v, want %v", got, want)
	}
}

func TestSplitLabelKeyRoundTrip(t *testing.T) {
	if got := splitLabelKey(""); got != nil {
		t.Errorf("空键应还原为 nil，得到 %v", got)
	}
	if got := splitLabelKey("a\x00b"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("还原失败: %v", got)
	}
}
