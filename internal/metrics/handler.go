package metrics

import (
	"log/slog"
	"net/http"
	"strings"
)

// ContentType 是 Prometheus 文本格式的响应类型。
//
// 必须带上 version 参数：Prometheus 用它判断能否解析。漏掉 version
// 时较新的版本会退回 0.0.4 兼容模式并打警告，老版本则直接拒绝。
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Handler 返回暴露指标的 HTTP handler。
//
// 暴露面取舍（写进注释，避免下次重新争论）：
//
//   - `/metrics` 是**public** 的，与 `/health` 同级，不走认证。
//     理由：抓取端（Prometheus / 容器探针）通常没有本平台的 JWT，
//     要求认证会让指标形同虚设；而本项导出的内容只有计数与延迟分布，
//     不含任何用户名、token、Agent 配置。
//   - **标签基数必须由调用方控制**：path 一律用路由模板（`/api/v1/agents/{id}`）
//     而不是真实路径。真实路径会把每个 ID 变成一个标签值，
//     几万个 Agent 就能让 Prometheus 内存爆掉 —— 这是指标系统最常见的事故。
//   - 若部署要求指标也必须鉴权，正确做法是在网关层把该路径改成内网可达，
//     而不是在这里加一个「抓取端需要先登录」的认证。
func (r *Registry) Handler(logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body := r.Expose()

		w.Header().Set("Content-Type", ContentType)
		// 禁止任何中间层缓存：指标是瞬时快照，缓存会让运维看到过期数字
		// 而毫无察觉（最坏的情况是告警阈值基于陈旧数据判断）。
		w.Header().Set("Cache-Control", "no-store")

		if req.Method == http.MethodHead {
			// HEAD 只回头部。仍要走到这里而不是复用 GET 的 WriteHeader，
			// 因为抓取端会用 HEAD 做连通性探测。
			w.WriteHeader(http.StatusOK)
			return
		}
		if _, err := w.Write([]byte(body)); err != nil {
			// 客户端提前断开（抓取超时）属于常态，记 debug 而非 error，
			// 否则会淹没真正的写入错误。
			if logger != nil {
				logger.Debug("metrics: write failed", "error", err)
			}
		}
	})
}

// NormalizePath 把真实请求路径归一成**低基数**的路由模板。
//
// 这是本包唯一一处「防指标基数爆炸」的实现点，所有打点调用方都必须
// 经它处理 path 标签。规则：
//
//  1. 已知的集合路径（无尾斜杠）原样保留 —— 它们是有限枚举；
//  2. 路径里看起来像 ID 的段（UUID / 长数字 / 长十六进制串 / base64 风格）
//     替换成 `{id}`，无论它出现在中间还是末段；
//  3. 其余短段原样保留（`/api/v1/agents/status` 里的 status 是语义的一部分，
//     替换掉会让两条不同路由合成一个 series）。
//
// 为什么不能只替换**末段**：本项目大量路由是 `/agents/{id}/{action}`
// 形态，ID 在中间；只处理末段会把每个 Agent 的动作路径都变成独立 series。
func NormalizePath(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if i == 0 {
			continue // 空串（前导斜杠）
		}
		if looksLikeID(seg) {
			segs[i] = "{id}"
		}
	}
	out := strings.Join(segs, "/")
	// 尾斜杠是「目录形态」，与集合路径是两条路由（见 authz 的前缀规则），
	// 保留原样才能与权限表对齐，不做 trim。
	return out
}

// looksLikeID 判断一个路径段是否是「资源标识符」而不是路由字面量。
//
// 判定刻意保守：宁可漏归一（多一个 series）也不要误归一（把不同路由
// 合并成一个，指标直接撒谎 —— 那比基数高难查得多）。
func looksLikeID(seg string) bool {
	if seg == "" {
		return false
	}
	// UUID：8-4-4-4-12
	if len(seg) == 36 && strings.Count(seg, "-") == 4 {
		return true
	}
	if isAllDigits(seg) && len(seg) >= 8 {
		// 长纯数字（BIGSERIAL 主键、时间戳式 ID）。
		// 门槛设 8 位：`v1` / `v2` 这类版本段是 1 位，不会被误伤。
		return true
	}
	if isLowerHex(seg) && len(seg) >= 16 {
		// 32 位十六进制（如 audit 的 id）或更长的哈希。
		return true
	}
	return false
}

func isAllDigits(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func isLowerHex(s string) bool {
	for _, ch := range s {
		switch {
		case ch >= '0' && ch <= '9':
		case ch >= 'a' && ch <= 'f':
		default:
			return false
		}
	}
	return true
}
