package auth

import (
	"context"
	"net/http"
	"strings"
)

// contextKey context key 类型
type contextKey string

const (
	// ClaimsKey JWT claims 在 context 中的 key
	ClaimsKey contextKey = "jwt_claims"
)

// Middleware 认证中间件
type Middleware struct {
	service AuthService
}

// NewMiddleware 创建认证中间件
func NewMiddleware(service AuthService) *Middleware {
	return &Middleware{service: service}
}

// RequireAuth 要求认证的中间件
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractTokenFromRequest(r)
		if token == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authorization token required"})
			return
		}

		claims, err := m.service.ValidateAccessToken(token)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired token"})
			return
		}

		// 将 claims 存入 context
		ctx := context.WithValue(r.Context(), ClaimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole 要求特定角色的中间件
func (m *Middleware) RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaimsFromContext(r.Context())
			if claims == nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}

			// 检查角色
			hasRole := false
			for _, role := range roles {
				if claims.Role == role {
					hasRole = true
					break
				}
			}

			if !hasRole {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// OptionalAuth 可选认证中间件（不强制要求 token）
func (m *Middleware) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractTokenFromRequest(r)
		if token != "" {
			claims, err := m.service.ValidateAccessToken(token)
			if err == nil {
				ctx := context.WithValue(r.Context(), ClaimsKey, claims)
				r = r.WithContext(ctx)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// GetClaimsFromContext 从 context 获取 claims
func GetClaimsFromContext(ctx context.Context) *Claims {
	if claims, ok := ctx.Value(ClaimsKey).(*Claims); ok {
		return claims
	}
	return nil
}

// extractTokenFromRequest 从请求中提取 token
func extractTokenFromRequest(r *http.Request) string {
	// 从 Authorization header 提取
	auth := r.Header.Get("Authorization")
	if auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return parts[1]
		}
	}

	// 从 query 参数提取（用于 WebSocket 等场景）
	if token := r.URL.Query().Get("token"); token != "" {
		return token
	}

	return ""
}
