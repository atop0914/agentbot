package monitor

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ===== Day 24：时间序列与告警处置的 HTTP 接口 =====

// RegisterRoutes 之外的扩展路由。
//
// 单独成文件是为了让 Day 24 新增的接口与既有监控接口在阅读时分离；
// 注册入口仍然是同一个 RegisterRoutes（见 handler.go）。
func (h *Handler) registerSeriesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/monitor/series/", h.handleSeries)
	mux.HandleFunc("/api/v1/monitor/task-outcomes", h.handleTaskOutcomes)
	mux.HandleFunc("/api/v1/monitor/alert-dispositions", h.handleDispositionSummary)
}

// handleSeries 处理 /api/v1/monitor/series/{agentID}。
//
// 查询参数：duration（默认 30m）、start/end（RFC3339 或 Unix 秒）。
// duration 与 start/end 同时给出时，start/end 优先。
func (h *Handler) handleSeries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	agentID := strings.TrimPrefix(r.URL.Path, "/api/v1/monitor/series/")
	if agentID == "" {
		writeErr(w, http.StatusBadRequest, "agent id is required")
		return
	}

	// 显式给出 start/end 时按窗口查询，否则按 duration 取最近窗口。
	if r.URL.Query().Get("start") != "" || r.URL.Query().Get("end") != "" {
		start, end, err := parseWindow(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		series, err := h.svc.GetTimeSeries(r.Context(), agentID, start, end)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, series)
		return
	}

	duration := DefaultSeriesWindow
	if v := r.URL.Query().Get("duration"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid duration: "+err.Error())
			return
		}
		duration = d
	}
	series, err := h.svc.GetTimeSeriesByDuration(r.Context(), agentID, duration)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, series)
}

// handleTaskOutcomes 处理任务结果上报：POST /api/v1/monitor/task-outcomes。
func (h *Handler) handleTaskOutcomes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var outcome TaskOutcome
	if err := json.NewDecoder(r.Body).Decode(&outcome); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if outcome.AgentID == "" {
		writeErr(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	if err := h.svc.ReportTaskOutcome(r.Context(), outcome); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, outcome)
}

// handleDispositionSummary 处理 GET /api/v1/monitor/alert-dispositions
// 与 GET /api/v1/monitor/alerts/{id}/dispositions。
func (h *Handler) handleDispositionSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	agentID := r.URL.Query().Get("agent_id")
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "invalid limit: must be a non-negative integer")
			return
		}
		limit = n
	}
	summary, err := h.svc.DispositionSummary(r.Context(), agentID, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}
