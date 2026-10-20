package metrics

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrLabelMismatch 表示同一个指标名被用不同的标签名集合注册了两次。
//
// 这是个必须响亮的错误：把两组标签合并进一个指标，导出的文本里
// 同一 series 会有两种标签集，Prometheus 抓取时整轮失败（invalid metric name /
// inconsistent label names），而故障现场看起来只是「指标怎么全没了」。
var ErrLabelMismatch = errors.New("metrics: metric registered with a different label set")

// ErrInvalidName 表示指标名不符合 Prometheus 命名约定。
var ErrInvalidName = errors.New("metrics: invalid metric name")

// validateName 校验指标名符合 [a-zA-Z_:][a-zA-Z0-9_:]*。
//
// 名称非法时 Prometheus 会**丢弃整个抓取**，所以宁可在注册期报错，
// 也不要等到运维发现仪表盘空了才回头查。
//
// ⚠️ 必须在**加命名空间前缀之前**校验原始名：前缀会掩盖两类错误 ——
//   - 空前缀化后变成 "ns_"，非空但毫无意义；
//   - "1leading_digit" 前缀化后首位不再是数字，看着合法。
//
// 两者都会产出「注册成功但语义错」的指标。
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty name", ErrInvalidName)
	}
	for i, ch := range name {
		ok := ch == '_' || ch == ':' ||
			(ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(i > 0 && ch >= '0' && ch <= '9')
		if !ok {
			return fmt.Errorf("%w: %q contains %q", ErrInvalidName, name, string(ch))
		}
	}
	return nil
}

// NewCounter 注册（或取回）一个计数器。
//
// 返回 error 而不是 panic：注册发生在装配期，配置驱动的指标名
// （例如按模块循环注册）可能来自外部输入，装配失败应该走
// NewWithConfig 的返回值，而不是让进程崩在启动路径上。
func (r *Registry) NewCounter(name, help string, labels ...string) (*Counter, error) {
	// 校验原始名（见 validateName 的注释：前缀会掩盖首位非法字符）
	if err := validateName(name); err != nil {
		return nil, err
	}
	full := r.qualify(name)

	r.mu.Lock()
	defer r.mu.Unlock()

	key := labelNamesKey(full, labels)
	if r.counterLabelKeys == nil {
		r.counterLabelKeys = make(map[string]string)
	}
	if existing, ok := r.counterLabelKeys[full]; ok && existing != key {
		return nil, fmt.Errorf("%w: counter %q", ErrLabelMismatch, full)
	}
	if c, ok := r.counters[key]; ok {
		return c, nil
	}
	c := &Counter{
		name:       full,
		help:       help,
		labels:     append([]string(nil), labels...),
		values:     make(map[string]float64),
		labelOrder: nil,
	}
	r.counters[key] = c
	r.counterLabelKeys[full] = key
	return c, nil
}

// MustCounter 是 NewCounter 的 panic 版本，供包级变量与测试使用。
func (r *Registry) MustCounter(name, help string, labels ...string) *Counter {
	c, err := r.NewCounter(name, help, labels...)
	if err != nil {
		panic(err)
	}
	return c
}

// Inc 把计数器加一。
func (c *Counter) Inc(values ...string) { c.Add(1, values...) }

// Add 把计数器加上 delta。
//
// delta 允许为负（例如「回滚了一次计量」），但导出的语义仍是计数器；
// 需要真正可增可减的量请用 Gauge。
func (c *Counter) Add(delta float64, values ...string) {
	if c == nil {
		// nil 接收者安全：调用方拿到的可能是「未注册成功」的零值指针，
		// 让它在热路径上安静地什么都不做，好过在业务代码里到处判空。
		return
	}
	key := labelKey(values)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, seen := c.values[key]; !seen {
		c.labelOrder = append(c.labelOrder, key)
	}
	c.values[key] += delta
}

// Value 返回某组标签下的当前值（测试与自检用）。
func (c *Counter) Value(values ...string) float64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.values[labelKey(values)]
}

// NewGauge 注册（或取回）一个瞬时值指标。
func (r *Registry) NewGauge(name, help string, labels ...string) (*Gauge, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	full := r.qualify(name)

	r.mu.Lock()
	defer r.mu.Unlock()

	key := labelNamesKey(full, labels)
	if r.gaugeLabelKeys == nil {
		r.gaugeLabelKeys = make(map[string]string)
	}
	if existing, ok := r.gaugeLabelKeys[full]; ok && existing != key {
		return nil, fmt.Errorf("%w: gauge %q", ErrLabelMismatch, full)
	}
	if g, ok := r.gauges[key]; ok {
		return g, nil
	}
	g := &Gauge{
		name:   full,
		help:   help,
		labels: append([]string(nil), labels...),
		values: make(map[string]float64),
	}
	r.gauges[key] = g
	r.gaugeLabelKeys[full] = key
	return g, nil
}

