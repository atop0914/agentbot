package audit

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Handler 提供审计日志的 HTTP 接口。
type Handler struct {
	svc Service
	// retention 可选：配置后开放留存策略读写与清理接口。
	retention *RetentionService
}

// NewHandler 创建审计 HTTP 处理器。
func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

// WithRetention 挂载留存服务，启用 /retention 系列接口。
//
// 不挂在 NewHandler 里是为了让「只想读日志」的调用方不必构造留存策略，
// 同时让「留存未配置」成为可探测的状态（接口返回 503 而不是静默成功）。
func (h *Handler) WithRetention(rs *RetentionService) *Handler {
	h.retention = rs
	return h
}

// RegisterRoutes 注册审计相关路由。
//
//	GET    /api/v1/audit/events            查询事件（支持过滤/分页/排序）
//	POST   /api/v1/audit/events            写入事件（供内部模块或后台工具补录）
//	GET    /api/v1/audit/events/{id}       查看单条事件
//	GET    /api/v1/audit/stats             按维度聚合统计
//	GET    /api/v1/audit/distinct          维度去重取值（筛选器选项）
//	GET    /api/v1/audit/export            导出（?format=json|csv），multipart 返回产物 + manifest
//	POST   /api/v1/audit/export/verify     校验导出产物与 manifest 是否一致
//	GET    /api/v1/audit/retention         查询留存策略
//	PUT    /api/v1/audit/retention         更新留存策略
//	POST   /api/v1/audit/retention/purge   按策略清理（默认 dry-run）
//	DELETE /api/v1/audit/purge             按显式 before 时间清理
//
// 注意：/export/verify 与 /retention/purge 都是「集合路径 + 子资源」，
// 前缀规则 /api/v1/audit/ 覆盖得到，但权限表里仍需按其真实语义登记。
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/audit/events", h.handleEvents)
	mux.HandleFunc("/api/v1/audit/events/", h.handleEventByID)
	mux.HandleFunc("/api/v1/audit/stats", h.handleStats)
	mux.HandleFunc("/api/v1/audit/distinct", h.handleDistinct)
	mux.HandleFunc("/api/v1/audit/export", h.handleExport)
	mux.HandleFunc("/api/v1/audit/export/verify", h.handleVerify)
	mux.HandleFunc("/api/v1/audit/retention", h.handleRetention)
	mux.HandleFunc("/api/v1/audit/retention/purge", h.handleRetentionPurge)
	mux.HandleFunc("/api/v1/audit/purge", h.handlePurge)
}

// actorFromRequest 从请求里取操作者标识，用于留存策略的变更留痕。
//
// 这里刻意只读 context 里已认证的身份（由认证中间件写入），
// **不读任何客户端可伪造的请求头** —— 否则「谁改了留存策略」就不可信了。
func actorFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if id, ok := r.Context().Value(actorContextKey{}).(string); ok {
		return id
	}
	return ""
}

// actorContextKey 是审计模块在 context 中保存操作者的键。
type actorContextKey struct{}

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

// statusFor 把服务层的哨兵错误映射为 HTTP 状态码。
func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrActionRequired),
		errors.Is(err, ErrEventRequired),
		errors.Is(err, ErrCutoffRequired),
		errors.Is(err, ErrUnknownDistinctField),
		errors.Is(err, ErrUnsupportedFormat),
		errors.Is(err, ErrExportRangeTooLarge),
		errors.Is(err, ErrInvalidRetention):
		return http.StatusBadRequest
	case errors.Is(err, ErrEventNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrIDRequired):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listEvents(w, r)
	case http.MethodPost:
		h.createEvent(w, r)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	records, err := h.svc.Query(r.Context(), filter)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	if records == nil {
		records = []*EventRecord{}
	}

	total, err := h.svc.Count(r.Context(), filter)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}

	// total 在分页场景下应为命中条件的全量数量，因此用不含 limit 的条件重算。
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total":    total,
		"limit":    filter.Limit,
		"offset":   filter.Offset,
		"events":   records,
		"has_more": filter.Offset+len(records) < total,
	})
}

func (h *Handler) createEvent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Event
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	rec, err := h.svc.LogEvent(r.Context(), body.Event)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

func (h *Handler) handleEventByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/audit/events/")
	id = strings.Trim(id, "/")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "event id is required")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	rec, err := h.svc.GetEvent(r.Context(), id)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	dimension := r.URL.Query().Get("dimension")
	if dimension == "" {
		dimension = DistinctAction
	}

	filter, err := parseFilter(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	stats, err := h.svc.Stats(r.Context(), filter, dimension)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}

	total := 0
	for _, n := range stats {
		total += n
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"dimension": dimension,
		"total":     total,
		"buckets":   stats,
	})
}

func (h *Handler) handleDistinct(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	field := r.URL.Query().Get("field")
	if field == "" {
		writeErr(w, http.StatusBadRequest, "field is required")
		return
	}

	values, err := h.svc.Distinct(r.Context(), field)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"field":  field,
		"values": values,
	})
}

func (h *Handler) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	filter, err := parseFilter(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = FormatJSON
	}

	res, err := h.svc.Export(r.Context(), filter, format)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}

	// Multipart 导出：manifest 作为独立 part 随产物一起下载。
	// 这样归档文件自带校验依据，拿到文件的一方可以独立重算摘要。
	if err := writeExportResponse(w, res); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (h *Handler) handlePurge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	raw := r.URL.Query().Get("before")
	if raw == "" {
		// 未指定时默认保留 30 天。
		raw = time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339)
	}

	cutoff, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "before must be RFC3339, e.g. 2026-01-01T00:00:00Z")
		return
	}

	removed, err := h.svc.Purge(r.Context(), cutoff)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"removed": removed,
		"before":  cutoff.UTC().Format(time.RFC3339),
	})
}

// parseFilter 从 query string 构造过滤条件。
//
// start_time / end_time 兼容 RFC3339 与 Unix 秒两种写法。
func parseFilter(r *http.Request) (Filter, error) {
	q := r.URL.Query()
	f := Filter{
		Actor:      q.Get("actor"),
		ActorType:  q.Get("actor_type"),
		EventType:  q.Get("event_type"),
		Resource:   q.Get("resource"),
		ResourceID: q.Get("resource_id"),
		Status:     q.Get("status"),
		IPAddress:  q.Get("ip_address"),
		SortBy:     q.Get("sort_by"),
		SortOrder:  q.Get("sort_order"),
		Limit:      parseLimit(q.Get("limit"), defaultLimit),
		Offset:     parseOffset(q.Get("offset")),
	}

	if raw := q.Get("start_time"); raw != "" {
		t, err := parseTime(raw)
		if err != nil {
			return f, err
		}
		f.StartTime = &t
	}
	if raw := q.Get("end_time"); raw != "" {
		t, err := parseTime(raw)
		if err != nil {
			return f, err
		}
		f.EndTime = &t
	}
	// action 是 event_type 的别名，兼容两种调用习惯。
	if f.EventType == "" {
		f.EventType = q.Get("action")
	}
	return f, nil
}

func parseTime(raw string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(sec, 0).UTC(), nil
	}
	return time.Time{}, errors.New("invalid time format, expected RFC3339 or unix seconds: " + raw)
}
