package backend_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/backend/anthropic"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/backend/google"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/backend/openai"
	"github.com/MeowSalty/LinguaFlow/backend/internal/telemetry"
)

var unaryReplies = map[string]string{
	"openai":    `{"id":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
	"anthropic": `{"id":"test","type":"message","role":"assistant","model":"test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
	"google":    `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`,
}
var streamReplies = map[string]string{
	"openai":    "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
	"anthropic": "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	"google":    "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n",
}

func metric(t *testing.T, c *telemetry.Collector, provider, operation string) telemetry.ExternalRequestSnapshot {
	t.Helper()
	for _, s := range c.Snapshot().ExternalRequests {
		if s.Provider == provider && s.Operation == operation {
			return s
		}
	}
	t.Fatal("missing metric")
	return telemetry.ExternalRequestSnapshot{}
}
func assertAttempts(t *testing.T, c *telemetry.Collector, provider, operation string, total, success, httpErrors int64) {
	t.Helper()
	s := metric(t, c, provider, operation)
	if s.Total != total || s.Inflight != 0 {
		t.Fatalf("attempts: %+v", s)
	}
	for _, o := range s.Outcomes {
		want := int64(0)
		if o.Outcome == "success" {
			want = success
		}
		if o.Outcome == "http_error" {
			want = httpErrors
		}
		if o.Finished != want || o.DurationCount != want {
			t.Fatalf("outcome: %+v want %d", o, want)
		}
	}
}

func TestProviderHTTPAttempts(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "google"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", provider, stream), func(t *testing.T) {
				var calls atomic.Int64
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprint(w, streamReplies[provider])
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprint(w, unaryReplies[provider])
					}
				}))
				defer srv.Close()
				c := telemetry.NewCollector()
				clients := telemetry.NewHTTPClients(c)
				defer clients.Shutdown()
				b, err := backend.Build(backend.Config{Type: provider, Name: "test", HTTPClient: clients.Client(provider, "generate"), Options: map[string]any{"api_key": "fake", "base_url": srv.URL, "model": "test", "response_format": "none", "stream": stream}})
				if err != nil {
					t.Fatal(err)
				}
				defer b.Close()
				resp, err := b.Translate(context.Background(), backend.Request{User: "hi"})
				if err != nil {
					t.Fatal(err)
				}
				if resp.Text != "ok" {
					t.Fatalf("response: %+v", resp)
				}
				assertAttempts(t, c, provider, "generate", calls.Load(), 1, 0)
			})
		}
	}
}

func TestProviderStreamingInflightAndCancellation(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "google"} {
		for _, cancelStream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel=%v", provider, cancelStream), func(t *testing.T) {
				headers := make(chan struct{})
				release := make(chan struct{})
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					close(headers)
					select {
					case <-release:
						_, _ = fmt.Fprint(w, streamReplies[provider])
					case <-r.Context().Done():
					}
				}))
				defer srv.Close()
				c := telemetry.NewCollector()
				clients := telemetry.NewHTTPClients(c)
				defer clients.Shutdown()
				b, err := backend.Build(backend.Config{Type: provider, HTTPClient: clients.Client(provider, "generate"), Options: map[string]any{"api_key": "fake", "base_url": srv.URL, "model": "test", "response_format": "none", "stream": true}})
				if err != nil {
					t.Fatal(err)
				}
				defer b.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() { _, err := b.Translate(ctx, backend.Request{User: "hi"}); done <- err }()
				select {
				case <-headers:
				case <-time.After(time.Second):
					t.Fatal("request did not arrive")
				}
				s := metric(t, c, provider, "generate")
				if s.Total != 1 || s.Inflight != 1 {
					t.Fatalf("headers prematurely finished attempt: %+v", s)
				}
				if cancelStream {
					cancel()
				} else {
					close(release)
				}
				select {
				case err := <-done:
					if (err != nil) != cancelStream {
						t.Fatalf("result: %v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("stream did not stop")
				}
				// Cancellation may return from the SDK before its response body is
				// handed to the stream decoder. Join transport cleanup before metrics.
				waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
				defer waitCancel()
				if err := clients.Wait(waitCtx); err != nil {
					t.Fatalf("stream transport did not finish: %v", err)
				}
				s = metric(t, c, provider, "generate")
				if s.Total != 1 || s.Inflight != 0 {
					t.Fatalf("stream not released: %+v", s)
				}
				want := "success"
				if cancelStream {
					want = "cancelled"
				}
				for _, outcome := range s.Outcomes {
					if outcome.Outcome == want && outcome.Finished != 1 {
						t.Fatalf("outcome: %+v", outcome)
					}
				}
			})
		}
	}
}

