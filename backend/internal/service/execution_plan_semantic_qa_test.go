package service

import (
	"errors"
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
}
