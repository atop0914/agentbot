package app

import "errors"

// 装配层的哨兵错误。
//
// 这些错误**不**携带任何内部细节：它们的最终去向是 HTTP 响应或审计记录，
// 带细节会让「邮箱是否已存在」这类信息泄漏给未认证的调用方。
var (
	// errTenantDirectoryUnavailable 表示账号目录不可用。
	errTenantDirectoryUnavailable = errors.New("app: user directory unavailable")
	// errTenantBindingRejected 表示 SSO 账号无法绑定到请求的租户。
	errTenantBindingRejected = errors.New("app: tenant binding rejected")
	// errSSOAccountInvalid 表示 SSO 回调返回的账号结构不可识别。
	errSSOAccountInvalid = errors.New("app: sso account is invalid")
	// errMetricsSetupFailed 表示指标注册失败（指标名非法 / 标签集冲突 / 超集重名）。
	//
	// 为什么它是**拒绝启动**而不是打条日志继续：注册失败意味着
	// /metrics 会导出语法非法或语义错乱的文本，抓取端会整轮失败。
	// 一个「看起来在跑但指标全丢」的进程比一个起不来的进程更难查。
	errMetricsSetupFailed = errors.New("app: metrics setup failed")
)
