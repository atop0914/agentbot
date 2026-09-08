package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrTokenExpired = errors.New("token expired")
)

// JWTManager JWT 管理器
type JWTManager struct {
	config TokenConfig
}

// NewJWTManager 创建 JWT 管理器
func NewJWTManager(config TokenConfig) *JWTManager {
	// 设置默认值
	if config.AccessExpiry == 0 {
		config.AccessExpiry = 24 * time.Hour
	}
	if config.RefreshExpiry == 0 {
		config.RefreshExpiry = 7 * 24 * time.Hour
	}
	if config.Issuer == "" {
		config.Issuer = "agentbot"
	}
	return &JWTManager{config: config}
}

// GenerateTokenPair 生成 Token 对
func (m *JWTManager) GenerateTokenPair(claims *Claims) (*TokenPair, error) {
	now := time.Now()
	accessExpiry := now.Add(m.config.AccessExpiry)

	// 创建 Access Token
	accessClaims := jwt.MapClaims{
		"user_id":  claims.UserID,
		"username": claims.Username,
		"email":    claims.Email,
		"role":     claims.Role,
		"iss":      m.config.Issuer,
		"iat":      now.Unix(),
		"exp":      accessExpiry.Unix(),
		"type":     "access",
	}

	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessTokenStr, err := accessToken.SignedString([]byte(m.config.Secret))
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token: %w", err)
	}

	// 创建 Refresh Token
	refreshExpiry := now.Add(m.config.RefreshExpiry)
	refreshClaims := jwt.MapClaims{
		"user_id": claims.UserID,
		"iss":     m.config.Issuer,
		"iat":     now.Unix(),
		"exp":     refreshExpiry.Unix(),
		"type":    "refresh",
	}

	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshTokenStr, err := refreshToken.SignedString([]byte(m.config.Secret))
	if err != nil {
		return nil, fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessTokenStr,
		RefreshToken: refreshTokenStr,
		ExpiresAt:    accessExpiry,
	}, nil
}

// ValidateAccessToken 验证 Access Token
func (m *JWTManager) ValidateAccessToken(tokenStr string) (*Claims, error) {
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(m.config.Secret), nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrInvalidToken
	}

	if !token.Valid {
		return nil, ErrInvalidToken
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, ErrInvalidToken
	}

	// 验证 token 类型
	tokenType, _ := mapClaims["type"].(string)
	if tokenType != "access" {
		return nil, ErrInvalidToken
	}

	claims := &Claims{}
	claims.UserID, _ = mapClaims["user_id"].(string)
	claims.Username, _ = mapClaims["username"].(string)
	claims.Email, _ = mapClaims["email"].(string)
	claims.Role, _ = mapClaims["role"].(string)

	return claims, nil
}

// ValidateRefreshToken 验证 Refresh Token
func (m *JWTManager) ValidateRefreshToken(tokenStr string) (string, error) {
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(m.config.Secret), nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return "", ErrTokenExpired
		}
		return "", ErrInvalidToken
	}

	if !token.Valid {
		return "", ErrInvalidToken
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", ErrInvalidToken
	}

	// 验证 token 类型
	tokenType, _ := mapClaims["type"].(string)
	if tokenType != "refresh" {
		return "", ErrInvalidToken
	}

	userID, _ := mapClaims["user_id"].(string)
	if userID == "" {
		return "", ErrInvalidToken
	}

	return userID, nil
}

// GenerateState 生成 OAuth state 参数
func GenerateState() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
