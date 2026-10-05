package sso

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// VerifyOptions 是一次 ID Token 校验所需的全部上下文。
//
// 把「期望值」显式传进来（而不是从全局配置里偷偷读）：
// 校验函数的正确性完全取决于这些期望值是否被真正比对，
// 显式参数让每一条断言在测试里都是可注入、可证伪的。
type VerifyOptions struct {
	Issuer     string
	ClientID   string
	Nonce      string
	AllowedAlg []string
	ClockSkew  time.Duration
	// Now 允许测试注入时间；零值表示使用真实时间。
	Now time.Time
}

// parsedJWT 是解析后的 JWT 三段结构。
type parsedJWT struct {
	Raw       string
	Header    map[string]interface{}
	Claims    map[string]interface{}
	SigningIn []byte
	Signature []byte
}

// parseJWT 做结构级解析（不校验签名）。
//
// 刻意只做「结构是否合法」的判断，把语义校验留给 VerifyIDToken：
// 两件事混在一起时，最典型的漏洞是「解析成功就当成校验通过」。
func parseJWT(token string) (*parsedJWT, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("%w: expected 3 segments, got %d", ErrInvalidToken, len(parts))
	}
	// JWS 三段都必须是 base64url（无填充）。用 RawURLEncoding 而不是
	// URLEncoding：后者要求 padding，会拒绝掉合法的紧凑序列化。
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: header is not base64url", ErrInvalidToken)
	}
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: payload is not base64url", ErrInvalidToken)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("%w: signature is not base64url", ErrInvalidToken)
	}

	var header map[string]interface{}
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		return nil, fmt.Errorf("%w: header is not JSON", ErrInvalidToken)
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		return nil, fmt.Errorf("%w: payload is not JSON", ErrInvalidToken)
	}
	return &parsedJWT{
		Raw:       token,
		Header:    header,
		Claims:    claims,
		SigningIn: []byte(parts[0] + "." + parts[1]),
		Signature: sig,
	}, nil
}

