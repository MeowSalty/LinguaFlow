package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
)

func TestValidateExecutionRounds_Adjudicate(t *testing.T) {
	t.Run("valid adjudicate only", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "adjudicate",
			BackendID: 1,
			Adjudicate: &schema.AdjudicateRoundConfig{
				BatchSize:   10,
				Concurrency: 1,
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("adjudicate before translate allowed", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{
			{
				Mode:      "adjudicate",
				BackendID: 1,
				Adjudicate: &schema.AdjudicateRoundConfig{
					BatchSize:       5,
					Concurrency:     1,
					AdjudicateCodes: []string{"source_residual", "length_ratio"},
				},
			},
			{
				Mode:      "translate",
				BackendID: 1,
				Translate: &schema.TranslateRoundConfig{
					PromptTemplateID: -1,
					BatchSize:        10,
					Concurrency:      1,
					FallbackShrink:   1.0,
				},
			},
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("nil adjudicate config", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "adjudicate",
			BackendID: 1,
		}})
		if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("both batch limits zero", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "adjudicate",
			BackendID: 1,
			Adjudicate: &schema.AdjudicateRoundConfig{
				Concurrency: 1,
			},
		}})
		if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("punctuation_surplus accepted", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "adjudicate",
			BackendID: 1,
			Adjudicate: &schema.AdjudicateRoundConfig{
				BatchSize:       10,
				Concurrency:     1,
				AdjudicateCodes: []string{"punctuation_surplus"},
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("mixed adjudicable codes accepted", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "adjudicate",
			BackendID: 1,
			Adjudicate: &schema.AdjudicateRoundConfig{
				BatchSize:       10,
				Concurrency:     1,
				AdjudicateCodes: []string{"punctuation_surplus", "source_residual"},
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("invalid adjudicate code", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "adjudicate",
			BackendID: 1,
			Adjudicate: &schema.AdjudicateRoundConfig{
				BatchSize:       10,
				Concurrency:     1,
				AdjudicateCodes: []string{"untranslated"},
			},
		}})
		if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
		const want = "allowed: source_residual, length_ratio, punctuation_surplus"
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err=%v want substring %q", err, want)
		}
	})
}
