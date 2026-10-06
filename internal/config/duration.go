package config

import (
	"strconv"
	"strings"
	"time"
)

// parseDurationOr 解析时间字面量，接受两种写法：
//
//   - Go duration 字面量："30s" / "5m" / "1h30m" / "500ms"
//   - 纯整数，按**秒**解释："30" → 30s
//
// 第二种写法刻意支持：JSON 与 k8s env 里写数字比写带单位字符串自然，
// 而如果直接把数字喂给 time.Duration，JSON 的 30 会变成 30 纳秒 ——
// 「配了 30 秒超时，实际 30 纳秒」是配置系统里最隐蔽的一类失效。
// 这里统一按秒解释，消除歧义。
//
// 空字符串返回错误（交给调用方保留默认值），不做「空 = 0」的隐式解释。
func parseDurationOr(raw string) (time.Duration, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return 0, strconv.ErrSyntax
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return time.ParseDuration(v)
}
