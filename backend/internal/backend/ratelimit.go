package backend

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
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
	// Capacity, persistence, and credential-policy failures stay terminal even
	// when their original cause includes a retryable transport timeout.
	var permanent interface{ Permanent() bool }
	if errors.As(err, &permanent) && permanent.Permanent() {
		return false
	}
	var requestTimeout *RequestTimeoutError
	if errors.As(err, &requestTimeout) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// 空响应类错误：上游返回 HTTP 200 但无可用内容（典型是内容过滤/安全拦截/空补全）。
	// 重试基本无效，交给上层转入 shrink/fallback 路径，而非退避重试刷屏。
	var emptyErr *EmptyResponseError
	if errors.As(err, &emptyErr) {
		return false
	}
	var hsErr HTTPStatusError
	if errors.As(err, &hsErr) {
		code := hsErr.HTTPStatus()
		return code >= 500 || code == http.StatusTooManyRequests || code == http.StatusRequestTimeout
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

// WrapUpstreamError is a compatibility fallback for untyped upstream errors.
// Provider adapters should prefer the SDK's public Error aliases and headers.
func WrapUpstreamError(prefix string, err error) error {
	if code, ok := ExtractHTTPStatusCode(err.Error()); ok {
		return fmt.Errorf("%s: %w",
			prefix,
			&StatusError{StatusCode: code, Err: err})
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

// WrapHTTPError retains typed HTTP status and provider backoff without losing
// the original SDK error. Unknown 409 conflicts remain nonretryable.
func WrapHTTPError(prefix string, err error, status int, headers http.Header) error {
	return fmt.Errorf("%s: %w", prefix, &StatusError{
		StatusCode: status, Err: err, RetryAfter: ParseRetryAfter(headers, time.Now()),
	})
}

// ParseRetryAfter supports seconds, HTTP dates, and providers' millisecond
// extension. It uses the later valid value and saturates overflow rather than
// turning an excessive delay into immediate dispatch.
func ParseRetryAfter(headers http.Header, now time.Time) time.Duration {
	parseNumber := func(raw string, unit time.Duration) time.Duration {
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if (err != nil && !errors.Is(err, strconv.ErrRange)) || math.IsNaN(value) || value <= 0 {
			return 0
		}
		if value >= float64(math.MaxInt64)/float64(unit) {
			return time.Duration(math.MaxInt64)
		}
		return time.Duration(value * float64(unit))
	}
	delay := parseNumber(headers.Get("Retry-After-Ms"), time.Millisecond)
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if at, err := http.ParseTime(raw); err == nil {
		delay = max(delay, at.Sub(now))
	} else {
		delay = max(delay, parseNumber(raw, time.Second))
	}
	return delay
}

// RetryDelay computes a delay for a zero-based network retry index. It does not
// own attempts or sleep, so a durable scheduler can persist its chosen deadline.
// Arithmetic saturates instead of letting large Retry-After values wrap or make
// jitter panic. The server's later deadline always wins, including on 5xx.
func RetryDelay(policy RetryPolicy, retryIndex int, lastErr error) time.Duration {
	wait := max(policy.Backoff, 0)
	retryIndex = max(retryIndex, 0)
	if wait > 0 {
		if retryIndex >= 63 || wait > time.Duration(math.MaxInt64)>>retryIndex {
			wait = time.Duration(math.MaxInt64)
		} else {
			wait <<= retryIndex
		}
	}
	var status HTTPStatusError
	if errors.As(lastErr, &status) && status.HTTPStatus() == http.StatusTooManyRequests {
		wait = max(wait, minRateLimitBackoff)
	}
	if server := extractRetryAfterErr(lastErr); server != nil {
		wait = max(wait, server.GetRetryAfter())
	}
	if policy.Jitter {
		room := time.Duration(math.MaxInt64) - wait
		spread := min(wait, room)
		if spread > 0 {
			wait += time.Duration(rand.Int63n(int64(spread) + 1))
		}
	}
	return wait
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
		wait := RetryDelay(policy, attempt-1, lastErr)
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

// NewRateLimiterPerMinute 创建每分钟 ratePerMinute 个令牌的限流器。
// ratePerMinute <= 0 时返回 nopLimiter（不限流）。
func NewRateLimiterPerMinute(ratePerMinute int) RateLimiter {
	if ratePerMinute <= 0 {
		return nopLimiter{}
	}
	r := &tokenBucket{
		interval: time.Minute / time.Duration(ratePerMinute),
		tokens:   make(chan struct{}, ratePerMinute),
		done:     make(chan struct{}),
	}
	for i := 0; i < ratePerMinute; i++ {
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
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-r.done:
		return ErrLimiterClosed
	default:
	}
	select {
	case <-r.tokens:
		return nil
	case <-r.done:
		return ErrLimiterClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

type nopLimiter struct{}

func (nopLimiter) Wait(ctx context.Context) error { return ctx.Err() }
func (nopLimiter) Close()                         {}

// RateLimitedBackend 包装一个 Backend，在每次 Translate 前先通过限流器。
// 用于按后端实例独立限流，与 Stage 级全局限流器互补。
type RateLimitedBackend struct {
	inner   Backend
	limiter RateLimiter
}

type admittedRequestKey struct{}

// WithAdmittedRequest marks a request whose current backend RPM was already
// charged by joint admission. The marker is internal context state and cannot
// be set through a model request or serialized execution snapshot.
func WithAdmittedRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, admittedRequestKey{}, true)
}

// NewRateLimitedBackend 创建一个带独立限流器的 Backend 包装。
// limiter 为 nil 时限流器为 nop（不限流）。
func NewRateLimitedBackend(inner Backend, limiter RateLimiter) *RateLimitedBackend {
	if limiter == nil {
		limiter = nopLimiter{}
	}
	return &RateLimitedBackend{
		inner:   inner,
		limiter: limiter,
	}
}

func (b *RateLimitedBackend) Name() string { return b.inner.Name() }

func (b *RateLimitedBackend) Translate(ctx context.Context, req Request) (*Response, error) {
	if admitted, _ := ctx.Value(admittedRequestKey{}).(bool); !admitted {
		if err := b.limiter.Wait(ctx); err != nil {
			return nil, err
		}
	}
	return b.inner.Translate(ctx, req)
}

func (b *RateLimitedBackend) Backend() Backend { return b.inner }

func (b *RateLimitedBackend) Close() error {
	return b.inner.Close()
}
