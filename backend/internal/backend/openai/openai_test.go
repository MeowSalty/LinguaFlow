package openai

import (
	"testing"

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
