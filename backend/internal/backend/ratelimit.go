package backend

import (
	"context"
	"errors"
	"time"
)

// RetryPolicy 定义指数退避重试策略。
type RetryPolicy struct {
	MaxAttempts int
	Backoff     time.Duration // 基础退避；第 N 次重试 = Backoff * 2^(N-1)
}

// WithRetry 包装 fn，按 policy 重试。ctx 取消时立即返回 ctx.Err()。
func WithRetry(ctx context.Context, policy RetryPolicy, fn func() error) error {
	if policy.MaxAttempts < 1 {
		policy.MaxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
		}
		if attempt == policy.MaxAttempts {
			break
		}
		wait := policy.Backoff << (attempt - 1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return lastErr
}

// RateLimiter 是一个简单的令牌桶接口（按秒补充）。
type RateLimiter interface {
	Wait(ctx context.Context) error
}

// NewRateLimiter 创建每秒 ratePerSec 个令牌的限流器。
// ratePerSec <= 0 时返回 nopLimiter（不限流）。
func NewRateLimiter(ratePerSec int) RateLimiter {
	if ratePerSec <= 0 {
		return nopLimiter{}
	}
	r := &tokenBucket{
		interval: time.Second / time.Duration(ratePerSec),
		tokens:   make(chan struct{}, ratePerSec),
	}
	// 预填满桶
	for i := 0; i < ratePerSec; i++ {
		r.tokens <- struct{}{}
	}
	go r.refill()
	return r
}

type tokenBucket struct {
	interval time.Duration
	tokens   chan struct{}
}

func (r *tokenBucket) refill() {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for range t.C {
		select {
		case r.tokens <- struct{}{}:
		default:
		}
	}
}

func (r *tokenBucket) Wait(ctx context.Context) error {
	select {
	case <-r.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type nopLimiter struct{}

func (nopLimiter) Wait(context.Context) error { return nil }
