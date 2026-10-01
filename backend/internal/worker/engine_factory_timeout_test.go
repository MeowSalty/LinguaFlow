package worker

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
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

func TestEngineFactoryProviderConsumesFrozenTimeout(t *testing.T) {
	responses := map[string]string{
		"openai":    `{"id":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		"anthropic": `{"id":"test","type":"message","role":"assistant","model":"test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
		"google":    `{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}]}`,
	}
	for provider, response := range responses {
		for _, timeout := range []string{"20ms", "0s"} {
			t.Run(provider+"/"+timeout, func(t *testing.T) {
				var calls atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					select {
					case <-r.Context().Done():
						return
					case <-time.After(80 * time.Millisecond):
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, response)
				}))
				defer upstream.Close()
				registry := credential.NewMemory()
				defer registry.Close()
				binding, err := registry.Register(provider, upstream.URL, "test-secret")
				if err != nil {
					t.Fatal(err)
				}
				spec := translateSnapshot(t)
				spec.Rounds[0].Backend.Type = provider
				spec.Rounds[0].Backend.Credential = binding
				spec.Rounds[0].Backend.Options, err = execution.ResolveBackendOptions(provider, map[string]any{"model": "test", "base_url": upstream.URL, "response_format": "none", "timeout": timeout})
				if err != nil {
					t.Fatal(err)
				}
				factory := NewEngineFactoryWithCredentials(nil, nil, registry, registry)
				eng, err := factory.BuildEngine(context.Background(), spec, engine.RuntimeResources{}, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer eng.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
				defer cancel()
				resp, err := extractBackend(eng.Rounds()[0].Handler).Translate(ctx, backend.Request{User: "hello"})
				if calls.Load() == 0 {
					t.Fatal("provider did not reach the test HTTP server")
				}
				if timeout == "20ms" {
					if err == nil {
						t.Fatal("20ms timeout was ignored and the 80ms response succeeded")
					}
				} else if err != nil || resp == nil || resp.Text != "ok" {
					t.Fatalf("zero timeout disabled request incorrectly: response=%v err=%v", resp, err)
				}
			})
		}
	}
}
