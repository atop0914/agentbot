package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/atop0914/agentbot/internal/admin"
	"github.com/atop0914/agentbot/internal/network"
)

// adminNetworkSource 把 network.Service 适配为 admin.NetworkSource。
//
// 适配发生在装配层：admin 只认自己的轻量视图类型，不依赖 network 的具体模型，
// 避免「加一个后台分区就要动出口模块的类型」。
type adminNetworkSource struct {
	svc network.Service
}

// Summarize 产出后台「网络」分区需要的出口摘要。
//
// 两个来源合并到一次调用：流量统计（Stats）与策略计数（ListRules）。
// 统计不可用时仍返回策略计数（并标记降级），因为「出口根本没开策略」
// 本身就是控制台最需要突出的信息 —— 它不该因为统计失败一起消失。
func (s adminNetworkSource) Summarize(ctx context.Context, window time.Duration) (*admin.NetworkSummary, error) {
	if window <= 0 {
		window = admin.DefaultNetworkWindow
	}

	summary := &admin.NetworkSummary{
		Window:      window.String(),
		GeneratedAt: time.Now().UTC(),
	}

	rules, rulesErr := s.svc.ListRules(ctx)
	if rulesErr != nil {
		summary.Degraded = true
		if summary.Errors == nil {
			summary.Errors = make(map[string]string)
		}
		summary.Errors["rules"] = rulesErr.Error()
	} else {
		for _, rule := range rules {
			if rule == nil {
				continue
			}
			switch rule.Effect {
			case network.EffectAllow:
				summary.AllowRules++
			case network.EffectDeny:
				summary.DenyRules++
			}
		}
		summary.TotalRules = len(rules)
	}

	stats, statsErr := s.svc.Stats(ctx, window)
	if statsErr != nil {
		summary.Degraded = true
		if summary.Errors == nil {
			summary.Errors = make(map[string]string)
		}
		summary.Errors["stats"] = statsErr.Error()
		return summary, nil
	}

	summary.TotalRequests = stats.TotalRequests
	summary.AllowedRequests = stats.AllowedRequests
	summary.BlockedRequests = stats.BlockedRequests
	summary.FailedRequests = stats.FailedRequests
	summary.DistinctDomains = stats.DistinctDomains
	summary.SuspiciousRequests = stats.SuspiciousRequests
	summary.TotalBytes = stats.TotalBytes
	summary.TopDomains = adminNetworkDomains(stats.Domains, admin.DefaultNetworkDomains)
	summary.SuspiciousDomains = adminNetworkDomains(stats.SuspiciousDomains, admin.DefaultNetworkDomains)
	return summary, nil
}

// adminNetworkDomains 把 network 的域名统计转成 admin 的轻量视图。
//
// 只带控制台要展示的字段：域名、请求数、被阻断数、流量、是否可疑。
// 全量字段（首次/末次出现、平均耗时）留给 /api/v1/network/stats，
// 聚合视图不该把明细接口的响应体复制一遍。
func adminNetworkDomains(domains []*network.DomainStat, limit int) []admin.NetworkDomainView {
	if limit <= 0 {
		limit = admin.DefaultNetworkDomains
	}
	out := make([]admin.NetworkDomainView, 0, len(domains))
	for _, d := range domains {
		if d == nil {
			continue
		}
		out = append(out, admin.NetworkDomainView{
			Domain:     d.Domain,
			Requests:   d.Requests,
			Blocked:    d.Blocked,
			Bytes:      d.Bytes,
			Suspicious: d.Suspicious,
			Reasons:    append([]string(nil), d.Reasons...),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// seedDefaultEgressRules 写入一组默认的出口 deny 条目。
//
// 目的不是「白名单」，而是让最常见的几类外联失败**有可读的理由**：
// 命中一条具体规则比落到「默认拒绝」更容易排障，也更容易在审计里做统计。
//
// 失败只记日志不阻断启动：出口策略的初始化不该成为服务起不来的原因
// （策略本身是 fail-closed 的，写不进去只会更严，不会更松）。
func seedDefaultEgressRules(svc network.Service, logger *slog.Logger) {
	ctx := context.Background()
	defaults := []network.PolicyRule{
		{
			ID:          "egress-deny-cloud-metadata",
			Target:      "169.254.169.254",
			Effect:      network.EffectDeny,
			Priority:    -100,
			Description: "云元数据服务：Agent 读到实例凭据即可横向移动到整个云账号",
		},
		{
			ID:          "egress-deny-localhost",
			Target:      "localhost",
			Effect:      network.EffectDeny,
			Priority:    -100,
			Description: "本机回环：Agent 出站回到平台自身会绕过出口审计的初衷",
		},
		{
			ID:          "egress-deny-aws-internal",
			Target:      "*.internal",
			Effect:      network.EffectDeny,
			Priority:    -90,
			Description: "内部域名：生产内网不应由 Agent 直接访问",
		},
	}
	for _, rule := range defaults {
		if _, err := svc.AddRule(ctx, rule); err != nil {
			if logger != nil {
				logger.Warn("seed default egress rule failed", "rule", rule.ID, "error", err.Error())
			}
		}
	}
}
