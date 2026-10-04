package network

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultTrafficLimit 是流量查询的默认返回上限。
//
// 上限存在的理由：内存实现下流量记录是会被写爆的（Agent 出站频率远高于
// 人工操作），接口必须有默认边界，否则一次 GET 就能把整个进程的内存状态
// 复制进响应体。
const DefaultTrafficLimit = 200

// DefaultStatsWindow 是流量统计的默认窗口（24h）。
const DefaultStatsWindow = 24 * time.Hour

// maxTrafficRecords 是内存存储保留记录的上限；超出时丢弃最旧的。
const maxTrafficRecords = 20000

// MemoryTrafficStore 是出站流量记录的内存实现。
type MemoryTrafficStore struct {
	mu      sync.RWMutex
	records []*TrafficRecord
	// dropped 统计因超出上限被丢弃的记录数，避免「悄悄丢数据」。
	dropped int
}

// NewMemoryTrafficStore 创建内存流量存储。
func NewMemoryTrafficStore() *MemoryTrafficStore {
	return &MemoryTrafficStore{records: make([]*TrafficRecord, 0, 256)}
}

// Append 追加一条流量记录。
//
// 存副本：调用方（网关）在写入后仍会引用同一条记录去构造响应，
// 直接存指针会让「历史记录」随调用方后续修改而变化。
func (s *MemoryTrafficStore) Append(_ context.Context, rec *TrafficRecord) error {
	if rec == nil {
		return nil
	}
	cp := *rec
	if cp.ID == "" {
		cp.ID = newID("egress")
	}
	if cp.Timestamp.IsZero() {
		cp.Timestamp = time.Now().UTC()
	}
	cp.SuspiciousReasons = append([]string(nil), rec.SuspiciousReasons...)

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) >= maxTrafficRecords {
		// 丢弃最旧的 10%，而不是逐条搬移 —— 摊还成本更低且行为可预测。
		drop := maxTrafficRecords / 10
		s.records = append([]*TrafficRecord(nil), s.records[drop:]...)
		s.dropped += drop
	}
	s.records = append(s.records, &cp)
	return nil
}

// Dropped 返回因容量上限被丢弃的记录数（用于健康检查/后台提示）。
func (s *MemoryTrafficStore) Dropped() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dropped
}

// Query 按条件过滤，按时间升序返回。
//
// 排序是硬要求：内存实现遍历 map/切片时顺序不稳定，接口输出必须在
// **任何实现**下都一致，否则前端的时间线会随机乱序。
func (s *MemoryTrafficStore) Query(_ context.Context, filter TrafficFilter) ([]*TrafficRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := filter.Limit
	if limit <= 0 {
		limit = DefaultTrafficLimit
	}
	domain := NormalizeDomain(filter.Domain)

	out := make([]*TrafficRecord, 0, len(s.records))
	for _, rec := range s.records {
		if rec == nil {
			continue
		}
		if filter.AgentID != "" && rec.AgentID != filter.AgentID {
			continue
		}
		if domain != "" && !domainMatchesExactly(rec.Domain, domain) {
			continue
		}
		if filter.Allowed != nil && rec.Allowed != *filter.Allowed {
			continue
		}
		if filter.OnlySuspicious && !rec.Suspicious {
			continue
		}
		if filter.Since != nil && rec.Timestamp.Before(*filter.Since) {
			continue
		}
		if filter.Until != nil && rec.Timestamp.After(*filter.Until) {
			continue
		}
		cp := *rec
		cp.SuspiciousReasons = append([]string(nil), rec.SuspiciousReasons...)
		out = append(out, &cp)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.Before(out[j].Timestamp)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		// 超出上限时保留**最近**的 limit 条：排障看的是刚刚发生了什么。
		out = out[len(out)-limit:]
	}
	return out, nil
}

