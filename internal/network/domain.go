package network

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 出口路由的错误分类。
//
// 调用方（HTTP 层 / 装配层）需要区分：
//   - ErrBlocked：策略明确拒绝 → 403，且这是**正常业务结果**，不是故障；
//   - ErrUnavailable：策略存储不可用 → 503，且必须 fail-closed（拒绝而非放行）；
//   - 其余为请求本身有问题 → 400。
var (
	// ErrBlocked 表示目标被出口策略拒绝（默认拒绝也走这个错误）。
	ErrBlocked = errors.New("network: egress blocked by policy")
	// ErrUnavailable 表示策略存储不可用；fail-closed，绝不放行。
	ErrUnavailable = errors.New("network: egress policy store unavailable")
	// ErrInvalidTarget 表示域名模式非法。
	ErrInvalidTarget = errors.New("network: invalid target pattern")
	// ErrInvalidEffect 表示 effect 不是 allow/deny。
	ErrInvalidEffect = errors.New("network: invalid policy effect")
	// ErrRuleNotFound 表示策略条目不存在。
	ErrRuleNotFound = errors.New("network: policy rule not found")
	// ErrAgentRequired 表示出站请求缺少 Agent 身份。
	ErrAgentRequired = errors.New("network: agent id is required")
	// ErrURLRequired 表示出站请求缺少目标 URL。
	ErrURLRequired = errors.New("network: target url is required")
)

// ErrBlockedRule 在默认拒绝/拒绝规则命中时包装出具体规则信息，
// 便于 handler 回显「被哪条规则拦下」以及测试断言。
type ErrBlockedRule struct {
	Domain  string
	RuleID  string
	Pattern string
	Reason  string
}

func (e *ErrBlockedRule) Error() string {
	if e == nil {
		return ErrBlocked.Error()
	}
	if e.RuleID != "" {
		return fmt.Sprintf("%s: domain=%s rule=%s pattern=%s", ErrBlocked, e.Domain, e.RuleID, e.Pattern)
	}
	return fmt.Sprintf("%s: domain=%s", ErrBlocked, e.Domain)
}

// Unwrap 让 errors.Is(err, ErrBlocked) 成立。
func (e *ErrBlockedRule) Unwrap() error { return ErrBlocked }

// ===== 域名模式匹配 =====

// NormalizeDomain 把用户输入的域名规整为可比对的形式：小写、去掉首尾空白、
// 去掉末尾的点（FQDN 写法）、去掉 scheme / 端口 / path（若给的是 URL）。
func NormalizeDomain(raw string) string {
	d := strings.TrimSpace(strings.ToLower(raw))
	if d == "" {
		return ""
	}
	// 容忍传入完整 URL：抽出主机名后再匹配。
	if strings.Contains(d, "://") {
		if u, err := url.Parse(d); err == nil && u.Host != "" {
			d = u.Host
		}
	} else if idx := strings.IndexAny(d, "/?#"); idx >= 0 {
		d = d[:idx]
	}
	// 去掉端口（IPv6 的 [::1]:8080 形式也要处理）。
	if host, _, err := net.SplitHostPort(d); err == nil {
		d = host
	}
	d = strings.TrimSuffix(d, ".")
	d = strings.Trim(d, "[]")
	return d
}

// DomainOf 从 URL 中抽出可比对的域名；无法解析时返回空串。
func DomainOf(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return ""
	}
	return NormalizeDomain(u.Host)
}

// ValidateTarget 校验域名模式是否合法，并返回规整后的模式。
//
// 合法的三种形态：精确域名、`*.suffix`、`*`。
// 拒绝：空串、只有通配符片段（如 `*` 之外还带别的 `*` 混排）、空后缀（`*.`）、
// 带 scheme / 端口 / path 的输入 —— 后者往往意味着「作者以为写的是 URL」，
// 静默接受会造成策略实际匹配不到任何流量（黑盒失效）。
func ValidateTarget(raw string) (string, error) {
	t := strings.TrimSpace(strings.ToLower(raw))
	if t == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidTarget)
	}
	if t == "*" {
		return t, nil
	}
	if strings.Contains(t, "://") || strings.ContainsAny(t, "/?#") || strings.Contains(t, ":") {
		return "", fmt.Errorf("%w: %q looks like a URL or carries a port/path; use a bare domain or *.domain", ErrInvalidTarget, raw)
	}
	if strings.HasPrefix(t, "*.") {
		suffix := strings.TrimPrefix(t, "*.")
		if suffix == "" {
			return "", fmt.Errorf("%w: %q has an empty suffix", ErrInvalidTarget, raw)
		}
		if strings.Contains(suffix, "*") {
			return "", fmt.Errorf("%w: %q has a wildcard in the middle of the suffix", ErrInvalidTarget, raw)
		}
		return "*." + NormalizeDomain(suffix), nil
	}
	if strings.Contains(t, "*") {
		// 只支持「最左一段是 *」的通配形式：a.*.example.com 这类模式没有
		// 明确的优先级定义，开放它只会制造「规则看起来生效了其实没有」的坑。
		return "", fmt.Errorf("%w: %q must use the form *.example.com", ErrInvalidTarget, raw)
	}
	return NormalizeDomain(t), nil
}

// matchSpecificity 返回模式对某个域名的「具体度」：值越大越具体。
// 返回 -1 表示不匹配；调用方据此实现「最具体的规则说了算」。
//
// 语义：
//
//	精确域名 api.example.com → 匹配 api.example.com 及其子域（长度参与比较）
//	*.example.com            → 匹配任意深度的子域，但不匹配 example.com 本身
//	*                        → 匹配任意域名（最低具体度）
func matchSpecificity(pattern, domain string) int {
	domain = NormalizeDomain(domain)
	if domain == "" || pattern == "" {
		return -1
	}
	if pattern == "*" {
		return 0
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*.")
		// 必须是子域：a.example.com 匹配 *.example.com；example.com 本身不匹配。
		if !strings.HasSuffix(domain, "."+suffix) {
			return -1
		}
		// 后缀越长越具体。
		return len(suffix)
	}
	if domain == pattern {
		// 精确命中给一个明显高于通配的高分。
		return 100000 + len(pattern)
	}
	// 精确域名的子域也放行：运维写 api.example.com 时，本意几乎总是
	// 「这个服务和它下面的分片」，而不是「只允许这个字面量」。
	if strings.HasSuffix(domain, "."+pattern) {
		return 90000 + len(pattern)
	}
	return -1
}

// newID 生成稳定前缀的标识；无法生成时回落到基于时间的值，绝不返回空串
// （空 ID 会让审计记录无法回溯到具体条目）。
func newID(prefix string) string {
	if id, err := uuid.NewRandom(); err == nil {
		return prefix + "-" + id.String()
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UTC().UnixNano())
}
