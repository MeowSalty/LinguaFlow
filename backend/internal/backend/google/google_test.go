package google

import (
	"errors"
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