// VerifyIDToken 校验一条 ID Token 并返回其中的声明。
//
// 校验顺序与理由（每一关都不能省）：
//
//  1. JWS 结构合法；
//  2. alg 在允许清单内 —— 挡掉 `alg: none` 与 HS256 混淆攻击；
//  3. kid 能在 JWKS 中找到，且签名验签通过；
//  4. iss 精确匹配（不是包含、不是前缀）；
//  5. aud 包含 ClientID（支持字符串与数组两种编码）；
//  6. exp 未过期；nbf 若存在则必须已生效；iat 不在未来（含时钟偏移容差）；
//  7. nonce 与会话中下发的一致（防重放）；
//  8. sub 非空。
//
// 返回的错误一律是 ErrInvalidToken 的包装，调用方不应据此区分失败原因
// 做不同的对外响应 —— 那会变成给攻击者的调试信息。
func VerifyIDToken(token string, keys *KeySet, opts VerifyOptions) (*IDTokenClaims, error) {
	parsed, err := parseJWT(token)
	if err != nil {
		return nil, err
	}

	algVal, _ := parsed.Header["alg"].(string)
	alg := strings.TrimSpace(algVal)
	if alg == "" {
		return nil, fmt.Errorf("%w: missing alg", ErrInvalidToken)
	}
	if strings.EqualFold(alg, "none") {
		// 最经典的降级攻击：把签名段清空并把 alg 改成 none。
		return nil, fmt.Errorf("%w: alg=none is never acceptable", ErrInvalidToken)
	}
	if len(opts.AllowedAlg) == 0 {
		opts.AllowedAlg = []string{"RS256"}
	}
	if !containsStr(opts.AllowedAlg, alg) {
		return nil, fmt.Errorf("%w: alg %q is not allowed", ErrInvalidToken, alg)
	}

	kid, _ := parsed.Header["kid"].(string)
	key, err := keys.FindByAlg(kid, alg)
	if err != nil {
		return nil, err
	}
	if err := verifySignature(alg, key, parsed.SigningIn, parsed.Signature); err != nil {
		return nil, err
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	skew := opts.ClockSkew
	if skew <= 0 {
		skew = 60 * time.Second
	}

	iss, _ := parsed.Claims["iss"].(string)
	if !strings.EqualFold(trimSpace(iss), trimSpace(opts.Issuer)) {
		return nil, fmt.Errorf("%w: issuer mismatch", ErrInvalidToken)
	}

	aud := parseAudience(parsed.Claims["aud"])
	if len(aud) == 0 || !containsStr(aud, opts.ClientID) {
		return nil, fmt.Errorf("%w: audience does not include this client", ErrInvalidToken)
	}

	exp, ok := numericDate(parsed.Claims["exp"])
	if !ok {
		return nil, fmt.Errorf("%w: missing or malformed exp", ErrInvalidToken)
	}
	if !now.Before(exp.Add(skew)) {
		return nil, fmt.Errorf("%w: token expired", ErrInvalidToken)
	}
	if iat, ok := numericDate(parsed.Claims["iat"]); ok && iat.After(now.Add(skew)) {
		return nil, fmt.Errorf("%w: token issued in the future", ErrInvalidToken)
	}
	if nbf, ok := numericDate(parsed.Claims["nbf"]); ok && now.Add(skew).Before(nbf) {
		return nil, fmt.Errorf("%w: token is not valid yet", ErrInvalidToken)
	}

	// nonce 只在调用方声明「本次登录有 nonce」时强制。
	// 注意方向：**不是**「token 没带 nonce 就跳过」—— 如果服务端发了 nonce，
	// token 必须回带它，否则重放一个没有 nonce 的旧 token 就能绕过。
	if opts.Nonce != "" {
		tokenNonce, _ := parsed.Claims["nonce"].(string)
		if tokenNonce == "" || !equalFold(tokenNonce, opts.Nonce) {
			return nil, fmt.Errorf("%w: nonce mismatch", ErrInvalidToken)
		}
	}

	sub, _ := parsed.Claims["sub"].(string)
	if strings.TrimSpace(sub) == "" {
		return nil, fmt.Errorf("%w: missing sub", ErrInvalidToken)
	}

	claims := &IDTokenClaims{
		Subject:  sub,
		Issuer:   iss,
		Audience: aud,
		Nonce:    stringField(parsed.Claims, "nonce"),
		Name:     firstNonEmpty(stringField(parsed.Claims, "name"), stringField(parsed.Claims, "preferred_username")),
		Email:    strings.ToLower(trimSpace(stringField(parsed.Claims, "email"))),
		Groups:   stringSlice(parsed.Claims["groups"]),
		Raw:      parsed.Claims,
	}
	claims.EmailVerified = boolField(parsed.Claims, "email_verified")
	claims.ExpiresAt = exp
	if iat, ok := numericDate(parsed.Claims["iat"]); ok {
		claims.IssuedAt = iat
	}
	return claims, nil
}

// FindByAlg 按 kid 与算法从密钥集中选出公钥。
//
// kid 为空时（部分 IdP 只发一个密钥）回落到「唯一可用密钥」；
// 有多把密钥却没有 kid 时必须拒绝 —— 猜一把密钥验证是典型的
// 「看起来通过了其实验错了对象」。
func (ks *KeySet) FindByAlg(kid, alg string) (interface{}, error) {
	if ks == nil || len(ks.Keys) == 0 {
		return nil, fmt.Errorf("%w: key set is empty", ErrUnavailable)
	}
	var candidates []JWK
	if kid != "" {
		for _, k := range ks.Keys {
			if k.Kid == kid {
				candidates = append(candidates, k)
			}
		}
	} else if len(ks.Keys) == 1 {
		candidates = ks.Keys
	} else {
		return nil, fmt.Errorf("%w: token has no kid but the key set holds %d keys", ErrInvalidToken, len(ks.Keys))
	}
	if len(candidates) == 0 {
		// kid 未命中：交由上层强制刷新一次 JWKS（密钥轮转后旧 kid 会先失败一次）。
		return nil, fmt.Errorf("%w: no key matches kid %q", ErrKeyNotFound, kid)
	}
	for _, k := range candidates {
		if k.Alg != "" && !equalFold(k.Alg, alg) {
			continue
		}
		if strings.EqualFold(k.Use, "enc") {
			// use=enc 是加密密钥，不能用来验签。
			continue
		}
		key, err := k.PublicKey()
		if err != nil {
			return nil, err
		}
		return key, nil
	}
	return nil, fmt.Errorf("%w: no usable key for alg %q", ErrInvalidToken, alg)
}

// PublicKey 把 JWK 还原为 Go 公钥。
func (k JWK) PublicKey() (interface{}, error) {
	switch strings.ToUpper(k.Kty) {
	case "RSA":
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, fmt.Errorf("%w: malformed RSA modulus", ErrInvalidToken)
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("%w: malformed RSA exponent", ErrInvalidToken)
		}
		e := 0
		for _, b := range eBytes {
			e = e<<8 | int(b)
		}
		if e <= 0 || len(nBytes) == 0 {
			return nil, fmt.Errorf("%w: malformed RSA key", ErrInvalidToken)
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
	case "EC":
		curve, err := curveFor(k.Crv)
		if err != nil {
			return nil, err
		}
		xBytes, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, fmt.Errorf("%w: malformed EC x", ErrInvalidToken)
		}
		yBytes, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			return nil, fmt.Errorf("%w: malformed EC y", ErrInvalidToken)
		}
		if len(xBytes) == 0 || len(yBytes) == 0 {
			return nil, fmt.Errorf("%w: malformed EC key", ErrInvalidToken)
		}
		return &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(xBytes), Y: new(big.Int).SetBytes(yBytes)}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported key type %q", ErrInvalidToken, k.Kty)
	}
}

