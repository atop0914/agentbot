package network

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Handler 提供出口路由的 HTTP 接口。
type Handler struct {
	svc Service
}

// NewHandler 创建出口路由 HTTP 处理器。
func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes 注册出口路由相关接口。
//
//	GET    /api/v1/network/rules              列出出口策略
//	POST   /api/v1/network/rules              新增策略（body: {agent_id,target,effect,methods,priority,description}）
//	DELETE /api/v1/network/rules/{id}         删除策略
//
// 注意：下面这些是**无尾斜杠的集合路径**，前缀规则覆盖不到，权限表必须显式登记，
// 否则会落进默认拒绝返回 403（Day 24 踩过同样的坑）。
//
//	POST   /api/v1/network/evaluate           出站前预检（不改状态）
//	POST   /api/v1/network/proxy              经网关转发一次出站请求
//	GET    /api/v1/network/traffic            查询出站流量记录
//	GET    /api/v1/network/stats              出站流量聚合统计
//	POST   /api/v1/network/traffic/purge      清理过期流量记录
//	GET    /api/v1/network/summary            后台「网络」分区摘要
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/network/rules", h.handleRules)
	mux.HandleFunc("/api/v1/network/rules/", h.handleRuleByID)
	mux.HandleFunc("/api/v1/network/evaluate", h.handleEvaluate)
	mux.HandleFunc("/api/v1/network/proxy", h.handleProxy)
	mux.HandleFunc("/api/v1/network/traffic", h.handleTraffic)
	mux.HandleFunc("/api/v1/network/traffic/purge", h.handleTrafficPurge)
	mux.HandleFunc("/api/v1/network/stats", h.handleStats)
	mux.HandleFunc("/api/v1/network/summary", h.handleSummary)
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// statusFor 把服务层哨兵错误映射为 HTTP 状态码。
//
//	被策略拒绝 → 403（这是策略生效的**正常结果**，不是故障）
//	策略引擎不可用 → 503（fail-closed，绝不因为「查不到策略」而放行）
//	其余入参问题 → 400
func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrBlocked):
		return http.StatusForbidden
	case errors.Is(err, ErrUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrRuleNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrInvalidTarget),
		errors.Is(err, ErrInvalidEffect),
		errors.Is(err, ErrAgentRequired),
		errors.Is(err, ErrURLRequired):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// handleRules 处理 GET（列出）与 POST（新增）。
func (h *Handler) handleRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rules, err := h.svc.ListRules(r.Context())
		if err != nil {
			writeErr(w, statusFor(err), err.Error())
			return
		}
		if rules == nil {
			rules = []*PolicyRule{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"rules": rules, "count": len(rules)})
	case http.MethodPost:
		var rule PolicyRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		created, err := h.svc.AddRule(r.Context(), rule)
		if err != nil {
			writeErr(w, statusFor(err), err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleRuleByID 处理 DELETE /api/v1/network/rules/{id}。
func (h *Handler) handleRuleByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/v1/network/rules/"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "rule id is required")
		return
	}
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := h.svc.DeleteRule(r.Context(), id); err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": id})
}

// evaluateRequest 是预检/代理接口的请求体。
type evaluateRequest struct {
	AgentID string `json:"agent_id"`
	Method  string `json:"method"`
	URL     string `json:"url"`
	// Body / Headers / Timeout 仅 /proxy 使用。
	Body    string            `json:"body,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Timeout string            `json:"timeout,omitempty"`
}

// handleEvaluate 处理 POST /api/v1/network/evaluate：只判定，不发起请求。
//
// 无论允许与否都返回 200，判定结果在 body 里 —— 这是「预检」接口，
// 调用方需要的是「能不能发」这个答案本身，而不是一个错误状态码。
// 真正的阻断语义在 /proxy 上（未获允许返回 403 且不发包）。
func (h *Handler) handleEvaluate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req evaluateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		writeErr(w, http.StatusBadRequest, ErrURLRequired.Error())
		return
	}
	decision, err := h.svc.Evaluate(r.Context(), req.AgentID, req.Method, req.URL)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent_id": req.AgentID,
		"url":      req.URL,
		"decision": decision,
	})
}

// handleProxy 处理 POST /api/v1/network/proxy：经网关执行一次出站请求。
func (h *Handler) handleProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req evaluateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	eres := EgressRequest{
		AgentID: strings.TrimSpace(req.AgentID),
		Method:  req.Method,
		URL:     strings.TrimSpace(req.URL),
		Headers: req.Headers,
	}
	if req.Body != "" {
		eres.Body = []byte(req.Body)
	}
	if req.Timeout != "" {
		d, err := time.ParseDuration(req.Timeout)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid timeout: "+err.Error())
			return
		}
		eres.Timeout = d
	}

	resp, err := h.svc.Proxy(r.Context(), eres)
	if err != nil {
		// 阻断 / 下游失败都带 body：调用方需要看到 decision 与 record，
		// 否则「为什么被拦」只能靠猜。
		status := statusFor(err)
		payload := map[string]interface{}{"error": err.Error()}
		if resp != nil {
			if resp.Decision.Allowed || resp.Decision.RuleID != "" || resp.Decision.Reason != "" {
				payload["decision"] = resp.Decision
			}
			if resp.Record != nil {
				payload["record"] = resp.Record
			}
		}
		writeJSON(w, status, payload)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"domain":      resp.Domain,
		"status":      resp.StatusCode,
		"body":        string(resp.Body),
		"decision":    resp.Decision,
		"record":      resp.Record,
		"duration_ms": resp.DurationMS,
	})
}

// handleTraffic 处理 GET /api/v1/network/traffic。
//
// 查询参数：agent_id、domain、allowed（true/false）、suspicious（true）、
// since/until（RFC3339 或 Unix 秒）、limit。
func (h *Handler) handleTraffic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	filter, err := parseTrafficFilter(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	records, err := h.svc.ListTraffic(r.Context(), filter)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	if records == nil {
		records = []*TrafficRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"records": records,
		"count":   len(records),
		"limit":   effectiveLimit(filter.Limit),
	})
}

// handleTrafficPurge 处理 POST /api/v1/network/traffic/purge。
//
// body: {"before":"<RFC3339 或 Unix 秒>","dry_run":true}
// 默认 dry_run=true：忘记传参不会误删观测数据。
func (h *Handler) handleTrafficPurge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Before string `json:"before"`
		DryRun *bool  `json:"dry_run"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
	}
	if strings.TrimSpace(req.Before) == "" {
		writeErr(w, http.StatusBadRequest, "before is required (RFC3339 or unix seconds)")
		return
	}
	before, err := parseTime(req.Before)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	dryRun := true
	if req.DryRun != nil {
		dryRun = *req.DryRun
	}

	if dryRun {
		// 预演要给出「会删多少」：只回一句 dry_run=true 等于什么都没说。
		candidates, err := h.svc.ListTraffic(r.Context(), TrafficFilter{Until: &before, Limit: 1 << 30})
		if err != nil {
			writeErr(w, statusFor(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"dry_run":    true,
			"cutoff":     before.UTC().Format(time.RFC3339),
			"candidates": len(candidates),
			"removed":    0,
		})
		return
	}

	removed, err := h.svc.PurgeTraffic(r.Context(), before)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"dry_run": false,
		"cutoff":  before.UTC().Format(time.RFC3339),
		"removed": removed,
	})
}

