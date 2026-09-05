package anthropic

import (
	"encoding/json"
	"errors"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"

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
		{backend.ThinkingLow, 8192, 2048, false},
		{backend.ThinkingMedium, 8192, 4096, false},
		{backend.ThinkingHigh, 8192, 6144, false}, // 0.75*8192，输出只剩 2048，由用户对 max_tokens 负责
		{backend.ThinkingHigh, 64000, 48000, false},
		{backend.ThinkingLow, 2000, 1024, false}, // 0.25*2000=500 → clamp 到下限 1024
		{backend.ThinkingHigh, 1500, 1125, false},
		// max_tokens 过小：报错（思考预算下限）
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

func TestBuildParams_ThinkingSkipsSampling(t *testing.T) {
	temp := 0.2
	topP := 0.9
	req := backend.Request{
		System:      "sys",
		User:        "user",
		Model:       "claude-sonnet-4-20250514",
		MaxTokens:   8192,
		Temperature: &temp,
		TopP:        &topP,
	}

	t.Run("off keeps temperature/top_p", func(t *testing.T) {
		b := &Backend{
			model:       "claude-sonnet-4-20250514",
			maxTokens:   8192,
			temperature: &temp,
			topP:        &topP,
			thinking:    backend.ThinkingOff,
		}
		params, _, err := b.buildParams(req)
		if err != nil {
			t.Fatal(err)
		}
		if params.Thinking.OfEnabled != nil {
			t.Fatal("want Thinking omitted when off")
		}
		if !params.Temperature.Valid() || params.Temperature.Value != temp {
			t.Fatalf("Temperature: valid=%v value=%v want %v", params.Temperature.Valid(), params.Temperature.Value, temp)
		}
		if !params.TopP.Valid() || params.TopP.Value != topP {
			t.Fatalf("TopP: valid=%v value=%v want %v", params.TopP.Valid(), params.TopP.Value, topP)
		}
	})

	t.Run("enabled sets budget and drops sampling", func(t *testing.T) {
		b := &Backend{
			model:       "claude-sonnet-4-20250514",
			maxTokens:   8192,
			temperature: &temp,
			topP:        &topP,
			thinking:    backend.ThinkingMedium,
		}
		params, _, err := b.buildParams(req)
		if err != nil {
			t.Fatal(err)
		}
		if params.Thinking.OfEnabled == nil {
			t.Fatal("want Thinking enabled")
		}
		if params.Thinking.OfEnabled.BudgetTokens != 4096 {
			t.Fatalf("BudgetTokens: got %d want 4096", params.Thinking.OfEnabled.BudgetTokens)
		}
		if params.Temperature.Valid() {
			t.Fatalf("Temperature should be unset when thinking enabled, got %v", params.Temperature.Value)
		}
		if params.TopP.Valid() {
			t.Fatalf("TopP should be unset when thinking enabled, got %v", params.TopP.Value)
		}
	})

	t.Run("enabled with small max_tokens errors", func(t *testing.T) {
		b := &Backend{
			model:     "claude-sonnet-4-20250514",
			maxTokens: 1024,
			thinking:  backend.ThinkingLow,
		}
		small := backend.Request{System: "s", User: "u", MaxTokens: 1024}
		_, _, err := b.buildParams(small)
		if err == nil {
			t.Fatal("want error when max_tokens <= 1024")
		}
	})
}

func TestFactory_ParseThinkingLevel(t *testing.T) {
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

// TestResponseFromMessage_TruncatedSignal 验证截断（stop_reason=max_tokens）一等化：
// 非空文本/tool input → 正常返回 Response 且 Truncated=true（不再报错丢弃）；
// 空内容 → EmptyResponseError（不可重试，携带 stop reason）。
// thinking 是否开启不再影响截断行为（调参提示由 pipeline 层统一承担）。
func TestResponseFromMessage_TruncatedSignal(t *testing.T) {
	b := &Backend{name: "claude", model: "claude-sonnet-4", thinking: backend.ThinkingHigh}

	t.Run("non_empty_text_returns_truncated", func(t *testing.T) {
		msg := &sdk.Message{
			Content:    []sdk.ContentBlockUnion{{Type: "text", Text: `{"items":[{"t":1}`}},
			StopReason: sdk.StopReasonMaxTokens,
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
		msg := &sdk.Message{
			Content: []sdk.ContentBlockUnion{{
				Type:  "tool_use",
				Name:  toolName,
				Input: json.RawMessage(`{"items":[]}`),
			}},
			StopReason: sdk.StopReasonMaxTokens,
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
		msg := &sdk.Message{
			Content:    []sdk.ContentBlockUnion{{Type: "text", Text: "done"}},
			StopReason: sdk.StopReasonEndTurn,
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
		msg := &sdk.Message{
			Content:    []sdk.ContentBlockUnion{}, // 无 text/tool_use 块
			StopReason: sdk.StopReasonMaxTokens,
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
		if ere.FinishReason != string(sdk.StopReasonMaxTokens) {
			t.Fatalf("FinishReason = %q, want %q", ere.FinishReason, sdk.StopReasonMaxTokens)
		}
		if backend.IsRetryable(err) {
			t.Fatal("empty truncated response must not be retryable")
		}
	})

	t.Run("thinking_enabled_no_longer_errors_on_truncation", func(t *testing.T) {
		// 旧契约：thinking 开启时截断报"shares max_tokens"错。新契约下该文案随
		// 报错分支一并移除，截断一律以 Truncated 标志返回。
		msg := &sdk.Message{
			Content:    []sdk.ContentBlockUnion{{Type: "text", Text: "partial"}},
			StopReason: sdk.StopReasonMaxTokens,
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
	msg := &sdk.Message{
		Content:    []sdk.ContentBlockUnion{}, // 无 text/tool_use 块
		StopReason: sdk.StopReasonRefusal,
	}

	t.Run("no_text_content", func(t *testing.T) {
		_, err := extractResponseText(msg, false, b, msg.StopReason, 512)
		var ere *backend.EmptyResponseError
		if !errors.As(err, &ere) {
			t.Fatalf("want *backend.EmptyResponseError, got %T: %v", err, err)
		}
		if ere.FinishReason != string(sdk.StopReasonRefusal) {
			t.Fatalf("FinishReason = %q, want %q", ere.FinishReason, sdk.StopReasonRefusal)
		}
		if ere.PromptTokens != 512 {
			t.Fatalf("PromptTokens = %d, want 512", ere.PromptTokens)
		}
		if backend.IsRetryable(err) {
			t.Fatal("empty response error must not be retryable")
		}
	})

	t.Run("empty_tool_use_input", func(t *testing.T) {
		msgWithTool := &sdk.Message{
			Content: []sdk.ContentBlockUnion{{
				Type:  "tool_use",
				Name:  toolName,
				Input: nil, // 空 input
			}},
			StopReason: sdk.StopReasonToolUse,
		}
		_, err := extractResponseText(msgWithTool, true, b, msgWithTool.StopReason, 256)
		var ere *backend.EmptyResponseError
		if !errors.As(err, &ere) {
			t.Fatalf("want *backend.EmptyResponseError, got %T: %v", err, err)
		}
		if ere.FinishReason != string(sdk.StopReasonToolUse) {
			t.Fatalf("FinishReason = %q, want %q", ere.FinishReason, sdk.StopReasonToolUse)
		}
		if backend.IsRetryable(err) {
			t.Fatal("empty tool_use input must not be retryable")
		}
	})
}
