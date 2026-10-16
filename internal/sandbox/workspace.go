package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// Workspace 是一个 Agent 的独立工作目录。
//
// ⚠️ 与「共享根目录 + 路径前缀校验」的区别（这是设计上的关键选择）：
// 前缀校验方案里，隔离强度完全依赖那一段校验代码的正确性 ——
// 本仓库的 filesystem 模块就曾因 HasPrefix 判定被兄弟目录绕过
// （见 internal/filesystem/sandbox_test.go）。
// 独立目录把「隔离」变成了文件系统本身的性质：越界不是被"拦住"的，
// 而是**根本不共享前缀**，任何路径拼接都拼不到别人的目录里去。
//
// 这不能替代路径校验（同一个 Workspace 内的 `../` 仍需拦），
// 但它让单点校验的失败不再等于跨 Agent 泄漏。
type Workspace struct {
	root    string
	agentID string
}

// NewWorkspace 为 agentID 创建（或复用）一个独立工作目录。
//
// 目录布局：<root>/<sanitized-agent-id>/ —— 每个 Agent 只拿到自己这一个，
// 调用方拿不到 root 本身，也就不存在"用路径前缀跳到别人目录"的入口。
func NewWorkspace(root, agentID string) (*Workspace, error) {
	if root == "" {
		return nil, &os.PathError{Op: "workspace", Path: root, Err: os.ErrInvalid}
	}
	if agentID == "" {
		return nil, &os.PathError{Op: "workspace", Path: agentID, Err: os.ErrInvalid}
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(absRoot, sanitize(agentID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Workspace{root: dir, agentID: agentID}, nil
}

// Root 返回该 Agent 工作目录的绝对路径。
func (w *Workspace) Root() string { return w.root }

// AgentID 返回工作目录所属的 Agent。
func (w *Workspace) AgentID() string { return w.agentID }

// Resolve 把相对路径解析到本 Agent 的工作目录之内；越界即拒绝。
//
// 用 filepath.Rel 求相对关系判定，**不用** strings.HasPrefix ——
// 理由见 filesystem 模块那次的教训：前缀匹配会把
// `<root>/agent-a-evil` 这类兄弟目录当成"在 <root>/agent-a 之内"。
func (w *Workspace) Resolve(rel string) (string, error) {
	if rel == "" {
		return w.root, nil
	}
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) {
		return "", &os.PathError{
			Op:   "resolve",
			Path: rel,
			Err:  os.ErrPermission,
		}
	}
	full := filepath.Join(w.root, clean)
	relToRoot, err := filepath.Rel(w.root, full)
	if err != nil {
		return "", err
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", &os.PathError{
			Op:   "resolve",
			Path: rel,
			Err:  os.ErrPermission,
		}
	}
	return full, nil
}

// Write 在 Agent 工作目录内写文件（父目录自动创建）。
func (w *Workspace) Write(rel string, content []byte) (string, error) {
	full, err := w.Resolve(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		return "", err
	}
	return full, nil
}

// Read 读取 Agent 工作目录内的文件。
func (w *Workspace) Read(rel string) ([]byte, error) {
	full, err := w.Resolve(rel)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(full)
}

// List 返回工作目录内的相对路径列表（含子目录），已排序。
//
// ⚠️ 排序不是为了好看：map/目录遍历顺序不稳定会让同一参数两次调用
// 返回不同顺序，接口输出与分页都会因此抖动。
func (w *Workspace) List() ([]string, error) {
	var out []string
	err := filepath.Walk(w.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(w.root, path)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortStrings(out)
	return out, nil
}

// Cleanup 删除整个工作目录。
func (w *Workspace) Cleanup() error {
	return os.RemoveAll(w.root)
}

// RunScript 在本 Agent 的工作目录内、以沙箱限制执行一段脚本。
func (w *Workspace) RunScript(ctx context.Context, mgr *Manager, script string) (*RunContext, error) {
	return mgr.Run(ctx, w.agentID, w.root, script, 0)
}

// sortStrings 避免为一处排序引入 sort 包的额外 import 噪音。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
