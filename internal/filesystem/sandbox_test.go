package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ⚠️ 这组测试守的是**兄弟目录绕过**，不是常见的 `../` 逃逸。
//
// 原实现用 `strings.HasPrefix(abs, basePath)` 判定越界，于是：
//
//	base          = <tmp>/fs
//	逃逸目标       = <tmp>/fs-evil/escape.txt
//	HasPrefix 结果 = true  ← 被判成「在沙箱内」
//
// 也就是说，只要沙箱根目录旁边存在一个**同前缀的兄弟目录**，路径校验就整个失效。
// 这在真实部署里不是理论问题：`/srv/agentbot/files` 旁边出现
// `/srv/agentbot/files-backup`、`files.bak`、`files-old` 都是常态，
// 而 `filepath.Clean` 不会改变 `/tmp/fs-evil/x` 这个结果 ——
// **问题在前缀判定本身，不在路径归一化**。
//
// 因此下面每条断言都成对写：既要「被拒绝」，也要「逃逸目标确实不存在」。

// siblingBase 构造一对同前缀的兄弟目录（base 与 base-evil），并返回管理器。
func siblingBase(t *testing.T) (mgr *LocalManager, base, sibling string) {
	t.Helper()
	parent := t.TempDir()
	base = filepath.Join(parent, "fs")
	sibling = filepath.Join(parent, "fs-evil")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatalf("mkdir sibling: %v", err)
	}
	m, err := NewLocalManager(base)
	if err != nil {
		t.Fatalf("NewLocalManager: %v", err)
	}
	return m, base, sibling
}

// TestSafePath_RejectsSiblingDirectoryPrefixBypass 是这条漏洞的回归测试。
func TestSafePath_RejectsSiblingDirectoryPrefixBypass(t *testing.T) {
	mgr, _, sibling := siblingBase(t)
	ctx := context.Background()

	// 经由 `..` 指向兄弟目录 —— 前缀判定会放行，相对路径判定必须拒绝。
	escapes := []string{
		"../fs-evil/escape.txt",
		"a/../../fs-evil/escape.txt",        // 前缀判定也漏（Clean 之后仍是绝对越界路径）
		"../fs-evil/nested/deep/escape.txt", // 中间目录不存在也必须先拒绝
	}

	for _, p := range escapes {
		t.Run(p, func(t *testing.T) {
			if _, err := mgr.WriteFile(ctx, p, []byte("pwned"), false); err == nil {
				t.Errorf("WriteFile(%q) 未被拒绝：兄弟目录绕过仍然成立", p)
			}
			if _, err := mgr.ReadFile(ctx, p); err == nil {
				t.Errorf("ReadFile(%q) 未被拒绝", p)
			}
			if _, err := mgr.ListDir(ctx, filepath.Dir(p), false, false); err == nil {
				t.Errorf("ListDir(%q) 未被拒绝", filepath.Dir(p))
			}
		})
	}

	// 反向断言：逃逸目标位置上**不得**出现任何文件。
	// 只断言「返回了 error」是不够的 —— 一个先写后报错的实现同样能通过，
	// 而数据已经落到沙箱之外了。
	var found []string
	_ = filepath.Walk(sibling, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 0 {
		t.Errorf("沙箱外的兄弟目录被写入了文件：%v", found)
	}
}

// TestSafePath_AllowsBaseItselfAndDescendants 确保收紧判定后没有误伤正常路径。
func TestSafePath_AllowsBaseItselfAndDescendants(t *testing.T) {
	mgr, base, _ := siblingBase(t)
	ctx := context.Background()

	// base 自身必须可达（前缀判定以外的第二个边界：rel == "."）
	if _, err := mgr.ListDir(ctx, ".", false, false); err != nil {
		t.Errorf("ListDir(\".\") 应被允许，却报错：%v", err)
	}

	// 正常子路径、以及「先出再进」但最终仍在 base 内的路径
	allowed := []string{
		"sub/dir/file.txt",
		"sub/../sub/dir/other.txt",
		"./a.txt",
	}
	for _, p := range allowed {
		if _, err := mgr.WriteFile(ctx, p, []byte("ok"), false); err != nil {
			t.Errorf("WriteFile(%q) 应被允许，却报错：%v", p, err)
		}
	}

	// 确认文件真的落在 base 之内
	if _, err := os.Stat(filepath.Join(base, "sub", "dir", "file.txt")); err != nil {
		t.Errorf("合法写入未落在 base 内：%v", err)
	}
	abs := filepath.Join(base, "a.txt")
	if _, err := os.Stat(abs); err != nil {
		t.Errorf("合法写入未落在 base 内：%v", err)
	}
}

// TestWithinBase_IsTheOnlyChokepoint 直接对判定函数做表驱动断言。
//
// 这里刻意把「前缀判定会怎么答」也写进用例名，避免将来有人
// 图省事退回 HasPrefix —— 那条路在 sibling 用例上必然是红的。
func TestWithinBase_IsTheOnlyChokepoint(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "root", "fs")
	cases := []struct {
		name string
		abs  string
		want bool
	}{
		{"base itself", base, true},
		{"direct child", filepath.Join(base, "a.txt"), true},
		{"nested child", filepath.Join(base, "x", "y", "z.txt"), true},
		{"sibling with common prefix", filepath.Join(string(filepath.Separator), "root", "fs-evil", "x"), false},
		{"parent", filepath.Join(string(filepath.Separator), "root"), false},
		{"unrelated", filepath.Join(string(filepath.Separator), "etc", "passwd"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := withinBase(base, tc.abs); got != tc.want {
				t.Errorf("withinBase(%q, %q) = %v, want %v", base, tc.abs, got, tc.want)
			}
			// 文档化「前缀判定在这里是错的」，防止回退
			if tc.name == "sibling with common prefix" && !strings.HasPrefix(tc.abs, base) {
				t.Logf("注意：该用例上前缀判定已失效，无需依赖 —— 但其余用例上前缀判定会误放行")
			}
		})
	}
}

// TestWithinBase_RootBase 覆盖 base 为文件系统根这一边界。
// 前缀判定在根目录上会失效（一切字符串都以 "/" 开头，包括 /etc/passwd），
// 相对路径判定则天然正确。
func TestWithinBase_RootBase(t *testing.T) {
	if !withinBase(string(filepath.Separator), filepath.Join(string(filepath.Separator), "etc", "passwd")) {
		t.Errorf("base 为 / 时，任何绝对路径都应在 base 之内")
	}
	if withinBase(filepath.Join(string(filepath.Separator), "srv"), filepath.Join(string(filepath.Separator), "etc")) {
		t.Errorf("/etc 不应被判为在 /srv 之内")
	}
}
