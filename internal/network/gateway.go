package network

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultEgressTimeout 是单次出站的默认超时。
//
// 出站请求必须**总是**有超时：Agent 场景下最容易出现的故障不是「连不上」，
// 而是「连上了但永远不返回」，一个没有超时的网关会把 Agent 挂死在等待上。
const DefaultEgressTimeout = 15 * time.Second

// maxEgressBodyBytes 是网关接受/回传的响应体上限（1 MiB）。
//
// 与流量记录配合：记录的是被代理的真实请求，响应体不能无限流进内存，
// 否则一个恶意大文件响应就能把网关变成内存放大器。
const maxEgressBodyBytes = 1 << 20

// HTTPGateway 是基于 net/http 的出口代理网关。
//
// 依赖注入 RoundTripper 而不是直接构造 http.Client：
//
//  1. 测试可以注入假的下游（本机没有外网也能完整验证放行/阻断/审计）；
//  2. 生产可以注入带连接池、TLS 策略、出口代理的自定义实现；
//  3. 网关本身不持有「域名 → 目标」的映射，避免出现绕过策略的隐含路径。
type HTTPGateway struct {
	policy   *MemoryPolicyStore
	store    TrafficStore
	recorder EgressRecorder
	client   *http.Client
	// now 便于测试注入时间源（记录时间戳的一致性）。
	now func() time.Time
}

// GatewayConfig 是网关的构造参数。
type GatewayConfig struct {
	// Policy 必填：没有策略引擎的网关只能全放行或全拒绝，两种都不该是默认。
	Policy *MemoryPolicyStore
	// Store 选填：为 nil 时不记录流量（仅用于纯策略测试）。
	Store TrafficStore
	// Recorder 选填：为 nil 时不落审计。
	Recorder EgressRecorder
	// Transport 选填：为 nil 时使用 http.DefaultTransport。
	Transport http.RoundTripper
	// Timeout 选填：<= 0 时使用 DefaultEgressTimeout。
	Timeout time.Duration
}

