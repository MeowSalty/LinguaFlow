package worker

import (
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func TestMergeSemanticQAIssuesReplacesPriorScan(t *testing.T) {
	existing := []qa.QualityIssue{
		{Code: "source_residual", Message: "rule"},
		{Code: "calque", Message: "old calque"},
		{Code: "naturalness", Message: "old naturalness"},
	}
	fresh := []qa.QualityIssue{{Code: "term_fidelity", Message: "new term issue"}}

	got := mergeSemanticQAIssues(existing, fresh)
	want := []qa.QualityIssue{
		{Code: "source_residual", Message: "rule"},
		{Code: "term_fidelity", Message: "new term issue"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged=%#v want %#v", got, want)
	}
}

func TestMergeSemanticQAIssuesEmptyScanClearsPriorSemanticIssues(t *testing.T) {
	existing := []qa.QualityIssue{
		{Code: "length_ratio", Message: "rule"},
		{Code: "calque", Message: "old calque"},
	}

	got := mergeSemanticQAIssues(existing, []qa.QualityIssue{})
	want := []qa.QualityIssue{{Code: "length_ratio", Message: "rule"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged=%#v want %#v", got, want)
	}
}
