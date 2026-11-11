package anthropic

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestAnthropicThinkingBudget(t *testing.T) {
	tests := []struct {
		level   backend.ThinkingLevel
		maxTok  int64
		want    int64
		wantErr bool
	}{
		// 纯比例计算，透明遵从用户配置（不引入输出预留 magic）
		{backend.ThinkingMinimal, 8192, 1024, false},  // 0.125*8192=1024，恰在预算下限
		{backend.ThinkingMinimal, 16384, 2048, false}, // 正常 12.5%
		{backend.ThinkingMinimal, 4096, 1024, false},  // 0.125*4096=512 → clamp 到下限 1024，与 low 同值
		{backend.ThinkingLow, 8192, 2048, false},
		{backend.ThinkingMedium, 8192, 4096, false},
		{backend.ThinkingHigh, 8192, 6144, false}, // 0.75*8192，输出只剩 2048，由用户对 max_tokens 负责
		{backend.ThinkingHigh, 64000, 48000, false},
		{backend.ThinkingLow, 2000, 1024, false}, // 0.25*2000=500 → clamp 到下限 1024
		{backend.ThinkingHigh, 1500, 1125, false},
		// max_tokens 过小：报错（思考预算下限）
		{backend.ThinkingMinimal, 1024, 0, true},
		{backend.ThinkingLow, 1024, 0, true},
		{backend.ThinkingMedium, 500, 0, true},
		{backend.ThinkingOff, 8192, 0, true},
	}
	for _, tt := range tests {
		got, err := anthropicThinkingBudget(tt.level, tt.maxTok)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("level=%s max=%d: want error", tt.level, tt.maxTok)
			}
			continue
		}
		if err != nil {
			t.Fatalf("level=%s max=%d: %v", tt.level, tt.maxTok, err)
		}
		if got != tt.want {
			t.Fatalf("level=%s max=%d: got %d want %d", tt.level, tt.maxTok, got, tt.want)
		}
	}
}

func TestWireThinkingSkipsSampling(t *testing.T) {
	temp, top := 0.2, 0.9
	for _, level := range []backend.ThinkingLevel{"", backend.ThinkingOff, backend.ThinkingMedium} {
		t.Run(string(level), func(t *testing.T) {
			b := &Backend{model: "claude", maxTokens: 8192, temperature: &temp, topP: &top, thinking: backend.Thinking{Level: level, Set: level != ""}}
			p, _, err := b.wireParams(backend.Request{User: "user"})
			if err != nil {
				t.Fatal(err)
			}
			if level == backend.ThinkingMedium {
				thinking := p["thinking"].(map[string]any)
				if thinking["budget_tokens"] != int64(4096) {
					t.Fatal("thinking budget")
				}
				if _, ok := p["temperature"]; ok {
					t.Fatal("temperature with thinking")
				}
				if _, ok := p["top_p"]; ok {
					t.Fatal("top_p with thinking")
				}
			} else {
				if p["temperature"] != temp || p["top_p"] != top {
					t.Fatal("sampling omitted")
				}
				if level == backend.ThinkingOff && p["thinking"].(map[string]any)["type"] != "disabled" {
					t.Fatal("thinking off")
				}
				if level == "" {
					if _, ok := p["thinking"]; ok {
						t.Fatal("thinking unset")
					}
				}
			}
		})
	}
	b := &Backend{model: "claude", maxTokens: 1024, thinking: backend.Thinking{Level: backend.ThinkingLow, Set: true}}
	if _, _, err := b.wireParams(backend.Request{User: "user"}); err == nil {
		t.Fatal("small thinking max accepted")
	}
}
func TestFactory_InvalidThinkingLevel(t *testing.T) {
	_, err := factory(backend.Config{
		Options: map[string]any{
			"api_key":        "k",
			"model":          "claude-sonnet-4-20250514",
			"thinking_level": "nope",
		},
	})
	if err == nil {
		t.Fatal("want error for invalid thinking_level")
	}
}

func TestFrozenThinkingBudgetOverridesCurrentCalculation(t *testing.T) {
	for _, level := range []string{"high", "off", ""} {
		t.Run(level, func(t *testing.T) {
			opts := map[string]any{"api_key": "key", "model": "claude", "max_tokens": 8192, "thinking_budget_tokens": 1536}
			if level != "" {
				opts["thinking_level"] = level
			}
			created, err := factory(backend.Config{Options: opts})
			if err != nil {
				t.Fatal(err)
			}
			defer created.Close()
			params, _, err := created.(*Backend).wireParams(backend.Request{User: "hello"})
			if err != nil {
				t.Fatal(err)
			}
			if level == "high" {
				if params["thinking"].(map[string]any)["budget_tokens"] != int64(1536) {
					t.Fatalf("frozen budget lost: %+v", params["thinking"])
				}
			} else if params["thinking"] != nil && params["thinking"].(map[string]any)["type"] == "enabled" {
				t.Fatal("budget must not implicitly enable thinking")
			}
		})
	}
}

