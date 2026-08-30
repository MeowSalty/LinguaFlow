package google

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	genai "google.golang.org/genai"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestBuildCfg_ThinkingLevel(t *testing.T) {
	req := backend.Request{
		System: "sys",
		User:   "user",
		Model:  "gemini-2.5-flash",
	}

	t.Run("off omits ThinkingConfig", func(t *testing.T) {
		b := &Backend{model: "gemini-2.5-flash", thinking: backend.ThinkingOff}
		_, _, cfg, err := b.buildCfg(req)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ThinkingConfig != nil {
			t.Fatal("want ThinkingConfig nil when off")
		}
	})

	t.Run("zero value omits ThinkingConfig", func(t *testing.T) {
		b := &Backend{model: "gemini-2.5-flash"}
		_, _, cfg, err := b.buildCfg(req)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ThinkingConfig != nil {
			t.Fatal("want ThinkingConfig nil when unset")
		}
	})

	cases := []struct {
		level backend.ThinkingLevel
		want  genai.ThinkingLevel
	}{
		{backend.ThinkingLow, genai.ThinkingLevelLow},
		{backend.ThinkingMedium, genai.ThinkingLevelMedium},
		{backend.ThinkingHigh, genai.ThinkingLevelHigh},
	}
	for _, tt := range cases {
		t.Run(string(tt.level), func(t *testing.T) {
			b := &Backend{model: "gemini-2.5-flash", thinking: tt.level}
			_, _, cfg, err := b.buildCfg(req)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ThinkingConfig == nil {
				t.Fatal("want ThinkingConfig set")
			}
			if cfg.ThinkingConfig.ThinkingLevel != tt.want {
				t.Fatalf("ThinkingLevel: got %q want %q", cfg.ThinkingConfig.ThinkingLevel, tt.want)
			}
			if cfg.ThinkingConfig.ThinkingBudget != nil {
				t.Fatal("ThinkingBudget must not be set")
			}
			if cfg.ThinkingConfig.IncludeThoughts {
				t.Fatal("IncludeThoughts must be false")
			}
		})
	}
}

func TestToGoogleThinkingLevel(t *testing.T) {
	if got := toGoogleThinkingLevel(backend.ThinkingLow); got != genai.ThinkingLevelLow {
		t.Fatalf("low: %q", got)
	}
	if got := toGoogleThinkingLevel(backend.ThinkingMedium); got != genai.ThinkingLevelMedium {
		t.Fatalf("medium: %q", got)
	}
	if got := toGoogleThinkingLevel(backend.ThinkingHigh); got != genai.ThinkingLevelHigh {
		t.Fatalf("high: %q", got)
	}
	if got := toGoogleThinkingLevel(backend.ThinkingOff); got != "" {
		t.Fatalf("off: %q", got)
	}
}

// TestEmptyResponseError_Google 验证 google 后端的空响应错误携带 finish_reason
// 与 prompt_tokens 诊断信息，且被 backend.IsRetryable 归为不可重试。
func TestEmptyResponseError_Google(t *testing.T) {
	b := &Backend{name: "gemini-pro", model: "gemini-2.5-pro"}
	err := emptyResponseError(b, string(genai.FinishReasonSafety), 2048)

	var ere *backend.EmptyResponseError
	if !errors.As(err, &ere) {
		t.Fatalf("want *backend.EmptyResponseError, got %T: %v", err, err)
	}
	if ere.FinishReason != "SAFETY" {
		t.Fatalf("FinishReason = %q, want SAFETY", ere.FinishReason)
	}
	if ere.PromptTokens != 2048 {
		t.Fatalf("PromptTokens = %d, want 2048", ere.PromptTokens)
	}
	if ere.Model != "gemini-2.5-pro" {
		t.Fatalf("Model = %q, want gemini-2.5-pro", ere.Model)
	}
	if backend.IsRetryable(err) {
		t.Fatal("empty response error must not be retryable")
	}
}

// newFakeGoogleBackend 构造一个指向 httptest 假服务器的 google Backend，
// 对非流式/流式路径分别返回 handler 的响应。
func newFakeGoogleBackend(t *testing.T, handler http.Handler, stream bool) *Backend {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cc := &genai.ClientConfig{
		APIKey:  "test-key",
		Backend: genai.BackendGeminiAPI,
	}
	cc.HTTPOptions.BaseURL = srv.URL
	client, err := genai.NewClient(context.Background(), cc)
	if err != nil {
		t.Fatalf("new genai client: %v", err)
	}
	return &Backend{name: "google:test", model: "gemini-test", client: client, stream: stream}
}

const geminiTruncatedUsage = `"usageMetadata":{"promptTokenCount":128,"candidatesTokenCount":64,"totalTokenCount":192}`

func fakeGoogleRequest(t *testing.T) backend.Request {
	t.Helper()
	return backend.Request{
		System:    "sys",
		User:      "user",
		Model:     "gemini-test",
		MaxTokens: 8192,
	}
}

// geminiEvent 构造一个 GenerateContentResponse JSON 事件（text 已由调用方按 JSON
// 转义），finishReason 为空表示不携带。
func geminiEvent(texts []string, finishReason string) string {
	s := `{"candidates":[{"content":{"parts":[`
	for i, p := range texts {
		if i > 0 {
			s += ","
		}
		s += `{"text":"` + p + `"}`
	}
	s += `]}` // close parts, close content
	if finishReason != "" {
		s += `,"finishReason":"` + finishReason + `"`
	}
	s += `}]` // close candidate, close candidates
	return s + `,` + geminiTruncatedUsage + `}`
}

