package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"
)

// integrity.go 实现导出产物的完整性校验。
//
// 为什么导出要带摘要：审计导出的用途是「归档」和「交给第三方审阅」，
// 离开平台之后没人能证明它没被改过。所以导出必须自带
// 「条数 + 内容摘要 + 覆盖范围」，并提供校验能力 —— 拿到文件的
// 一方可以独立重算摘要，对不上就知道产物被动过。
//
// 与 Mini Shai-Hulud 的教训一致：**「签名有效」不等于「产物可信」**，
// 同理「文件能打开」不等于「内容完整」。能自证是底线要求。

// ExportManifest 描述一次导出产物的元信息。
type ExportManifest struct {
	// Format 是导出格式（json / csv）。
	Format string `json:"format"`
	// Count 是产物中实际包含的事件条数。
	Count int `json:"count"`
	// SHA256 是产物正文（不含 manifest 本身）的十六进制摘要。
	SHA256 string `json:"sha256"`
	// Algo 是摘要算法标识，便于将来换算法而不破坏解析。
	Algo string `json:"algo"`
	// Truncated 标记本次导出是否因超限被截断。
	Truncated bool `json:"truncated"`
	// Limit 触发截断的条数上限（未截断时为 0）。
	Limit int `json:"limit,omitempty"`
	// TotalMatched 是命中过滤条件的总条数（可能大于 Count）。
	TotalMatched int `json:"total_matched,omitempty"`
	// WindowStart / WindowEnd 是产物覆盖的时间范围；无事件时为空。
	WindowStart string `json:"window_start,omitempty"`
	WindowEnd   string `json:"window_end,omitempty"`
	// GeneratedAt 是导出时刻。
	GeneratedAt time.Time `json:"generated_at"`
}

// ExportResult 是导出产物与元信息的组合。
type ExportResult struct {
	// Data 是可直接落盘/下载的产物；json 格式下已内嵌 manifest。
	Data []byte
	// Manifest 是本次导出的元信息。
	Manifest ExportManifest
	// ContentType 是建议的 HTTP Content-Type。
	ContentType string
	// Filename 是建议的文件名。
	Filename string
}

// ComputeDigest 计算导出正文的 SHA256 摘要。
func ComputeDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// VerifyExport 校验一份导出产物是否与其 manifest 一致。
//
// 返回值 ok 为 false 时 reason 说明原因（条数不符 / 摘要不符 / 解析失败）。
// 校验对 json 与 csv 都适用：csv 的条数是正文行数减表头。
func VerifyExport(data []byte, manifest ExportManifest) (bool, string) {
	if manifest.Algo != "" && manifest.Algo != ExportHashAlgo {
		return false, "unsupported digest algorithm"
	}

	switch normalizeFormat(manifest.Format) {
	case FormatCSV:
		rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
		if err != nil {
			return false, "parse csv: " + err.Error()
		}
		// 表头行不计入条数。
		n := len(rows)
		if n > 0 {
			n--
		}
		if n != manifest.Count {
			return false, "count mismatch"
		}
	default:
		// JSON 产物含 manifest 自身，摘要只覆盖 records 的正则化序列化，
		// 因此这里重算时用同一函数，避免自指。
		records, _, err := parseExportJSON(data)
		if err != nil {
			return false, "parse json: " + err.Error()
		}
		if len(records) != manifest.Count {
			return false, "count mismatch"
		}
		if ComputeDigest(canonicalJSON(records)) != manifest.SHA256 {
			return false, "digest mismatch"
		}
		return true, ""
	}

	if ComputeDigest(data) != manifest.SHA256 {
		return false, "digest mismatch"
	}
	return true, ""
}

// exportPayload 是 JSON 导出的完整结构：manifest + records。
//
// manifest 字段用 json.RawMessage 承接，以便解析时无损区分
// 「新格式（带 manifest）」与「旧格式（只有 count/records）」。
type exportPayload struct {
	Manifest rawManifest    `json:"manifest"`
	Records  []*EventRecord `json:"records"`
}

// rawManifest 用一个自定义类型包住 manifest，便于同时拿到「原始字节是否存在」
// 与「结构化之后的取值」，避免为了做格式探测把 JSON 解析两遍。
type rawManifest struct {
	Raw   json.RawMessage
	Value ExportManifest
}

// UnmarshalJSON 记录原始字节并解析出结构体。
func (r *rawManifest) UnmarshalJSON(data []byte) error {
	r.Raw = append([]byte(nil), data...)
	return json.Unmarshal(data, &r.Value)
}

// MarshalJSON 输出结构体本身（Raw 仅用于解析期探测格式）。
func (r rawManifest) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.Value)
}

