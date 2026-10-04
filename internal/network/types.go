// Package network 实现 Agent 的网络出口路由（Day 26）。
//
// 为什么出口要自己管一层：
//
//	AgentBot 的 Agent 能执行终端命令、操作浏览器、下载依赖。这意味着
//	「Agent 能连上什么」本身就是攻击面 —— 数据外泄、拉取恶意载荷、
//	连回 C2 都只需要一次出站请求。2025-2026 年的 Agent 平台事件反复验证了
//	同一条结论：**给 Agent 的能力边界里，出站网络是最容易被忽略的一环**。
//
// 本包因此把出站流量显式建模为「策略 + 网关 + 审计」三件事：
//
//  1. 出口策略（Policy）：按 Agent / 域名声明允许与拒绝，**默认拒绝**；
//  2. 代理网关（Gateway）：请求必须经网关转发，网关在转发前做判定，
//     未获允许的目标直接阻断并落审计；
//  3. 出站流量审计（TrafficLog）：域名/时间窗聚合，暴露可疑外联。
//
// 设计约束：不引入第三方依赖，网关只依赖 net/http 的 RoundTripper 抽象，
// 因此既可以在测试里注入假的下游，也可以在这样一台**没有外网**的机器上
// 完整验证「阻断 / 放行 / 审计」的行为。
package network

import (
	"context"
	"time"
)

// DefaultDenyReason 是默认拒绝时的判定理由，出现在审计明细里。
const DefaultDenyReason = "default-deny: no egress policy rule allows this destination"

// PolicyEffect 是策略条目的效果。
type PolicyEffect string

const (
	// EffectAllow 允许匹配到的目标出站。
	EffectAllow PolicyEffect = "allow"
	// EffectDeny 拒绝匹配到的目标出站。Deny 条目始终优先于 Allow。
	EffectDeny PolicyEffect = "deny"
)

// Valid 判断效果取值是否合法。
func (e PolicyEffect) Valid() bool {
	return e == EffectAllow || e == EffectDeny
}

