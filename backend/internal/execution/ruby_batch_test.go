package execution

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestResolveRubyRetryBatch(t *testing.T) {
	for _, tt := range []struct {
		name               string
		batch, words, wait *int
		want               RubyRetryBatchConfig
		bad                bool
	}{
		{"omitted", nil, nil, nil, RubyRetryBatchConfig{1, 0, 25}, false},
		{"segments", new(4), new(0), new(0), RubyRetryBatchConfig{4, 0, 0}, false},
		{"words", new(0), new(100), nil, RubyRetryBatchConfig{0, 100, 25}, false},
		{"both", new(8), new(200), new(50), RubyRetryBatchConfig{8, 200, 50}, false},
		{"no_limit", new(0), nil, nil, RubyRetryBatchConfig{}, true},
		{"negative_batch", new(-1), nil, nil, RubyRetryBatchConfig{}, true},
		{"negative_words", nil, new(-1), nil, RubyRetryBatchConfig{}, true},
		{"negative_wait", nil, nil, new(-1), RubyRetryBatchConfig{}, true},
		{"overflow_wait", nil, nil, new(int(math.MaxInt64/int64(time.Millisecond) + 1)), RubyRetryBatchConfig{}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveRubyRetryBatch(tt.batch, tt.words, tt.wait)
			if (err != nil) != tt.bad || got != tt.want {
				t.Fatalf("config=%+v err=%v, want=%+v invalid=%t", got, err, tt.want, tt.bad)
			}
		})
	}
}

func TestRubyBatchSnapshotPresenceAndCompatibility(t *testing.T) {
	in := validExecutionInput()
	in.RubyRetry = &ExecutionPlanRubyRetrySnapshot{Enabled: true, Backend: in.Rounds[0].Backend, MaxAttempts: 2, BatchSize: new(0), MaxWordsPerBatch: new(100), BatchWaitMS: new(0)}
	frozen, err := Resolve(in)
	if err != nil {
		t.Fatal(err)
	}
	want := RubyRetryBatchConfig{0, 100, 0}
	if got := EffectiveRubyRetryBatch(frozen); got != want {
		t.Fatalf("explicit zero lost: %+v", got)
	}
	in.RubyRetry.MaxWordsPerBatch = new(999)
	for _, version := range []int{1, 2, 3} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			raw, err := json.Marshal(frozen)
			if err != nil {
				t.Fatal(err)
			}
			var restored JobExecutionSnapshot
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			restored.SchemaVersion, restored.DefaultsVersion = version, version
			if version < 3 {
				restored.RubyRetry.BatchSize = nil
				restored.RubyRetry.MaxWordsPerBatch = nil
				restored.RubyRetry.BatchWaitMS = nil
				restored.RubyBatchProtocolVersion = 0
				restored.RubyTemplates.BatchJSON, restored.RubyTemplates.BatchText = "", ""
			}
			if err := ValidateSpec(&restored); err != nil {
				t.Fatal(err)
			}
			got := EffectiveRubyRetryBatch(&restored)
			if version == 3 {
				if got != want || EffectiveRubyBatchProtocolVersion(&restored) != 1 {
					t.Fatalf("v3 settings lost: %+v", got)
				}
			} else if got != (RubyRetryBatchConfig{BatchSize: 1}) || EffectiveRubyBatchProtocolVersion(&restored) != 0 {
				t.Fatalf("legacy snapshot acquired batching: %+v", got)
			}
		})
	}
	for _, field := range []string{"batch_size", "max_words_per_batch", "batch_wait_ms"} {
		t.Run("missing_"+field, func(t *testing.T) {
			raw, err := json.Marshal(frozen)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatal(err)
			}
			delete(object["ruby_retry"].(map[string]any), field)
			raw, err = json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			var restored JobExecutionSnapshot
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			before := restored
			if err := ValidateSpec(&restored); err == nil {
				t.Fatal("restore silently supplied missing batch configuration")
			}
			if !reflect.DeepEqual(before, restored) {
				t.Fatal("validation changed frozen snapshot")
			}
		})
	}
	for _, mutate := range []func(*JobExecutionSnapshot){
		func(s *JobExecutionSnapshot) { s.RubyBatchProtocolVersion = 0 },
		func(s *JobExecutionSnapshot) { s.RubyBatchProtocolVersion = 99 },
		func(s *JobExecutionSnapshot) { s.RubyTemplates.BatchJSON = "" },
		func(s *JobExecutionSnapshot) { s.RubyTemplates.BatchText = "" },
	} {
		copy := *frozen
		mutate(&copy)
		if err := ValidateSpec(&copy); err == nil {
			t.Fatal("accepted incomplete or unsupported v3 batch contract")
		}
	}
}

func TestRubyBatchConfigurationSurvivesRoundSelection(t *testing.T) {
	in := validExecutionInput()
	in.RubyRetry = &ExecutionPlanRubyRetrySnapshot{Enabled: true, Backend: in.Rounds[0].Backend, MaxAttempts: 3, BatchSize: new(4), MaxWordsPerBatch: new(70), BatchWaitMS: new(0)}
	frozen, err := Resolve(in)
	if err != nil {
		t.Fatal(err)
	}
	frozen.Rounds = []JobRoundSnapshot{{Mode: "revise", Backend: frozen.Rounds[0].Backend, Revise: &JobReviseRoundSnapshot{TemplateContent: "revision", BatchSize: 1, Concurrency: 1}}}
	preview, err := Resolve(*frozen)
	if err != nil {
		t.Fatal(err)
	}
	if EffectiveRubyRetryBatch(preview) != EffectiveRubyRetryBatch(frozen) || preview.RubyTemplates != frozen.RubyTemplates || preview.RubyBatchProtocolVersion != 1 {
		t.Fatal("preview round selection changed batch contract")
	}
}
