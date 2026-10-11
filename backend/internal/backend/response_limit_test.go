package backend_test

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestProviderSingleHTTPAttemptAndRetryAfter(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "google"} {
		for _, status := range []int{408, 409, 429, 503} {
			t.Run(fmt.Sprintf("%s/%d", provider, status), func(t *testing.T) {
				var calls atomic.Int64
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", "12")
					w.WriteHeader(status)
					_, _ = fmt.Fprintf(w, `{"error":{"code":%d,"type":"overloaded_error","message":"retry"}}`, status)
				}))
				defer srv.Close()
				b, err := backend.Build(backend.Config{Type: provider, Options: map[string]any{
					"api_key": "test", "base_url": srv.URL, "model": "test", "response_format": "none",
				}})
				if err != nil {
					t.Fatal(err)
				}
				defer b.Close()
				_, err = b.Translate(context.Background(), backend.Request{User: "hi"})
				if err == nil || calls.Load() != 1 {
					t.Fatalf("calls=%d err=%v", calls.Load(), err)
				}
				var retry backend.RetryAfterError
				if !errors.As(err, &retry) || retry.HTTPStatus() != status || retry.GetRetryAfter() != 12*time.Second {
					t.Fatalf("retry metadata: %v", err)
				}
				if backend.IsRetryable(err) != (status != 409) {
					t.Fatalf("retry classification: %v", err)
				}
			})
		}
	}
}

func TestResponseLimitExactBoundaryAndGzip(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		for _, size := range []int{1 << 20, (1 << 20) + 1} {
			t.Run(fmt.Sprintf("gzip=%v/bytes=%d", compressed, size), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var writer io.Writer = w
					if compressed {
						w.Header().Set("Content-Encoding", "gzip")
						gz := gzip.NewWriter(w)
						defer gz.Close()
						writer = gz
					}
					_, _ = io.WriteString(writer, strings.Repeat("x", size))
				}))
				defer srv.Close()
				client := backend.LimitResponseClient(srv.Client(), backend.DefaultMaxResponseBytes)
				req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
				// Force explicit gzip handling rather than relying on auto-decode.
				if compressed {
					req.Header.Set("Accept-Encoding", "gzip")
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if int64(len(body)) != backend.DefaultMaxResponseBytes {
					t.Fatalf("read %d bytes", len(body))
				}
				if size == 1<<20 {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, backend.ErrResponseTooLarge) || backend.IsRetryable(err) {
					t.Fatalf("capacity error=%v", err)
				}
			})
		}
	}
}

func TestProviderUnaryAndStreamResponseLimits(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "google"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", provider, stream), func(t *testing.T) {
				reply := unaryReplies[provider]
				if stream {
					reply = streamReplies[provider]
				}
				reply = strings.Replace(reply, `"ok"`, `"`+strings.Repeat("x", 1<<20)+`"`, 1)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
					} else {
						w.Header().Set("Content-Type", "application/json")
					}
					_, _ = io.WriteString(w, reply)
				}))
				defer srv.Close()
				b, err := backend.Build(backend.Config{Type: provider, Options: map[string]any{
					"api_key": "test", "base_url": srv.URL, "model": "test", "response_format": "none", "stream": stream,
				}})
				if err != nil {
					t.Fatal(err)
				}
				defer b.Close()
				_, err = b.Translate(context.Background(), backend.Request{User: "hi"})
				if !errors.Is(err, backend.ErrResponseTooLarge) || backend.IsRetryable(err) {
					t.Fatalf("expected capacity failure, got %v", err)
				}
			})
		}
	}
}

func TestRetryAfterDatesAndOverflow(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	header := http.Header{"Retry-After": []string{now.Add(time.Minute).Format(http.TimeFormat)}, "Retry-After-Ms": []string{"90000"}}
	if got := backend.ParseRetryAfter(header, now); got != 90*time.Second {
		t.Fatalf("got %v", got)
	}
	for _, raw := range []string{"9223372036854775808", "+Inf", "1e1000"} {
		header.Set("Retry-After", raw)
		if got := backend.ParseRetryAfter(header, now); got <= 0 {
			t.Fatalf("overflow wrapped to %v", got)
		}
	}
	delay := backend.RetryDelay(backend.RetryPolicy{Backoff: time.Second, Jitter: true}, 1000, errors.New("network"))
	if delay <= 0 {
		t.Fatal("exponential backoff overflowed")
	}
	delay = backend.RetryDelay(backend.RetryPolicy{Backoff: time.Second}, 0, &backend.StatusError{StatusCode: 503, RetryAfter: time.Minute, Err: errors.New("busy")})
	if delay != time.Minute {
		t.Fatal("503 Retry-After was ignored")
	}
}

func TestProviderAttemptTimeoutVersusParentCancellation(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "google"} {
		for _, parent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/parent=%v", provider, parent), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
					}
				}))
				defer srv.Close()
				timeout := "20ms"
				if parent {
					timeout = "0s"
				}
				b, err := backend.Build(backend.Config{Type: provider, Options: map[string]any{
					"api_key": "test", "base_url": srv.URL, "model": "test", "response_format": "none", "timeout": timeout,
				}})
				if err != nil {
					t.Fatal(err)
				}
				defer b.Close()
				ctx := context.Background()
				if parent {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
					defer cancel()
				}
				_, err = b.Translate(ctx, backend.Request{User: "hi"})
				if !errors.Is(err, context.DeadlineExceeded) || backend.IsRetryable(err) == parent {
					t.Fatalf("timeout classification: %v", err)
				}
			})
		}
	}
}
