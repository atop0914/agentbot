package app

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/atop0914/agentbot/internal/authz"
	"github.com/atop0914/agentbot/internal/role"
)

// 本文件是 Day 3-7 第三批（请求追踪 + 访问日志分级）的测试。
//
// 这一批的两个行为都**只能通过日志观察**，所以测试必须真的去读日志输出 ——
// 断言「响应头里有 X-Request-ID」是不够的（那是上一批已有的行为），
// 本批要证明的是「同一个 ID 也出现在日志里、且业务代码拿得到它」。
//
// 注意本文件在 package app（内部测试）：需要直接构造中间件与读未导出字段。
// internal/app 里既有 app_test（外部）也有 app（内部）测试文件，两者可共存。

// captureLogger 返回一个把日志写进 buffer 的 logger 与其缓冲区。
func captureLogger(level slog.Level) (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level})
	return slog.New(h), buf
}

// countLines 统计包含某子串的日志行数。
func countLines(buf *bytes.Buffer, substr string) int {
	n := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

// TestRequestIDReachesBusinessContext 是本批最核心的断言：
// 中间件写进 context 的 ID，必须能被**业务代码**用同一个入口取到。
//
// 只断言响应头有 ID 是不够的 —— 那只证明 header 被设了。
// 真正的追踪要求「同一次请求里，任何一处日志都能带上同一个 ID」。
func TestRequestIDReachesBusinessContext(t *testing.T) {
	const upstream = "upstream-trace-id-12345"

	var seen string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 业务处理器视角：从 context 取 ID
		seen = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	h := requestIDMiddleware()(inner)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req.Header.Set("X-Request-ID", upstream)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// 尊重上游传入的值：跨服务链路不能在本服务处断开
	if seen != upstream {
		t.Errorf("业务代码取到的 request_id = %q, want %q（必须尊重上游传入的值）", seen, upstream)
	}
	if got := rec.Header().Get("X-Request-ID"); got != upstream {
		t.Errorf("响应头 X-Request-ID = %q, want %q", got, upstream)
	}
}

// TestRequestIDGeneratedWhenAbsent 验证没有上游 ID 时自己生成一个，
// 且响应头与 context 里的是**同一个**值（两个来源不一致等于链路断了）。
func TestRequestIDGeneratedWhenAbsent(t *testing.T) {
	var seen string
	h := requestIDMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	header := rec.Header().Get("X-Request-ID")
	if header == "" {
		t.Fatal("未传上游 ID 时应自动生成")
	}
	if seen != header {
		t.Errorf("context 里的 %q 与响应头的 %q 不一致 —— 链路在两个来源间断了", seen, header)
	}
	// UUID v4 形态（36 字符、4 个连字符）—— 断言形态而不是「非空」，
	// 否则一个固定的常量 ID 也能通过。
	if len(header) != 36 || strings.Count(header, "-") != 4 {
		t.Errorf("自动生成的 ID 形态不像 UUID: %q", header)
	}
}

// TestRequestIDAbsentFromContextWhenNoMiddleware 反向断言：
// 没经过中间件时取到空串，而不是某个残留值。
func TestRequestIDAbsentFromContextWhenNoMiddleware(t *testing.T) {
	if got := RequestIDFromContext(context.Background()); got != "" {
		t.Errorf("空 context 应返回空串，得到 %q", got)
	}
	//nolint:staticcheck // 故意传 nil 验证 nil 安全
	if got := RequestIDFromContext(nil); got != "" {
		t.Errorf("nil context 应返回空串，得到 %q", got)
	}
}

// TestAccessLogLevelIsDebugForSuccess 验证成功请求的访问日志降到 Debug ——
// 本批的核心痛点：Info 级别会让日志被请求行淹没。
func TestAccessLogLevelIsDebugForSuccess(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Info 级别：成功的请求**不应**出现在日志里
	logger, buf := captureLogger(slog.LevelInfo)
	h := requestIDMiddleware()(loggingMiddleware(logger)(inner))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))

	if n := countLines(buf, `"msg":"request"`); n != 0 {
		t.Errorf("Info 级别下成功请求不应打访问日志，实际 %d 行\n%s", n, buf.String())
	}

	// Debug 级别：这时才应出现
	logger2, buf2 := captureLogger(slog.LevelDebug)
	h2 := requestIDMiddleware()(loggingMiddleware(logger2)(inner))
	h2.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))

	if n := countLines(buf2, `"msg":"request"`); n != 1 {
		t.Errorf("Debug 级别下应打 1 行访问日志，实际 %d 行\n%s", n, buf2.String())
	}
}

