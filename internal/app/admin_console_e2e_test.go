package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atop0914/agentbot/internal/config"
)

// --- 前端构建产物的端到端托管验证（Day 3-6） ---
//
// 这一组测试回答的是「产物真的被托管出去了吗」，而不是「构建命令退出码是 0」。
// 两者的差别很大：`npm run build` 成功只说明 dist/ 里有文件，
// 但 base 配错（默认 '/' 而不是 '/admin/'）时页面访问会白屏 ——
// 构建照样成功、产物照样存在、索引页照样返回 200，
// 唯一暴露问题的地方是 index.html 里引用资源的**路径**。
// 因此这里断言的是引用路径，而不只是 HTTP 200。

// consoleDistDir 是仓库内前端产物的相对路径。
const consoleDistDir = "web/admin/dist"

// repoFile 把相对仓库根的路径解析为绝对路径。
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	// internal/app 距仓库根是两级。用 filepath.Abs 的当前目录推导，
	// 避免依赖 os.Getwd 恰好等于包目录这一隐含前提。
	abs, err := filepath.Abs(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("resolve %s: %v", rel, err)
	}
	return abs
}

// newConsoleApp 构造一个把静态目录指向真实 dist/ 的 App 与路由。
//
// 不用 newTestEnv：那条路径用的是配置默认值（开发默认 console_dir 恰好
// 也是 web/admin/dist，但依赖默认值会让测试在默认值变化时静默失效）。
// 这里显式指定目录，让测试表达的是「给定产物目录，服务端托管行为如何」。
func newConsoleApp(t *testing.T, dir string) *testEnv {
	t.Helper()
	cfg := config.DevelopmentDefault()
	cfg.Admin.ConsoleDir = dir
	a, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	ts := httptest.NewServer(NewRouter(a))
	t.Cleanup(ts.Close)

	// 控制台静态资源路由本身是 public（见 authz 权限表），
	// 因此这里不需要 token —— 用 testEnv 只是为了复用 do() 的请求构造。
	return &testEnv{t: t, server: ts, app: a}
}

// newConsoleAdminEnv 在指定静态目录的基础上，额外创建一个有 role:manage
// 权限的管理员身份。
//
// 之所以要单独一条：/api/v1/admin/static 与 /api/v1/admin/snapshot 在前缀
// 权限规则下需要 role:manage，而 /admin/* 是 public。同一份 App 配置下
// 两条路径的访问条件不同，混用一个辅助函数会让「为什么这条 401」变得难查。
func newConsoleAdminEnv(t *testing.T, dir string) *testEnv {
	t.Helper()
	env := newConsoleApp(t, dir)
	env.bootstrapAdmin(t)
	return env
}

// TestAdminConsoleServesBuiltArtifacts 验证产物存在时 /admin/ 返回真实页面。
//
// 这是 Day 3-6 的核心验收断言：此前 /admin/* 一直返回占位页
// （"admin console assets are not built yet"）。产物入库后必须变成真实页面。
func TestAdminConsoleServesBuiltArtifacts(t *testing.T) {
	dir := repoFile(t, consoleDistDir)
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		t.Fatalf("前端产物缺失：%s —— 先跑 `cd web/admin && npm run build`。"+
			"dist/ 是入库的，正常 clone 就应该存在，缺失说明入库决定被回退了", err)
	}

	env := newConsoleApp(t, dir)

	resp := env.do(http.MethodGet, "/admin/", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin/ status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	page := string(body)

	// 反向断言（本组最关键的一条）：返回的**不是**占位页。
	// 只断言 200 是不够的 —— 占位页也是 200。
	if strings.Contains(page, "admin console assets are not built yet") ||
		strings.Contains(page, "把前端构建产物放置到上述目录后刷新本页即可") {
		t.Fatalf("返回的仍是占位页而不是真实控制台：\n%s", truncate(page, 400))
	}
	if !strings.Contains(page, "AgentBot 管理控制台") {
		t.Errorf("index.html 不含控制台标题，可能不是我们的产物：\n%s", truncate(page, 400))
	}
	if !strings.Contains(page, `id="root"`) {
		t.Errorf("index.html 缺少 React 挂载点 #root")
	}
}

// TestAdminConsoleAssetPathsUseAdminBase 断言资源引用路径带 /admin/ 前缀。
//
// 这是「构建成功但页面白屏」这类 bug 的唯一捕捉点：
// Vite 的 base 默认是 '/'，产物会引用 /assets/index-xxx.js；
// 而服务端把产物挂在 /admin/ 下，浏览器去根路径取就是 404 + 白屏。
// npm run build 对此完全无感，所以断言必须落在路径上。
func TestAdminConsoleAssetPathsUseAdminBase(t *testing.T) {
	dir := repoFile(t, consoleDistDir)
	indexPath := filepath.Join(dir, "index.html")
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read %s: %v", indexPath, err)
	}
	page := string(raw)

	// 找出所有 src=/href= 的引用路径。
	refs := extractAssetRefs(page)
	if len(refs) == 0 {
		t.Fatalf("index.html 里没有任何外链资源引用，产物结构异常：\n%s", truncate(page, 400))
	}
	for _, ref := range refs {
		if !strings.HasPrefix(ref, "/admin/") {
			t.Errorf("资源引用 %q 未带 /admin/ 前缀 —— "+
				"说明 vite.config.ts 的 base 被改回了默认 '/'，浏览器会 404 白屏", ref)
		}
	}

	// 正例：每个引用路径都真的能通过服务端取到（不只是路径长得对）。
	env := newConsoleApp(t, dir)
	for _, ref := range refs {
		resp := env.do(http.MethodGet, ref, "")
		status := resp.StatusCode
		resp.Body.Close()
		if status != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200（引用路径与托管目录不一致）", ref, status)
		}
	}
}

