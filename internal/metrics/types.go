// Package metrics 提供零第三方依赖的 Prometheus 文本格式指标导出。
//
// 为什么自绘而不引 prometheus/client_golang：
//
//  1. 该依赖的传递依赖 github.com/munnerz/goautoutils 仓库已不可访问
//     （本项目的 research/踩坑记录里有明确记载），`go mod tidy` 会直接失败；
//  2. 本项目只需要「计数器 + 直方图 + 导出文本」这三件事，
//     完整 client 库带来的是 Prometheus 服务发现、推送网关等用不上的面；
//  3. 零新增第三方依赖是本仓库的默认基线（见 README 开发约定）。
//
// 导出的文本严格遵循 Prometheus text exposition format 0.0.4：
// 服务端可以直接被 Prometheus 抓取，不需要任何适配层。
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultBuckets 是 HTTP 请求延迟直方图的默认分桶边界（秒）。
//
// 取值覆盖「本机内存仓库的亚毫秒响应」到「数据库 + 外部网关的数十秒」，
// 因为本项目既有毫秒级的 /health，也有会走 Postgres、外联探测的长请求。
// 分桶过粗会让 P95 失真，过细会放大标签基数 —— 11 个桶是常见的折中。
var DefaultBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

// Registry 是一个指标注册表。
//
// 所有方法都可在并发下安全调用。内部用一把互斥锁保护：本项目的指标
// 写入频率是「每请求几次」，锁竞争远不是瓶颈；而分片锁（sharded lock）
// 会让「导出时看到一致快照」这件事变复杂 —— 导出一致性比写入吞吐更重要，
// 因为运维会拿同一份快照里的两个计数做比值。
type Registry struct {
	mu         sync.RWMutex
	namespace  string
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
	// *LabelKeys 记录「指标名 → 标签名集合指纹」，用于把
	// 「同名指标被两组不同标签注册」这种会毁掉整个抓取的错误在注册期拦下。
	counterLabelKeys map[string]string
	gaugeLabelKeys   map[string]string
	histLabelKeys    map[string]string
	// startTime 用于导出进程运行时长（process_uptime_seconds 的替代，
	// 不依赖 runtime 内部细节）。
	startTime time.Time
}

// NewRegistry 创建一个带命名空间前缀的注册表。
//
// namespace 会作为所有指标名的前缀（`agentbot_http_requests_total`）。
// 传空串表示不加前缀 —— 测试里用得上，生产里应该带前缀，
// 否则同一个 Prometheus 抓多个服务时会撞名。
func NewRegistry(namespace string) *Registry {
	return &Registry{
		namespace:  strings.TrimSpace(namespace),
		counters:   make(map[string]*Counter),
		gauges:     make(map[string]*Gauge),
		histograms: make(map[string]*Histogram),
		startTime:  time.Now(),
	}
}

// Counter 是单调递增的计数器。
type Counter struct {
	mu     sync.Mutex
	name   string
	help   string
	labels []string
	values map[string]float64
	// labelOrder 保留首次出现的标签值顺序，让导出结果稳定 ——
	// map 迭代顺序随机会让每次抓取的文本行序不同，diff 抓取结果时全是噪声。
	labelOrder []string
}

// Gauge 是可增可减的瞬时值。
type Gauge struct {
	mu     sync.Mutex
	name   string
	help   string
	labels []string
	values map[string]float64
	order  []string
}

// Histogram 是累积直方图。
//
// 只导出 `_bucket` / `_sum` / `_count` 三个系列，不导出 quantile：
// 分位数由 Prometheus 用 histogram_quantile() 在查询侧算，
// 服务端自算分位数会在多实例聚合时出错（分位数不可加）。
type Histogram struct {
	mu      sync.Mutex
	name    string
	help    string
	labels  []string
	buckets []float64
	entries map[string]*histEntry
	order   []string
}

type histEntry struct {
	// counts[i] 是落在 buckets[i] 之前（含）的**累积**计数。
	counts []uint64
	sum    float64
	count  uint64
}

// labelKey 把标签值序列拼成 map 键。
//
// 用 \x00 分隔：标签值里可以合法出现逗号、等号、冒号，
// 用它们做分隔符会导致两组不同的标签拼出同一个键（静默串指标）。
func labelKey(values []string) string {
	return strings.Join(values, "\x00")
}

// labelNamesKey 是「指标名 + 标签名集合」的指纹。
// 同名指标若用不同的标签名集合注册，说明是两处在各自定义同一件事 ——
// 必须报错而不是合并，否则导出的文本里同一个 series 会出现两种标签集。
func labelNamesKey(name string, labels []string) string {
	sorted := append([]string(nil), labels...)
	sort.Strings(sorted)
	return name + "\x00" + strings.Join(sorted, "\x00")
}

// qualify 给指标名加命名空间前缀。
func (r *Registry) qualify(name string) string {
	if r.namespace == "" {
		return name
	}
	return r.namespace + "_" + name
}

// escapeLabelValue 按 Prometheus 文本格式转义标签值。
//
// 必须转义的只有三个：反斜杠、双引号、换行。少一个都会产出
// **语法非法**的导出文本，抓取端整轮抓取失败（而不是丢一行）。
func escapeLabelValue(v string) string {
	var b strings.Builder
	b.Grow(len(v))
	for _, ch := range v {
		switch ch {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// Label 是导出的一个标签键值对。
type Label struct {
	Name  string
	Value string
}

// formatSeries 输出 `name{label="v",...} value` 一行。
//
// 无标签系列不带花括号 —— 带空花括号 `metric{}` 虽然能被解析，
// 但与常见导出器的输出形态不一致，会让基于文本 diff 的告警规则失效。
func formatSeries(name string, labels []Label, value string) string {
	var b strings.Builder
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i, l := range labels {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(l.Name)
			b.WriteString(`="`)
			b.WriteString(escapeLabelValue(l.Value))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(value)
	b.WriteByte('\n')
	return b.String()
}

// formatFloat 用最短往返表示输出浮点值。
//
// 刻意不用 %f：%f 会把 0.0000001 打印成 0.000000，直方图的
// _sum 会静默失真。%v 走的是 strconv 的最短往返表示，精度不丢。
func formatFloat(v float64) string {
	return fmt.Sprintf("%v", v)
}
