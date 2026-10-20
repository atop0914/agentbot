package auth

import (
	"time"

	"github.com/atop0914/agentbot/internal/redis"
)

// RedisRevocationStore 是跨副本共享的吊销表。
//
// 与 MemoryRevocationStore 的语义完全一致（契约测试同时跑两套），
// 区别只在于状态存在哪里：多副本部署下，A 副本的登出必须让
// B 副本立刻拒绝同一凭据。
type RedisRevocationStore struct {
	store *redis.RevocationStore
	// timeout 是单次查询的上限。
	//
	// IsRevoked 挂在**每一个已认证请求**的路径上（ValidateAccessToken），
	// 且签名不带 context（auth.AuthService 是同步接口）。没有超时段，
	// 一次 Redis 卡顿会把所有 API 请求一起拖住 —— 一个「安全增强」
	// 变成全站可用性故障。这里用固定的短超时把它隔离掉。
	timeout time.Duration
}

// defaultRevocationTimeout 是吊销查询的超时。
//
// 取 300ms：本机 Redis 往返在百微秒级，300ms 足够覆盖正常的
// 网络抖动与一次瞬时阻塞，同时远低于用户可感知的延迟。
const defaultRevocationTimeout = 300 * time.Millisecond

// NewRedisRevocationStore 创建 Redis 吊销表。
func NewRedisRevocationStore(store *redis.RevocationStore) *RedisRevocationStore {
	return &RedisRevocationStore{store: store, timeout: defaultRevocationTimeout}
}

// WithTimeout 覆盖查询超时（测试用）。
func (s *RedisRevocationStore) WithTimeout(d time.Duration) *RedisRevocationStore {
	if d > 0 {
		s.timeout = d
	}
	return s
}

// Revoke 实现 RevocationStore。
func (s *RedisRevocationStore) Revoke(userID, fingerprint string, ttl time.Duration) error {
	if fingerprint == "" {
		return errRevocationIDRequired
	}
	if ttl <= 0 {
		return errRevocationTTLRequired
	}
	ctx, cancel := contextWithTimeout(s.timeout)
	defer cancel()
	if err := s.store.Revoke(ctx, redis.SubjectKey(userID, fingerprint), ttl); err != nil {
		return wrapRevocationErr(err)
	}
	return nil
}

// IsRevoked 实现 RevocationStore。
func (s *RedisRevocationStore) IsRevoked(userID, fingerprint string) (bool, error) {
	if fingerprint == "" {
		return false, nil
	}
	ctx, cancel := contextWithTimeout(s.timeout)
	defer cancel()
	revoked, err := s.store.IsRevoked(ctx, redis.SubjectKey(userID, fingerprint))
	if err != nil {
		// 明确包成 ErrRevocationUnavailable，让调用方能区分
		// 「确认未吊销」与「查不到」—— 后者绝不能当成未吊销。
		return false, wrapRevocationErr(err)
	}
	return revoked, nil
}
