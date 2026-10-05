package sso

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// DefaultJWKSCacheTTL 是公钥集的默认缓存时长。
//
// 取值权衡：太短会让每次登录都打 IdP（可用性风险），太长会让密钥轮转后的
// 废弃密钥继续被信任（安全风险）。1 小时配合「kid 未命中强制刷新」，
// 两个方向都有兜底。
const DefaultJWKSCacheTTL = time.Hour

// JWKSClient 拉取并缓存 IdP 的公钥集。
//
// 两条关键行为：
//
//  1. **kid 未命中时强制刷新一次**（RefreshOnMiss）。IdP 轮转密钥后，
//     缓存里是旧密钥，新 token 的 kid 找不到 —— 不刷新就会表现为
//     「密钥轮转之后所有人都登不进来」。刷新失败再拒绝，绝不回落到
//     「用旧密钥试试」，因为我们无法确认旧密钥是否已被废弃。
//  2. 刷新失败时**保留旧缓存但标记 stale**，由调用方决定是否接受。
//     默认策略是拒绝：身份校验的可信度不能用「上次拉到了」来担保。
type JWKSClient struct {
	url     string
	client  *http.Client
	ttl     time.Duration
	now     func() time.Time
	mu      sync.RWMutex
	cached  *KeySet
	fetched time.Time
	// refreshes 统计强制刷新次数，便于观测密钥轮转频率。
	refreshes int
}

// JWKSConfig 是 JWKSClient 的构造参数。
type JWKSConfig struct {
	URL    string
	Client *http.Client
	TTL    time.Duration
	Now    func() time.Time
}

// NewJWKSClient 创建公钥集客户端。
func NewJWKSClient(cfg JWKSConfig) *JWKSClient {
	c := cfg.Client
	if c == nil {
		// 显式设置超时：没有超时的 http.Client 会让一次 IdP 卡顿
		// 变成登录接口的整体挂起。
		c = &http.Client{Timeout: 10 * time.Second}
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = DefaultJWKSCacheTTL
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	return &JWKSClient{url: cfg.URL, client: c, ttl: ttl, now: nowFn}
}

// Keys 返回当前可用的公钥集。
//
// force 为 true 时忽略缓存重新拉取（调用方在 kid 未命中时使用）。
func (c *JWKSClient) Keys(ctx context.Context, force bool) (*KeySet, error) {
	if c == nil || trimSpace(c.url) == "" {
		return nil, ErrNotConfigured
	}

	c.mu.RLock()
	cached, fetched := c.cached, c.fetched
	c.mu.RUnlock()

	if !force && cached != nil && c.now().Sub(fetched) < c.ttl {
		return cached, nil
	}

	ks, err := c.fetch(ctx)
	if err != nil {
		// 刷新失败：有旧缓存也不返回 —— 见类型注释第 2 条。
		return nil, err
	}

	c.mu.Lock()
	c.cached = ks
	c.fetched = c.now()
	if force {
		c.refreshes++
	}
	c.mu.Unlock()
	return ks, nil
}

// RefreshCount 返回强制刷新次数（测试与观测用）。
func (c *JWKSClient) RefreshCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.refreshes
}

// Prime 预置密钥集（测试与离线环境使用）。
//
// 生产路径上没有这个口子：运行时替换公钥集意味着可以植入一把攻击者的
// 密钥然后签出任意身份。
func (c *JWKSClient) Prime(ks *KeySet) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cached = ks
	c.fetched = c.now()
}

func (c *JWKSClient) fetch(ctx context.Context) (*KeySet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: jwks endpoint returned %d", ErrUnavailable, resp.StatusCode)
	}

	var ks KeySet
	if err := json.NewDecoder(resp.Body).Decode(&ks); err != nil {
		return nil, fmt.Errorf("%w: jwks response is not valid JSON", ErrUnavailable)
	}
	if len(ks.Keys) == 0 {
		// 空密钥集绝不能当成「暂时没有密钥」放行：那等于关掉了签名校验。
		return nil, fmt.Errorf("%w: jwks contains no keys", ErrUnavailable)
	}
	return &ks, nil
}
