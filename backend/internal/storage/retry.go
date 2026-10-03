package storage

import "time"

// RetryAfterError 仅保留经过净化处理的提供方延迟。Error 文本与
// Unwrap 暴露稳定的 storage 错误码，绝不暴露 HTTP 头或响应体。
type RetryAfterError struct {
	Delay time.Duration
}

func (e *RetryAfterError) Error() string             { return ErrUnavailable.Error() }
func (e *RetryAfterError) Unwrap() error             { return ErrUnavailable }
func (e *RetryAfterError) RetryAfter() time.Duration { return e.Delay }
