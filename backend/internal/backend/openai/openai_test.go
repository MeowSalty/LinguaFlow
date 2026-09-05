package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openaigo "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestBuildParams_ThinkingLevel(t *testing.T) {
	req := backend.Request{
		System: "sys",
		User:   "user",
		Model:  "gpt-4o",
	}

	t.Run("off omits ReasoningEffort", func(t *testing.T) {
		b := &Backend{model: "gpt-4o", thinking: backend.ThinkingOff}
		params, err := b.buildParams(req)
		if err != nil {
			t.Fatal(err)
		}
		if params.ReasoningEffort != "" {
			t.Fatalf("want empty ReasoningEffort, got %q", params.ReasoningEffort)
		}
	})

	t.Run("zero value omits ReasoningEffort", func(t *testing.T) {
		b := &Backend{model: "gpt-4o"}
		params, err := b.buildParams(req)
		if err != nil {
			t.Fatal(err)
		}
		if params.ReasoningEffort != "" {
			t.Fatalf("want empty ReasoningEffort, got %q", params.ReasoningEffort)
		}
	})

	for _, level := range []backend.ThinkingLevel{
		backend.ThinkingLow,
		backend.ThinkingMedium,
		backend.ThinkingHigh,
	} {
		t.Run(string(level), func(t *testing.T) {
			b := &Backend{model: "gpt-4o", thinking: level}
			params, err := b.buildParams(req)
			if err != nil {
				t.Fatal(err)
			}
			want := shared.ReasoningEffort(level)
			if params.ReasoningEffort != want {
				t.Fatalf("ReasoningEffort: got %q want %q", params.ReasoningEffort, want)
			}
		})
	}
}

func TestFactory_ParseThinkingLevel(t *testing.T) {
	_, err := factory(backend.Config{
		Options: map[string]any{
			"api_key":        "k",
			"model":          "gpt-4o",
			"thinking_level": "invalid",
		},
	})
	if err == nil {
		t.Fatal("want error for invalid thinking_level")
	}
}

// newFakeOpenAIBackend 构造一个指向 httptest 假服务器的 openai Backend。
func newFakeOpenAIBackend(t *testing.T, handler http.Handler, stream bool) *Backend {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := openaigo.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL(srv.URL),
	)
	return &Backend{name: "openai:test", model: "gpt-test", client: client, stream: stream}
}

func fakeOpenAIRequest(t *testing.T) backend.Request {
	t.Helper()
	return backend.Request{
		System:    "sys",
		User:      "user",
		Model:     "gpt-test",
		MaxTokens: 8192,
	}
}

const openaiChatUsage = `"usage":{"prompt_tokens":128,"completion_tokens":64,"total_tokens":192}`

// chatCompletionJSON 构造非流式 chat.completion 响应。
func chatCompletionJSON(content, finishReason string) string {
	return `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-test",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"` + content + `"},"finish_reason":"` + finishReason + `"}],` +
		openaiChatUsage + `}`
}

// chatChunkJSON 构造流式 chat.completion.chunk 事件；usage 非空时附加。
func chatChunkJSON(content, finishReason, usage string) string {
	s := `{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"gpt-test",` +
		`"choices":[{"index":0,"delta":{"content":"` + content + `"},"finish_reason":`
	if finishReason == "" {
		s += `null`
	} else {
		s += `"` + finishReason + `"`
	}
	s += `}]`
	if usage != "" {
		s += `,` + usage
	}
	return s + `}`
}