// MustGauge 是 NewGauge 的 panic 版本。
func (r *Registry) MustGauge(name, help string, labels ...string) *Gauge {
	g, err := r.NewGauge(name, help, labels...)
	if err != nil {
		panic(err)
	}
	return g
}

// Set 设置瞬时值。
func (g *Gauge) Set(v float64, values ...string) {
	if g == nil {
		return
	}
	key := labelKey(values)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, seen := g.values[key]; !seen {
		g.order = append(g.order, key)
	}
	g.values[key] = v
}

// Add 在瞬时值上做增减。
func (g *Gauge) Add(delta float64, values ...string) {
	if g == nil {
		return
	}
	key := labelKey(values)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, seen := g.values[key]; !seen {
		g.order = append(g.order, key)
	}
	g.values[key] += delta
}

// Value 返回某组标签下的当前值。
func (g *Gauge) Value(values ...string) float64 {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.values[labelKey(values)]
}

// NewHistogram 注册（或取回）一个直方图。buckets 为空时用 DefaultBuckets。
//
// buckets 会在注册时被**排序并去重**：上游把边界写反（如 {5, 1, 2.5}）
// 时，累积计数的语义会完全错乱（观察到 3 秒会被算进 <=1 的桶里），
// 而导出的文本看起来仍然合法。排序去重让这类错误在注册期就被消灭。
func (r *Registry) NewHistogram(name, help string, buckets []float64, labels ...string) (*Histogram, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	full := r.qualify(name)

	bounds := normalizeBuckets(buckets)

	r.mu.Lock()
	defer r.mu.Unlock()

	key := labelNamesKey(full, labels)
	if r.histLabelKeys == nil {
		r.histLabelKeys = make(map[string]string)
	}
	if existing, ok := r.histLabelKeys[full]; ok && existing != key {
		return nil, fmt.Errorf("%w: histogram %q", ErrLabelMismatch, full)
	}
	if h, ok := r.histograms[key]; ok {
		return h, nil
	}
	h := &Histogram{
		name:    full,
		help:    help,
		labels:  append([]string(nil), labels...),
		buckets: bounds,
		entries: make(map[string]*histEntry),
	}
	r.histograms[key] = h
	r.histLabelKeys[full] = key
	return h, nil
}

// MustHistogram 是 NewHistogram 的 panic 版本。
func (r *Registry) MustHistogram(name, help string, buckets []float64, labels ...string) *Histogram {
	h, err := r.NewHistogram(name, help, buckets, labels...)
	if err != nil {
		panic(err)
	}
	return h
}

// normalizeBuckets 排序去重分桶边界；空输入回退到 DefaultBuckets。
func normalizeBuckets(buckets []float64) []float64 {
	src := buckets
	if len(src) == 0 {
		src = DefaultBuckets
	}
	sorted := append([]float64(nil), src...)
	sort.Float64s(sorted)
	out := sorted[:0]
	for i, v := range sorted {
		if i > 0 && v == sorted[i-1] {
			continue
		}
		out = append(out, v)
	}
	// out 与 sorted 共享底层数组，而复制的初衷就是不复用调用方的切片 ——
	// 这里已经脱离原切片，直接返回是安全的。
	return out
}

// Observe 记录一次观测。
//
// 落在最后一个桶之外的值仍会计入 _count 与 _sum，但不会进入任何
// `le` 桶 —— 这正是 Prometheus 的约定（`_bucket{le="+Inf"}` 等于 _count，
// 见导出处补的 +Inf 桶）。
func (h *Histogram) Observe(v float64, values ...string) {
	if h == nil {
		return
	}
	key := labelKey(values)
	h.mu.Lock()
	defer h.mu.Unlock()
	e, seen := h.entries[key]
	if !seen {
		e = &histEntry{counts: make([]uint64, len(h.buckets))}
		h.entries[key] = e
		h.order = append(h.order, key)
	}
	for i, ub := range h.buckets {
		if v <= ub {
			e.counts[i]++
		}
	}
	e.sum += v
	e.count++
}

// ObserveDuration 记录一段耗时（秒）。
func (h *Histogram) ObserveDuration(d time.Duration, values ...string) {
	h.Observe(d.Seconds(), values...)
}

// Count 返回观测次数（测试与自检用）。
func (h *Histogram) Count(values ...string) uint64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if e, ok := h.entries[labelKey(values)]; ok {
		return e.count
	}
	return 0
}

// splitLabelKey 把 labelKey 拼出的键还原成标签值序列。
func splitLabelKey(key string) []string {
	if key == "" {
		return nil
	}
	return strings.Split(key, "\x00")
}
