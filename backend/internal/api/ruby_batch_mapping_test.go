package api

import "testing"

func TestRubyBatchAPIPresenceRoundTrip(t *testing.T) {
	for _, value := range []*int{nil, new(0), new(-1), new(4)} {
		input := &ExecutionPlanRubyRetryConfig{Enabled: true, BatchSize: value, MaxWordsPerBatch: value, BatchWaitMs: value}
		config := parseRubyRetryConfig(input)
		output := toRubyRetryConfigAPI(config)
		for _, got := range []*int{config.BatchSize, config.MaxWordsPerBatch, config.BatchWaitMS, output.BatchSize, output.MaxWordsPerBatch, output.BatchWaitMs} {
			if (got == nil) != (value == nil) || (value != nil && *got != *value) {
				t.Fatalf("value presence lost: input=%+v output=%+v", input, output)
			}
			if value != nil && got == value {
				t.Fatal("mapping aliased input")
			}
		}
	}
}