// TestAccessLogLevelIsWarnFor4xx 验证 4xx 留在 Warn ——
// request_id 是这类排障的第一入口，降成 Debug 会让它在默认级别下消失。
func TestAccessLogLevelIsWarnFor4xx(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	logger, buf := captureLogger(slog.LevelInfo)
	h := requestIDMiddleware()(loggingMiddleware(logger)(inner))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil))

	if n := countLines(buf, `"msg":"request"`); n != 1 {
		t.Errorf("4xx 应在 Info 级别下也可见，实际 %d 行\n%s", n, buf.String())
	}
	if n := countLines(buf, `"level":"WARN"`); n != 1 {
		t.Errorf("4xx 应为 WARN 级别，实际 WARN 行数 %d\n%s", n, buf.String())
	}
}

// TestAccessLogLevelIsErrorFor5xx 验证 5xx 以 Error 打 —— 不刷屏且必须被看见。
func TestAccessLogLevelIsErrorFor5xx(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	logger, buf := captureLogger(slog.LevelError)
	h := requestIDMiddleware()(loggingMiddleware(logger)(inner))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil))

	if n := countLines(buf, `"level":"ERROR"`); n != 1 {
		t.Errorf("5xx 应为 ERROR 级别（最严格的级别下仍可见），实际 %d 行\n%s", n, buf.String())
	}
}

// TestAccessLogCarriesRequestID 验证访问日志里的 request_id 与请求头一致 ——
// 这是「拿到 ID 能去日志里查」的前提。
func TestAccessLogCarriesRequestID(t *testing.T) {
	const upstream = "trace-abc-999"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // 用 Error 级别保证一定打出来
	})

	logger, buf := captureLogger(slog.LevelDebug)
	h := requestIDMiddleware()(loggingMiddleware(logger)(inner))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req.Header.Set("X-Request-ID", upstream)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !strings.Contains(buf.String(), `"request_id":"`+upstream+`"`) {
		t.Errorf("访问日志应带上来路一致的 request_id %q\n%s", upstream, buf.String())
	}
}

// TestAuthzShortCircuitIsObservableInLog 是本批第二个真问题的回归测试：
// authz 对未授权请求短路返回 401/403（不调用 next）。
//
// 这条请求**不会**到达业务 handler，但必须被访问日志记录 ——
// 否则「403 从哪来」在日志里完全查不到。验证方式：用真实 authz 中间件
// 叠加日志中间件，断言拒绝请求留下了一行日志。
func TestAuthzShortCircuitIsObservableInLog(t *testing.T) {
	mw := authz.NewMiddleware(&denyAllAuthz{}, authz.DefaultRouteTable())

	logger, buf := captureLogger(slog.LevelInfo)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("未授权请求不应到达业务 handler")
	})

	// 顺序与生产一致：日志在外、authz 在内（短路的仍然是 authz）
	h := requestIDMiddleware()(loggingMiddleware(logger)(mw.Authorize(inner)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil))

	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("未授权请求状态码 = %d, 期望 401/403", rec.Code)
	}
	if n := countLines(buf, `"msg":"request"`); n != 1 {
		t.Errorf("被 authz 短路的请求也必须在访问日志里留痕，实际 %d 行\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), `"status":403`) && !strings.Contains(buf.String(), `"status":401`) {
		t.Errorf("日志里的状态码必须是真实的拒绝码\n%s", buf.String())
	}
}

// denyAllAuthz 是只实现「默认拒绝」路径的 authz.Service 桩。
//
// 本测试走的链路是「无主体 → 401」，在 authz 里这会**先于** Decide
// 短路返回；因此其余方法不会被调用，返回零值即可。
// 刻意不为它们编造行为 —— 写了但永远不执行的桩代码会让读者
// 误以为这些路径被覆盖到了。
type denyAllAuthz struct{}

func (d *denyAllAuthz) Assign(context.Context, authz.Subject, string, string) (*authz.Assignment, error) {
	return nil, nil
}
func (d *denyAllAuthz) Revoke(context.Context, authz.Subject, string) error { return nil }
func (d *denyAllAuthz) ListAssignments(context.Context, authz.Subject) ([]authz.Assignment, error) {
	return nil, nil
}
func (d *denyAllAuthz) Authorize(context.Context, authz.Subject) (*authz.Authorization, error) {
	return nil, nil
}
func (d *denyAllAuthz) Decide(context.Context, authz.Subject, role.Permission, authz.Target) (*authz.Decision, error) {
	return &authz.Decision{Allowed: false}, nil
}
func (d *denyAllAuthz) Require(context.Context, authz.Subject, role.Permission, authz.Target) error {
	return authz.ErrForbidden
}
func (d *denyAllAuthz) Audit(context.Context, int) ([]authz.AuditEntry, error) { return nil, nil }
func (d *denyAllAuthz) Stats(context.Context) (*authz.Stats, error) {
	return &authz.Stats{}, nil
}
