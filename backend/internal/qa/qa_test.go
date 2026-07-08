package qa

import (
	"context"
	"testing"
)

func TestUntranslatedChecker(t *testing.T) {
	checker := NewUntranslatedChecker()

	tests := []struct {
		name     string
		segments []CheckInput
		want     int
	}{
		{
			name: "untranslated detected",
			segments: []CheckInput{
				{Index: 0, SourceText: "Hello World", TargetText: "Hello World"},
			},
			want: 1,
		},
		{
			name: "translated passes",
			segments: []CheckInput{
				{Index: 0, SourceText: "Hello", TargetText: "你好"},
			},
			want: 0,
		},
		{
			name: "pure numbers exempt",
			segments: []CheckInput{
				{Index: 0, SourceText: "123", TargetText: "123"},
			},
			want: 0,
		},
		{
			name: "pure punctuation exempt",
			segments: []CheckInput{
				{Index: 0, SourceText: "...", TargetText: "..."},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := checker.Check(context.Background(), tt.segments)
			if len(issues) != tt.want {
				t.Errorf("got %d issues, want %d", len(issues), tt.want)
			}
		})
	}
}

func TestLengthRatioChecker(t *testing.T) {
	checker := NewLengthRatioChecker(0.2, 3.0)

	tests := []struct {
		name     string
		segments []CheckInput
		want     int
	}{
		{
			name: "normal ratio passes",
			segments: []CheckInput{
				{Index: 0, SourceText: "Hello World", TargetText: "你好世界"},
			},
			want: 0,
		},
		{
			name: "too short detected",
			segments: []CheckInput{
				{Index: 0, SourceText: "This is a long sentence with many words", TargetText: "短"},
			},
			want: 1,
		},
		{
			name: "too long detected",
			segments: []CheckInput{
				{Index: 0, SourceText: "Hello World", TargetText: "这是一个非常非常非常非常非常非常非常非常非常非常非常长的译文"},
			},
			want: 1,
		},
		{
			name: "short source skipped",
			segments: []CheckInput{
				{Index: 0, SourceText: "Hi", TargetText: "你好"},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := checker.Check(context.Background(), tt.segments)
			if len(issues) != tt.want {
				t.Errorf("got %d issues, want %d", len(issues), tt.want)
			}
			for _, issue := range issues {
				if issue.Severity != SeverityWarning {
					t.Errorf("expected warning severity, got %s", issue.Severity)
				}
				if issue.Code != "length_ratio" {
					t.Errorf("expected code length_ratio, got %s", issue.Code)
				}
			}
		})
	}
}

func TestLengthRatioChecker_MinRatioZeroDisablesShortCheck(t *testing.T) {
	checker := NewLengthRatioChecker(0, 3.0)

	segments := []CheckInput{
		{Index: 0, SourceText: "This is a long sentence with many words", TargetText: "短"},
	}

	issues := checker.Check(context.Background(), segments)
	if len(issues) != 0 {
		t.Errorf("minRatio=0 should disable short check, got %d issues", len(issues))
	}
}

func TestLengthRatioChecker_NegativeMinRatioFallsBack(t *testing.T) {
	checker := NewLengthRatioChecker(-1, 3.0)

	segments := []CheckInput{
		{Index: 0, SourceText: "This is a long sentence with many words", TargetText: "短"},
	}

	issues := checker.Check(context.Background(), segments)
	if len(issues) != 1 {
		t.Errorf("negative minRatio should fall back to default, want 1 issue, got %d", len(issues))
	}
}

func TestLengthRatioChecker_ZeroMaxRatioDisablesLongCheck(t *testing.T) {
	checker := NewLengthRatioChecker(0.2, 0)

	segments := []CheckInput{
		{Index: 0, SourceText: "Hello World", TargetText: "这是一个非常非常非常非常非常非常非常非常非常非常非常长的译文"},
	}

	issues := checker.Check(context.Background(), segments)
	if len(issues) != 0 {
		t.Errorf("maxRatio=0 should disable long check, got %d issues", len(issues))
	}
}

func TestLengthRatioChecker_NegativeMaxRatioFallsBack(t *testing.T) {
	checker := NewLengthRatioChecker(0.2, -1)

	segments := []CheckInput{
		{Index: 0, SourceText: "Hello World", TargetText: "这是一个非常非常非常非常非常非常非常非常非常非常非常长的译文"},
	}

	issues := checker.Check(context.Background(), segments)
	if len(issues) != 1 {
		t.Errorf("negative maxRatio should fall back to default, want 1 issue, got %d", len(issues))
	}
}

