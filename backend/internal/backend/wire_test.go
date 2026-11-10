package backend_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

type testBuildWorkers struct {
	sem                 chan struct{}
	active, peak, calls atomic.Int32
}

type countingErrorBody struct {
	reader io.Reader
	read   int
	closed bool
}

func (r *countingErrorBody) Read(p []byte) (int, error) {
	n, e := r.reader.Read(p)
	r.read += n
	return n, e
}
func (r *countingErrorBody) Close() error { r.closed = true; return nil }

func TestRubyHandoffErrorEnvelopeUsesExactResponseCredit(t *testing.T) {
	ctx := backend.WithRequestCredits(context.Background(), 100, 8)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid", nil)
	body := &countingErrorBody{reader: strings.NewReader(strings.Repeat("x", 4096))}
	resp, err := backend.GuardProviderErrors(req, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"7"}}, Body: body}, nil
	})
	if err == nil || resp.StatusCode != 429 || body.read != 9 || !body.closed {
		t.Fatalf("small-credit envelope: read=%d closed=%v err=%v", body.read, body.closed, err)
	}
	var status *backend.StatusError
	if len(err.Error()) > 512 || !errors.As(err, &status) || status.StatusCode != 429 || status.RetryAfter != 7*time.Second || !errors.Is(err, backend.ErrResponseTooLarge) || backend.IsRetryable(err) {
		t.Fatalf("status not retained in finite error: %v", err)
	}
}

func (w *testBuildWorkers) Run(ctx context.Context, fn func() error) error {
	select {
	case w.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	n := w.active.Add(1)
	for p := w.peak.Load(); n > p && !w.peak.CompareAndSwap(p, n); p = w.peak.Load() {
	}
	w.calls.Add(1)
	defer func() { w.active.Add(-1); <-w.sem }()
	return fn()
}

func TestRubyHandoffProviderBuildGateDoesNotThrottleHTTP(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "google"} {
		t.Run(provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			release := make(chan struct{})
			ready := make(chan struct{}, 32)
			var once sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ready <- struct{}{}
				select {
				case <-release:
					_, _ = io.WriteString(w, unaryReplies[provider])
				case <-r.Context().Done():
				}
			}))
			defer srv.Close()
			b, e := backend.Build(backend.Config{Type: provider, Options: map[string]any{"api_key": "test", "model": "test", "base_url": srv.URL, "response_format": "none"}})
			if e != nil {
				t.Fatal(e)
			}
			defer b.Close()
			workers := &testBuildWorkers{sem: make(chan struct{}, 2)}
			ctx = backend.WithBuildWorkers(ctx, workers)
			var wg sync.WaitGroup
			results := make(chan error, 32)
			cleanup := func() { cancel(); once.Do(func() { close(release) }); wg.Wait() }
			defer cleanup()
			for i := 0; i < 32; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					out, e := b.Translate(ctx, backend.Request{User: "hello"})
					if e == nil && (out == nil || out.Text != "ok" || out.Raw != nil) {
						e = fmt.Errorf("invalid useful response")
					}
					results <- e
				}()
			}
			for i := 0; i < 32; i++ {
				select {
				case <-ready:
				case <-ctx.Done():
					cleanup()
					t.Fatal("provider HTTP remained behind CPU permits")
				}
			}
			if workers.active.Load() != 0 || workers.calls.Load() < 32 {
				cleanup()
				t.Fatal("CPU permits held across network wait")
			}
			once.Do(func() { close(release) })
			wg.Wait()
			for i := 0; i < 32; i++ {
				if e := <-results; e != nil {
					t.Fatal(e)
				}
			}
			if workers.peak.Load() > 2 || workers.calls.Load() < 32 || workers.active.Load() != 0 {
				t.Fatalf("CPU gate calls%d peak%d active%d", workers.calls.Load(), workers.peak.Load(), workers.active.Load())
			}
		})
	}
}

