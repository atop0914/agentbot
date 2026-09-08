package errors

import (
	"errors"
	"net/http"
)

// 应用级错误
var (
	// 通用错误
	ErrNotFound     = errors.New("resource not found")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrBadRequest   = errors.New("bad request")
	ErrConflict     = errors.New("resource conflict")
	ErrInternal     = errors.New("internal server error")

	// 认证相关错误
	ErrInvalidToken    = errors.New("invalid or expired token")
	ErrTokenExpired    = errors.New("token has expired")
	ErrInvalidPassword = errors.New("invalid password")
	ErrUserNotFound    = errors.New("user not found")
	ErrEmailExists     = errors.New("email already exists")
	ErrUsernameExists  = errors.New("username already exists")
	ErrOAuthFailed     = errors.New("oauth authentication failed")
	ErrOAuthStateInvalid = errors.New("invalid oauth state")
)

// AppError 应用错误结构
type AppError struct {
	Err        error  `json:"-"`
	StatusCode int    `json:"status_code"`
	Message    string `json:"message"`
	Detail     string `json:"detail,omitempty"`
}

// Error 实现 error 接口
func (e *AppError) Error() string {
	if e.Detail != "" {
		return e.Message + ": " + e.Detail
	}
	return e.Message
}

// Unwrap 支持 errors.Unwrap
func (e *AppError) Unwrap() error {
	return e.Err
}

// NewAppError 创建应用错误
func NewAppError(err error, statusCode int, message string, detail string) *AppError {
	return &AppError{
		Err:        err,
		StatusCode: statusCode,
		Message:    message,
		Detail:     detail,
	}
}

// FromError 从标准错误创建应用错误
func FromError(err error) *AppError {
	if err == nil {
		return nil
	}

	// 检查是否已经是 AppError
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr
	}

	// 匹配已知错误
	switch {
	case errors.Is(err, ErrNotFound) || errors.Is(err, ErrUserNotFound):
		return NewAppError(err, http.StatusNotFound, "Not Found", err.Error())
	case errors.Is(err, ErrUnauthorized):
		return NewAppError(err, http.StatusUnauthorized, "Unauthorized", err.Error())
	case errors.Is(err, ErrForbidden):
		return NewAppError(err, http.StatusForbidden, "Forbidden", err.Error())
	case errors.Is(err, ErrBadRequest):
		return NewAppError(err, http.StatusBadRequest, "Bad Request", err.Error())
	case errors.Is(err, ErrConflict), errors.Is(err, ErrEmailExists), errors.Is(err, ErrUsernameExists):
		return NewAppError(err, http.StatusConflict, "Conflict", err.Error())
	case errors.Is(err, ErrInvalidToken), errors.Is(err, ErrTokenExpired):
		return NewAppError(err, http.StatusUnauthorized, "Unauthorized", err.Error())
	case errors.Is(err, ErrInvalidPassword):
		return NewAppError(err, http.StatusUnauthorized, "Unauthorized", err.Error())
	case errors.Is(err, ErrOAuthFailed), errors.Is(err, ErrOAuthStateInvalid):
		return NewAppError(err, http.StatusUnauthorized, "OAuth Failed", err.Error())
	default:
		return NewAppError(err, http.StatusInternalServerError, "Internal Server Error", err.Error())
	}
}
