package execution

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

func validExecutionInput() JobExecutionSnapshot {
	p := DefaultProfile()
	return JobExecutionSnapshot{SourceLang: "en", TargetLang: "zh", RubyTemplates: RubyTemplates{JSON: "json alignment", Text: "text alignment", BatchJSON: "json batch alignment", BatchText: "text batch alignment"},
		Strategy: StrategySnapshot{Protect: p.Protect, Postprocess: p.Postprocess, Repair: p.Repair, Context: p.Context, Ruby: p.Ruby, QA: p.QA},
		Rounds:   []JobRoundSnapshot{{Mode: "translate", Backend: BackendSnapshot{ID: 1, Type: "openai", Credential: credential.Binding{ID: 1, Version: 2}, Options: map[string]any{"model": "test"}}, Translate: &JobTranslateRoundSnapshot{Prompt: PromptSnapshot{Content: "frozen translation"}, BatchSize: 0, MaxWordsPerBatch: 100, Concurrency: 1, FallbackShrink: 1, SegmentFilter: &SegmentFilterSnapshot{StatusFilter: "pending_only"}}}},
	}
}

func TestResolveFreezesDefaultsAndOwnsItsValues(t *testing.T) {
	input := validExecutionInput()
	input.Strategy.Context = ProfileContextConfig{Enabled: false, Before: 0, After: 0}
	input.Strategy.Ruby.PreserveKinds = []string{}
	input.Strategy.QA.Checks = []string{}
	spec, err := Resolve(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Rounds[0].Backend.Options["model"] = "changed"
	input.Rounds[0].Translate.Prompt.Content = "changed template"
	input.Strategy.Protect.Rules[0] = "changed rule"
	if spec.Rounds[0].Backend.Options["model"] != "test" || spec.Rounds[0].Translate.Prompt.Content != "frozen translation" || spec.Strategy.Protect.Rules[0] != "code" {
		t.Fatal("resolved execution shares mutable input")
	}
	if spec.Strategy.Context.Enabled || spec.Strategy.Context.Before != 0 || spec.Strategy.Context.After != 0 {
		t.Fatal("explicit zero context lost")
	}
	if spec.Strategy.Ruby.PreserveKinds == nil || len(spec.Strategy.Ruby.PreserveKinds) != 0 || spec.Strategy.QA.Checks == nil {
		t.Fatal("explicit empty arrays lost")
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
	if restored.Rounds[0].Backend.Credential.Version != 2 || restored.Strategy.QA.Checks == nil {
		t.Fatal("restore changed fixed values")
	}
}

func TestSpecRejectsUnversionedIncompleteOrSensitiveContent(t *testing.T) {
	for name, mutate := range map[string]func(*JobExecutionSnapshot){
		"missing_version":          func(s *JobExecutionSnapshot) { s.SchemaVersion = 0 },
		"unknown_defaults":         func(s *JobExecutionSnapshot) { s.DefaultsVersion = 99 },
		"missing_qa_checks":        func(s *JobExecutionSnapshot) { s.Strategy.QA.Checks = nil },
		"missing_qa_method":        func(s *JobExecutionSnapshot) { s.Strategy.QA.LengthMethod = "" },
		"missing_template":         func(s *JobExecutionSnapshot) { s.Rounds[0].Translate.Prompt.Content = "" },
		"missing_binding":          func(s *JobExecutionSnapshot) { s.Rounds[0].Backend.Credential = credential.Binding{} },
		"missing_provider_default": func(s *JobExecutionSnapshot) { delete(s.Rounds[0].Backend.Options, "max_tokens") },
		"empty_endpoint":           func(s *JobExecutionSnapshot) { s.Rounds[0].Backend.Options["base_url"] = "" },
		"unnormalized_endpoint":    func(s *JobExecutionSnapshot) { s.Rounds[0].Backend.Options["base_url"] = "HTTPS://API.OPENAI.COM/v1/" },
		"secret":                   func(s *JobExecutionSnapshot) { s.Rounds[0].Backend.Options["api_key"] = "must-never-appear" },
	} {
		t.Run(name, func(t *testing.T) {
			s, err := Resolve(validExecutionInput())
			if err != nil {
				t.Fatal(err)
			}
			mutate(s)
			err = ValidateSpec(s)
			if err == nil {
				t.Fatal("invalid snapshot accepted")
			}
			if strings.Contains(err.Error(), "must-never-appear") {
				t.Fatal("secret disclosed")
			}
		})
	}
}

func TestResolveRecomputesProvenanceAfterRequestOverrides(t *testing.T) {
	first, err := Resolve(validExecutionInput())
	if err != nil {
		t.Fatal(err)
	}
	prior := first.Sources[len(first.Sources)-1].Digest
	first.Rounds[0].Translate.Prompt.Content = "request override"
	second, err := Resolve(*first)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Sources) != len(first.Sources) || second.Sources[len(second.Sources)-1].Digest == prior {
		t.Fatal("request override did not replace derived provenance")
	}
}

func TestBackendResolutionDurationsAndFrozenThinking(t *testing.T) {
	options, err := ResolveBackendOptions("anthropic", map[string]any{"model": "test", "timeout": "1500ms", "thinking_level": "low"})
	if err != nil {
		t.Fatal(err)
	}
	if options["thinking_budget_tokens"] != int64(2048) || options["timeout"] != "1.5s" {
		t.Fatalf("incorrect frozen options: %#v", options)
	}
	b := BackendSnapshot{ID: 1, Type: "anthropic", Options: options, Credential: credential.Binding{ID: 1, Version: 1}}
	if err := validateBackend(b); err != nil {
		t.Fatal(err)
	}
	delete(options, "thinking_budget_tokens")
	if err := validateBackend(b); err == nil {
		t.Fatal("restore recalculated missing thinking budget")
	}
}

func TestProfilePresenceDefaultsAndValidation(t *testing.T) {
	base := DefaultProfile()
	got, err := DecodeProfileJSON([]byte(`{"context":{"enabled":false,"before":0,"after":0},"ruby":{"preserve_kinds":[]},"qa":{"checks":[]}}`), base)
	if err != nil {
		t.Fatal(err)
	}
	if got.Context.Enabled || got.Context.Before != 0 || got.Context.After != 0 || got.Ruby.PreserveKinds == nil || got.QA.Checks == nil {
		t.Fatal("presence lost")
	}
	if !reflect.DeepEqual(got.Protect, base.Protect) {
		t.Fatal("absent fields overwritten")
	}
	for _, raw := range []string{`{"schema_version":0}`, `{"context":null}`, `{"ruby":{"enabled":null}}`, `{"context":{"before":-1}}`, `{"unknown":true}`, `{"context":{"enabled":"false"}}`, `{"context":{"before":0,"before":2}}`, `{} {}`} {
		if _, err := DecodeProfileJSON([]byte(raw), base); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
