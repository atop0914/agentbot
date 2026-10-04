package network

import "net/http"

// testhooks.go 提供**仅供测试**替换出口网关底层传输的入口。
//
// 为什么需要它：本机（以及大多数 CI）没有外网，但「放行路径」——
// 策略判定 → 转发 → 流量记录 → 审计 —— 恰恰是最需要端到端验证的一段。
// 换掉 RoundTripper（而不是换掉整个网关）保证被验证的仍是真实的判定与
// 记录代码，只是下游换成了一个可控的 httptest.Server。
//
// 刻意不把它做成 Service 接口的一部分：生产代码不需要、也不应该
// 有能力在运行时替换出口传输（那等于给「绕过出口审计」留了一个合法入口）。

// SetGatewayTransportForTest 替换 svc 底层网关的传输实现。
//
// 仅当 svc 的底层实现是 *HTTPGateway 时生效，返回是否替换成功。
// 生产代码调用它不会 panic，只会返回 false。
func SetGatewayTransportForTest(svc Service, rt http.RoundTripper) bool {
	s, ok := svc.(*service)
	if !ok || s == nil || s.gateway == nil {
		return false
	}
	gw, ok := s.gateway.(*HTTPGateway)
	if !ok || gw == nil {
		return false
	}
	gw.SetTransport(rt)
	return true
}

// NewHTTPGatewayTransportForTest 返回一个「透传到真实网络」的传输实现。
//
// 存在意义是让测试显式表达「我要走真实 RoundTrip 语义」而不是
// 传 nil 隐式回落（隐式回落会让「测试忘了装假下游」变成一次真实外联）。
func NewHTTPGatewayTransportForTest() http.RoundTripper {
	return cloneDefaultTransport()
}

// SetTransport 替换网关的底层传输（测试用；nil 表示恢复默认）。
func (g *HTTPGateway) SetTransport(rt http.RoundTripper) {
	if g == nil {
		return
	}
	if rt == nil {
		rt = cloneDefaultTransport()
	}
	g.client.Transport = rt
}

// RetrievePolicy 暴露底层策略存储，供装配层校验与测试断言。
func RetrievePolicy(svc Service) (*MemoryPolicyStore, bool) {
	s, ok := svc.(*service)
	if !ok || s == nil {
		return nil, false
	}
	return s.policy, s.policy != nil
}
