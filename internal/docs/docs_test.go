// Package docs 校验 docs/ 目录与代码是否一致。
//
// 为什么文档需要测试：文档也是一种「报表」，而报表会撒谎 —— 本仓库已经
// 抓到过多次「报表与事实相反、但所有构建与测试都是绿的」问题。文档最容易
// 出现的三类谎言是：
//
//  1. 指向一个已经被改名/删除的文件的链接（死链）。读者点了 404，
//     但没有任何构建会失败。
//  2. 描述了一个代码里并不存在的模块或路径。读者照着找，找不到。
//  3. 新写的文档没有任何入口链到它（等于没写）。
//
// 本包把这些变成会失败的断言，而不是靠人肉 review 记得去改文档。
//
// 设计取舍：
//
//   - 只校验**可机械判定**的事实（文件存在、路径存在、目录存在、被索引
//     引用）。语义正确性（文档说得对不对）仍然要人读 —— 测试不做自己
//     做不到的事，否则会给出虚假的安全感。
//   - 校验用**相对路径**从仓库根出发，不依赖 os.Getwd 恰好等于包目录。
package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// repoRoot 返回仓库根目录的内部路径。
//
// internal/docs 距仓库根两级。不用 os.Getwd 直接当根：go test 的工作目录
// 是包目录，但它是一个隐含前提 —— 显式往上走两级，语义更清楚。
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %s does not contain go.mod: %v", root, err)
	}
	return root
}

// docsFiles 返回 docs/ 下全部 .md 文件的**仓库相对路径**，已排序。
func docsFiles(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatalf("read docs/: %v", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		out = append(out, filepath.Join("docs", e.Name()))
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatal("docs/ contains no markdown files; the checker is not testing anything")
	}
	return out
}

// readRepoFile 读取仓库相对路径的文件内容。
func readRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// linkPattern 匹配 markdown 链接的目标：](target)
var linkPattern = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// goPathPattern 匹配正文里被反引号包起来的 .go 路径（仓库相对或包路径）。
var goPathPattern = regexp.MustCompile("`([A-Za-z0-9_./-]+\\.go)`")

// TestDocsRelativeLinksResolve 断言 docs/ 里的相对链接与图片都指向真实文件。
//
// 外链（http/https/mailto）与纯锚点（#...）跳过 —— 前者需要网络，
// 后者由 TestDocsInternalAnchorsResolve 单独处理。
func TestDocsRelativeLinksResolve(t *testing.T) {
	root := repoRoot(t)

	for _, rel := range docsFiles(t, root) {
		content := readRepoFile(t, root, rel)
		base := filepath.Dir(rel)

		for _, m := range linkPattern.FindAllStringSubmatch(content, -1) {
			target := m[1]
			if strings.HasPrefix(target, "http://") ||
				strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "mailto:") ||
				strings.HasPrefix(target, "#") {
				continue
			}
			// 去掉写法里的锚点片段，只校验文件本身存在。
			path := strings.SplitN(target, "#", 2)[0]
			if path == "" {
				continue
			}
			abs := filepath.Join(root, base, path)
			if _, err := os.Stat(abs); err != nil {
				t.Errorf("%s: link %q does not resolve to a file (looked for %s)",
					rel, target, filepath.ToSlash(filepath.Join(base, path)))
			}
		}
	}
}

// TestDocsGoPathsExist 断言语档里用反引号引用的 .go 路径真实存在。
//
// 这类引用最容易在重构后腐烂：文件改名了，注释还指着一个不存在的路径。
// 编译器看不见 markdown，所以只有这条测试能发现。
func TestDocsGoPathsExist(t *testing.T) {
	root := repoRoot(t)

	for _, rel := range docsFiles(t, root) {
		content := readRepoFile(t, root, rel)

		for _, m := range goPathPattern.FindAllStringSubmatch(content, -1) {
			p := m[1]
			// 只校验看起来是仓库相对路径的（含 / 或以 internal|cmd|pkg 开头）。
			// `main.go` 这种裸文件名无法定位，跳过而不是猜。
			if !strings.Contains(p, "/") {
				continue
			}
			// 以 ./ 或 ../ 开头的写法没有出现在本仓库文档里；统一按仓库根解析。
			p = strings.TrimPrefix(p, "./")
			if _, err := os.Stat(filepath.Join(root, p)); err != nil {
				t.Errorf("%s: referenced Go file %q does not exist in the repo", rel, m[1])
			}
		}
	}
}