// Stats 按窗口聚合统计。
//
// 域名维度同时给出「命中启发式的域名列表」，让后台可以直接展示
// 「哪些外联看起来不像正常业务」而不需要前端二次过滤。
func (s *MemoryTrafficStore) Stats(_ context.Context, window time.Duration, now time.Time) (*TrafficStats, error) {
	if window <= 0 {
		window = DefaultStatsWindow
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	start := now.Add(-window)

	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := &TrafficStats{
		GeneratedAt: now,
		Window:      window.String(),
		Start:       start,
		End:         now,
	}
	heuristics := DefaultHeuristics()

	byDomain := make(map[string]*DomainStat)
	byAgent := make(map[string]struct{})
	// 域名维度的请求计数单独维护，供高频启发式判定。
	domainCounts := make(map[string]int)

	for _, rec := range s.records {
		if rec == nil || rec.Timestamp.Before(start) || rec.Timestamp.After(now) {
			continue
		}
		stats.TotalRequests++
		stats.TotalBytes += rec.Bytes
		if rec.Allowed {
			stats.AllowedRequests++
		} else {
			stats.BlockedRequests++
		}
		if rec.Error != "" {
			stats.FailedRequests++
		}
		if rec.Suspicious {
			stats.SuspiciousRequests++
		}
		if rec.AgentID != "" {
			byAgent[rec.AgentID] = struct{}{}
		}
		domainCounts[rec.Domain]++

		ds := byDomain[rec.Domain]
		if ds == nil {
			ds = &DomainStat{Domain: rec.Domain, FirstSeen: rec.Timestamp, LastSeen: rec.Timestamp}
			byDomain[rec.Domain] = ds
		}
		ds.Requests++
		if !rec.Allowed {
			ds.Blocked++
		}
		if rec.Error != "" {
			ds.Failed++
		}
		ds.Bytes += rec.Bytes
		if rec.Timestamp.Before(ds.FirstSeen) {
			ds.FirstSeen = rec.Timestamp
		}
		if rec.Timestamp.After(ds.LastSeen) {
			ds.LastSeen = rec.Timestamp
		}
	}

	stats.DistinctAgents = len(byAgent)

	domains := make([]*DomainStat, 0, len(byDomain))
	for domain, ds := range byDomain {
		ds.AvgDurationMS = averageDurationMS(ds, s.records, start, now, domain)
		reasons := suspiciousReasons(domain, domainCounts[domain], heuristics)
		if len(reasons) > 0 {
			ds.Suspicious = true
			ds.Reasons = reasons
			stats.SuspiciousDomains = append(stats.SuspiciousDomains, ds)
		}
		domains = append(domains, ds)
	}

	// 稳定排序：请求数降序 → 域名升序。只按请求数排序时同分域名的顺序
	// 会随 map 迭代顺序抖动，控制台的 Top 列表会「跳动」。
	sort.SliceStable(domains, func(i, j int) bool {
		if domains[i].Requests != domains[j].Requests {
			return domains[i].Requests > domains[j].Requests
		}
		return domains[i].Domain < domains[j].Domain
	})
	sort.SliceStable(stats.SuspiciousDomains, func(i, j int) bool {
		if stats.SuspiciousDomains[i].Domain != stats.SuspiciousDomains[j].Domain {
			return stats.SuspiciousDomains[i].Domain < stats.SuspiciousDomains[j].Domain
		}
		return stats.SuspiciousDomains[i].Requests > stats.SuspiciousDomains[j].Requests
	})

	stats.Domains = domains
	stats.DistinctDomains = len(domains)
	return stats, nil
}

// PurgeBefore 删除早于 cutoff 的记录。
func (s *MemoryTrafficStore) PurgeBefore(_ context.Context, cutoff time.Time) (int, error) {
	if cutoff.IsZero() {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.records[:0]
	removed := 0
	for _, rec := range s.records {
		if rec != nil && rec.Timestamp.Before(cutoff) {
			removed++
			continue
		}
		kept = append(kept, rec)
	}
	s.records = kept
	return removed, nil
}

// Len 返回当前保留的记录数。
func (s *MemoryTrafficStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// domainMatchesExactly 判断域名是否与过滤条件相等（含子域）。
func domainMatchesExactly(recDomain, filter string) bool {
	d := NormalizeDomain(recDomain)
	if d == filter {
		return true
	}
	return strings.HasSuffix(d, "."+filter)
}

// averageDurationMS 计算某域名在窗口内的平均耗时。
//
// 单独走一遍记录而不是在聚合循环里累加，是因为 DurationStat 需要在
// 聚合完成后才知道哪些域名需要（避免为「被过滤掉的域名」做无用功）。
// 记录量有 2 万条上限，多一遍 O(n) 扫描的代价可接受。
func averageDurationMS(ds *DomainStat, records []*TrafficRecord, start, now time.Time, domain string) int64 {
	if ds == nil || ds.Requests == 0 {
		return 0
	}
	var total int64
	var n int
	for _, rec := range records {
		if rec == nil || rec.Domain != domain {
			continue
		}
		if rec.Timestamp.Before(start) || rec.Timestamp.After(now) {
			continue
		}
		total += rec.DurationMS
		n++
	}
	if n == 0 {
		return 0
	}
	return total / int64(n)
}

// suspiciousReasons 返回域名命中的可疑外联理由集合。
//
// 两条启发式：
//  1. 高风险 TLD：.tk / .xyz / .zip 这类「临时载荷分发」常用域；
//  2. 高频外联：窗口内同一域名请求数超过阈值（可能是批量窃取或死循环重试）。
//
// 刻意返回**理由列表**而不是 bool：误报是这类启发式的常态，只有告诉
// 运维「因为什么被标记」，阈值才可能被合理调整。
func suspiciousReasons(domain string, count int, h Heuristics) []string {
	if domain == "" {
		return nil
	}
	var reasons []string
	if tld, ok := topLevelOf(domain); ok {
		for _, risky := range h.ScratchTLDs {
			if tld == risky {
				reasons = append(reasons, "high-risk tld: ."+tld)
				break
			}
		}
	}
	if h.HighFrequencyThreshold > 0 && count > h.HighFrequencyThreshold {
		reasons = append(reasons, "high frequency egress: "+itoa(count)+" requests in window")
	}
	// 直连 IP 字面量通常意味着「绕过了正常的域名解析路径」，
	// 在 Agent 场景里比用户浏览器里更值得看一眼。
	if isIPLiteral(domain) {
		reasons = append(reasons, "direct ip literal destination")
	}
	return reasons
}

func topLevelOf(domain string) (string, bool) {
	d := NormalizeDomain(domain)
	if d == "" {
		return "", false
	}
	idx := strings.LastIndex(d, ".")
	if idx < 0 || idx == len(d)-1 {
		return "", false
	}
	return d[idx+1:], true
}

// isIPLiteral 判断域名是否本身就是 IP 字面量（IPv4 / IPv6）。
func isIPLiteral(domain string) bool {
	if domain == "" {
		return false
	}
	if strings.Count(domain, ".") == 3 {
		parts := strings.Split(domain, ".")
		for _, p := range parts {
			if p == "" || len(p) > 3 {
				return false
			}
			for _, c := range p {
				if c < '0' || c > '9' {
					return false
				}
			}
		}
		return true
	}
	return strings.Contains(domain, ":")
}

// itoa 避免为了一个整数格式化引入 strconv 到热路径的额外分配。
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
