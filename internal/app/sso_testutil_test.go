package app

import "github.com/atop0914/agentbot/internal/sso"

// ssoTestAccount 是一个已通过 ID Token 校验的 SSO 账号样本。
//
// LocalUserID 用 "alice" 以便与测试里建的租户成员对齐：
// 「成员校验只认服务端事实」这条断言需要账号 ID 真实存在于租户成员表里。
var ssoTestAccount = sso.Account{
	LocalUserID: "alice",
	Email:       "alice@corp.example",
	Username:    "Alice",
	Subject:     "idp-subject-alice",
	Issuer:      "https://idp.corp.example",
	Provider:    "oidc",
}
