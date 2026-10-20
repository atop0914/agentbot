package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// 本文件是 Day 3-7 第三批：把「一次请求」在日志里串成一条链，并把
// 访问日志的级别降下来。
//
// 要解决的两个实际问题：
//
//  1. **访问日志刷屏**。原实现用 logger.Info 记录每一个请求。在真实流量下
//     这让日志里再也找不到启动信息与错误 —— 排障时最需要的那几行被淹掉。
//     访问日志的正确级别是 debug：默认看不见，需要时 LOG_LEVEL=debug 打开。
//     但 **5xx 必须留在 error**：它不刷屏，且是运维真正要盯的少数事件。
//
//  2. **追踪断了**。request_id 此前只在「访问日志」这一行出现，
//     业务日志（例如「创建 Agent 失败」）里没有它 —— 于是拿到一个 request_id
//     也无法回答「这次请求到底发生了什么」。request_id 必须进入 context，
//     由各处的日志统一带上。

// requestIDKey 是 request_id 在 context 中的键。
//
// 用**本包私有**的 struct 类型而不是字符串：Go 里不同包定义的同名字符串键
// 会互相冲突（context.Value 按 == 比较），而字符串键几乎必然会撞。
type requestIDKey struct{}

// RequestIDFromContext 取出当前请求的 ID，没有则返回空串。
//
// 导出它是因为**业务模块**需要用同一个 ID 打日志 —— 这是本批的核心：
// 只把 ID 写进访问日志等于没串起来。
//
// 它是取 request_id 的**唯一入口**。不要另外提供「从 header 反查」或
// 「从 ResponseWriter 反查」的辅助：多个来源必然产出
// 「中间件说 A、日志说 B」这类最难查的分歧。
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

// WithRequestID 把 request_id 放进 context。
//
// 导出是为了让**调用方自己发起的下游请求**（例如装配层构造子请求、
// 测试直接调 handler）也能带上同一个 ID，从而不丢失链路。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// requestIDMiddleware adds a unique request ID to each request.
//
// 三个必须同时成立的行为（少任何一个，链路都是断的）：
//   - **尊重上游传入的值**：跨服务调用时上游已生成了 ID，这里若覆盖它，
//     链路会在本服务处断开（上游日志里的 ID 在本服务查不到）；
//   - **回传到响应头**：调用方要能拿到 ID 才能记进自己的日志；
//   - **写进 context**：只写 header 的话业务代码取不到它。
//
// 注意 `r = r.WithContext(...)` 必须**原地赋值**：写成
// `r.WithContext(...)` 而不接收返回值，是这里最经典的一个静默失效
// （编译通过、header 正常、context 里什么都没有）。
func requestIDMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get("X-Request-ID")
			if reqID == "" {
				reqID = uuid.New().String()
			}
			w.Header().Set("X-Request-ID", reqID)
			r.Header.Set("X-Request-ID", reqID)
			r = r.WithContext(WithRequestID(r.Context(), reqID))

			next.ServeHTTP(w, r)
		})
	}
}

// loggingMiddleware logs each request with method, path, status, duration.
//
// 级别策略（本批改动）：
//   - 5xx → Error：不刷屏，且是运维要盯的少数事件；
//   - 4xx → Warn：客户端错误值得看，且 request_id 是这类排障的第一入口；
//   - 其余 → Debug：访问日志默认**不打**。
//
// 为什么不是「全部 Info」：实测一次 `go test -run TestMetrics` 就产出
// 数十行 request 日志；真实流量下这个量级会让启动信息与真正的错误都被淹掉。
// 日志的价值在于「找得到」，不在于「全都记」。
//
// 为什么不做「按路径过滤」这类更细策略：那会把「哪些路径重要」的判断
// 写死在中间件里，而它其实是**运维视角**的问题，应由 LOG_LEVEL 决定。
func loggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(sw, r)

			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", RequestIDFromContext(r.Context()),
				"remote", r.RemoteAddr,
			}

			switch {
			case sw.status >= 500:
				logger.Error("request", attrs...)
			case sw.status >= 400:
				logger.Warn("request", attrs...)
			default:
				logger.Debug("request", attrs...)
			}
		})
	}
}
