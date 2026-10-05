package sso

import "crypto"

// crypto 哈希标识的本地别名。
//
// RSA 验签需要传入 crypto.Hash，而 crypto 包的标识名较长（crypto.SHA256），
// 在 verifySignature 的 switch 里写别名能让「算法 → 哈希」的映射一眼可读，
// 减少「复制粘贴时改漏一个」这类错误（改漏的后果是验签用错哈希 → 全部失败，
// 或者更糟的方向：用了更弱的哈希）。
const (
	cryptoSHA256 = crypto.SHA256
	cryptoSHA384 = crypto.SHA384
	cryptoSHA512 = crypto.SHA512
)
