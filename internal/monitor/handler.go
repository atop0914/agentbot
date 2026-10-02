package monitor

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Handler 提供监控相关的 HTTP 接口。
type Handler struct {
	svc Service
}

// NewHandler 创建监控 HTTP 处理器。
func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes 注册监控相关路由。
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/monitor/agents", h.handleAgents)
	mux.HandleFunc("/api/v1/monitor/agents/", h.handleAgentByID)
	mux.HandleFunc("/api/v1/monitor/alerts", h.handleAlerts)
	mux.HandleFunc("/api/v1/monitor/alerts/", h.handleAlertByID)
	mux.HandleFunc("/api/v1/monitor/dashboard", h.handleDashboard)
	mux.HandleFunc("/api/v1/monitor/metrics/", h.handleMetrics)
	// Day 24：时间序列与告警处置（实现见 handler_series.go）。
	h.registerSeriesRoutes(mux)
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

// handleAgents 处理 GET（列表）与 POST（上报状态）。
func (h *Handler) handleAgents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		statuses, err := h.svc.ListAgentStatuses(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// 列表附带健康分，方便前端直接渲染。
		type item struct {
			*AgentStatus
			HealthScore int `json:"health_score"`
		}
		out := make([]item, 0, len(statuses))
		for _, st := range statuses {
			out = append(out, item{AgentStatus: st, HealthScore: HealthScore(st)})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"agents": out, "total": len(out)})
	case http.MethodPost:
		var status AgentStatus
		if err := json.NewDecoder(r.Body).Decode(&status); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		if status.AgentID == "" {
			writeErr(w, http.StatusBadRequest, "agent_id is required")
			return
		}
		if err := h.svc.ReportStatus(r.Context(), &status); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, status)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleAgentByID 处理 /api/v1/monitor/agents/{id}[/health|/metrics]。
func (h *Handler) handleAgentByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/monitor/agents/")
	parts := strings.SplitN(rest, "/", 2)
	agentID := parts[0]
	if agentID == "" {
		writeErr(w, http.StatusBadRequest, "agent id is required")
		return
	}
	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}

	switch sub {
	case "health":
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		healthy, err := h.svc.HealthCheck(r.Context(), agentID)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		status, err := h.svc.GetAgentStatus(r.Context(), agentID)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"agent_id":     agentID,
			"healthy":      healthy,
			"health_score": HealthScore(status),
			"state":        status.State,
			"last_active":  status.LastActive,
		})
		return
	case "metrics":
		if r.Method == http.MethodGet {
			h.getMetrics(w, r, agentID)
			return
		}
		if r.Method == http.MethodPost {
			var usage ResourceUsage
			if err := json.NewDecoder(r.Body).Decode(&usage); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
				return
			}
			if err := h.svc.ReportMetrics(r.Context(), agentID, &usage); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, usage)
			return
		}
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	case "":
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		status, err := h.svc.GetAgentStatus(r.Context(), agentID)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	default:
		writeErr(w, http.StatusNotFound, "unknown sub-resource: "+sub)
	}
}

func (h *Handler) getMetrics(w http.ResponseWriter, r *http.Request, agentID string) {
	start, end, err := parseWindow(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// aggregate=1 时返回窗口内聚合值而非原始采样序列。
	if r.URL.Query().Get("aggregate") == "1" {
		agg, err := h.svc.GetAggregatedMetrics(r.Context(), agentID, end.Sub(start))
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, agg)
		return
	}
	samples, err := h.svc.GetMetrics(r.Context(), agentID, start, end)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent_id": agentID,
		"start":    start,
		"end":      end,
		"samples":  samples,
		"total":    len(samples),
	})
}

// handleMetrics 处理不带 agent 前缀的指标查询：/api/v1/monitor/metrics/{agentID}
func (h *Handler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	agentID := strings.TrimPrefix(r.URL.Path, "/api/v1/monitor/metrics/")
	if agentID == "" {
		writeErr(w, http.StatusBadRequest, "agent id is required")
		return
	}
	if r.Method == http.MethodGet {
		h.getMetrics(w, r, agentID)
		return
	}
	writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

// handleAlerts 处理告警规则的创建与列表（GET 未解决告警 / POST 新建规则）。
func (h *Handler) handleAlerts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		agentID := r.URL.Query().Get("agent_id")
		if r.URL.Query().Get("rules") == "1" {
			rules, err := h.svc.ListAlertRules(r.Context(), agentID)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"rules": rules, "total": len(rules)})
			return
		}
		resolved := r.URL.Query().Get("resolved") == "1"
		alerts, err := h.svc.ListAlerts(r.Context(), agentID, resolved)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"alerts": alerts, "total": len(alerts)})
	case http.MethodPost:
		var rule AlertRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		if rule.AgentID == "" {
			writeErr(w, http.StatusBadRequest, "agent_id is required")
			return
		}
		if rule.Type == "" {
			writeErr(w, http.StatusBadRequest, "type is required")
			return
		}
		alert, err := h.svc.CreateAlert(r.Context(), rule)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]interface{}{"rule": rule, "alert": alert})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleAlertByID 处理告警规则的删除与告警的处置。
