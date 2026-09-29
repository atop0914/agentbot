package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// contextBackground 是占位页取状态时使用的兜底 context。
func contextBackground() context.Context { return context.Background() }

// Handler 提供管理后台相关的 HTTP 接口。
type Handler struct {
	svc Service
	// staticDir 是前端构建产物目录；为空表示不挂载静态资源。
	staticDir string
	// staticPrefix 是静态资源的 URL 前缀，默认 /admin。
	staticPrefix string
}

// NewHandler 创建管理后台 HTTP 处理器。
func NewHandler(svc Service, staticDir string) *Handler {
	return &Handler{
		svc:          svc,
		staticDir:    staticDir,
		staticPrefix: "/admin",
	}
}

// SetStaticPrefix 覆盖静态资源 URL 前缀（需在 RegisterRoutes 之前调用）。
func (h *Handler) SetStaticPrefix(prefix string) {
	if prefix == "" {
		return
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	h.staticPrefix = strings.TrimSuffix(prefix, "/")
}

// RegisterRoutes 注册管理后台路由。
//
//	GET  /api/v1/admin/config            查看控制台配置
//	PUT  /api/v1/admin/config            更新控制台配置
//	GET  /api/v1/admin/snapshot          聚合视图（?sections=&recent_limit=&audit_window=）
//	GET  /api/v1/admin/static            静态资源挂载状态
//	GET  /admin/                         控制台首页（静态资源缺失时返回占位页）
//	GET  /admin/*                        控制台静态资源
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/admin/config", h.handleConfig)
	mux.HandleFunc("/api/v1/admin/snapshot", h.handleSnapshot)
	mux.HandleFunc("/api/v1/admin/static", h.handleStaticStatus)

	// 静态资源：同时挂前缀本身（无尾斜杠）与子树，避免多一次重定向。
	prefix := h.staticPrefix
	mux.HandleFunc(prefix, h.handleStatic)
	mux.HandleFunc(prefix+"/", h.handleStatic)
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

// handleConfig 处理 GET（读取）与 PUT（更新）控制台配置。
func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := h.svc.Config(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	case http.MethodPut, http.MethodPatch:
		var req UpdateConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		cfg, err := h.svc.UpdateConfig(r.Context(), req)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleSnapshot 处理聚合视图查询。
func (h *Handler) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	query, err := ParseSnapshotQuery(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	snap, err := h.svc.Snapshot(r.Context(), query)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleStaticStatus 返回静态资源挂载状态。
func (h *Handler) handleStaticStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	status, err := h.svc.StaticStatus(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// ParseSnapshotQuery 解析聚合视图的查询参数。
//
//	sections=overview,agents  只取指定分区（缺省取全部）
//	recent_limit=20           「最近」列表长度，上限 MaxRecentLimit
//	audit_window=24h          审计事件统计时间窗，支持 Go duration 语法
func ParseSnapshotQuery(r *http.Request) (SnapshotQuery, error) {
	q := r.URL.Query()
	var out SnapshotQuery

	if raw := strings.TrimSpace(q.Get("sections")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			name := Section(strings.TrimSpace(part))
			if name == "" {
				continue
			}
			if !name.Valid() {
				return out, fmt.Errorf("admin: unknown section %q", part)
			}
			out.Sections = append(out.Sections, name)
		}
	}

	if raw := strings.TrimSpace(q.Get("recent_limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return out, fmt.Errorf("admin: recent_limit must be an integer")
		}
		if n < 0 {
			return out, fmt.Errorf("admin: recent_limit must not be negative")
		}
		out.RecentLimit = n
	}

	if raw := strings.TrimSpace(q.Get("audit_window")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return out, fmt.Errorf("admin: audit_window must be a duration such as 24h")
		}
		if d < 0 {
			return out, fmt.Errorf("admin: audit_window must not be negative")
		}
		out.AuditWindow = d
	}

	return out.Normalize(), nil
}

// handleStatic 托管控制台前端构建产物。
//
// 产物缺失时返回 200 + 明确的占位页（而不是 404 或 panic），
// 以便开发期直接访问 /admin/ 就能看到「尚未构建」的说明。
func (h *Handler) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	rel := strings.TrimPrefix(r.URL.Path, h.staticPrefix)
	rel = strings.TrimPrefix(rel, "/")
	rel = path.Clean("/" + rel)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		rel = "index.html"
	}

	target, ok := h.resolveStaticFile(rel)
	if !ok {
		h.servePlaceholder(w, rel)
		return
	}

	// /admin/foo 这类无扩展名的深层路由交给前端 SPA 处理。
	if _, err := os.Stat(target); err != nil && filepath.Ext(rel) == "" {
		index, indexOK := h.resolveStaticFile("index.html")
		if indexOK {
			http.ServeFile(w, r, index)
			return
		}
		h.servePlaceholder(w, rel)
		return
	}
	http.ServeFile(w, r, target)
}

// resolveStaticFile 把 URL 相对路径映射为磁盘路径，并做目录逃逸防护。
func (h *Handler) resolveStaticFile(rel string) (string, bool) {
	if h.staticDir == "" {
		return "", false
	}
	root, err := filepath.Abs(h.staticDir)
	if err != nil {
		return "", false
	}
	target := filepath.Join(root, filepath.FromSlash(rel))
	// 路径穿越防护：解析后的路径必须仍在 root 之内。
	cleanTarget := filepath.Clean(target)
	if cleanTarget != root && !strings.HasPrefix(cleanTarget, root+string(os.PathSeparator)) {
		return "", false
	}
	info, err := os.Stat(cleanTarget)
	if err != nil || info.IsDir() {
		return "", false
	}
	return cleanTarget, true
}

// servePlaceholder 在静态产物缺失时返回可读的占位页。
func (h *Handler) servePlaceholder(w http.ResponseWriter, rel string) {
	status, _ := h.svc.StaticStatus(contextBackground())
	message := "admin console assets are not mounted"
	mountPath := ""
	if status != nil {
		if status.Message != "" {
			message = status.Message
		}
		mountPath = status.Dir
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, placeholderHTML, message, mountPath, rel)
}

// placeholderHTML 是占位页模板：%s 依次为原因、期望目录、请求路径。
const placeholderHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>AgentBot Admin</title>
</head>
<body>
<h1>AgentBot Admin Console</h1>
<p>%s</p>
<ul>
<li>期望的构建产物目录：<code>%s</code></li>
<li>请求路径：<code>/%s</code></li>
</ul>
<p>把前端构建产物放置到上述目录后刷新本页即可。</p>
</body>
</html>
`
