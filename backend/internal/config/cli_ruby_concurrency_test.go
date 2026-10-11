package config

import (
	"strings"
	"testing"
)

func TestCLIRubyConcurrencyPresence(t *testing.T) {
	for _, tt := range []struct {
		name, field string
		want        *int
		bad         bool
	}{
		{"omitted", "", nil, false}, {"one", "    concurrency: 1\n", new(1), false}, {"three", "    concurrency: 3\n", new(3), false}, {"zero", "    concurrency: 0\n", nil, true}, {"negative", "    concurrency: -2\n", nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			document := strings.Replace(minimalTranslation, "execution:\n", "execution:\n  ruby_retry:\n    enabled: true\n    backend: test\n"+tt.field, 1)
			cfg, err := ResolveCLIConfig(translationInput(t, document))
			if (err != nil) != tt.bad {
				t.Fatalf("err=%v", err)
			}
			if tt.bad {
				return
			}
			got := cfg.Execution.RubyRetry.Concurrency
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Fatalf("presence lost: %+v", got)
			}
		})
	}
}
