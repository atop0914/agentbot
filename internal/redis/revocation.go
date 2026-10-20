package redis

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RevocationStore 是跨副本共享的吊销表。
//
// 语义：被 Revoke 的 key 在 TTL 到期前一直 IsRevoked == true。
// TTL 的选择不是「存多久」，而是「至少活到该凭据自然过期」——
// 凭据一旦自然过期，吊销记录就没有意义了，让它自动消失
// 可以省掉任何清理逻辑，也不会让表无限增长。
type RevocationStore struct {
	client *Client
	prefix string
}

// NewRevocationStore 创建吊销表。
func NewRevocationStore(c *Client, prefix string) *RevocationStore {
	return &RevocationStore{client: c, prefix: prefix}
}

// key 生成吊销键。
//
// **不对 key 做哈希**：实现者很容易想把 token 哈希后存，
// 但这里存的是 token 的**指纹**（调用方传入的已是 sha256 摘要），
// 而不是原始 token。哈希责任在调用方，因为只有调用方知道
// 「哪些字段构成凭据身份」。本包只负责存取。
func (s *RevocationStore) key(id string) string {
	return s.prefix + ":revoked:" + id
}

// Revoke 吊销一个凭据，ttl 后自动清除。
//
// ttl <= 0 时拒绝执行而不是静默存成永久：一个「永不过期」的吊销记录
// 会永久占用内存/磁盘，而它保护的是一个早已过期的凭据。
// 调用方必须显式给出「该凭据还能活多久」。
func (s *RevocationStore) Revoke(ctx context.Context, id string, ttl time.Duration) error {
	if id == "" {
		return errors.New("redis: revocation id is required")
	}
	if ttl <= 0 {
		return errors.New("redis: revocation ttl must be positive")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// 值写 "1" 而不是空串：空串会让「值存在但内容为空」与
	// 「键不存在」在只读 value 的实现里无法区分。
	// 虽然本实现用 EXISTS 判定（不受值影响），但保持值非空
	// 能让 redis-cli 手工排查时一眼看出记录是活的。
	if _, err := s.client.Do("SET", s.key(id), "1", "EX", int(ttl/time.Second)); err != nil {
		return err
	}
	return nil
}

// IsRevoked 判定凭据是否已被吊销。
//
// 用 EXISTS 而不是 GET：EXISTS 对「值是什么」不敏感，
// 少一层类型断言，也不会因为值被误写而误判。
func (s *RevocationStore) IsRevoked(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	v, err := s.client.Do("EXISTS", s.key(id))
	if err != nil {
		return false, err
	}
	n, ok := v.(int64)
	if !ok {
		return false, fmt.Errorf("redis: EXISTS returned %T, want int64", v)
	}
	return n > 0, nil
}

// SubjectKey 把 subject（用户）+ 凭据指纹合成吊销键标识。
//
// 带上 subject 是为了让「同一用户的新 token 不被旧吊销记录误伤」：
// 吊销是按**具体凭据**（token 指纹）而不是按用户，
// 否则登出会连带把该用户刚签发的新 token 也废掉。
func SubjectKey(subject, fingerprint string) string {
	return subject + ":" + fingerprint
}
