package sso

import "strings"

// trimSpace 是 strings.TrimSpace 的本地别名，集中在这里便于
// 全包统一处理用户输入（配置里混入空白字符会让 iss/aud 比较静默失败）。
func trimSpace(s string) string { return strings.TrimSpace(s) }

// equalFold 是大小写不敏感的字符串比较，用于算法名、scope 这类
// 大小写不敏感但必须精确匹配语义的字段。
func equalFold(a, b string) bool { return strings.EqualFold(a, b) }
