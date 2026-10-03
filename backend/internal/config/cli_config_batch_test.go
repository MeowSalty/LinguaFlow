package config

import (
	"strings"
	"testing"
)

func TestCLIExtractBatchLimitsPreservePresence(t *testing.T) {
	for _, tc := range []struct {
		name, settings string
		batch, words   int
	}{
		{"omitted", "{}", 20, 0},
		{"send_all", "\n        batch_size: 0\n        max_words_per_batch: 0", 0, 0},
		{"word_limit", "\n        batch_size: 0\n        max_words_per_batch: 100", 0, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := minimalTranslation + "    - mode: extract\n      backend: test\n      extract: " + tc.settings + "\n"
			cfg, err := ResolveCLIConfig(translationInput(t, doc))
			if err != nil {
				t.Fatal(err)
			}
			extract := cfg.Execution.Rounds[1].Extract
			if extract.BatchSize != tc.batch || extract.MaxWordsPerBatch != tc.words {
				t.Fatalf("batch limits = (%d, %d), want (%d, %d)", extract.BatchSize, extract.MaxWordsPerBatch, tc.batch, tc.words)
			}
		})
	}
}

func TestCLIBatchLimitsRejectInvalidConfigurations(t *testing.T) {
	for _, tc := range []struct {
		name, mode, settings, want string
	}{
		{"translate_send_all", "translate", "batch_size: 0\n        max_words_per_batch: 0", "batch_size and max_words_per_batch"},
		{"revise_send_all", "revise", "batch_size: 0\n        max_words_per_batch: 0", "batch_size and max_words_per_batch"},
		{"extract_negative_batch", "extract", "batch_size: -1", "batch_size"},
		{"extract_negative_words", "extract", "max_words_per_batch: -1", "max_words_per_batch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := minimalTranslation + "    - mode: " + tc.mode + "\n      backend: test\n      " + tc.mode + ":\n        " + tc.settings + "\n"
			_, err := ResolveCLIConfig(translationInput(t, doc))
			if err == nil || !strings.Contains(err.Error(), "execution.rounds[1]."+tc.mode+"."+tc.want) {
				t.Fatalf("error = %v, want mode and field context", err)
			}
		})
	}
}
