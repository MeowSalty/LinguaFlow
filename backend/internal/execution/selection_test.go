package execution

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func executionSelectionInput(mode string) JobExecutionSnapshot {
	in := validExecutionInput()
	round := JobRoundSnapshot{Mode: mode, Backend: in.Rounds[0].Backend}
	switch mode {
	case "adjudicate":
		round.Adjudicate = &JobAdjudicateRoundSnapshot{TemplateContent: "adjudication prompt", BatchSize: 10, Concurrency: 1}
	case "semantic_qa":
		round.SemanticQA = &JobSemanticQARoundSnapshot{TemplateContent: "semantic QA prompt", BatchSize: 10, Concurrency: 1}
	case "revise":
		round.Revise = &JobReviseRoundSnapshot{TemplateContent: "revision prompt", BatchSize: 10, Concurrency: 1}
	}
	in.Rounds = []JobRoundSnapshot{round}
	return in
}

func TestResolveFreezesRoundSelections(t *testing.T) {
	for _, mode := range []string{"adjudicate", "semantic_qa", "revise"} {
		t.Run(mode, func(t *testing.T) {
			input := executionSelectionInput(mode)
			spec, err := Resolve(input)
			if err != nil {
				t.Fatal(err)
			}
			round := spec.Rounds[0]
			switch mode {
			case "adjudicate":
				if !reflect.DeepEqual(round.Adjudicate.AdjudicateCodes, qa.DefaultAdjudicateCodes()) {
					t.Fatalf("missing frozen adjudication codes: %+v", round.Adjudicate)
				}
			case "semantic_qa":
				if round.SemanticQA.SegmentScope != "all" || round.SemanticQA.IssueCodes == nil {
					t.Fatalf("missing frozen semantic QA selection: %+v", round.SemanticQA)
				}
			case "revise":
				if round.Revise.SegmentScope != "with_issues" || !reflect.DeepEqual(round.Revise.IssueCodes, qa.SemanticQACodes()) {
					t.Fatalf("missing frozen revision selection: %+v", round.Revise)
				}
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
				t.Fatalf("frozen selection was not persisted: %v", err)
			}
		})
	}
}

func TestResolvePreservesExplicitEmptyRoundSelections(t *testing.T) {
	for _, mode := range []string{"adjudicate", "semantic_qa", "revise"} {
		t.Run(mode, func(t *testing.T) {
			input := executionSelectionInput(mode)
			switch mode {
			case "adjudicate":
				input.Rounds[0].Adjudicate.AdjudicateCodes = []string{}
			case "semantic_qa":
				input.Rounds[0].SemanticQA.IssueCodes = []string{}
			case "revise":
				input.Rounds[0].Revise.IssueCodes = []string{}
			}
			spec, err := Resolve(input)
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
			var codes []string
			switch mode {
			case "adjudicate":
				codes = restored.Rounds[0].Adjudicate.AdjudicateCodes
			case "semantic_qa":
				codes = restored.Rounds[0].SemanticQA.IssueCodes
			case "revise":
				codes = restored.Rounds[0].Revise.IssueCodes
			}
			if codes == nil || len(codes) != 0 {
				t.Fatalf("explicit empty selection changed during resolve or persistence: %v", codes)
			}
		})
	}
}

func TestRestoreRejectsMissingOrInvalidRoundSelections(t *testing.T) {
	for _, mode := range []string{"adjudicate", "semantic_qa", "revise"} {
		for _, invalid := range []string{"missing_codes", "invalid_code", "missing_scope", "invalid_scope", "empty_filtered_codes"} {
			if mode == "adjudicate" && invalid != "missing_codes" && invalid != "invalid_code" {
				continue
			}
			t.Run(mode+"/"+invalid, func(t *testing.T) {
				spec, err := Resolve(executionSelectionInput(mode))
				if err != nil {
					t.Fatal(err)
				}
				var codes *[]string
				var scope *string
				switch mode {
				case "adjudicate":
					codes = &spec.Rounds[0].Adjudicate.AdjudicateCodes
				case "semantic_qa":
					codes, scope = &spec.Rounds[0].SemanticQA.IssueCodes, &spec.Rounds[0].SemanticQA.SegmentScope
				case "revise":
					codes, scope = &spec.Rounds[0].Revise.IssueCodes, &spec.Rounds[0].Revise.SegmentScope
				}
				switch invalid {
				case "missing_codes":
					*codes = nil
				case "invalid_code":
					*codes = []string{"unrecognized_code"}
				case "missing_scope":
					*scope = ""
				case "invalid_scope":
					*scope = "unrecognized_scope"
				case "empty_filtered_codes":
					*scope, *codes = "with_issue_codes", []string{}
				}
				if err := ValidateSpec(spec); err == nil {
					t.Fatal("restored execution accepted an incomplete selection")
				}
			})
		}
	}
}

func TestResolveFreezesAllQAChecksAndPreservesSelections(t *testing.T) {
	all := append(qa.AllCheckerNames(), qa.CodeDuplicateSourceDivergence)
	for _, test := range []struct {
		name        string
		input, want []string
	}{
		{"omitted", nil, all},
		{"empty", []string{}, []string{}},
		{"subset", []string{qa.CheckUntranslated, qa.CodeDuplicateSourceDivergence}, []string{qa.CheckUntranslated, qa.CodeDuplicateSourceDivergence}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := validExecutionInput()
			input.Strategy.QA.Enabled = true
			input.Strategy.QA.Checks = test.input
			spec, err := Resolve(input)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(spec.Strategy.QA.Checks, test.want) {
				t.Fatalf("resolved checks=%v want=%v", spec.Strategy.QA.Checks, test.want)
			}
			if qa.DuplicateSourceDivergenceEnabled(spec.Strategy.QA.Checks) != (test.name != "empty") {
				t.Fatal("document QA selection changed while freezing defaults")
			}
		})
	}
	if _, err := DecodeProfileJSON([]byte(`{"qa":{"checks":["misspelled_check"]}}`), DefaultProfile()); err == nil {
		t.Fatal("unknown QA checker silently disabled checking")
	}
}