// PolicyRule 是一条出口策略。
//
// Target 支持三种写法：
//
//	"api.example.com"    精确域名（同时覆盖其子域：a.api.example.com 也匹配）
//	"*.example.com"      通配子域（匹配 a.example.com、a.b.example.com，**不**匹配 example.com 本身）
//	"*"                  任意域名（等价于全部放行；仅建议在受控环境使用）
//
// 匹配优先级（先按优先级数值小的先判定，数值相同时 Deny 优先）：
// 精确域名 > 最长通配后缀 > 更短的思路通配后缀 > "*"。
// 即「配置最具体的规则说了算」，避免一条宽泛的 allow 覆盖掉一条精确的 deny。
type PolicyRule struct {
	// ID 是策略条目的稳定标识，出现在审计明细里，便于回溯到配置。
	ID string `json:"id"`
	// AgentID 为空表示该条目对所有 Agent 生效（全局策略）；
	// 非空时只作用于指定 Agent。Agent 维度的条目优先级高于全局条目。
	AgentID string `json:"agent_id,omitempty"`
	// Target 是域名模式，见上方规则说明。
	Target string `json:"target"`
	// Effect 是 allow / deny。
	Effect PolicyEffect `json:"effect"`
	// Priority 是显式优先级，数值越小越先判定；缺省 0。
	// 用于表达「同具体度下这条更重要」的诉求。
	Priority int `json:"priority,omitempty"`
	// Methods 限定允许/拒绝的 HTTP 方法；为空表示不限方法。
	Methods []string `json:"methods,omitempty"`
	// Description 是给人看的说明（为什么开这条口子）。
	Description string `json:"description,omitempty"`
	// CreatedAt / UpdatedAt 便于后台按时间排序。
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// Decision 是网关对一次出站请求的判定结果。
//
// 之所以把判定结果**完整返回**（而不是只给一个 bool）：
// 审计、管理后台与排障都需要回答「为什么被拒」，只有规则 ID 与匹配模式
// 才能解释清楚，否则默认拒绝会变成一个无法诊断的黑盒。
type Decision struct {
	// Allowed 是否放行。
	Allowed bool `json:"allowed"`
	// RuleID 是做出判定的策略条目 ID；默认拒绝时为空。
	RuleID string `json:"rule_id,omitempty"`
	// MatchedPattern 是命中的域名模式（便于确认「确实是这条规则生效」）。
	MatchedPattern string `json:"matched_pattern,omitempty"`
	// Scope 说明命中条目是 agent 维度还是 global 维度。
	Scope string `json:"scope,omitempty"`
	// Reason 是面向人的判定理由。
	Reason string `json:"reason"`
}

// TrafficRecord 是一条出站流量的记录。
//
// 记录的是「网关代理出去的请求」而非「Agent 声称要发起的请求」：
// 只有经过网关的流量才算数，这是出口路由不能被绕过的前提。
type TrafficRecord struct {
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	Domain  string `json:"domain"`
	Method  string `json:"method"`
	URL     string `json:"url"`
	// StatusCode 是下游返回的状态码；被策略阻断时为 0。
	StatusCode int `json:"status_code"`
	// Bytes 是出站请求体 + 入站响应体的大小之和。
	Bytes int64 `json:"bytes"`
	// DurationMS 是网关处理耗时（毫秒），便于发现异常慢的外联。
	DurationMS int64 `json:"duration_ms"`
	// Allowed 是否被策略放行。
	Allowed bool `json:"allowed"`
	// RuleID / Reason 与 Decision 对应，让「阻断原因」在流量记录里直接可读。
	RuleID string `json:"rule_id,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Error 是转发过程中的错误（下游不可达、超时等）；策略阻断时为空。
	Error string `json:"error,omitempty"`
	// Suspicious 标记该条记录是否命中可疑外联启发式规则。
	Suspicious bool `json:"suspicious"`
	// SuspiciousReasons 列出命中的启发式条目，便于解释与调参。
	SuspiciousReasons []string  `json:"suspicious_reasons,omitempty"`
	Timestamp         time.Time `json:"timestamp"`
}

// TrafficFilter 是流量查询条件。
type TrafficFilter struct {
	AgentID string
	Domain  string
	Allowed *bool
	// OnlySuspicious 只返回被标记为可疑的记录。
	OnlySuspicious bool
	Since          *time.Time
	Until          *time.Time
	// Limit <= 0 时使用默认上限。
	Limit int
}

// DomainStat 是按域名聚合的一条统计。
type DomainStat struct {
	Domain string `json:"domain"`
	// Requests 是该域名的出站请求总数。
	Requests int `json:"requests"`
	// Blocked 是被策略阻断的请求数。
	Blocked int `json:"blocked"`
	// Failed 是转发失败（下游错误/超时）的请求数。
	Failed int `json:"failed"`
	// Bytes 是该域名的累计流量。
	Bytes int64 `json:"bytes"`
	// AvgDurationMS 是平均耗时（毫秒）。
	AvgDurationMS int64 `json:"avg_duration_ms"`
	// Suspicious 是否命中可疑外联启发式。
	Suspicious bool `json:"suspicious"`
	// Reasons 是命中的可疑理由。
	Reasons   []string  `json:"reasons,omitempty"`
	FirstSeen time.Time `json:"first_seen,omitempty"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
}

// TrafficStats 是出站流量的聚合统计。
//
// Window 字段用可读字符串而不是 time.Duration：Duration 直接序列化是纳秒整数，
// 前端每次都要换算（Day 24 时间序列踩过这个坑）。
type TrafficStats struct {
	GeneratedAt time.Time `json:"generated_at"`
	Window      string    `json:"window"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`

	TotalRequests   int   `json:"total_requests"`
	AllowedRequests int   `json:"allowed_requests"`
	BlockedRequests int   `json:"blocked_requests"`
	FailedRequests  int   `json:"failed_requests"`
	DistinctDomains int   `json:"distinct_domains"`
	DistinctAgents  int   `json:"distinct_agents"`
	TotalBytes      int64 `json:"total_bytes"`
	// SuspiciousRequests 是命中可疑启发式的请求数。
	SuspiciousRequests int `json:"suspicious_requests"`

	// Domains 按请求数降序，便于直接渲染「Top 外联目标」。
	Domains []*DomainStat `json:"domains"`
	// SuspiciousDomains 是按域名去重后的可疑外联列表。
	SuspiciousDomains []*DomainStat `json:"suspicious_domains,omitempty"`
}

// ===== 可疑外联启发式（Heuristic）=====

// Heuristics 是判定「可疑外联」的阈值与清单。
//
// 这里刻意做成可配置结构而不是写死常量：不同租户的「正常外联频率」差别极大，
// 阈值必须能随部署调整，且**必须显式可读**——安全策略藏在代码里等于没有策略。
type Heuristics struct {
	// ScratchTLDs 是通常不作为生产依赖的顶级域。Agent 访问这些域往往意味着
	// 临时载荷分发或 C2。列表保守（只收公认的高风险 TLD），避免误报泛滥。
	ScratchTLDs []string
	// HighFrequencyThreshold 是窗口内「同一域名请求数」的告警阈值（默认 200）。
	HighFrequencyThreshold int
}

// DefaultHeuristics 返回默认启发式配置。
func DefaultHeuristics() Heuristics {
	return Heuristics{
		ScratchTLDs: []string{
			"tk", "ml", "ga", "cf", "gq", "top", "xyz", "click", "link",
			"work", "rest", "zip", "mov",
		},
		HighFrequencyThreshold: 200,
	}
}

// ===== 网关 =====

// EgressRequest 是一次出站请求的网关侧描述。
//
// 用结构体而不是直接传 *http.Request，是为了让「网关能被真实 handler 调用，
// 也能被测试直接调用」——测试不需要构造 HTTP 请求对象即可验证策略判定。
type EgressRequest struct {
	// AgentID 是发起出站的 Agent 身份，策略按它做 Agent 维度判定。
	AgentID string `json:"agent_id"`
	// Method / URL 是目标与方法。
	Method string `json:"method"`
	URL    string `json:"url"`
	// Body 是出站请求体（可为 nil）。
	Body []byte `json:"-"`
	// Headers 是转发时附带的请求头。
	Headers map[string]string `json:"-"`
	// Timeout 是单次出站超时；<= 0 时使用网关默认值。
	Timeout time.Duration `json:"-"`
}

// EgressResponse 是出站请求的结果。
type EgressResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"-"`
	Body       []byte              `json:"-"`
	Domain     string              `json:"domain"`
	DurationMS int64               `json:"duration_ms"`
	// Record 是本次出站留下的流量记录（放行与阻断都有）。
	Record *TrafficRecord `json:"record"`
	// Decision 是策略判定结果。
	Decision Decision `json:"decision"`
}

// Gateway 是出口代理网关：所有出站请求必须经它转发。
type Gateway interface {
	// Proxy 执行一次出站请求：先判定策略，未获允许直接阻断（不发起连接），
	// 放行则经底层 RoundTripper 转发并记录流量。
	Proxy(ctx context.Context, req EgressRequest) (*EgressResponse, error)
	// Evaluate 只做策略判定，不发起请求（供 Agent 出站前预检 / 后台模拟）。
	Evaluate(agentID, method, url string) (Decision, error)
}

// TrafficStore 是出站流量的存储。
type TrafficStore interface {
	// Append 追加一条流量记录。实现必须存副本，避免调用方后续修改污染历史。
	Append(ctx context.Context, rec *TrafficRecord) error
	// Query 按条件查询，按时间升序返回。
	Query(ctx context.Context, filter TrafficFilter) ([]*TrafficRecord, error)
	// Stats 按窗口聚合统计（window <= 0 时使用默认窗口）。
	Stats(ctx context.Context, window time.Duration, now time.Time) (*TrafficStats, error)
	// PurgeBefore 删除早于 cutoff 的记录，返回删除条数。
	PurgeBefore(ctx context.Context, cutoff time.Time) (int, error)
}

// EgressRecorder 是出站动作的审计落地点。
//
// 与 monitor.DispositionRecorder 同一套理由：network 是出口控制面模块，
// 不该反向依赖 internal/audit 的模型；装配层负责把记录翻译成审计事件，
// 测试可以注入内存实现断言「阻断是否真的留痕」而无需启动审计服务。
type EgressRecorder interface {
	// RecordEgress 记录一次出站动作（放行或阻断）。
	//
	// 返回的错误**不**改变网关行为：请求已经发生（或已经被阻断），
	// 审计失败只意味着留痕缺失。装配层会把它记到日志，而不是把一次
	// 真实出站变成 5xx 重试。
	RecordEgress(ctx context.Context, rec *TrafficRecord) error
}

// EgressRecorderFunc 让普通函数满足 EgressRecorder。
type EgressRecorderFunc func(ctx context.Context, rec *TrafficRecord) error

// RecordEgress 实现 EgressRecorder。
func (f EgressRecorderFunc) RecordEgress(ctx context.Context, rec *TrafficRecord) error {
	if f == nil {
		return nil
	}
	return f(ctx, rec)
}

// Service 是出口路由的业务门面：策略管理 + 网关代理 + 流量审计。
type Service interface {
	// ===== 策略 =====

	// AddRule 新增一条策略；校验域名模式与方法合法性。
	AddRule(ctx context.Context, rule PolicyRule) (*PolicyRule, error)
	// ListRules 返回全部策略（按 Agent 维度、优先级、域名稳定排序）。
	ListRules(ctx context.Context) ([]*PolicyRule, error)
	// DeleteRule 删除一条策略。
	DeleteRule(ctx context.Context, id string) error
	// Evaluate 做一次策略判定（不改动任何状态）。
	Evaluate(ctx context.Context, agentID, method, url string) (Decision, error)

	// ===== 网关 =====

	// Proxy 经网关执行一次出站请求。
	Proxy(ctx context.Context, req EgressRequest) (*EgressResponse, error)

	// ===== 流量审计 =====

	// ListTraffic 按条件查询流量记录。
	ListTraffic(ctx context.Context, filter TrafficFilter) ([]*TrafficRecord, error)
	// Stats 返回窗口内的出站流量聚合统计。
	Stats(ctx context.Context, window time.Duration) (*TrafficStats, error)
	// PurgeTraffic 清理早于 cutoff 的流量记录。
	PurgeTraffic(ctx context.Context, cutoff time.Time) (int, error)
}