// geminiSSE 把若干事件编码为 SSE 报文。
func geminiSSE(events ...string) string {
	var sb strings.Builder
	for _, e := range events {
		sb.WriteString("data: " + e + "\n\n")
	}
	return sb.String()
}

// TestTranslate_TruncatedSignal 验证截断（finish_reason=MAX_TOKENS）一等化：
// 非空文本 → 正常返回 Response 且 Truncated=true（不再报错丢弃）；
// 空文本 → EmptyResponseError（不可重试，携带 finish_reason）。
func TestTranslate_TruncatedSignal(t *testing.T) {
	t.Run("non_stream_non_empty_returns_truncated", func(t *testing.T) {
		body := `{"candidates":[{"content":{"parts":[{"text":"{\"items\":"}],"role":"model"},"finishReason":"MAX_TOKENS"}],` + geminiTruncatedUsage + `}`
		b := newFakeGoogleBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
		}), false)

		resp, err := b.Translate(context.Background(), fakeGoogleRequest(t))
		if err != nil {
			t.Fatalf("truncated non-empty response must not error, got: %v", err)
		}
		if !resp.Truncated {
			t.Fatal("want Truncated=true for finish_reason=MAX_TOKENS")
		}
		if resp.Text != `{"items":` {
			t.Fatalf("Text = %q, want partial prefix", resp.Text)
		}
		if resp.Usage.PromptTokens != 128 || resp.Usage.CompletionTokens != 64 {
			t.Fatalf("Usage = %+v, want prompt=128 completion=64", resp.Usage)
		}
	})

	t.Run("non_stream_stop_keeps_truncated_false", func(t *testing.T) {
		body := `{"candidates":[{"content":{"parts":[{"text":"done"}],"role":"model"},"finishReason":"STOP"}],` + geminiTruncatedUsage + `}`
		b := newFakeGoogleBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
		}), false)

		resp, err := b.Translate(context.Background(), fakeGoogleRequest(t))
		if err != nil {
			t.Fatalf("normal response must not error, got: %v", err)
		}
		if resp.Truncated {
			t.Fatal("want Truncated=false for finish_reason=STOP")
		}
	})

	t.Run("non_stream_empty_returns_empty_response_error", func(t *testing.T) {
		body := `{"candidates":[{"finishReason":"MAX_TOKENS"}],` + geminiTruncatedUsage + `}`
		b := newFakeGoogleBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
		}), false)

		resp, err := b.Translate(context.Background(), fakeGoogleRequest(t))
		if err == nil {
			t.Fatal("want EmptyResponseError for empty truncated text")
		}
		if resp != nil {
			t.Fatalf("want nil response on error, got %+v", resp)
		}
		var ere *backend.EmptyResponseError
		if !errors.As(err, &ere) {
			t.Fatalf("want *backend.EmptyResponseError, got %T: %v", err, err)
		}
		if ere.FinishReason != string(genai.FinishReasonMaxTokens) {
			t.Fatalf("FinishReason = %q, want MAX_TOKENS", ere.FinishReason)
		}
		if backend.IsRetryable(err) {
			t.Fatal("empty truncated response must not be retryable")
		}
	})

	t.Run("stream_non_empty_returns_truncated", func(t *testing.T) {
		b := newFakeGoogleBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			sse := geminiSSE(
				geminiEvent([]string{`{\"items\":`}, ""),
				geminiEvent([]string{`[{\"t\":1}`}, "MAX_TOKENS"),
			)
			w.Write([]byte(sse))
		}), true)

		resp, err := b.Translate(context.Background(), fakeGoogleRequest(t))
		if err != nil {
			t.Fatalf("truncated non-empty stream must not error, got: %v", err)
		}
		if !resp.Truncated {
			t.Fatal("want Truncated=true for finish_reason=MAX_TOKENS")
		}
		if resp.Text != `{"items":[{"t":1}` {
			t.Fatalf("Text = %q, want accumulated partial prefix", resp.Text)
		}
		if resp.Usage.PromptTokens != 128 {
			t.Fatalf("Usage.PromptTokens = %d, want 128", resp.Usage.PromptTokens)
		}
	})

	t.Run("stream_empty_returns_empty_response_error", func(t *testing.T) {
		b := newFakeGoogleBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			sse := geminiSSE(
				geminiEvent([]string{""}, ""),
				geminiEvent(nil, "MAX_TOKENS"),
			)
			w.Write([]byte(sse))
		}), true)

		resp, err := b.Translate(context.Background(), fakeGoogleRequest(t))
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
		if ere.FinishReason != string(genai.FinishReasonMaxTokens) {
			t.Fatalf("FinishReason = %q, want MAX_TOKENS", ere.FinishReason)
		}
		if backend.IsRetryable(err) {
			t.Fatal("empty truncated stream must not be retryable")
		}
	})
}

// TestTranslate_TruncationNoLongerErrors 保持与旧契约的对照：MAX_TOKENS 截断
// 在文本非空时不再产生 error（含 thinking 场景的旧文案也不应再出现）。
func TestTranslate_TruncationNoLongerErrors(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"partial"}],"role":"model"},"finishReason":"MAX_TOKENS"}],` + geminiTruncatedUsage + `}`
	b := newFakeGoogleBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}), false)

	resp, err := b.Translate(context.Background(), fakeGoogleRequest(t))
	if err != nil {
		t.Fatalf("truncation with non-empty text must not error, got: %v", err)
	}
	if resp.Truncated != true || !strings.Contains(resp.Text, "partial") {
		t.Fatalf("want Truncated=true and partial text, got Truncated=%v Text=%q", resp.Truncated, resp.Text)
	}
}
