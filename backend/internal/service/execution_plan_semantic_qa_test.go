package service

import (
	"errors"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
)

func TestValidateExecutionRounds_SemanticQA(t *testing.T) {
	t.Run("valid semantic_qa only", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:   10,
				Concurrency: 1,
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("semantic_qa before translate allowed", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{
			{
				Mode:      "semantic_qa",
				BackendID: 1,
				SemanticQA: &schema.SemanticQARoundConfig{
					BatchSize:   5,
					Concurrency: 1,
				},
			},
			{
				Mode:      "translate",
				BackendID: 1,
				Translate: &schema.TranslateRoundConfig{
					PromptTemplateID: -1,
					ProfileID:        -1,
					BatchSize:        10,
					Concurrency:      1,
				},
			},
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("nil semantic_qa config", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
		}})
		if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("both batch limits zero", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				Concurrency: 1,
			},
		}})
		if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("invalid segment_scope", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "invalid",
			},
		}})
		if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("invalid issue_codes", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "with_issue_codes",
				IssueCodes:   []string{"foo"},
			},
		}})
		if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("empty segment_scope allowed", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:   10,
				Concurrency: 1,
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("with_issue_codes and valid codes", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "with_issue_codes",
				IssueCodes:   []string{"source_residual", "calque"},
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("with_issue_codes and new semantic codes", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "with_issue_codes",
				IssueCodes:   []string{"mistranslation", "omission", "addition", "grammar", "register"},
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("with_issue_codes mixed rule and new codes", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "with_issue_codes",
				IssueCodes:   []string{"untranslated", "mistranslation", "calque", "register"},
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("with_issue_codes accepts deterministic checker codes", func(t *testing.T) {
		// 全部确定性 checker code（含新纳入的 11 个）均可作筛选键
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "with_issue_codes",
				IssueCodes:   []string{"punctuation_pairing", "number_mismatch", "forbidden_term", "xml_tag_mismatch"},
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("with_issue_codes requires codes", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "with_issue_codes",
			},
		}})
		if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})
}

func TestMaterializeSemanticQASnapshotDefaults(t *testing.T) {
	t.Run("empty scope materializes to all", func(t *testing.T) {
		s := &schema.SemanticQARoundConfig{
			BatchSize:   5,
			Concurrency: 1,
			IssueCodes:  []string{"source_residual"},
		}
		snap := snapshotSemanticQARound(s)
		if snap.SegmentScope != "all" {
			t.Fatalf("SegmentScope=%q want all", snap.SegmentScope)
		}
		if !reflect.DeepEqual(snap.IssueCodes, []string{"source_residual"}) {
			t.Fatalf("IssueCodes=%v", snap.IssueCodes)
		}
		// deep copy: mutating source must not affect snapshot
		s.IssueCodes[0] = "calque"
		if snap.IssueCodes[0] != "source_residual" {
			t.Fatalf("snapshot IssueCodes mutated via shared slice: %v", snap.IssueCodes)
		}
	})

	t.Run("with_issue_codes preserved", func(t *testing.T) {
		s := &schema.SemanticQARoundConfig{
			BatchSize:    5,
			Concurrency:  1,
			SegmentScope: "with_issue_codes",
			IssueCodes:   []string{"source_residual", "length_ratio"},
		}
		snap := snapshotSemanticQARound(s)
		if snap.SegmentScope != "with_issue_codes" {
			t.Fatalf("SegmentScope=%q", snap.SegmentScope)
		}
		if !reflect.DeepEqual(snap.IssueCodes, []string{"source_residual", "length_ratio"}) {
			t.Fatalf("IssueCodes=%v", snap.IssueCodes)
		}
	})
}
