package metrics

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Expose 把整张注册表导出为 Prometheus text exposition format 0.0.4。
//
// 输出顺序刻意固定（指标名 → 标签组合按首次出现顺序）：
// 抓取结果可 diff、可做 golden test。map 迭代顺序随机会让每次抓取的
// 文本行序不同，任何「导出内容」的断言都会变成随机失败。
func (r *Registry) Expose() string {
	r.mu.RLock()
	counters := make([]*Counter, 0, len(r.counters))
	for _, c := range r.counters {
		counters = append(counters, c)
	}
	gauges := make([]*Gauge, 0, len(r.gauges))
	for _, g := range r.gauges {
		gauges = append(gauges, g)
	}
	histograms := make([]*Histogram, 0, len(r.histograms))
	for _, h := range r.histograms {
		histograms = append(histograms, h)
	}
	start := r.startTime
	r.mu.RUnlock()

	// 按指标名排序，保证指标块之间也稳定。
	sort.Slice(counters, func(i, j int) bool { return counters[i].name < counters[j].name })
	sort.Slice(gauges, func(i, j int) bool { return gauges[i].name < gauges[j].name })
	sort.Slice(histograms, func(i, j int) bool { return histograms[i].name < histograms[j].name })

	var b strings.Builder

	// uptime 是一个自带的 gauge：运维排查「进程是不是刚重启过」
	// 的第一手信号，比任何业务指标都常用。
	b.WriteString(r.exposeGauge("process_uptime_seconds",
		"Seconds since the process metrics registry was created.",
		nil, "", time.Since(start).Seconds(), false))

	for _, c := range counters {
		b.WriteString(exposeCounter(c))
	}
	for _, g := range gauges {
		b.WriteString(exposeGauge(g))
	}
	for _, h := range histograms {
		b.WriteString(exposeHistogram(h))
	}
	return b.String()
}

// exposeGauge 输出单个 gauge 指标块（含 HELP / TYPE），或一条系列。
//
// 拆成这个形态是为了让自带的 process_uptime_seconds 与注册表中的
// gauge 走同一条格式化路径 —— 两处各写一份格式，迟早有一处漏转义。
func (r *Registry) exposeGauge(name, help string, labels []string, key string, value float64, withHeader bool) string {
	var b strings.Builder
	if withHeader {
		b.WriteString("# HELP " + name + " " + help + "\n")
		b.WriteString("# TYPE " + name + " gauge\n")
	}
	b.WriteString(formatSeries(name, labelsFor(labels, key), formatFloat(value)))
	return b.String()
}

func exposeGauge(g *Gauge) string {
	g.mu.Lock()
	defer g.mu.Unlock()

	var b strings.Builder
	b.WriteString("# HELP " + g.name + " " + g.help + "\n")
	b.WriteString("# TYPE " + g.name + " gauge\n")
	for _, key := range g.order {
		b.WriteString(formatSeries(g.name, labelsFor(g.labels, key), formatFloat(g.values[key])))
	}
	return b.String()
}

func exposeCounter(c *Counter) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var b strings.Builder
	b.WriteString("# HELP " + c.name + " " + c.help + "\n")
	b.WriteString("# TYPE " + c.name + " counter\n")
	for _, key := range c.labelOrder {
		b.WriteString(formatSeries(c.name, labelsFor(c.labels, key), formatFloat(c.values[key])))
	}
	return b.String()
}

func exposeHistogram(h *Histogram) string {
	h.mu.Lock()
	defer h.mu.Unlock()

	var b strings.Builder
	b.WriteString("# HELP " + h.name + " " + h.help + "\n")
	b.WriteString("# TYPE " + h.name + " histogram\n")
	for _, key := range h.order {
		e := h.entries[key]
		base := labelsFor(h.labels, key)
		// 累积桶：counts[i] 已经是累计值，直接输出即可。
		for i, ub := range h.buckets {
			le := formatFloat(ub)
			b.WriteString(formatSeries(h.name+"_bucket", withLabel(base, "le", le),
				strconv.FormatUint(e.counts[i], 10)))
		}
		// +Inf 桶必须存在且等于 _count：Prometheus 查询
		// histogram_quantile() 依赖它，缺失会让整个分位数计算返回 NaN。
		b.WriteString(formatSeries(h.name+"_bucket", withLabel(base, "le", "+Inf"),
			strconv.FormatUint(e.count, 10)))
		b.WriteString(formatSeries(h.name+"_sum", base, formatFloat(e.sum)))
		b.WriteString(formatSeries(h.name+"_count", base, strconv.FormatUint(e.count, 10)))
	}
	return b.String()
}

// labelsFor 把（标签名序列 + labelKey）还原成导出的标签对。
//
// 标签个数与键的段数不一致时**不 panic**：说明有调用方漏传了标签值，
// 这时补空串让导出继续，比让 /metrics 整体 500 更合适 ——
// 健康探针与抓取都不该因为一个会计错误而中断。
func labelsFor(names []string, key string) []Label {
	if len(names) == 0 {
		return nil
	}
	values := splitLabelKey(key)
	out := make([]Label, 0, len(names))
	for i, n := range names {
		v := ""
		if i < len(values) {
			v = values[i]
		}
		out = append(out, Label{Name: n, Value: v})
	}
	return out
}

// withLabel 在已有标签后追加一个（如直方图的 le）。
//
// 追加而不是前插：Prometheus 约定 le 是最后一个标签，
// 且 histogram_quantile 的实现会假定桶系列之间除 le 外标签一致。
func withLabel(base []Label, name, value string) []Label {
	out := make([]Label, 0, len(base)+1)
	out = append(out, base...)
	out = append(out, Label{Name: name, Value: value})
	return out
}