func curveFor(crv string) (elliptic.Curve, error) {
	switch crv {
	case "P-256":
		return elliptic.P256(), nil
	case "P-384":
		return elliptic.P384(), nil
	case "P-521":
		return elliptic.P521(), nil
	default:
		return nil, fmt.Errorf("%w: unsupported curve %q", ErrInvalidToken, crv)
	}
}

// verifySignature 按算法验签。
//
// 支持 RS256/384/512 与 ES256/384/512。HMAC 算法**故意不支持**：
// 开放 HS* 会让「用 IdP 公钥当 HMAC 密钥」的算法混淆攻击成立。
func verifySignature(alg string, key interface{}, signingIn, sig []byte) error {
	switch strings.ToUpper(alg) {
	case "RS256", "RS384", "RS512":
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("%w: key is not an RSA public key", ErrInvalidToken)
		}
		hashed := hashByAlg(alg, signingIn)
		var err error
		switch strings.ToUpper(alg) {
		case "RS256":
			err = rsa.VerifyPKCS1v15(pub, cryptoSHA256, hashed, sig)
		case "RS384":
			err = rsa.VerifyPKCS1v15(pub, cryptoSHA384, hashed, sig)
		default:
			err = rsa.VerifyPKCS1v15(pub, cryptoSHA512, hashed, sig)
		}
		if err != nil {
			return fmt.Errorf("%w: signature verification failed", ErrInvalidToken)
		}
		return nil
	case "ES256", "ES384", "ES512":
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("%w: key is not an EC public key", ErrInvalidToken)
		}
		hashed := hashByAlg(alg, signingIn)
		// JWS 的 ECDSA 签名是 R||S 定长拼接，不是 ASN.1 DER。
		n := (pub.Curve.Params().BitSize + 7) / 8
		if len(sig) != 2*n {
			return fmt.Errorf("%w: malformed ECDSA signature length", ErrInvalidToken)
		}
		r := new(big.Int).SetBytes(sig[:n])
		s := new(big.Int).SetBytes(sig[n:])
		if !ecdsa.Verify(pub, hashed, r, s) {
			return fmt.Errorf("%w: signature verification failed", ErrInvalidToken)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported signing algorithm %q", ErrInvalidToken, alg)
	}
}

func hashByAlg(alg string, data []byte) []byte {
	switch strings.ToUpper(alg) {
	case "RS256", "ES256":
		sum := sha256.Sum256(data)
		return sum[:]
	case "RS384", "ES384":
		sum := sha512.Sum384(data)
		return sum[:]
	default:
		sum := sha512.Sum512(data)
		return sum[:]
	}
}

// parseAudience 解析 aud 声明，兼容字符串与数组两种编码。
//
// OIDC 规范允许两者，只支持一种会让「IdP 换了个写法」直接变成登录失败。
func parseAudience(v interface{}) []string {
	switch t := v.(type) {
	case string:
		if trimSpace(t) == "" {
			return nil
		}
		return []string{t}
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok && trimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	default:
		return nil
	}
}

// numericDate 解析 NumericDate 声明（JSON 数字，允许浮点）。
func numericDate(v interface{}) (time.Time, bool) {
	switch t := v.(type) {
	case float64:
		sec := int64(t)
		nsec := int64((t - float64(sec)) * 1e9)
		return time.Unix(sec, nsec).UTC(), true
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return time.Time{}, false
		}
		sec := int64(f)
		return time.Unix(sec, 0).UTC(), true
	case int64:
		return time.Unix(t, 0).UTC(), true
	default:
		return time.Time{}, false
	}
}

func stringField(claims map[string]interface{}, key string) string {
	s, _ := claims[key].(string)
	return s
}

func boolField(claims map[string]interface{}, key string) bool {
	b, _ := claims[key].(bool)
	return b
}

func stringSlice(v interface{}) []string {
	items, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && trimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimSpace(v) != "" {
			return v
		}
	}
	return ""
}