func TestRubyHandoffSSELongLinesCRLFAndFragmentedPrefixes(t *testing.T) {
	text := strings.Repeat("x", 20000)
	body := "event: ignored\r\n: comment\r\ndata: " + text + "\r\ndata: second\r\n\r\ndata: tail"
	var got []string
	err := backend.ReadSSE(strings.NewReader(body), int64(len(body)), func(p []byte) error { got = append(got, string(p)); return nil })
	if err != nil || !reflect.DeepEqual(got, []string{text + "\nsecond", "tail"}) {
		t.Fatalf("long CRLF lines: %v %v", got, err)
	}
}

func TestRubyHandoffProviderWireDefaultsSchemaAndExactCredit(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "google"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", provider, stream), func(t *testing.T) {
				var body []byte
				var headers http.Header
				var path string
				var calls atomic.Int64
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					body, _ = io.ReadAll(r.Body)
					headers = r.Header.Clone()
					path = r.URL.Path
					w.Header().Set("Content-Type", "application/json")
					reply := unaryReplies[provider]
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						reply = streamReplies[provider]
					}
					_, _ = io.WriteString(w, reply)
				}))
				defer srv.Close()
				b, err := backend.Build(backend.Config{Type: provider, Options: map[string]any{"api_key": "test-secret", "model": "default-model", "base_url": srv.URL, "stream": stream, "thinking_level": "off", "enable_prompt_cache": true, "temperature": 0.3, "top_p": 0.7, "max_tokens": 8192}})
				if err != nil {
					t.Fatal(err)
				}
				defer b.Close()
				b = backend.NewPolicyBackend(backend.NewRateLimitedBackend(backend.NewMeteredBackend(b), nil), func(context.Context) error { return nil })
				zero := 0.0
				top := 0.8
				schema := map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}, "additionalProperties": false}
				req := backend.Request{Model: "override", System: "sys\n汉字", User: "user\"\\", Temperature: &zero, TopP: &top, MaxTokens: 256, ResponseFormat: "json_schema", JSONSchema: schema}
				n, err := backend.MeasureRequest(b, req)
				if err != nil {
					t.Fatal(err)
				}
				ctx := backend.WithRequestCredits(context.Background(), n, 1<<20)
				resp, err := b.Translate(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				if resp.Raw != nil {
					t.Fatal("SDK Raw retained")
				}
				if int64(len(body)) != n {
					t.Fatalf("measured=%d actual=%d", n, len(body))
				}
				var got map[string]any
				if err = json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				switch provider {
				case "openai":
					if got["model"] != "override" || got["temperature"] != zero || got["top_p"] != top || got["max_tokens"] != float64(256) || got["reasoning_effort"] != "none" {
						t.Fatalf("defaults/override %v", got)
					}
					format := got["response_format"].(map[string]any)["json_schema"].(map[string]any)
					if format["strict"] != true {
						t.Fatal("strict lost")
					}
					verifySchema(t, format["schema"], schema)
					if headers.Get("Authorization") != "Bearer test-secret" || !strings.HasSuffix(path, "chat/completions") {
						t.Fatal("openai endpoint/auth")
					}
				case "anthropic":
					if got["temperature"] != zero || got["top_p"] != top || got["max_tokens"] != float64(256) {
						t.Fatalf("overrides %v", got)
					}
					tool := got["tools"].([]any)[0].(map[string]any)
					verifySchema(t, tool["input_schema"], schema)
					if got["tool_choice"].(map[string]any)["name"] != "emit_translations" {
						t.Fatal("tool choice")
					}
					system := got["system"].([]any)[0].(map[string]any)
					if system["cache_control"].(map[string]any)["type"] != "ephemeral" || headers.Get("X-Api-Key") != "test-secret" || headers.Get("Anthropic-Version") == "" {
						t.Fatal("anthropic headers/cache")
					}
				case "google":
					config := got["generationConfig"].(map[string]any)
					verifySchema(t, config["responseJsonSchema"], schema)
					if config["responseMimeType"] != "application/json" || config["temperature"] != zero || config["maxOutputTokens"] != float64(256) {
						t.Fatalf("gemini config %v", config)
					}
					if headers.Get("x-goog-api-key") != "test-secret" || !strings.HasPrefix(path, "/v1beta/models/override:") {
						t.Fatal("google endpoint/auth")
					}
				}
				_, err = b.Translate(backend.WithRequestCredits(context.Background(), n-1, 1<<20), req)
				if !errors.Is(err, backend.ErrBuildCapacity) || calls.Load() != 1 {
					t.Fatalf("over-credit dispatched calls=%d error=%v", calls.Load(), err)
				}
			})
		}
	}
}

