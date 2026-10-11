package api

import "testing"

func TestRubyConcurrencyAPIPresenceRoundTrip(t *testing.T) {
	for _, value := range []*int{nil, new(0), new(-1), new(3)} {
		input := &ExecutionPlanRubyRetryConfig{Enabled: true, Concurrency: value}
		config := parseRubyRetryConfig(input)
		output := toRubyRetryConfigAPI(config)
		if (output.Concurrency == nil) != (value == nil) || (value != nil && *output.Concurrency != *value) {
			t.Fatalf("value presence lost: %+v -> %+v", input, output)
		}
		if value != nil && (output.Concurrency == value || config.Concurrency == value) {
			t.Fatal("mapping aliased input pointer")
		}
	}
}
