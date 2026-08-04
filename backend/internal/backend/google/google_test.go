package google

import (
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
