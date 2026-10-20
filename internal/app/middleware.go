package app

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/atop0914/agentbot/internal/metrics"
)

// recoveryMiddleware catches panics and returns 500.
//
// 它同时承担 panic **计数**：metricsMiddleware 在最内层，panic 传播时
// 它后面的统计代码根本走不到，因此 panics_total 只能在 recovery 里记 ——
// 若把这条计数写在 metricsMiddleware 里，它会永远为 0（一个永不触发的
// 告警指标比没有指标更危险：看着有监控，实际瞎了）。
func recoveryMiddleware(logger *slog.Logger, m *httpMetrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered",
						"error", rec,
						"stack", string(debug.Stack()),
						"method", r.Method,
						"path", r.URL.Path,
					)
					if m != nil {
						m.panics.Inc(metrics.NormalizePath(r.URL.Path))
					}
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// corsMiddleware handles CORS headers for development.
func corsMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "86400")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// statusWriter wraps ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}