// TestAdminConsoleSpaFallbackServesIndex 验证 SPA 深层路由回落到 index.html。
//
// 无扩展名的路径（如 /admin/agents）必须交给前端路由，
// 否则用户刷新子页面会拿到占位页。
func TestAdminConsoleSpaFallbackServesIndex(t *testing.T) {
	dir := repoFile(t, consoleDistDir)
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		t.Skipf("前端产物缺失，跳过 SPA 回落验证: %v", err)
	}
	env := newConsoleApp(t, dir)

	resp := env.do(http.MethodGet, "/admin/agents", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin/agents status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	if strings.Contains(page, "把前端构建产物放置到上述目录后刷新本页即可") {
		t.Error("/admin/agents 落到了占位页，SPA 回落失效")
	}
	if !strings.Contains(page, `id="root"`) {
		t.Errorf("SPA 回落应返回 index.html 的内容，实际：\n%s", truncate(page, 300))
	}
}

// TestAdminConsolePlaceholderWhenArtifactsMissing 是上一条的反向断言：
// 产物确实不存在时，必须回到占位页（不能因为「有回落」就编一个空页面）。
//
// 两条一起才说明「产物存在」与「产物缺失」走了不同的分支 ——
// 只测正向的话，一个永远返回 index.html 的实现也能过。
func TestAdminConsolePlaceholderWhenArtifactsMissing(t *testing.T) {
	empty := t.TempDir() // 空目录：不存在 index.html
	env := newConsoleApp(t, empty)

	resp := env.do(http.MethodGet, "/admin/", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200（占位页应当是 200 而不是 404）", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	if !strings.Contains(page, "把前端构建产物放置到上述目录后刷新本页即可") {
		t.Errorf("产物缺失时应返回占位页，实际：\n%s", truncate(page, 300))
	}
	if strings.Contains(page, `id="root"`) {
		t.Error("产物缺失时不应返回 index.html 的内容")
	}
}

// TestAdminStaticStatusReportsMounted 验证 self-check 端点如实报告挂载状态。
//
// 控制台自己会读这个端点（快照的 static 分区），所以它谎报的影响面
// 不只是 API 使用者，还包括页面上那张「静态资源」表格。
//
// 注意：/api/v1/admin/static 落在 `{Pattern: "/api/v1/admin/"}` 前缀规则下，
// 需要 role:manage —— 这是**有意的**（挂载状态会暴露服务器目录结构）。
// 因此这里必须用带权限的 testEnv，而不能像静态页面那样匿名访问。
func TestAdminStaticStatusReportsMounted(t *testing.T) {
	dir := repoFile(t, consoleDistDir)
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		t.Skipf("前端产物缺失，跳过挂载状态验证: %v", err)
	}
	env := newTestEnv(t) // 带 role-coordinator 的管理员身份

	resp := env.do(http.MethodGet, "/api/v1/admin/static", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	for _, want := range []string{`"prefix":"/admin"`} {
		if !strings.Contains(page, want) {
			t.Errorf("静态状态缺少 %s，实际：%s", want, truncate(page, 300))
		}
	}

	// mounted / index_exists 是否真为 true，取决于测试进程的 console_dir
	// 配置指向哪里 —— 不要在这里硬断言 true，否则测试会与「默认配置是否
	// 恰好指向仓库内 dist」这个无关事实耦合。改由独立的一条测试断言：
	// 显式配置 dist 目录时，状态必须如实报告已挂载。
	envWithDist := newConsoleAdminEnv(t, dir)
	resp2 := envWithDist.do(http.MethodGet, "/api/v1/admin/static", "")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200（显式配置 dist）", resp2.StatusCode)
	}
	body2, _ := io.ReadAll(resp2.Body)
	page2 := string(body2)
	for _, want := range []string{`"mounted":true`, `"index_exists":true`} {
		if !strings.Contains(page2, want) {
			t.Errorf("显式指向 dist 时静态状态缺少 %s，实际：%s", want, truncate(page2, 300))
		}
	}
}

// extractAssetRefs 从 index.html 里抓出 src/href 属性和模块预加载链接。
//
// 手写扫描而不引 HTML 解析器：这个文件的形状由 Vite 生成且极简
// （一个 script + 一个 link），引入 goquery 之类只为解析 5 行 HTML 不划算。
func extractAssetRefs(page string) []string {
	var refs []string
	for _, attr := range []string{`src="`, `href="`} {
		rest := page
		for {
			idx := strings.Index(rest, attr)
			if idx < 0 {
				break
			}
			rest = rest[idx+len(attr):]
			end := strings.Index(rest, `"`)
			if end < 0 {
				break
			}
			ref := rest[:end]
			// 只关心同源的绝对路径资源；data: / https: 外链跳过。
			if strings.HasPrefix(ref, "/") {
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

// truncate 限制报错信息里的正文长度：把整个 index.html 打进日志没意义。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(截断)"
}
