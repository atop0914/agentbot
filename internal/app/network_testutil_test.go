package app

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/atop0914/agentbot/internal/network"
)

// installTestTransport 把 App 内出口网关的 RoundTripper 换成可控实现。
//
// 为什么需要这个口子：本机没有外网，但「放行路径」（判定 → 转发 →
// 记录 → 审计）恰恰是最需要端到端验证的一段。换掉 RoundTripper 而不是
// 换掉整个网关，保证被验证的仍然是真实的判定与记录代码。
//
// 通过 App 暴露的服务实例替换：EgressSvc 是接口，gateway 是它的实现细节，
// 因此 network 包提供一个仅测试用的注入入口（生产代码无法在运行时替换传输，
// 否则「绕过出口审计」就有了一个合法入口）。
func installTestTransport(t *testing.T, a *App, transport http.RoundTripper) {
	t.Helper()
	if !network.SetGatewayTransportForTest(a.EgressSvc, transport) {
		t.Fatal("egress service does not support injecting a transport; e2e forwarding cannot be verified")
	}
}

// rewriteTransport 把任意出站请求改写到固定的本地地址。
//
// 之所以需要它：出口策略要求写**裸域名**（ValidateTarget 拒绝 host:port），
// 而 httptest.Server 的地址是 127.0.0.1:<随机端口>。这个传输把请求改写到
// 本地测试服务，从而在「策略允许一个域名」与「真的能拿到响应」之间搭起桥，
// 同时不放松任何一条策略校验（策略判定的输入仍是那个域名）。
type rewriteTransport struct {
	to        string
	transport http.RoundTripper
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := url.Parse(t.to)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	// 连接指向本地假下游，但请求的 Host 语义仍是策略里那个域名 ——
	// 这正是「用假下游替代外网」的准确语义。
	clone.URL.Scheme = target.Scheme
	clone.URL.Host = target.Host
	return t.transport.RoundTrip(clone)
}