// NewHTTPGateway 创建出口网关。
func NewHTTPGateway(cfg GatewayConfig) *HTTPGateway {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultEgressTimeout
	}
	transport := cfg.Transport
	if transport == nil {
		transport = cloneDefaultTransport()
	}
	return &HTTPGateway{
		policy:   cfg.Policy,
		store:    cfg.Store,
		recorder: cfg.Recorder,
		client:   &http.Client{Transport: transport, Timeout: timeout},
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// SetClock 注入时间源（测试用）。
func (g *HTTPGateway) SetClock(now func() time.Time) {
	if g == nil || now == nil {
		return
	}
	g.now = now
}

// Evaluate 只做策略判定，不发起请求。
func (g *HTTPGateway) Evaluate(agentID, method, url string) (Decision, error) {
	if g == nil || g.policy == nil {
		// 没有策略引擎时 fail-closed：宁可阻断也不放行。
		return Decision{Allowed: false, Reason: "egress policy engine is not configured"}, ErrUnavailable
	}
	return g.policy.Evaluate(context.Background(), agentID, method, url)
}

// Proxy 执行一次出站请求。
//
// 流程（顺序不可调换）：
//
//  1. 校验 Agent 身份与目标 URL —— 无法归因的流量不该存在；
//  2. 策略判定 —— 未获允许**直接返回**，不建立任何连接；
//  3. 转发（带超时）；
//  4. 记录流量 + 落审计 —— 放行与阻断**都**记录，
//     只记放行等于给「被拦下的攻击尝试」留下了盲区。
//
// 返回的 error 语义：策略阻断返回 ErrBlocked；转发失败返回下游错误。
// 两种情况下 *EgressResponse 都非 nil（调用方据此拿到 record 与 decision）。
func (g *HTTPGateway) Proxy(ctx context.Context, req EgressRequest) (*EgressResponse, error) {
	if g == nil || g.policy == nil {
		return nil, ErrUnavailable
	}
	agentID := strings.TrimSpace(req.AgentID)
	rawURL := strings.TrimSpace(req.URL)
	started := g.now()

	if agentID == "" {
		return nil, ErrAgentRequired
	}
	if rawURL == "" {
		return nil, ErrURLRequired
	}
	domain := DomainOf(rawURL)
	if domain == "" {
		return nil, fmt.Errorf("%w: %q", ErrURLRequired, rawURL)
	}

	decision, err := g.policy.Evaluate(ctx, agentID, req.Method, rawURL)
	if err != nil {
		return nil, err
	}

	if !decision.Allowed {
		rec := &TrafficRecord{
			ID:         newID("egress"),
			AgentID:    agentID,
			Domain:     domain,
			Method:     req.Method,
			URL:        rawURL,
			Allowed:    false,
			RuleID:     decision.RuleID,
			Reason:     decision.Reason,
			DurationMS: 0,
			Timestamp:  started,
		}
		g.persist(ctx, rec)
		return &EgressResponse{
				Domain:   domain,
				Decision: decision,
				Record:   rec,
			}, &ErrBlockedRule{
				Domain:  domain,
				RuleID:  decision.RuleID,
				Pattern: decision.MatchedPattern,
				Reason:  decision.Reason,
			}
	}

	httpReq, err := http.NewRequestWithContext(ctx, normalizeMethod(req.Method), rawURL, bytesReader(req.Body))
	if err != nil {
		return nil, fmt.Errorf("network: build egress request: %w", err)
	}
	for k, v := range req.Headers {
		// 逐条设置而不是 Add：调用方给出的是一张「头 → 值」的表，
		// 语义是覆盖而非追加，避免测试注入的头与默认头叠加。
		httpReq.Header.Set(k, v)
	}
	if len(req.Body) > 0 && httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = g.client.Timeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq = httpReq.WithContext(cctx)

	resp, err := g.client.Do(httpReq)
	elapsed := g.now().Sub(started)
	rec := &TrafficRecord{
		ID:         newID("egress"),
		AgentID:    agentID,
		Domain:     domain,
		Method:     httpReq.Method,
		URL:        rawURL,
		Allowed:    true,
		RuleID:     decision.RuleID,
		Reason:     decision.Reason,
		DurationMS: elapsed.Milliseconds(),
		Bytes:      int64(len(req.Body)),
		Timestamp:  started,
	}

	out := &EgressResponse{Domain: domain, Decision: decision, DurationMS: rec.DurationMS}
	if err != nil {
		rec.Error = err.Error()
		markSuspicious(rec, DefaultHeuristics())
		out.Record = rec
		g.persist(ctx, rec)
		return out, fmt.Errorf("network: egress to %s failed: %w", domain, err)
	}
	// 响应体必须关闭，但关闭失败不影响已经拿到的结果 —— 显式忽略而非
	// 用裸 defer（否则 errcheck 会把它算成未处理的错误返回）。
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxEgressBodyBytes))
	if readErr != nil {
		rec.Error = "read response body: " + readErr.Error()
	}
	rec.StatusCode = resp.StatusCode
	rec.Bytes += int64(len(body))
	markSuspicious(rec, DefaultHeuristics())

	out.StatusCode = resp.StatusCode
	out.Headers = resp.Header
	out.Body = body
	out.Record = rec
	g.persist(ctx, rec)

	if readErr != nil {
		return out, fmt.Errorf("network: read egress response from %s: %w", domain, readErr)
	}
	return out, nil
}

// persist 把流量记录写入存储并落审计。
//
// 两个失败都**不**影响调用方的结果判定：记录失败意味着一份观测数据缺失，
// 把它升级成请求失败会让「网关不可用」变成「所有 Agent 断网」。
func (g *HTTPGateway) persist(ctx context.Context, rec *TrafficRecord) {
	if rec == nil {
		return
	}
	if g.store != nil {
		_ = g.store.Append(ctx, rec)
	}
	if g.recorder != nil {
		_ = g.recorder.RecordEgress(ctx, rec)
	}
}

// markSuspicious 按启发式给记录打可疑标记。
func markSuspicious(rec *TrafficRecord, h Heuristics) {
	if rec == nil {
		return
	}
	reasons := suspiciousReasons(rec.Domain, 1, Heuristics{ScratchTLDs: h.ScratchTLDs})
	if len(reasons) == 0 {
		return
	}
	rec.Suspicious = true
	rec.SuspiciousReasons = reasons
}

func normalizeMethod(method string) string {
	m := strings.ToUpper(strings.TrimSpace(method))
	if m == "" {
		return http.MethodGet
	}
	return m
}

func bytesReader(b []byte) io.Reader {
	if len(b) == 0 {
		return nil
	}
	return bytes.NewReader(b)
}

// cloneDefaultTransport 复制默认 Transport。
//
// 直接赋值 http.DefaultTransport 并在其上改配置会**全局生效**（它是共享指针），
// 一个模块的出口策略调整会悄悄影响进程里所有 HTTP 客户端。
func cloneDefaultTransport() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		return base.Clone()
	}
	return &http.Transport{}
}
