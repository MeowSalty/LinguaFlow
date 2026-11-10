package v013

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// Historical importer assertions inspect the persisted historical value, not
// the current executable snapshot reader, whose version refusal is intentional.
func readFrozenExecutionTest(row *ent.Job) (*execution.JobExecutionSnapshot, error) {
	raw, err := json.Marshal(row.ExecutionConfig)
	if err != nil {
		return nil, err
	}
	var spec execution.JobExecutionSnapshot
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, err
	}
	if err := validateFrozenExecution(&spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

func historicalTargetTestSpec(t *testing.T) *execution.JobExecutionSnapshot {
	t.Helper()
	options, err := freezeBackendOptions("openai", map[string]any{"model": "saved-model"})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := freezeExecution(execution.JobExecutionSnapshot{
		SourceLang: "ja", TargetLang: "en",
		Rounds: []execution.JobRoundSnapshot{{
			Mode:    "translate",
			Backend: execution.BackendSnapshot{ID: 1, Type: "openai", Scope: "user", Options: options, Credential: credential.Binding{ID: 1, Version: 1}},
			Translate: &execution.JobTranslateRoundSnapshot{
				Prompt: execution.PromptSnapshot{Content: "saved prompt"}, BatchSize: 1, Concurrency: 1,
				FallbackShrink: 0.5, SegmentFilter: &execution.SegmentFilterSnapshot{StatusFilter: "all"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestFrozenTargetIndependentOfCurrentExecutionFormat(t *testing.T) {
	spec := historicalTargetTestSpec(t)
	if spec.SchemaVersion != 1 || spec.DefaultsVersion != 1 || spec.ResponseFormatVersion != 0 {
		t.Fatal("historical target acquired current versions")
	}
	if err := validateFrozenExecution(spec); err != nil {
		t.Fatal(err)
	}
	if err := execution.ValidateSpec(spec); err == nil {
		t.Fatal("historical offline conversion enabled current execution")
	}
	raw, err := marshalFrozenExecution(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range [][]byte{[]byte(`"response_format_version"`), []byte(`"ruby_protocol_version"`), []byte(`"ruby_validator_version"`), []byte(`"ruby_batch_protocol_version"`)} {
		if bytes.Contains(raw, field) {
			t.Fatalf("current field entered historical target: %s", field)
		}
	}
	var encoded map[string]any
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	if encoded["schema_version"] != float64(1) || encoded["defaults_version"] != float64(1) {
		t.Fatal("encoded historical target versions changed")
	}
	if _, err := service.GetSnapshot(&ent.Job{ExecutionConfig: encoded}); err == nil {
		t.Fatal("current snapshot reader accepted the historical migration target")
	}
}

func TestFrozenTargetRetainsDomainValidation(t *testing.T) {
	for name, mutate := range map[string]func(*execution.JobExecutionSnapshot){
		"schema":        func(s *execution.JobExecutionSnapshot) { s.SchemaVersion = 4 },
		"response":      func(s *execution.JobExecutionSnapshot) { s.ResponseFormatVersion = 1 },
		"languages":     func(s *execution.JobExecutionSnapshot) { s.SourceLang = "" },
		"context":       func(s *execution.JobExecutionSnapshot) { s.Strategy.Context.Before = -1 },
		"checks":        func(s *execution.JobExecutionSnapshot) { s.Strategy.QA.Checks = []string{"unsupported-check"} },
		"ruby_kind":     func(s *execution.JobExecutionSnapshot) { s.Strategy.Ruby.PreserveKinds = []string{"unsupported-kind"} },
		"protection":    func(s *execution.JobExecutionSnapshot) { s.Strategy.Protect.Rules = []string{"unsupported-rule"} },
		"missing_round": func(s *execution.JobExecutionSnapshot) { s.Rounds[0].Translate = nil },
		"batch":         func(s *execution.JobExecutionSnapshot) { s.Rounds[0].Translate.BatchSize = 0 },
		"concurrency":   func(s *execution.JobExecutionSnapshot) { s.Rounds[0].Translate.Concurrency = 0 },
		"template":      func(s *execution.JobExecutionSnapshot) { s.Rounds[0].Translate.Prompt.Content = " " },
		"retry":         func(s *execution.JobExecutionSnapshot) { s.Rounds[0].Translate.Retry.BackoffMs = -1 },
		"shrink":        func(s *execution.JobExecutionSnapshot) { s.Rounds[0].Translate.FallbackShrink = 0 },
		"filter":        func(s *execution.JobExecutionSnapshot) { s.Rounds[0].Translate.SegmentFilter = nil },
		"credential":    func(s *execution.JobExecutionSnapshot) { s.Rounds[0].Backend.Credential = credential.Binding{} },
		"option":        func(s *execution.JobExecutionSnapshot) { delete(s.Rounds[0].Backend.Options, "timeout") },
		"endpoint": func(s *execution.JobExecutionSnapshot) {
			s.Rounds[0].Backend.Options["base_url"] = "https://api.openai.com/v1"
		},
		"stream":      func(s *execution.JobExecutionSnapshot) { s.Rounds[0].Backend.Options["stream"] = "false" },
		"temperature": func(s *execution.JobExecutionSnapshot) { s.Rounds[0].Backend.Options["temperature"] = 3.0 },
	} {
		t.Run(name, func(t *testing.T) {
			spec := historicalTargetTestSpec(t)
			mutate(spec)
			if err := validateFrozenExecution(spec); err == nil {
				t.Fatal("invalid frozen historical target was accepted")
			}
		})
	}
}
