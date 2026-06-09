package backend

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"sync"
	"time"
)

// HTTPStatusError 是一个可选接口。错误实现此接口后，
// WithRetry 可根据 HTTP 状态码决定是否重试。
// 未实现此接口的错误默认视为可重试（网络错误等）。
type HTTPStatusError interface {
	error
	HTTPStatus() int
}

// StatusError 将 HTTP 状态码与底层错误包装为 HTTPStatusError。
type StatusError struct {
	StatusCode int
	Err        error
	RetryAfter time.Duration // 可选：服务端建议的重试等待时间（如 429 的 Retry-After 头）
}

func (e *StatusError) Error() string                { return e.Err.Error() }
func (e *StatusError) Unwrap() error                { return e.Err }
func (e *StatusError) HTTPStatus() int              { return e.StatusCode }
func (e *StatusError) GetRetryAfter() time.Duration { return e.RetryAfter }

// IsRetryable 判断一个错误是否值得重试。
func IsRetryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var hsErr HTTPStatusError
	if errors.As(err, &hsErr) {
		code := hsErr.HTTPStatus()
		return code >= 500 || code == 429
	}
	// 未实现 HTTPStatusError 的错误（网络超时、DNS 失败等）默认可重试
	return true
}

// reHTTPStatus 匹配 OpenAI/Anthropic SDK 错误消息中的 HTTP 状态码。
// 格式：POST "url": 401 Unauthorized ...
var reHTTPStatus = regexp.MustCompile(`: (\d{3}) \w`)

// ExtractHTTPStatusCode 从错误消息中提取 HTTP 状态码。
// 用于 OpenAI/Anthropic SDK 的 internal/apierror.Error 消息解析。
func ExtractHTTPStatusCode(msg string) (int, bool) {
	m := reHTTPStatus.FindStringSubmatch(msg)
	if len(m) < 2 {
		return 0, false
	}
	var code int
	_, err := fmt.Sscanf(m[1], "%d", &code)
	if err != nil {
		return 0, false
	}
	return code, true
}

// minRateLimitBackoff 是 429 错误的最小退避时间。
// 当计算出的退避值低于此值时，强制使用此值，避免反复触发限流。
const minRateLimitBackoff = 5 * time.Second

// RetryAfterError 是可选接口。错误实现此接口后，
// WithRetry 会使用 max(计算退避，RetryAfter) 作为等待时间。
type RetryAfterError interface {
	HTTPStatusError
	GetRetryAfter() time.Duration
}

// extractStatusErr 从 error 链中提取 *StatusError。
func extractStatusErr(err error) *StatusError {
	var se *StatusError
	if errors.As(err, &se) {
		return se
	}
	return nil
}

// extractRetryAfterErr 从 error 链中提取 RetryAfterError。
func extractRetryAfterErr(err error) RetryAfterError {
	var ra RetryAfterError
	if errors.As(err, &ra) {
		return ra
	}
	return nil
}

// RetryPolicy 定义指数退避重试策略。
type RetryPolicy struct {
	MaxAttempts int
	Backoff     time.Duration // 基础退避；第 N 次重试 = Backoff * 2^(N-1)
	Jitter      bool          // 为 true 时添加 equal jitter 防止惊群
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
			if !IsRetryable(err) {
				return err // 不可重试的错误立即返回
			}
		}
		if attempt == policy.MaxAttempts {
			break
		}
		wait := policy.Backoff << (attempt - 1)

		// 429 错误：优先使用服务端 Retry-After，否则强制最小退避。
		if raErr := extractRetryAfterErr(lastErr); raErr != nil && raErr.HTTPStatus() == 429 {
			effectiveMin := minRateLimitBackoff
			if ra := raErr.GetRetryAfter(); ra > effectiveMin {
				effectiveMin = ra
			}
			if wait < effectiveMin {
				wait = effectiveMin
			}
		}

		if policy.Jitter {
			// Equal jitter: wait + rand(0, wait)
			wait += time.Duration(rand.Int63n(int64(wait) + 1))
		}
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
	Close() // 停止内部 goroutine，释放资源。
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
		done:     make(chan struct{}),
	}
	// 预填满桶
	for i := 0; i < ratePerSec; i++ {
		r.tokens <- struct{}{}
	}
	go r.refill()
	return r
}

type tokenBucket struct {
	interval  time.Duration
	tokens    chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func (r *tokenBucket) refill() {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-t.C:
			select {
			case r.tokens <- struct{}{}:
			default:
			}
		}
	}
}

// Close 停止 refill goroutine。多次调用安全。
func (r *tokenBucket) Close() {
	r.closeOnce.Do(func() {
		close(r.done)
	})
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
func (nopLimiter) Close()                     {}

// RateLimitedBackend 包装一个 Backend，在每次 Translate 前先通过限流器。
// 用于按后端实例独立限流，与 Stage 级全局限流器互补。
type RateLimitedBackend struct {
	inner   Backend
	limiter RateLimiter
}

// NewRateLimitedBackend 创建一个带独立限流器的 Backend 包装。
// ratePerSec <= 0 时限流器为 nop（不限流）。
func NewRateLimitedBackend(inner Backend, ratePerSec int) *RateLimitedBackend {
	return &RateLimitedBackend{
		inner:   inner,
		limiter: NewRateLimiter(ratePerSec),
	}
}

func (b *RateLimitedBackend) Name() string { return b.inner.Name() }

func (b *RateLimitedBackend) Translate(ctx context.Context, req Request) (*Response, error) {
	if err := b.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	return b.inner.Translate(ctx, req)
}

func (b *RateLimitedBackend) Close() error {
	b.limiter.Close()
	return b.inner.Close()
}