func TestDuplicateTranslationChecker(t *testing.T) {
	checker := NewDuplicateTranslationChecker()

	tests := []struct {
		name     string
		segments []CheckInput
		want     int
	}{
		{
			name: "no duplicates",
			segments: []CheckInput{
				{Index: 0, SourceText: "Hello", TargetText: "你好"},
				{Index: 1, SourceText: "World", TargetText: "世界"},
			},
			want: 0,
		},
		{
			name: "duplicate detected",
			segments: []CheckInput{
				{Index: 0, SourceText: "Hello", TargetText: "你好"},
				{Index: 1, SourceText: "World", TargetText: "你好"},
			},
			want: 1,
		},
		{
			name: "same source same target exempt",
			segments: []CheckInput{
				{Index: 0, SourceText: "Hello", TargetText: "你好"},
				{Index: 1, SourceText: "Hello", TargetText: "你好"},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := checker.Check(context.Background(), tt.segments)
			if len(issues) != tt.want {
				t.Errorf("got %d issues, want %d", len(issues), tt.want)
			}
		})
	}
}

func TestEngineRun(t *testing.T) {
	cfg := Config{
		Enabled:        true,
		AutoReject:     false,
		LengthRatioMin: 0.2,
		LengthRatioMax: 3.0,
	}
	engine := NewEngine(cfg, nil)

	segments := []CheckInput{
		{Index: 0, SourceText: "Hello World", TargetText: "Hello World"},
		{Index: 1, SourceText: "World", TargetText: "世界"},
	}

	issues := engine.Run(context.Background(), segments)
	if len(issues) == 0 {
		t.Error("expected issues, got none")
	}

	foundUntranslated := false
	for _, issue := range issues {
		if issue.Code == "untranslated" {
			foundUntranslated = true
		}
	}
	if !foundUntranslated {
		t.Error("expected untranslated issue")
	}
}

func TestEngineDisabled(t *testing.T) {
	cfg := Config{Enabled: false}
	engine := NewEngine(cfg, nil)

	segments := []CheckInput{
		{Index: 0, SourceText: "Hello", TargetText: ""},
	}

	issues := engine.Run(context.Background(), segments)
	if len(issues) != 0 {
		t.Errorf("expected no issues when disabled, got %d", len(issues))
	}
}

func TestHasErrors(t *testing.T) {
	issues := []QualityIssue{
		{Severity: SeverityWarning, Code: "length_ratio"},
		{Severity: SeverityError, Code: "untranslated"},
	}
	if !HasErrors(issues) {
		t.Error("expected HasErrors to return true")
	}

	warningOnly := []QualityIssue{
		{Severity: SeverityWarning, Code: "length_ratio"},
	}
	if HasErrors(warningOnly) {
		t.Error("expected HasErrors to return false for warnings only")
	}
}

func TestIssuesFor(t *testing.T) {
	issues := []QualityIssue{
		{SegmentIndex: 0, Code: "untranslated"},
		{SegmentIndex: 1, Code: "length_ratio"},
		{SegmentIndex: 0, Code: "duplicate"},
	}

	result := IssuesFor(0, issues)
	if len(result) != 2 {
		t.Errorf("expected 2 issues for index 0, got %d", len(result))
	}

	result = IssuesFor(1, issues)
	if len(result) != 1 {
		t.Errorf("expected 1 issue for index 1, got %d", len(result))
	}

	result = IssuesFor(2, issues)
	if len(result) != 0 {
		t.Errorf("expected 0 issues for index 2, got %d", len(result))
	}
}

func TestWeightedLen(t *testing.T) {
	tests := []struct {
		text string
		want int
	}{
		{"Hello", 5},
		{"你好", 4},
		{"Hello你好", 9},
		{"", 0},
	}
	for _, tt := range tests {
		got := weightedLen(tt.text)
		if got != tt.want {
			t.Errorf("weightedLen(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}

func TestIsCJK(t *testing.T) {
	tests := []struct {
		r    rune
		want bool
	}{
		{'你', true},
		{'あ', true},
		{'ア', true},
		{'가', true},
		{'A', false},
		{'1', false},
	}
	for _, tt := range tests {
		got := isCJK(tt.r)
		if got != tt.want {
			t.Errorf("isCJK(%q) = %v, want %v", tt.r, got, tt.want)
		}
	}
}
