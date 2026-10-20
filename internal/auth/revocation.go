package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// TokenRef 是一个已被签发的 access token 的引用。
//
// 为什么存指纹而不是原始 token：原始 token 是 bearer 凭据本身，
// 把它写进 Redis 等于把「可直接使用的凭据」复制到了另一个存储上 ——
// 一次 Redis 泄露就变成一次全量凭据泄露。指纹（sha256）足以做
// 等值判定，且泄露后无法反推出 token。
type TokenRef struct {
	// UserID 是签发对象。
	UserID string
	// Fingerprint 是 token 的 sha256 十六进制摘要。
	Fingerprint string
	// ExpiresAt 是该 token 的自然过期时刻。
	//
	// 吊销记录的存活时间由它推导 —— 凭据一旦自然过期，
	// 吊销记录就再无意义，不该继续占用存储。
	ExpiresAt time.Time
}

// Fingerprint 计算 token 的指纹。
//
// 这是**唯一**的指纹入口。调用方不得自行拼接或哈希，
// 否则「写入用一套算法、查询用另一套」会产出最难查的分歧：
// 吊销似乎成功（写了），但校验永远查不到（键对不上）。
func Fingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RevocationStore 是凭据吊销表的抽象。
//
// 两套实现必须语义一致（契约见 revocation_contract_test.go）：
// 内存实现服务于单副本/开发，Redis 实现服务于多副本。
type RevocationStore interface {
	// Revoke 吊销一个凭据；ttl 后记录自动失效。
	Revoke(userID, fingerprint string, ttl time.Duration) error
	// IsRevoked 判定凭据是否已被吊销。
	//
	// 返回 error 而不是「失败即视为未吊销」：调用方必须能区分
	// 「确认未吊销」与「查不到」。后者若被当成未吊销，一次 Redis
	// 抖动就等于「所有已登出的 token 全部复活」。
	IsRevoked(userID, fingerprint string) (bool, error)
}

// MemoryRevocationStore 是进程内的吊销表。
//
// 它的存在理由只有两个：单副本部署，以及作为显式启用的回退实现。
// **它不解决多副本问题** —— 这正是 Redis 实现存在的理由。
// 用它在多副本下部署，登出只对命中的那一个副本生效。
type MemoryRevocationStore struct {
	mu      sync.RWMutex
	revoked map[string]time.Time
}

// NewMemoryRevocationStore 创建内存吊销表。
func NewMemoryRevocationStore() *MemoryRevocationStore {
	return &MemoryRevocationStore{revoked: make(map[string]time.Time)}
}

// memoryKey 组合 user 与指纹。
//
// 带 user 是为了让不同用户的相同 token（理论上不可能，但取决于
// 签发实现）不互相影响，也与 Redis 实现的键形态保持一致 ——
// 两套实现的键形态必须一样，否则换后端会让已有吊销记录全部失配。
func memoryKey(userID, fingerprint string) string {
	return userID + ":" + fingerprint
}

// Revoke 实现 RevocationStore。
func (m *MemoryRevocationStore) Revoke(userID, fingerprint string, ttl time.Duration) error {
	if fingerprint == "" {
		return errRevocationIDRequired
	}
	if ttl <= 0 {
		return errRevocationTTLRequired
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gcLocked(now)
	m.revoked[memoryKey(userID, fingerprint)] = now.Add(ttl)
	return nil
}

// IsRevoked 实现 RevocationStore。
func (m *MemoryRevocationStore) IsRevoked(userID, fingerprint string) (bool, error) {
	if fingerprint == "" {
		return false, nil
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	expiry, ok := m.revoked[memoryKey(userID, fingerprint)]
	if !ok {
		return false, nil
	}
	// 惰性过期：读到时已过期就当场判定为未吊销并顺手删除。
	// 内存实现若不在读路径做过期，一个长期运行的进程会让
	// 吊销表无限增长（Redis 侧靠 TTL 自动回收，不需要这段逻辑）。
	if !now.Before(expiry) {
		delete(m.revoked, memoryKey(userID, fingerprint))
		return false, nil
	}
	return true, nil
}

// gcLocked 清理过期项（调用方必须持锁）。
func (m *MemoryRevocationStore) gcLocked(now time.Time) {
	for k, expiry := range m.revoked {
		if !now.Before(expiry) {
			delete(m.revoked, k)
		}
	}
}
