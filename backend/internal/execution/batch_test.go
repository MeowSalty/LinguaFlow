package execution

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestValidateBatchLimits(t *testing.T) {
	for _, mode := range []string{"translate", "extract", "adjudicate", "semantic_qa", "revise"} {
		for _, tc := range []struct {
			name         string
			batch, words int
			want         string
		}{
			{"send_all", 0, 0, ""},
			{"segments", 10, 0, ""},
			{"words", 0, 100, ""},
			{"both_limits", 10, 100, ""},
			{"negative_segments", -1, 100, "batch_size"},
			{"negative_words", 10, -1, "max_words_per_batch"},
			{"negative_segments_without_words", -1, 0, "batch_size"},
			{"negative_words_without_segments", 0, -1, "max_words_per_batch"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				want := tc.want
				if tc.batch == 0 && tc.words == 0 && mode != "extract" {
					want = "cannot both be 0"
				}
				err := ValidateBatchLimits(mode, tc.batch, tc.words)
				if want == "" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want %q", err, want)
				}
			})
		}
	}
	for _, mode := range []string{"correct", "unknown", ""} {
		if err := ValidateBatchLimits(mode, 1, 0); err == nil {
			t.Fatalf("non-LLM mode %q accepted batch limits", mode)
		}
	}
}

func extractSendAllInput() JobExecutionSnapshot {
	in := validExecutionInput()
	in.Rounds[0] = JobRoundSnapshot{
		Mode: "extract", Backend: in.Rounds[0].Backend,
		Extract: &JobExtractRoundSnapshot{TemplateContent: "saved extract prompt", Concurrency: 1},
	}
	return in
}

func TestExtractSendAllSnapshotRoundTrip(t *testing.T) {
	in := extractSendAllInput()
	spec, err := Resolve(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var restored JobExecutionSnapshot
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSpec(&restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Rounds[0].Extract, in.Rounds[0].Extract) || !restored.GlossaryEnabled {
		t.Fatal("extract send-all configuration changed while freezing or restoring")
	}
	if !reflect.DeepEqual(restored.Sources, spec.Sources) || restored.Sources[len(restored.Sources)-1].Digest != source("round", "0", restored.Rounds[0]).Digest {
		t.Fatal("round provenance does not describe the preserved send-all configuration")
	}
}

func TestExtractSendAllStillRequiresCompleteParameters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*JobExtractRoundSnapshot)
		want   string
	}{
		{"empty_template", func(r *JobExtractRoundSnapshot) { r.TemplateContent = "" }, "template"},
		{"blank_template", func(r *JobExtractRoundSnapshot) { r.TemplateContent = " \n\t" }, "template"},
		{"zero_concurrency", func(r *JobExtractRoundSnapshot) { r.Concurrency = 0 }, "concurrency"},
		{"negative_concurrency", func(r *JobExtractRoundSnapshot) { r.Concurrency = -1 }, "concurrency"},
		{"negative_attempts", func(r *JobExtractRoundSnapshot) { r.Retry.MaxAttempts = -1 }, "retry"},
		{"negative_backoff", func(r *JobExtractRoundSnapshot) { r.Retry.BackoffMs = -1 }, "retry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := Resolve(extractSendAllInput())
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(spec.Rounds[0].Extract)
			_, resolveErr := Resolve(*spec)
			for _, err := range []error{resolveErr, ValidateSpec(spec)} {
				if err == nil || !strings.Contains(err.Error(), "round[0].extract") || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error = %v, want extract round and %q", err, tc.want)
				}
			}
		})
	}
}

func TestOtherSnapshotModesStillRequireBatchLimits(t *testing.T) {
	for _, mode := range []string{"translate", "adjudicate", "semantic_qa", "revise"} {
		t.Run(mode, func(t *testing.T) {
			in := validExecutionInput()
			if mode != "translate" {
				in = executionSelectionInput(mode)
			}
			spec, err := Resolve(in)
			if err != nil {
				t.Fatal(err)
			}
			round := &spec.Rounds[0]
			switch mode {
			case "translate":
				round.Translate.BatchSize, round.Translate.MaxWordsPerBatch = 0, 0
			case "adjudicate":
				round.Adjudicate.BatchSize, round.Adjudicate.MaxWordsPerBatch = 0, 0
			case "semantic_qa":
				round.SemanticQA.BatchSize, round.SemanticQA.MaxWordsPerBatch = 0, 0
			case "revise":
				round.Revise.BatchSize, round.Revise.MaxWordsPerBatch = 0, 0
			}
			_, resolveErr := Resolve(*spec)
			for _, err := range []error{resolveErr, ValidateSpec(spec)} {
				if err == nil || !strings.Contains(err.Error(), "round[0]."+mode+".batch_size") {
					t.Fatalf("error = %v, want batch limits error", err)
				}
			}
		})
	}
}