type wireCheckingLimiter struct {
	waits int
	err   error
}

func (l *wireCheckingLimiter) Close() {}

func (l *wireCheckingLimiter) Wait(ctx context.Context) error {
	l.waits++
	if l.err != nil {
		return l.err
	}
	return ctx.Err()
}

func TestRubyHandoffMeteredProviderExactCreditKeepsExecutionGuards(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "google"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", provider, stream), func(t *testing.T) {
				var calls, bodyBytes atomic.Int64
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					bodyBytes.Store(int64(len(body)))
					reply := unaryReplies[provider]
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						reply = streamReplies[provider]
					}
					_, _ = io.WriteString(w, reply)
				}))
				defer srv.Close()
				inner, err := backend.Build(backend.Config{Type: provider, Options: map[string]any{"api_key": "test", "model": "test", "base_url": srv.URL, "response_format": "none", "stream": stream}})
				if err != nil {
					t.Fatal(err)
				}
				meter := backend.NewMeteredBackend(inner)
				limiter := &wireCheckingLimiter{}
				var policyErr error
				checks := 0
				b := backend.NewPolicyBackend(backend.NewRateLimitedBackend(meter, limiter), func(context.Context) error { checks++; return policyErr })
				defer b.Close()
				req := backend.Request{System: "sys\n\t\r\b\f", User: "hello\n\t\r\b\f"}
				system, _ := backend.MeasureJSONString(req.System)
				user, _ := backend.MeasureJSONString(req.User)
				m := backend.PromptMeasure{SystemBytes: int64(len(req.System)), UserBytes: int64(len(req.User)), SystemJSONBytes: system, UserJSONBytes: user}
				n, err := backend.MeasureRequest(b, req)
				if err != nil {
					t.Fatal(err)
				}
				borrowed := req
				borrowed.System, borrowed.User = "", ""
				promptBytes, err := backend.MeasureRequestPrompt(b, borrowed, m)
				if err != nil || promptBytes != n || checks != 0 || limiter.waits != 0 || meter.Metrics().APICalls != 0 {
					t.Fatalf("measurement used execution guards or drifted: wire=%d prompt=%d err=%v", n, promptBytes, err)
				}
				ctx := backend.WithRequestCredits(context.Background(), n, 1<<20)
				response, err := b.Translate(ctx, req)
				if err != nil || response == nil || response.Text != "ok" || bodyBytes.Load() != n || checks != 1 || limiter.waits != 1 || calls.Load() != 1 {
					t.Fatalf("wrapped exact credit execution: response=%v wire=%d actual=%d checks=%d waits=%d calls=%d err=%v", response, n, bodyBytes.Load(), checks, limiter.waits, calls.Load(), err)
				}
				metrics := meter.Metrics()
				if metrics.APICalls != 1 || metrics.InputTokens != response.Usage.PromptTokens || metrics.OutputTokens != response.Usage.CompletionTokens {
					t.Fatalf("meter lost provider usage: %+v", metrics)
				}
				policyErr = errors.New("revoked test credential")
				if _, err := b.Translate(ctx, req); !errors.Is(err, policyErr) || limiter.waits != 1 || meter.Metrics() != metrics || calls.Load() != 1 {
					t.Fatalf("policy refusal bypassed: %v", err)
				}
				policyErr = nil
				limiter.err = errors.New("closed test limiter")
				if _, err := b.Translate(ctx, req); !errors.Is(err, limiter.err) || limiter.waits != 2 || meter.Metrics() != metrics || calls.Load() != 1 {
					t.Fatalf("limiter refusal bypassed: %v", err)
				}
				limiter.err = nil
				_, err = b.Translate(backend.WithRequestCredits(context.Background(), n-1, 1<<20), req)
				after := meter.Metrics()
				if !errors.Is(err, backend.ErrBuildCapacity) || limiter.waits != 3 || after.APICalls != 2 || after.InputTokens != metrics.InputTokens || after.OutputTokens != metrics.OutputTokens || calls.Load() != 1 {
					t.Fatalf("insufficient exact credit dispatched or lost metering: %+v err=%v", after, err)
				}
			})
		}
	}
}
func verifySchema(t *testing.T, got any, want map[string]any) {
	t.Helper()
	encoded, _ := json.Marshal(want)
	var normalized any
	_ = json.Unmarshal(encoded, &normalized)
	if !reflect.DeepEqual(got, normalized) {
		t.Fatalf("schema changed: %v", got)
	}
}

