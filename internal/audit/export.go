package audit

import (
	"bytes"
	"encoding/csv"
	"strconv"
)

// csvHeader 是 CSV 导出的固定列顺序。
var csvHeader = []string{
	"id", "timestamp", "actor", "actor_type", "action",
	"resource", "resource_id", "status", "error", "ip_address", "user_agent",
}

// exportCSV 把事件导出为 CSV，便于导入表格做二次分析。
func exportCSV(records []*EventRecord) []byte {
	buf := &bytes.Buffer{}
	w := csv.NewWriter(buf)

	_ = w.Write(csvHeader)
	for _, rec := range records {
		_ = w.Write([]string{
			rec.ID,
			rec.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z"),
			rec.Actor,
			rec.ActorType,
			rec.Action,
			rec.Resource,
			rec.ResourceID,
			rec.Status,
			rec.Error,
			rec.IPAddress,
			rec.UserAgent,
		})
	}
	w.Flush()
	return buf.Bytes()
}

// parseLimit 解析查询参数中的 limit，非法或超界时返回 fallback。
func parseLimit(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

// parseOffset 解析查询参数中的 offset，非法时返回 0。
func parseOffset(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
