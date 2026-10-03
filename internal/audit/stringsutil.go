package audit

import (
	"strings"
)

// stringsutil.go 收集审计模块内部用的小工具，避免每个文件各写一遍。
//
// 放在这里而不是 service.go，是因为 redact.go / integrity.go / handler.go
// 都要用；集中在注释里说明「为什么不是直接用 strings 包」——
// 这些函数都只是固定参数的一次包装，作用是把调用点的意图写清楚。

// lowerTrim 小写化并去掉首尾空白，用于格式/维度这类枚举入参归一化。
func lowerTrim(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