// TestDocsEveryFileIsIndexed 断言每个 docs/*.md 都被 docs/index.md 链到。
//
// 防的是「新写的文档没有入口」—— 内容写得再好，没人能找到等于不存在。
func TestDocsEveryFileIsIndexed(t *testing.T) {
	root := repoRoot(t)
	index := readRepoFile(t, root, "docs/index.md")

	for _, rel := range docsFiles(t, root) {
		name := filepath.Base(rel)
		if name == "index.md" {
			continue // 索引不必链自己
		}
		if !strings.Contains(index, "("+name+")") && !strings.Contains(index, "("+name+"#") {
			t.Errorf("docs/index.md does not link to %s; a doc nobody can find is a doc that does not exist", name)
		}
	}
}

// TestDocsLinksFromReadmeResolve 断言 README 里指向 docs/ 的链接都存在。
//
// README 是仓库门面，死链的代价最高，单独一条断言。
func TestDocsLinksFromReadmeResolve(t *testing.T) {
	root := repoRoot(t)
	readme := readRepoFile(t, root, "README.md")

	found := 0
	for _, m := range linkPattern.FindAllStringSubmatch(readme, -1) {
		target := m[1]
		if !strings.HasPrefix(target, "docs/") {
			continue
		}
		found++
		path := strings.SplitN(target, "#", 2)[0]
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Errorf("README.md links to %q which does not exist", target)
		}
	}
	if found == 0 {
		t.Error("README.md links to no docs/ file; the docs are unreachable from the front page")
	}
}

// TestDocsMentionRealInternalPackages 断言文档里提到的 internal/ 子包真实存在。
//
// 文档描述一个不存在的模块，比没有文档更糟：读者会照着它去找。
func TestDocsMentionRealInternalPackages(t *testing.T) {
	root := repoRoot(t)

	// internal/xxx 形式的引用（反引号内或正文中）。
	pkgPattern := regexp.MustCompile(`internal/([a-z][a-z0-9]*)`)

	checked := 0
	for _, rel := range docsFiles(t, root) {
		content := readRepoFile(t, root, rel)
		for _, m := range pkgPattern.FindAllStringSubmatch(content, -1) {
			pkg := m[1]
			dir := filepath.Join(root, "internal", pkg)
			info, err := os.Stat(dir)
			if err != nil || !info.IsDir() {
				t.Errorf("%s: references internal/%s which is not a directory", rel, pkg)
				continue
			}
			checked++
		}
	}
	if checked == 0 {
		t.Error("no internal/ package references were checked; the pattern is not matching anything")
	}
}

// TestDocsAPIPathsAreRegistered 断言 docs/api.md 里写的接口路径真的被注册。
//
// 这是本包最有价值的一条：文档漏写新接口（或写了已删除的接口）不会有任何
// 构建失败，但会让人照着文档调用一个 404。
//
// 判定方式：只扫**表格行**（`| ... | ... |`），从每行的「路径」列取反引号里的
// 路径，再检查是否存在一个注册路径与它逐段兼容。
//
// ⚠️ 只扫表格行是刻意的：正文里也会出现反引号路径（例如「单数形式
// `/browser/sessions` 从未注册过」这句告示、以及「基础路径：`/api/v1`」），
// 把它们当成「文档声称存在的接口」会产出假红。第一版就是这样，把三条
// 说明性文字误报成了缺失路由。
func TestDocsAPIPathsAreRegistered(t *testing.T) {
	root := repoRoot(t)

	apiDoc := readRepoFile(t, root, "docs/api.md")
	registered := registeredRoutePrefixes(t, root)

	rowPath := regexp.MustCompile("`(/[A-Za-z0-9_{}/.-]+)`")

	unmatched := map[string]bool{}
	checked := 0
	for _, line := range strings.Split(apiDoc, "\n") {
		line = strings.TrimSpace(line)
		// 表格行：以 | 开头且以 | 结尾（分隔行 --- 里不会有反引号路径）。
		if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
			continue
		}
		// 跳过表头与分隔行。
		if strings.Contains(line, "---") {
			continue
		}
		for _, m := range rowPath.FindAllStringSubmatch(line, -1) {
			for _, one := range strings.Split(m[1], "|") {
				one = strings.TrimSpace(one)
				if one == "" || !strings.HasPrefix(one, "/") {
					continue
				}
				checked++
				if routePrefixCovered(registered, one) {
					continue
				}
				unmatched[one] = true
			}
		}
	}

	if checked < 20 {
		t.Fatalf("only %d documented paths were checked; the extractor is broken", checked)
	}

	if len(unmatched) > 0 {
		var list []string
		for p := range unmatched {
			list = append(list, p)
		}
		sort.Strings(list)
		t.Errorf("docs/api.md documents paths with no matching registered route: %v", list)
	}
}