// buildExport 组装导出产物。
//
// 截断语义：records 由调用方按 totalMatched 判断是否已被截断，
// 本函数只负责如实标注 —— 绝不能把「只导出了前 1000 条」呈现成完整导出。
func buildExport(records []*EventRecord, format string, totalMatched int, truncated bool, limit int, now time.Time) (ExportResult, error) {
	format = normalizeFormat(format)
	if records == nil {
		records = []*EventRecord{}
	}

	m := ExportManifest{
		Format:       format,
		Count:        len(records),
		Algo:         ExportHashAlgo,
		Truncated:    truncated,
		Limit:        limit,
		TotalMatched: totalMatched,
		GeneratedAt:  now.UTC(),
	}
	if len(records) > 0 {
		m.WindowStart = earliest(records).UTC().Format(time.RFC3339)
		m.WindowEnd = latest(records).UTC().Format(time.RFC3339)
	}

	res := ExportResult{Manifest: m}
	switch format {
	case FormatCSV:
		data := exportCSV(records)
		// CSV 无法内嵌 manifest，摘要直接覆盖正文。
		m.SHA256 = ComputeDigest(data)
		res.Manifest = m
		res.Data = data
		res.ContentType = "text/csv; charset=utf-8"
	default:
		// 摘要覆盖 records 的正则化序列化（不含 manifest 自身），
		// 这样校验方可以独立重算。
		m.SHA256 = ComputeDigest(canonicalJSON(records))
		res.Manifest = m

		payload := exportPayload{
			Manifest: rawManifest{Value: m, Raw: json.RawMessage("null")},
			Records:  records,
		}
		buf := &bytes.Buffer{}
		enc := json.NewEncoder(buf)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(payload); err != nil {
			return ExportResult{}, err
		}
		res.Data = buf.Bytes()
		res.ContentType = "application/json"
	}
	return res, nil
}

// canonicalJSON 生成与字段顺序无关、与缩进无关的正则化 JSON 字节，
// 用作摘要输入，保证导出方与校验方算出的摘要一致。
//
// 实现说明：encoding/json 对 map 的键是按字典序输出的，对 struct 是按
// 字段声明顺序输出的，两者都是稳定的，因此「紧凑编码」即可作为正则形式。
func canonicalJSON(records []*EventRecord) []byte {
	if records == nil {
		records = []*EventRecord{}
	}
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(records); err != nil {
		// 审计记录是我们自己的结构体，不含不可序列化字段；
		// 万分之一的可能性下退化为空摘要输入，由校验方判为不符。
		return nil
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// parseExportJSON 解析 JSON 导出产物，返回记录与 manifest。
//
// 兼容两种形态：
//   - 新格式：manifest + records，直接沿用导出时写入的 manifest；
//   - 旧格式：只有 {count, records}，此时按当前算法的紧凑编码重算摘要 ——
//     这样历史归档文件仍然可以校验，不会因为格式升级而变成「不可验证」。
//
// 判定方式刻意用「manifest 字段是否存在」而不是「records 是否非空」：
// 空导出（records 为 []）也是合法的新格式，不能因为没记录就回退到旧格式分支
// 去丢掉 manifest 里的 sha256/时间窗。
func parseExportJSON(data []byte) ([]*EventRecord, ExportManifest, error) {
	var probe struct {
		Manifest json.RawMessage `json:"manifest"`
		Count    int             `json:"count"`
		Records  []*EventRecord  `json:"records"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, ExportManifest{}, err
	}

	records := probe.Records
	if records == nil {
		records = []*EventRecord{}
	}

	// 新格式：有 manifest 字段。
	var payload exportPayload
	if err := json.Unmarshal(data, &payload); err == nil && len(payload.Manifest.Raw) > 0 {
		m := payload.Manifest.Value
		return payload.Records, m, nil
	}

	m := ExportManifest{
		Format: FormatJSON,
		Count:  probe.Count,
		Algo:   ExportHashAlgo,
		SHA256: ComputeDigest(canonicalJSON(records)),
	}
	if m.Count == 0 {
		m.Count = len(records)
	}
	return records, m, nil
}

// normalizeFormat 归一化导出格式，空值默认 json。
func normalizeFormat(f string) string {
	f = lowerTrim(f)
	if f == "" {
		return FormatJSON
	}
	return f
}

// earliest / latest 取记录集中的最小/最大时间戳。
func earliest(records []*EventRecord) time.Time {
	t := records[0].Timestamp
	for _, r := range records[1:] {
		if r.Timestamp.Before(t) {
			t = r.Timestamp
		}
	}
	return t
}

func latest(records []*EventRecord) time.Time {
	t := records[0].Timestamp
	for _, r := range records[1:] {
		if r.Timestamp.After(t) {
			t = r.Timestamp
		}
	}
	return t
}

// isOverWindow 判断时间窗跨度是否超过上限，返回超限与跨度。
func isOverWindow(start, end *time.Time) (bool, time.Duration) {
	if start == nil || end == nil {
		return false, 0
	}
	d := end.Sub(*start)
	return d > MaxExportRange, d
}

// parseCountQuery 解析形如 "count=3" 的查询参数（校验接口用）。
func parseCountQuery(raw string) (int, bool) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