// handleStats 处理 GET /api/v1/network/stats?window=24h。
func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var window time.Duration
	if v := r.URL.Query().Get("window"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid window: "+err.Error())
			return
		}
		if d < 0 {
			writeErr(w, http.StatusBadRequest, "invalid window: must not be negative")
			return
		}
		window = d
	}
	stats, err := h.svc.Stats(r.Context(), window)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// handleSummary 处理 GET /api/v1/network/summary：后台「网络」分区的轻量摘要。
//
// 单独开一个接口而不是让后台去调 /stats + /rules：控制台首屏的每个分区
// 只该发一个请求，否则聚合视图的「分区独立降级」无法实现（一个分区打了
// 三个接口，部分失败时无法判断该分区是否可用）。
func (h *Handler) handleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var window time.Duration
	if v := r.URL.Query().Get("window"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid window: "+err.Error())
			return
		}
		window = d
	}
	stats, err := h.svc.Stats(r.Context(), window)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	rules, err := h.svc.ListRules(r.Context())
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}

	counts := map[string]int{"allow": 0, "deny": 0}
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		counts[string(rule.Effect)]++
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"generated_at":        stats.GeneratedAt,
		"window":              stats.Window,
		"rule_counts":         counts,
		"total_requests":      stats.TotalRequests,
		"allowed_requests":    stats.AllowedRequests,
		"blocked_requests":    stats.BlockedRequests,
		"failed_requests":     stats.FailedRequests,
		"distinct_domains":    stats.DistinctDomains,
		"suspicious_requests": stats.SuspiciousRequests,
		"total_bytes":         stats.TotalBytes,
		"top_domains":         truncateDomains(stats.Domains, defaultSummaryDomains),
		"suspicious_domains":  truncateDomains(stats.SuspiciousDomains, defaultSummaryDomains),
	})
}

// defaultSummaryDomains 是摘要里携带的域名条数上限。
const defaultSummaryDomains = 5

func truncateDomains(domains []*DomainStat, n int) []*DomainStat {
	if len(domains) <= n {
		if domains == nil {
			return []*DomainStat{}
		}
		return domains
	}
	return domains[:n]
}

func parseTrafficFilter(r *http.Request) (TrafficFilter, error) {
	q := r.URL.Query()
	filter := TrafficFilter{
		AgentID: strings.TrimSpace(q.Get("agent_id")),
		Domain:  strings.TrimSpace(q.Get("domain")),
	}
	if v := q.Get("allowed"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return filter, errors.New("invalid allowed: must be true or false")
		}
		filter.Allowed = &b
	}
	if v := q.Get("suspicious"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return filter, errors.New("invalid suspicious: must be true or false")
		}
		filter.OnlySuspicious = b
	}
	if v := q.Get("since"); v != "" {
		t, err := parseTime(v)
		if err != nil {
			return filter, errors.New("invalid since: " + err.Error())
		}
		filter.Since = &t
	}
	if v := q.Get("until"); v != "" {
		t, err := parseTime(v)
		if err != nil {
			return filter, errors.New("invalid until: " + err.Error())
		}
		filter.Until = &t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return filter, errors.New("invalid limit: must be a non-negative integer")
		}
		filter.Limit = n
	}
	return filter, nil
}

func effectiveLimit(n int) int {
	if n <= 0 {
		return DefaultTrafficLimit
	}
	return n
}

// parseTime 接受 RFC3339 或 Unix 秒两种写法。
func parseTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, errors.New("empty time value")
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(secs, 0).UTC(), nil
	}
	return time.Time{}, errors.New("time must be RFC3339 or unix seconds: " + v)
}