func TestRubyHandoffProviderRejectsDeepOrLargeMetadata(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "google"} {
		t.Run(provider, func(t *testing.T) {
			reply := strings.TrimSuffix(unaryReplies[provider], "}") + `,"unused":` + strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65) + "}"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, reply) }))
			defer srv.Close()
			b, e := backend.Build(backend.Config{Type: provider, Options: map[string]any{"api_key": "test", "base_url": srv.URL, "model": "test", "response_format": "none"}})
			if e != nil {
				t.Fatal(e)
			}
			defer b.Close()
			_, e = b.Translate(context.Background(), backend.Request{User: "hello"})
			if !errors.Is(e, backend.ErrBuildCapacity) || backend.IsRetryable(e) {
				t.Fatalf("depth accepted %v", e)
			}
		})
	}
}

func TestRubyHandoffProviderErrorEnvelopePreservesClassification(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "google"} {
		t.Run(provider, func(t *testing.T) {
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(429)
				_, _ = io.WriteString(w, strings.Repeat("x", 2<<20))
			}))
			defer srv.Close()
			b, e := backend.Build(backend.Config{Type: provider, Options: map[string]any{"api_key": "test", "base_url": srv.URL, "model": "test", "response_format": "none"}})
			if e != nil {
				t.Fatal(e)
			}
			defer b.Close()
			_, e = b.Translate(context.Background(), backend.Request{User: "hello"})
			var status backend.RetryAfterError
			if !errors.As(e, &status) || status.HTTPStatus() != 429 || status.GetRetryAfter() != 7*time.Second || !backend.IsRetryable(e) || calls.Load() != 1 {
				t.Fatalf("classification %v attempts%d", e, calls.Load())
			}
			if len(e.Error()) > 2048 {
				t.Fatal("error body retained")
			}
		})
	}
}

func TestRubyHandoffSSEMultilineAndUsageTail(t *testing.T) {
	for _, provider := range []string{"openai", "google"} {
		t.Run(provider, func(t *testing.T) {
			sse := streamReplies[provider]
			if provider == "openai" {
				sse = strings.Replace(sse, "data: [DONE]", `data: {"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`+"\n\ndata: [DONE]", 1)
			} else {
				sse += `data: {"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5}}` + "\n\n"
			}
			sse = strings.Replace(sse, `"choices":`, `\n`+`"choices":`, 1)
			sse = strings.Replace(sse, `\n`, "\ndata: ", 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, ": comment\r\n\r\n"+sse)
			}))
			defer srv.Close()
			b, e := backend.Build(backend.Config{Type: provider, Options: map[string]any{"api_key": "test", "base_url": srv.URL, "model": "test", "response_format": "none", "stream": true}})
			if e != nil {
				t.Fatal(e)
			}
			defer b.Close()
			out, e := b.Translate(context.Background(), backend.Request{User: "hello"})
			if e != nil || out.Text != "ok" || out.Usage.TotalTokens != 5 || out.Raw != nil {
				t.Fatalf("SSE %v %v", out, e)
			}
		})
	}
	var seen []byte
	if e := backend.ReadSSE(strings.NewReader("data: {\r\ndata: \"x\":1}\r\n\r\n"), 128, func(d []byte) error { seen = bytes.Clone(d); return nil }); e != nil || !json.Valid(seen) {
		t.Fatalf("multiline %q %v", seen, e)
	}
}
