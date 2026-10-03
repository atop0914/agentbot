package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

// exporthandler.go 负责把导出结果写出去，并提供完整性校验接口。
//
// 为什么用 multipart 而不是「一个文件 + 一个 HTTP 头」：
//   - 产物是会被下载、转存、贴进工单的 —— HTTP 头在第一次转存时就丢了；
//   - manifest 必须跟着产物一起走，才能让下游（第三方审阅、外部审计）
//     独立校验。把 manifest 当成旁路元数据，等于默认「没人会校验」。

// ExportManifestPart 是 multipart 响应里 manifest 部分的字段名。
const ExportManifestPart = "manifest.json"

// writeExportResponse 把导出结果写为 multipart/mixed 响应。
//
// 结构：part 1 = 产物（application/json 或 text/csv）
//
//	part 2 = manifest.json（application/json）
//
// 注意 multipart/mixed 的每个 part 必须显式声明 Content-Type，
// 否则接收方（含 Go 自己的 multipart.Reader）会按 text/plain 处理。
func writeExportResponse(w http.ResponseWriter, res ExportResult) error {
	filename := "audit-export-" + res.Manifest.GeneratedAt.UTC().Format("20060102-150405")
	if res.Manifest.Format == FormatCSV {
		filename += ".csv"
	} else {
		filename += ".json"
	}

	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)

	productHeader := make(textproto.MIMEHeader)
	productHeader.Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"`, filename))
	productHeader.Set("Content-Type", res.ContentType)
	if res.Manifest.Truncated {
		// 截断必须显式暴露，不能让下游以为拿到的是全集。
		productHeader.Set("X-Audit-Truncated", "true")
	}
	product, err := mw.CreatePart(productHeader)
	if err != nil {
		return err
	}
	if _, err := product.Write(res.Data); err != nil {
		return err
	}

	manifestHeader := make(textproto.MIMEHeader)
	manifestHeader.Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"`, ExportManifestPart))
	manifestHeader.Set("Content-Type", "application/json")
	manifestPart, err := mw.CreatePart(manifestHeader)
	if err != nil {
		return err
	}
	rawManifest, err := json.MarshalIndent(res.Manifest, "", "  ")
	if err != nil {
		return err
	}
	if _, err := manifestPart.Write(rawManifest); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}

	w.Header().Set("Content-Type", "multipart/mixed; boundary="+mw.Boundary())
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s.manifest"`, filename))
	if res.Manifest.Truncated {
		w.Header().Set("X-Audit-Truncated", "true")
		w.Header().Set("X-Audit-Limit", itoa(res.Manifest.Limit))
	}
	w.Header().Set("X-Audit-Count", itoa(res.Manifest.Count))
	w.Header().Set("X-Audit-SHA256", res.Manifest.SHA256)
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(buf.Bytes())
	return err
}

// verifyRequest 是校验接口的请求体。
type verifyRequest struct {
	// Manifest 是导出时随产物下发的元信息。
	Manifest ExportManifest `json:"manifest"`
	// Content 是导出产物正文（UTF-8 文本）。
	//
	// 用文本而不是 base64，是为了让校验请求可以人工构造与阅读 ——
	// 这个接口的主要使用者是排障中的人。
	Content string `json:"content"`
}

// handleVerify 校验一份导出产物是否与其 manifest 一致。
//
//	POST /api/v1/audit/export/verify
//
// 返回 verified 布尔值 + reason（不通过时给出具体原因），
// 始终 200：这是「校验结果」而不是「校验请求失败」，调用方看 verified。
func (h *Handler) handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req verifyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 32<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeErr(w, http.StatusBadRequest, "content is required")
		return
	}

	ok, reason := h.svc.VerifyExport([]byte(req.Content), req.Manifest)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"verified":    ok,
		"reason":      reason,
		"count":       req.Manifest.Count,
		"sha256":      req.Manifest.SHA256,
		"verified_at": time.Now().UTC(),
	})
}

// handleRetention 读取或更新留存策略。
//
//	GET /api/v1/audit/retention   查询当前策略
//	PUT /api/v1/audit/retention   更新策略（body: {"enabled":true,"retention_days":90}）
func (h *Handler) handleRetention(w http.ResponseWriter, r *http.Request) {
	if h.retention == nil {
		writeErr(w, http.StatusServiceUnavailable, "retention service is not configured")
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, h.retention.Policy())
	case http.MethodPut, http.MethodPost:
		var body struct {
			Enabled       bool `json:"enabled"`
			RetentionDays int  `json:"retention_days"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		policy, err := h.retention.SetPolicy(RetentionPolicy{
			Enabled:       body.Enabled,
			RetentionDays: body.RetentionDays,
		}, actorFromRequest(r))
		if err != nil {
			writeErr(w, statusFor(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, policy)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// purgeRequest 是清理接口的 body（全部可选）。
type purgeRequest struct {
	// DryRun 为 true 时只预演不删除。默认 true —— 忘记传参不会误删数据。
	DryRun *bool `json:"dry_run"`
	// Now 允许注入基准时刻，便于测试与补跑。
	Now string `json:"now"`
}

// handleRetentionPurge 按留存策略执行清理（或预演）。
//
//	POST /api/v1/audit/retention/purge   body: {"dry_run": true}
//
// 未显式传 dry_run 时按 true 处理：**默认安全**。
// 真要删数据必须显式写 {"dry_run": false}。
func (h *Handler) handleRetentionPurge(w http.ResponseWriter, r *http.Request) {
	if h.retention == nil {
		writeErr(w, http.StatusServiceUnavailable, "retention service is not configured")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req purgeRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
	}

	dryRun := true
	if req.DryRun != nil {
		dryRun = *req.DryRun
	}

	now := time.Now().UTC()
	if req.Now != "" {
		t, err := parseTime(req.Now)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		now = t
	}

	plan, err := h.retention.Purge(r.Context(), now, dryRun)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plan)
}