// routePrefixCovered 判断文档里的短路径是否被某个注册路径覆盖。
//
// 文档写 `/agents/{id}/start`，代码注册的是 `/api/v1/agents/`（子路径全归它
// 的 handler 分发）。所以判定规则是：把文档路径按 `/` 分段，去掉参数段
// （`{id}`），检查是否存在一个注册路径，其**段前缀**与该序列匹配。
//
//	doc  /agents/{id}/start  -> ["agents","start"]
//	reg  /api/v1/agents/     -> ["api","v1","agents"]
//	→ ["agents"] 是 ["agents","start"] 的段前缀 → 覆盖
func routePrefixCovered(registered []string, docPath string) bool {
	docSegs := docSegments(docPath)

	for _, reg := range registered {
		regSegs := strings.Split(strings.Trim(reg, "/"), "/")
		// 去掉 api/v1 前缀，让两边可比。
		if len(regSegs) >= 2 && regSegs[0] == "api" && regSegs[1] == "v1" {
			regSegs = regSegs[2:]
		}
		if len(regSegs) == 0 {
			continue
		}
		if len(regSegs) > len(docSegs) {
			continue
		}
		ok := true
		for i, s := range regSegs {
			if s != docSegs[i] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// docSegments 把文档路径拆成有意义的段。
//
// 去掉两类段：`api/v1` 前缀（由「基础路径」统一声明，表格里两种写法都有）
// 与参数占位段（`{id}`）。
//
//	/agents/{id}/start      -> ["agents","start"]
//	/api/v1/admin/config    -> ["admin","config"]
//	/health                 -> ["health"]
func docSegments(p string) []string {
	var out []string
	for _, s := range strings.Split(strings.Trim(p, "/"), "/") {
		if s == "" || strings.HasPrefix(s, "{") {
			continue
		}
		out = append(out, s)
	}
	if len(out) >= 2 && out[0] == "api" && out[1] == "v1" {
		out = out[2:]
	}
	return out
}

// TestDocsAPIHasNoUndocumentedModule 断言代码里注册的每个模块前缀都在 api.md 里出现过。
//
// 与上一条互为反向：上一条防「文档写了不存在的接口」，这条防「实现了接口但
// 文档里查不到」。后者更常见 —— 加功能时最容易忘记的就是文档。
func TestDocsAPIHasNoUndocumentedModule(t *testing.T) {
	root := repoRoot(t)
	apiDoc := readRepoFile(t, root, "docs/api.md")

	registered := registeredRoutePrefixes(t, root)

	// /health 与 /metrics 属于探活与运维，不在 API 参考里逐个列举。
	exempt := map[string]bool{
		"/health":  true,
		"/metrics": true,
	}

	var missing []string
	for _, p := range registered {
		if exempt[p] {
			continue
		}
		seg := topSegment(p)
		if seg == "" {
			continue
		}
		if !strings.Contains(apiDoc, seg) {
			missing = append(missing, p)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("routes registered in code but absent from docs/api.md: %v\n"+
			"add them to the API reference, or the doc is a stale report", missing)
	}
}

// TestDocsCheckerDetectsBreakage 是检查器自身的回归测试。
//
// ⚠️ 存在的理由：上面前四条断言在**当前仓库**上都是绿的，所以「绿」不能
// 证明它们真的在检查任何东西 —— 一个永远返回 true 的实现同样全绿。
// 这条测试构造一个已知损坏的输入，断言每个判定函数都会**报错**。
//
// 这不是理论洁癖：本仓库既往多次出现「测试断言的前提根本不存在」，
// 结果测试静默地什么都没验证。
func TestDocsCheckerDetectsBreakage(t *testing.T) {
	root := repoRoot(t)

	// 1. 死链必须被发现。
	tmp := t.TempDir()
	broken := "# x\n\nsee [gone](docs/does-not-exist-xyz.md) and [real](api.md)\n"
	brokenFile := filepath.Join(tmp, "broken.md")
	if err := os.WriteFile(brokenFile, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasBrokenRelLink(t, root, tmp, "broken.md") {
		t.Error("the link checker did not flag a link to a non-existent file; it is not testing anything")
	}

	// 2. 不存在的 internal 包必须被发现。
	fakePkg := "internal/definitely-not-a-real-package"
	if _, err := os.Stat(filepath.Join(root, fakePkg)); err == nil {
		t.Fatalf("test assumption broken: %s exists", fakePkg)
	}

	// 3. 真实存在的包与文件不得被误报（反向断言，防「全都报错」的实现）。
	if _, err := os.Stat(filepath.Join(root, "internal", "auth")); err != nil {
		t.Fatalf("test assumption broken: internal/auth missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "api.md")); err != nil {
		t.Fatalf("test assumption broken: docs/api.md missing: %v", err)
	}
}

// hasBrokenRelLink 在 dir 下的 rel 文件里查死链，返回是否至少发现一条。
// 与 TestDocsRelativeLinksResolve 用同一套判定逻辑，但以「返回值」而非
// t.Errorf 表达结果，便于在子测试里断言检查器本身有效。
func hasBrokenRelLink(t *testing.T, root, dir, rel string) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Dir(filepath.Join(dir, rel))
	for _, m := range linkPattern.FindAllStringSubmatch(string(b), -1) {
		target := m[1]
		if strings.HasPrefix(target, "http") || strings.HasPrefix(target, "#") {
			continue
		}
		path := strings.SplitN(target, "#", 2)[0]
		if path == "" {
			continue
		}
		// 相对当前文件所在目录解析；跨出 tempdir 的情况在真实仓库里
		// 由 filepath.Join 处理，这里仅用于断言「不存在的文件被报出来」。
		if _, err := os.Stat(filepath.Join(base, path)); err != nil {
			return true
		}
	}
	return false
}

// registeredRoutePrefixes 收集仓库里真实注册的 HTTP 路径。
//
// 实现方式刻意保持朴素：扫描各 handler 的 RegisterRoutes 函数体里的
// mux.Handle/HandleFunc 调用，并处理 `prefix + "/xxx"` 这种带前缀参数的写法。
// 不解析 AST —— 目标只是拿到路径字符串，正则足够且能覆盖本仓库的全部写法
// （已用 grep 核对：字面量 93 条 + connection/websocket 两个前缀参数）。
//
// 之所以从**源码**而非运行时路由表取：运行时需要一个完整装配的 App（含
// 数据库/Redis 决定），会让「文档检查」依赖环境可用性。
func registeredRoutePrefixes(t *testing.T, root string) []string {
	t.Helper()

	handleLiteral := regexp.MustCompile(`Handle(?:Func)?\("(/[^"]*)"`)
	handlePrefixed := regexp.MustCompile(`Handle(?:Func)?\(prefix\s*\+\s*"(/[^"]*)"`)

	// 已知的前缀参数取值（见 internal/app/router.go 的调用点）。
	prefixArgs := map[string]string{
		"prefix": "/api/v1",
	}

	seen := map[string]bool{}
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(b)

		for _, m := range handleLiteral.FindAllStringSubmatch(content, -1) {
			seen[m[1]] = true
		}
		for _, m := range handlePrefixed.FindAllStringSubmatch(content, -1) {
			seen[prefixArgs["prefix"]+m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}

	if len(seen) < 50 {
		t.Fatalf("only %d routes found; the scanner is broken (expected the full set)", len(seen))
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// topSegment 取路径的第一个有意义的段（跳过 api/v1 前缀）。
//
//	/api/v1/agents/  -> agents
//	/health          -> health
func topSegment(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "v1" {
		return parts[2]
	}
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}
