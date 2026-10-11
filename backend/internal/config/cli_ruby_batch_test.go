package config

import (
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

func TestCLIRubyBatchPresenceAndValidation(t *testing.T) {
	for _, tt := range []struct {
		name, fields string
		want         execution.RubyRetryBatchConfig
		bad          bool
	}{
		{"omitted", "", execution.RubyRetryBatchConfig{BatchSize: 1, BatchWaitMS: 25}, false},
		{"words", "    batch_size: 0\n    max_words_per_batch: 200\n    batch_wait_ms: 0\n", execution.RubyRetryBatchConfig{MaxWordsPerBatch: 200}, false},
		{"segments", "    batch_size: 4\n", execution.RubyRetryBatchConfig{BatchSize: 4, BatchWaitMS: 25}, false},
		{"no_limit", "    batch_size: 0\n", execution.RubyRetryBatchConfig{}, true},
		{"negative", "    max_words_per_batch: -1\n", execution.RubyRetryBatchConfig{}, true},
		{"negative_wait", "    batch_wait_ms: -1\n", execution.RubyRetryBatchConfig{}, true},
		{"overflow_wait", "    batch_wait_ms: 9223372036855\n", execution.RubyRetryBatchConfig{}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			document := strings.Replace(minimalTranslation, "execution:\n", "execution:\n  ruby_retry:\n    enabled: true\n    backend: test\n"+tt.fields, 1)
			cfg, err := ResolveCLIConfig(translationInput(t, document))
			if (err != nil) != tt.bad {
				t.Fatalf("err=%v", err)
			}
			if tt.bad {
				return
			}
			ruby := cfg.Execution.RubyRetry
			got, err := execution.ResolveRubyRetryBatch(ruby.BatchSize, ruby.MaxWordsPerBatch, ruby.BatchWaitMS)
			if err != nil || got != tt.want {
				t.Fatalf("batch=%+v err=%v want=%+v", got, err, tt.want)
			}
			if tt.name == "omitted" && (ruby.BatchSize != nil || ruby.MaxWordsPerBatch != nil || ruby.BatchWaitMS != nil) {
				t.Fatal("CLI decoding lost omitted-field presence")
			}
			if tt.name == "words" && (ruby.BatchSize == nil || ruby.BatchWaitMS == nil || *ruby.BatchSize != 0 || *ruby.BatchWaitMS != 0) {
				t.Fatal("CLI decoding lost explicit zero")
			}
		})
	}
}
