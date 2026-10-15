package app

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/atop0914/agentbot/internal/metrics"
	"github.com/atop0914/agentbot/internal/storage"
)

// 本文件是 Day 3-7 第二批：把 metrics 包接进真实请求路径。
//
// 设计取舍 —— 为什么指标注册发生在装配层而不是各业务模块：
//
//	业务模块只应关心「做了什么」，不应该知道「如何被观测」。
//	一旦 agent 包自己 import metrics，指标名与标签就会散落到十几个包，
//	「哪些指标存在」这件事将无法从一处回答。装配层是唯一同时认识
//	所有模块的地方，指标面在这里一次成型。
//
// 因此本文件持有**全部指标定义**，业务代码零改动。

// httpMetrics 是一组 HTTP 层指标。
type httpMetrics struct {
	requests  *metrics.Counter
	duration  *metrics.Histogram
	inFlight  *metrics.Gauge
	panics    *metrics.Counter
	statusCls *metrics.Counter
}

// newHTTPMetrics 在给定注册表上注册 HTTP 层指标。
//
// 标签集刻意只有 (method, route, status)：route 用 NormalizePath 归一后的
// 路由模板而不是真实路径。基数上界 = 路由数 × 方法数 × 状态码数，
// 是**常量级**；若用真实路径，几万个 Agent 就能让 Prometheus 内存爆掉。
func newHTTPMetrics(reg *metrics.Registry) (*httpMetrics, error) {
	requests, err := reg.NewCounter(
		"http_requests_total",
		"Total HTTP requests handled, by method, normalized route template and status code.",
		"method", "route", "status")
	if err != nil {
		return nil, err
	}

	duration, err := reg.NewHistogram(
		"http_request_duration_seconds",
		"HTTP request latency in seconds, by method and normalized route template.",
		metrics.DefaultBuckets, "method", "route")
	if err != nil {
		return nil, err
	}

	inFlight, err := reg.NewGauge(
		"http_requests_in_flight",
		"HTTP requests currently being served.")
	if err != nil {
		return nil, err
	}

	panics, err := reg.NewCounter(
		"http_panics_total",
		"Recovered panics in the HTTP layer, by normalized route template.",
		"route")
	if err != nil {
		return nil, err
	}

	// statusCls 与 requests 有重叠，但保留它是有意的：按状态类聚合
	// （2xx/4xx/5xx）的告警规则不需要在 PromQL 里写正则匹配 status，
	// 而正则匹配在抓取量大时是查询侧的主要开销。
	statusCls, err := reg.NewCounter(
		"http_responses_by_status_class_total",
		"HTTP responses by status class (2xx/4xx/5xx), for cheap alerting without regex.",
		"class")
	if err != nil {
		return nil, err
	}

	return &httpMetrics{
		requests:  requests,
		duration:  duration,
		inFlight:  inFlight,
		panics:    panics,
		statusCls: statusCls,
	}, nil
}

// metricsMiddleware 记录每个请求的方法、归一化路由、状态码与耗时。
//
// 放在链路的**最内层**（紧贴 mux）：这样它观测到的耗时是「业务真实耗时」，
// 不把 CORS 预处理与认证的开销混进来 —— 后者是常量级的，
// 混进来只会稀释掉真正想看的业务延迟信号。
//
// 反过来说，panic 的观测必须在**外层**的 recovery 里做，
// 否则本中间件在 panic 传播时根本走不到统计代码（见 recoveryMiddleware）。
func metricsMiddleware(m *httpMetrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.inFlight.Add(1)
			defer m.inFlight.Add(-1)

			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(sw, r)

			route := metrics.NormalizePath(r.URL.Path)
			status := strconv.Itoa(sw.status)

			m.requests.Inc(r.Method, route, status)
			m.duration.ObserveDuration(time.Since(start), r.Method, route)
			// 状态码为 0 说明 handler 既没写头也没写体（例如被 304 语义
			// 短路），归到 "0" 而不是硬塞一个 2xx —— 谎报状态类比丢一个
			// 未知分类更糟。
			if cls := statusClass(sw.status); cls != "" {
				m.statusCls.Inc(cls)
			}
		})
	}
}

// statusClass 把状态码归到 2xx/4xx/5xx 三类；无法归类时返回空串。
func statusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	default:
		return ""
	}
}

// registerStorageMetrics 暴露「当前用的是哪个存储后端」。
//
// 这是本项目最需要被外部观察的一个事实：内存后端重启即丢数据，
// 而它在接口上完全不体现（注册仍返回 201）。把它做成指标，
// 告警规则可以写成「memory 后端持续 5 分钟 > 0 就报」，
// 而不是等人读启动日志时才发现。
//
// 用 gauge 而不是 info 风格的常量指标：gauge 允许运维按
// 「切换后端」这类事件写 `changes()` 告警。
func registerStorageMetrics(reg *metrics.Registry, backend string, fallbackAllowed bool) error {
	mem, err := reg.NewGauge(
		"storage_backend_memory",
		"1 when the in-memory storage backend is in use (data is lost on restart), else 0.")
	if err != nil {
		return err
	}
	pg, err := reg.NewGauge(
		"storage_backend_postgres",
		"1 when the PostgreSQL storage backend is in use, else 0.")
	if err != nil {
		return err
	}
	fallback, err := reg.NewGauge(
		"storage_memory_fallback_allowed",
		"1 when starting without a reachable database is explicitly permitted, else 0.")
	if err != nil {
		return err
	}

	// 两个后端 gauge 互斥且必须**同时**设置：只设其中一个为 1 而不把另一个
	// 置 0，切换后端时两个都会是 1，任何基于它们的告警都会失去意义。
	if backend == storage.BackendMemory {
		mem.Set(1)
		pg.Set(0)
	} else {
		mem.Set(0)
		pg.Set(1)
	}
	if fallbackAllowed {
		fallback.Set(1)
	} else {
		fallback.Set(0)
	}
	return nil
}

// logMetricsPath 在启动日志里写出指标端点的实际路径。
//
// 配置里 metrics.path 是可改的（默认 /metrics）。若静默使用配置值，
// 排障时会出现「按默认路径抓不到指标」的困惑 —— 而这恰恰是
// 可观测性最不该制造的问题。
func logMetricsPath(logger *slog.Logger, path string) {
	if logger == nil {
		return
	}
	logger.Info("metrics endpoint registered", "metrics.path", path)
}

// backendOf 把「有没有数据库句柄」翻译成后端标识。
//
// 判定依据是**句柄**而不是配置：配置说「要用 postgres」与实际拿到句柄
// 是两件事（连接失败会走回退）。指标必须反映**实际生效**的那个后端 ——
// 按配置导出会产出「指标说 postgres、数据其实在内存里」这种最危险的谎言。
func backendOf(db *storage.DB) string {
	if db == nil {
		return storage.BackendMemory
	}
	return storage.BackendPostgres
}

// MetricsPath 返回 /metrics 的实际挂载路径。
//
// 为什么由 App 提供、而不是各处直接写 "/metrics"：配置里 metrics.path
// 是可改的（默认 /metrics）。若路由注册与文档/告警规则各写一份字面量，
// 改配置后会出现「路由挂在新路径、告警还在抓老路径」的静默失联 ——
// 而失联的监控看起来和「一切正常」完全一样。
func (a *App) MetricsPath() string {
	if a == nil || a.Config.Metrics.Path == "" {
		return "/metrics"
	}
	return a.Config.Metrics.Path
}
