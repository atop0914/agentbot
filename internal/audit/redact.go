package audit

import (
	"sort"
	"strings"
)

// redact.go 实现审计明细的**写入前**脱敏。
//
// 为什么必须在写入前脱敏（而不是读取时过滤）：
//  1. 审计日志会落盘、导出、进备份、被检索系统抓取。一旦明文写入，
//     「读的时候不返回」并不能消除泄漏 —— 数据已经在存储里了，
//     任何一个有存储读权限的路径（备份、导出、运维直连）都能拿到它。
//  2. CSA 2026 的研究显示 AI 服务相关密钥泄漏同比 +81%（一年 127 万条），
//     日志正是泄漏的主要出口之一。
//  3. 写入点只有一个（Repository.Append），读取点却有很多（查询/导出/聚合），
//     在写入点收敛脱敏逻辑才能真正做到不漏。
//
// 设计取舍：
//   - **保留原始字段名**（`api_key` 仍然是 `api_key`），只替换值，
//     并把被脱敏的键路径记录到 details 的 `_redacted` 列表里 ——
//     这样运维排查时知道「这里原本有个密钥」，且字段结构不被打乱，
//     而调用方依赖字段名的解析逻辑不会断。
//   - **递归处理嵌套 map / []interface{}**，因为大部分泄漏藏在
//     `{"headers": {"authorization": "..."}}` 这类结构里。
//   - 只对值做替换，不对键名做替换（键名本身是结构信息，不是秘密）。

// sensitiveKeyNames 是大小写无关的敏感字段名集合。
//
// 覆盖凭证类（token/password/secret/credential）、认证头
// （authorization/cookie/set-cookie）、私钥与各类厂商 key。
var sensitiveKeyNames = map[string]struct{}{
	"password":              {},
	"passwd":                {},
	"pwd":                   {},
	"secret":                {},
	"client_secret":         {},
	"token":                 {},
	"access_token":          {},
	"refresh_token":         {},
	"id_token":              {},
	"api_key":               {},
	"apikey":                {},
	"api_token":             {},
	"authorization":         {},
	"auth":                  {},
	"auth_token":            {},
	"cookie":                {},
	"set-cookie":            {},
	"session":               {},
	"session_id":            {},
	"private_key":           {},
	"secret_key":            {},
	"signing_key":           {},
	"encryption_key":        {},
	"credential":            {},
	"credentials":           {},
	"access_key":            {},
	"access_key_id":         {},
	"secret_access_key":     {},
	"aws_secret_access_key": {},
	"bearer":                {},
	"jwt":                   {},
	"dsn":                   {},
	"connection_string":     {},
}

// sensitiveSuffixes 用于捕获带前缀的变体，如 `x-api-key`、`db_password`、
// `github_token`、`openai_api_key`。
var sensitiveSuffixes = []string{
	"password", "passwd", "secret", "token", "api_key", "apikey",
	"private_key", "access_key", "credential",
}

// RedactedKeysField 是被脱敏键路径在明细中的登记字段名。
//
// 前缀下划线把它与业务字段区分开：调用方看到 `_redacted` 就知道
// 本次事件有密钥被替换，而无需解析原始值。
const RedactedKeysField = "_redacted"

// IsSensitiveKey 判断字段名是否属于需要脱敏的敏感字段。
//
// 匹配规则（依次）：
//  1. 全名大小写无关命中敏感名集合（`Authorization` / `API_KEY`）；
//  2. 以下划线/连字符切分后，任一段命中集合（`db_password` / `x-api-key`）；
//  3. 以敏感后缀结尾（`github_token` / `openai_api_key`）。
func IsSensitiveKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" {
		return false
	}
	if _, ok := sensitiveKeyNames[k]; ok {
		return true
	}
	// 规范化分隔符后按段检查：x-api-key -> x_api_key -> [x, api, key] 中
	// 「api_key」不是单段，因此除了按段还要看归一化后的整体后缀。
	norm := strings.NewReplacer("-", "_", ".", "_").Replace(k)
	if _, ok := sensitiveKeyNames[norm]; ok {
		return true
	}
	for _, seg := range strings.Split(norm, "_") {
		if seg == "" {
			continue
		}
		if _, ok := sensitiveKeyNames[seg]; ok {
			return true
		}
	}
	for _, suf := range sensitiveSuffixes {
		if strings.HasSuffix(norm, "_"+suf) || norm == suf {
			return true
		}
	}
	return false
}

// RedactDetails 返回脱敏后的明细副本，原对象不被修改。
//
// 第二个返回值是本次被脱敏的键路径（按字典序排序，如
// `["headers.authorization", "password"]`）；没有命中时返回 nil。
//
// 键路径用点号连接，嵌套数组用 `[i]` 表示，例如 `items[0].token`。
func RedactDetails(details map[string]interface{}) (map[string]interface{}, []string) {
	if len(details) == 0 {
		return details, nil
	}
	var paths []string
	out := redactValue(details, "", &paths).(map[string]interface{})
	if len(paths) == 0 {
		return out, nil
	}
	sortStrings(paths)
	return out, paths
}

// redactValue 递归脱敏任意值。path 是当前值的键路径（用于登记）。
func redactValue(v interface{}, path string, paths *[]string) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			child := k
			if path != "" {
				child = path + "." + k
			}
			if IsSensitiveKey(k) {
				// 值一律替换为占位符；非字符串（如 map/数字）同样替换 ——
				// 结构本身可能就带信息，但秘密优先。
				if isEmptyValue(val) {
					out[k] = val
					continue
				}
				out[k] = RedactedPlaceholder
				*paths = append(*paths, child)
				continue
			}
			out[k] = redactValue(val, child, paths)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, item := range t {
			out[i] = redactValue(item, indexPath(path, i), paths)
		}
		return out
	default:
		return v
	}
}

// indexPath 拼接数组下标路径。
func indexPath(path string, i int) string {
	return path + "[" + itoa(i) + "]"
}

// isEmptyValue 判断值是否为「空」，空值无需脱敏（脱敏反而丢失「这里没值」的信息）。
func isEmptyValue(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case map[string]interface{}:
		return len(t) == 0
	case []interface{}:
		return len(t) == 0
	default:
		return false
	}
}

// sortStrings 就地对字符串切片排序（避免为一次排序引入闭包）。
func sortStrings(s []string) {
	sort.Strings(s)
}

// itoa 是无 strconv 依赖的整数转字符串（仅用于数组下标，取值集合极小）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