func TestProviderRetriesKeepEachHTTPOutcome(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "google"} {
		for _, status := range []int{429, 503} {
			t.Run(fmt.Sprintf("%s/%d", provider, status), func(t *testing.T) {
				var calls atomic.Int64
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if calls.Add(1) == 1 {
						w.Header().Set("Retry-After", "0.001")
						w.WriteHeader(status)
						_, _ = fmt.Fprintf(w, `{"error":{"code":%d,"type":"overloaded_error","message":"retry"}}`, status)
						return
					}
					_, _ = fmt.Fprint(w, unaryReplies[provider])
				}))
				defer srv.Close()
				c := telemetry.NewCollector()
				clients := telemetry.NewHTTPClients(c)
				defer clients.Shutdown()
				b, err := backend.Build(backend.Config{Type: provider, HTTPClient: clients.Client(provider, "generate"), Options: map[string]any{"api_key": "fake", "base_url": srv.URL, "model": "test", "response_format": "none"}})
				if err != nil {
					t.Fatal(err)
				}
				defer b.Close()
				err = backend.WithRetry(context.Background(), backend.RetryPolicy{MaxAttempts: 2, Backoff: time.Millisecond}, func() error { _, err := b.Translate(context.Background(), backend.Request{User: "hi"}); return err })
				if err != nil {
					t.Fatal(err)
				}
				assertAttempts(t, c, provider, "generate", calls.Load(), 1, 1)
				if calls.Load() != 2 {
					t.Fatalf("upstream attempts=%d", calls.Load())
				}
			})
		}
	}
}

func TestProviderModelsUseInstrumentedClients(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "google"} {
		t.Run(provider, func(t *testing.T) {
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				n := calls.Add(1)
				switch provider {
				case "openai":
					_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"one","object":"model"},{"id":"two","object":"model"}]}`)
				case "anthropic":
					if n == 1 {
						_, _ = fmt.Fprint(w, `{"data":[{"id":"one","type":"model","display_name":"One"}],"has_more":true,"first_id":"one","last_id":"one"}`)
					} else {
						_, _ = fmt.Fprint(w, `{"data":[{"id":"two","type":"model","display_name":"Two"}],"has_more":false,"first_id":"two","last_id":"two"}`)
					}
				case "google":
					if n == 1 {
						_, _ = fmt.Fprint(w, `{"models":[{"name":"models/one","displayName":"One"}],"nextPageToken":"page2"}`)
					} else {
						_, _ = fmt.Fprint(w, `{"models":[{"name":"models/two","displayName":"Two"}]}`)
					}
				}
			}))
			defer srv.Close()
			c := telemetry.NewCollector()
			clients := telemetry.NewHTTPClients(c)
			defer clients.Shutdown()
			l, err := backend.NewModelLister(provider, map[string]any{"api_key": "fake", "base_url": srv.URL}, clients.Client(provider, "list_models"))
			if err != nil {
				t.Fatal(err)
			}
			models, err := l.ListModels(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(models) != 2 {
				t.Fatalf("models=%+v", models)
			}
			want := int64(2)
			if provider == "openai" {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("upstream pages=%d want %d", calls.Load(), want)
			}
			assertAttempts(t, c, provider, "list_models", want, want, 0)
			assertAttempts(t, c, provider, "generate", 0, 0, 0)
		})
	}
}

func TestProviderValidationAndEmptyResponseAreNotHTTPFailures(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "google"} {
		t.Run(provider, func(t *testing.T) {
			c := telemetry.NewCollector()
			clients := telemetry.NewHTTPClients(c)
			defer clients.Shutdown()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{}`)
			}))
			defer srv.Close()
			b, err := backend.Build(backend.Config{Type: provider, HTTPClient: clients.Client(provider, "generate"), Options: map[string]any{"api_key": "fake", "base_url": srv.URL, "model": "test", "response_format": "none"}})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			_, err = b.Translate(context.Background(), backend.Request{User: "hi", ResponseFormat: "invalid"})
			if err == nil {
				t.Fatal("expected validation failure")
			}
			assertAttempts(t, c, provider, "generate", 0, 0, 0)
			_, err = b.Translate(context.Background(), backend.Request{User: "hi"})
			if err == nil {
				t.Fatal("expected empty response failure")
			}
			assertAttempts(t, c, provider, "generate", 1, 1, 0)
		})
	}
}
