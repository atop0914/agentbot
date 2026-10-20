package auth

import "errors"

var (
	// errRevocationIDRequired 表示缺少凭据指纹。
	errRevocationIDRequired = errors.New("auth: credential fingerprint is required")

	// errRevocationTTLRequired 表示未给出吊销记录的存活时长。
	errRevocationTTLRequired = errors.New("auth: revocation ttl must be positive")

	// ErrRevocationUnavailable 表示吊销表当前不可用。
	//
	// **这个错误必须向上传播，不能被当成「未吊销」吞掉**：
	// 把「查不到」读成「没被吊销」，会让一次存储抖动等于
	// 「所有已登出的 token 全部复活」。
	ErrRevocationUnavailable = errors.New("auth: revocation store is unavailable")

	// ErrTokenRevoked 表示凭据已被主动吊销（登出）。
	//
	// 与「签名无效/过期」分开：前者的正确处置是「重新登录」，
	// 后者可能是「后端换了密钥」。合成一个错误会让排障方向跑偏。
	ErrTokenRevoked = errors.New("auth: token has been revoked")
)