// TestTranslate_TruncatedSignal 验证截断（finish_reason=length）一等化：
// 非空 content → 正常返回 Response 且 Truncated=true（此前被静默当作普通成功）；
// 空 content → emptyChoicesError（不可重试，携带 finish_reason）。
func TestTranslate_TruncatedSignal(t *testing.T) {
	t.Run("non_stream_non_empty_returns_truncated", func(t *testing.T) {
		b := newFakeOpenAIBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(chatCompletionJSON(`{\"items\":[{\"t\":1}`, "length")))
		}), false)

		resp, err := b.Translate(context.Background(), fakeOpenAIRequest(t))
		if err != nil {
			t.Fatalf("truncated non-empty response must not error, got: %v", err)
		}
		if !resp.Truncated {
			t.Fatal("want Truncated=true for finish_reason=length")
		}
		if resp.Text != `{"items":[{"t":1}` {
			t.Fatalf("Text = %q, want partial prefix", resp.Text)
		}
		if resp.Usage.PromptTokens != 128 || resp.Usage.CompletionTokens != 64 {
			t.Fatalf("Usage = %+v, want prompt=128 completion=64", resp.Usage)
		}
	})

	t.Run("non_stream_stop_keeps_truncated_false", func(t *testing.T) {
		b := newFakeOpenAIBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(chatCompletionJSON("done", "stop")))
		}), false)

		resp, err := b.Translate(context.Background(), fakeOpenAIRequest(t))
		if err != nil {
			t.Fatalf("normal response must not error, got: %v", err)
		}
		if resp.Truncated {
			t.Fatal("want Truncated=false for finish_reason=stop")
		}
	})

	t.Run("non_stream_empty_returns_empty_choices_error", func(t *testing.T) {
		b := newFakeOpenAIBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(chatCompletionJSON("", "length")))
		}), false)

		resp, err := b.Translate(context.Background(), fakeOpenAIRequest(t))
		if err == nil {
			t.Fatal("want EmptyResponseError for empty truncated content")
		}
		if resp != nil {
			t.Fatalf("want nil response on error, got %+v", resp)
		}
		var ere *backend.EmptyResponseError
		if !errors.As(err, &ere) {
			t.Fatalf("want *backend.EmptyResponseError, got %T: %v", err, err)
		}
		if ere.FinishReason != "length" {
			t.Fatalf("FinishReason = %q, want length", ere.FinishReason)
		}
		if backend.IsRetryable(err) {
			t.Fatal("empty truncated response must not be retryable")
		}
	})

	t.Run("stream_non_empty_returns_truncated", func(t *testing.T) {
		b := newFakeOpenAIBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			sse := "data: " + chatChunkJSON(`{\"items\":[`, "", "") + "\n\n" +
				"data: " + chatChunkJSON(`{\"t\":1}]`, "length", openaiChatUsage) + "\n\n" +
				"data: [DONE]\n\n"
			w.Write([]byte(sse))
		}), true)

		resp, err := b.Translate(context.Background(), fakeOpenAIRequest(t))
		if err != nil {
			t.Fatalf("truncated non-empty stream must not error, got: %v", err)
		}
		if !resp.Truncated {
			t.Fatal("want Truncated=true for finish_reason=length")
		}
		if resp.Text != `{"items":[{"t":1}]` {
			t.Fatalf("Text = %q, want accumulated content", resp.Text)
		}
	})

	t.Run("stream_empty_returns_empty_choices_error", func(t *testing.T) {
		b := newFakeOpenAIBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			sse := "data: " + chatChunkJSON("", "", "") + "\n\n" +
				"data: " + chatChunkJSON("", "length", openaiChatUsage) + "\n\n" +
				"data: [DONE]\n\n"
			w.Write([]byte(sse))
		}), true)

		resp, err := b.Translate(context.Background(), fakeOpenAIRequest(t))
		if err == nil {
			t.Fatal("want EmptyResponseError for empty truncated stream")
		}
		if resp != nil {
			t.Fatalf("want nil response on error, got %+v", resp)
		}
		var ere *backend.EmptyResponseError
		if !errors.As(err, &ere) {
			t.Fatalf("want *backend.EmptyResponseError, got %T: %v", err, err)
		}
		if ere.FinishReason != "length" {
			t.Fatalf("FinishReason = %q, want length", ere.FinishReason)
		}
		if backend.IsRetryable(err) {
			t.Fatal("empty truncated stream must not be retryable")
		}
	})
}

// TestTranslate_LengthWithContentNoLongerSilent 对照旧契约：finish_reason=length
// 且 content 非空时旧实现静默放行（Truncated 信号缺失），新实现必须显式带标志。
func TestTranslate_LengthWithContentNoLongerSilent(t *testing.T) {
	b := newFakeOpenAIBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(chatCompletionJSON("partial", "length")))
	}), false)

	resp, err := b.Translate(context.Background(), fakeOpenAIRequest(t))
	if err != nil {
		t.Fatalf("finish_reason=length with content must not error, got: %v", err)
	}
	if resp.Truncated != true || !strings.Contains(resp.Text, "partial") {
		t.Fatalf("want Truncated=true and partial text, got Truncated=%v Text=%q", resp.Truncated, resp.Text)
	}
}