//
// 路由：
//
//	POST /alerts/{id}/resolve          — 解决告警（兼容旧契约，无处置人时记 system）
//	POST /alerts/{id}/ack              — 认领告警
//	POST /alerts/{id}/reopen           — 重新打开告警
//	GET  /alerts/{id}/dispositions     — 查看处置记录
//	DELETE /alerts/{id}                — 删除告警规则
func (h *Handler) handleAlertByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/monitor/alerts/")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "alert id is required")
		return
	}

	// 子资源路由优先匹配：/alerts/{id}/<action>
	for _, sub := range []struct {
		suffix string
		handle func(http.ResponseWriter, *http.Request, string)
	}{
		{"/resolve", h.handleResolveAlert},
		{"/ack", h.handleAckAlert},
		{"/reopen", h.handleReopenAlert},
		{"/dispositions", h.handleAlertDispositions},
	} {
		if strings.HasSuffix(id, sub.suffix) {
			sub.handle(w, r, strings.TrimSuffix(id, sub.suffix))
			return
		}
	}

	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := h.svc.DeleteAlert(r.Context(), id); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "rule_id": id})
}

// decodeDisposition 解析处置请求体。
//
// 兼容两种用法：正文为空（旧契约）时使用调用方给出的默认处置人；
// 正文存在时按 DispositionRequest 解析。
func decodeDisposition(r *http.Request, defaultOperator string) (DispositionRequest, error) {
	var req DispositionRequest
	if r.Body == nil {
		req.Operator = defaultOperator
		return req, nil
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		// 空 body（EOF）视为「无正文」，不算错误。
		if err.Error() == "EOF" {
			req.Operator = defaultOperator
			return req, nil
		}
		return req, err
	}
	if req.Operator == "" {
		// 正文里没写处置人时，回退到认证身份，避免整条链路因缺字段而失败。
		req.Operator = defaultOperator
	}
	return req, nil
}

// operatorFromRequest 从请求里解析处置人。
//
// 处置人来源优先级：
//  1. 请求体 / 查询参数显式给出（供自动化与测试使用）；
//  2. JWT claims 中的 user id（由认证中间件写入 context）。
//
// 注意：这里刻意**不**从任意自定义 header 取值 —— 否则任何人都能伪造处置人。
func operatorFromRequest(r *http.Request) string {
	if v := r.URL.Query().Get("operator"); v != "" {
		return v
	}
	if id := SubjectIDFromContext(r.Context()); id != "" {
		return id
	}
	return "system"
}

func (h *Handler) handleResolveAlert(w http.ResponseWriter, r *http.Request, alertID string) {
	if alertID == "" {
		writeErr(w, http.StatusBadRequest, "alert id is required")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeDisposition(r, operatorFromRequest(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	req.Action = AlertActionResolve
	alert, err := h.svc.ResolveAlertWithDisposition(r.Context(), alertID, req)
	if alert == nil {
		writeErr(w, dispositionErrorStatus(err), err.Error())
		return
	}
	// 状态迁移成功、审计留痕失败时仍返回 200，并用 record_error 暴露问题：
	// 处置已经真实发生，报 5xx 会让调用方重试并产生重复处置记录。
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":       string(alert.Status),
		"alert":        alert,
		"record_error": errText(err),
	})
}

func (h *Handler) handleAckAlert(w http.ResponseWriter, r *http.Request, alertID string) {
	if alertID == "" {
		writeErr(w, http.StatusBadRequest, "alert id is required")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeDisposition(r, operatorFromRequest(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	req.Action = AlertActionAck
	alert, err := h.svc.AcknowledgeAlert(r.Context(), alertID, req)
	if alert == nil {
		writeErr(w, dispositionErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":       string(alert.Status),
		"alert":        alert,
		"record_error": errText(err),
	})
}

func (h *Handler) handleReopenAlert(w http.ResponseWriter, r *http.Request, alertID string) {
	if alertID == "" {
		writeErr(w, http.StatusBadRequest, "alert id is required")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := decodeDisposition(r, operatorFromRequest(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	req.Action = AlertActionReopen
	alert, err := h.svc.ReopenAlert(r.Context(), alertID, req)
	if alert == nil {
		writeErr(w, dispositionErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":       string(alert.Status),
		"alert":        alert,
		"record_error": errText(err),
	})
}

// errText 把可能为 nil 的错误转成可序列化的字符串（nil → 空串）。
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (h *Handler) handleAlertDispositions(w http.ResponseWriter, r *http.Request, alertID string) {
	if alertID == "" {
		writeErr(w, http.StatusBadRequest, "alert id is required")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	records, err := h.svc.ListDispositions(r.Context(), alertID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"alert_id":     alertID,
		"dispositions": records,
		"total":        len(records),
	})
}

// dispositionErrorStatus 把处置错误映射为 HTTP 状态码。
//
// 「找不到」与「状态不允许」必须区分：前者 404，后者 409（冲突）。
// 统一返回 404 会让运维误以为告警不存在，从而重复触发。
func dispositionErrorStatus(err error) int {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not found"):
		return http.StatusNotFound
	case strings.Contains(msg, "already"), strings.Contains(msg, "cannot"):
		return http.StatusConflict
	case strings.Contains(msg, "required"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func (h *Handler) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	dash, err := h.svc.GetDashboard(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 每个 Agent 附带健康分。
	writeJSON(w, http.StatusOK, dash)
}

// parseWindow 解析 start/end 查询参数，缺省为最近 1 小时。
func parseWindow(r *http.Request) (time.Time, time.Time, error) {
	q := r.URL.Query()
	end := time.Now().UTC()
	start := end.Add(-time.Hour)

	if v := q.Get("end"); v != "" {
		t, err := parseTimeParam(v)
		if err != nil {
			return start, end, err
		}
		end = t
	}
	if v := q.Get("start"); v != "" {
		t, err := parseTimeParam(v)
		if err != nil {
			return start, end, err
		}
		start = t
	}
	if v := q.Get("duration"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return start, end, err
		}
		start = end.Add(-d)
	}
	return start, end, nil
}

func parseTimeParam(v string) (time.Time, error) {
	// 支持 Unix 秒时间戳与 RFC3339 两种写法。
	if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(sec, 0).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
