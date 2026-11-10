package anthropic

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestRubyHandoffAnthropicToolInputDeltasAndTextFallback(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		stream           bool
	}{
		{"unary_tool", `{"content":[{"type":"text","text":"ignored"},{"type":"tool_use","name":"emit_translations","input":{"items":[]}}],"usage":{"input_tokens":2,"output_tokens":3},"stop_reason":"max_tokens"}`, `{"items":[]}`, false},
		{"unary_fallback", `{"content":[{"type":"text","text":"one"},{"type":"text","text":"two"}],"usage":{"input_tokens":2,"output_tokens":3},"stop_reason":"max_tokens"}`, "onetwo", false},
		{"stream_tool", anthToolSSE(false), `{"items":[]}`, true},
		{"stream_empty_object", anthToolSSE(true), `{}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			b, e := factory(backend.Config{HTTPClient: srv.Client(), Options: map[string]any{"api_key": "test", "model": "claude", "base_url": srv.URL, "stream": tc.stream}})
			if e != nil {
				t.Fatal(e)
			}
			defer b.Close()
			req := backend.Request{User: "hello", ResponseFormat: "json_schema", JSONSchema: map[string]any{"type": "object"}}
			out, e := b.Translate(context.Background(), req)
			if e != nil || out.Text != tc.want || !out.Truncated || out.Usage.TotalTokens != 5 || out.Raw != nil {
				t.Fatalf("tool result %v %v", out, e)
			}
		})
	}
}
func anthToolSSE(empty bool) string {
	parts := []string{`{"type":"message_start","message":{"usage":{"input_tokens":2}}}`, `{"type":"content_block_start","index":2147483647,"content_block":{"type":"tool_use","name":"emit_translations","input":{}}}`}
	if !empty {
		parts = append(parts, `{"type":"content_block_delta","index":2147483647,"delta":{"type":"input_json_delta","partial_json":"{\"items\":"}}`, `{"type":"content_block_delta","index":2147483647,"delta":{"type":"input_json_delta","partial_json":"[]}"}}`)
	}
	parts = append(parts, `{"type":"content_block_stop","index":2147483647}`, `{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":3}}`, `{"type":"message_stop"}`)
	out := ""
	for _, p := range parts {
		out += fmt.Sprintf("data: %s\n\n", p)
	}
	return out
}