// TestResponseFromMessage_TruncatedSignal 验证截断（stop_reason=max_tokens）一等化：
// 非空文本/tool input → 正常返回 Response 且 Truncated=true（不再报错丢弃）；
// 空内容 → EmptyResponseError（不可重试，携带 stop reason）。
// thinking 是否开启不再影响截断行为（调参提示由 pipeline 层统一承担）。
func TestResponseFromMessage_TruncatedSignal(t *testing.T) {
	b := &Backend{name: "claude", model: "claude-sonnet-4", thinking: backend.Thinking{Level: backend.ThinkingHigh, Set: true}}

	t.Run("non_empty_text_returns_truncated", func(t *testing.T) {
		msg := &messageWire{
			Content:    []messageBlock{{Type: "text", Text: `{"items":[{"t":1}`}},
			StopReason: "max_tokens",
		}
		resp, err := b.responseFromMessage(msg, false)
		if err != nil {
			t.Fatalf("truncated non-empty text must not error, got: %v", err)
		}
		if !resp.Truncated {
			t.Fatal("want Truncated=true for stop_reason=max_tokens")
		}
		if resp.Text != `{"items":[{"t":1}` {
			t.Fatalf("Text = %q, want partial prefix", resp.Text)
		}
	})

	t.Run("non_empty_tool_use_input_returns_truncated", func(t *testing.T) {
		msg := &messageWire{
			Content: []messageBlock{{
				Type:  "tool_use",
				Name:  toolName,
				Input: json.RawMessage(`{"items":[]}`),
			}},
			StopReason: "max_tokens",
		}
		resp, err := b.responseFromMessage(msg, true)
		if err != nil {
			t.Fatalf("truncated non-empty tool input must not error, got: %v", err)
		}
		if !resp.Truncated {
			t.Fatal("want Truncated=true for stop_reason=max_tokens")
		}
		if resp.Text != `{"items":[]}` {
			t.Fatalf("Text = %q, want tool_use input JSON", resp.Text)
		}
	})

	t.Run("non_max_tokens_stop_keeps_truncated_false", func(t *testing.T) {
		msg := &messageWire{
			Content:    []messageBlock{{Type: "text", Text: "done"}},
			StopReason: "end_turn",
		}
		resp, err := b.responseFromMessage(msg, false)
		if err != nil {
			t.Fatalf("normal response must not error, got: %v", err)
		}
		if resp.Truncated {
			t.Fatal("want Truncated=false for stop_reason=end_turn")
		}
	})

	t.Run("empty_content_returns_empty_response_error", func(t *testing.T) {
		msg := &messageWire{
			Content:    []messageBlock{}, // 无 text/tool_use 块
			StopReason: "max_tokens",
		}
		resp, err := b.responseFromMessage(msg, false)
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
		if ere.FinishReason != string("max_tokens") {
			t.Fatalf("FinishReason = %q, want %q", ere.FinishReason, "max_tokens")
		}
		if backend.IsRetryable(err) {
			t.Fatal("empty truncated response must not be retryable")
		}
	})

	t.Run("thinking_enabled_no_longer_errors_on_truncation", func(t *testing.T) {
		// 旧契约：thinking 开启时截断报"shares max_tokens"错。新契约下该文案随
		// 报错分支一并移除，截断一律以 Truncated 标志返回。
		msg := &messageWire{
			Content:    []messageBlock{{Type: "text", Text: "partial"}},
			StopReason: "max_tokens",
		}
		resp, err := b.responseFromMessage(msg, false)
		if err != nil {
			t.Fatalf("thinking-enabled truncation must not error, got: %v", err)
		}
		if !resp.Truncated || resp.Text != "partial" {
			t.Fatalf("want Truncated=true with partial text, got Truncated=%v Text=%q", resp.Truncated, resp.Text)
		}
	})
}

// TestExtractResponseText_EmptyContent 验证 anthropic 后端的空内容路径返回
// EmptyResponseError（携带 stop_reason/input_tokens 诊断信息），且被
// backend.IsRetryable 归为不可重试。refusal stop_reason 是内容过滤的典型信号。
func TestExtractResponseText_EmptyContent(t *testing.T) {
	b := &Backend{name: "claude", model: "claude-sonnet-4"}
	msg := &messageWire{
		Content:    []messageBlock{}, // 无 text/tool_use 块
		StopReason: "refusal",
	}

	t.Run("no_text_content", func(t *testing.T) {
		_, err := extractResponseText(msg, false, b, msg.StopReason, 512)
		var ere *backend.EmptyResponseError
		if !errors.As(err, &ere) {
			t.Fatalf("want *backend.EmptyResponseError, got %T: %v", err, err)
		}
		if ere.FinishReason != string("refusal") {
			t.Fatalf("FinishReason = %q, want %q", ere.FinishReason, "refusal")
		}
		if ere.PromptTokens != 512 {
			t.Fatalf("PromptTokens = %d, want 512", ere.PromptTokens)
		}
		if backend.IsRetryable(err) {
			t.Fatal("empty response error must not be retryable")
		}
	})

	t.Run("empty_tool_use_input", func(t *testing.T) {
		msgWithTool := &messageWire{
			Content: []messageBlock{{
				Type:  "tool_use",
				Name:  toolName,
				Input: nil, // 空 input
			}},
			StopReason: "tool_use",
		}
		_, err := extractResponseText(msgWithTool, true, b, msgWithTool.StopReason, 256)
		var ere *backend.EmptyResponseError
		if !errors.As(err, &ere) {
			t.Fatalf("want *backend.EmptyResponseError, got %T: %v", err, err)
		}
		if ere.FinishReason != string("tool_use") {
			t.Fatalf("FinishReason = %q, want %q", ere.FinishReason, "tool_use")
		}
		if backend.IsRetryable(err) {
			t.Fatal("empty tool_use input must not be retryable")
		}
	})
}
