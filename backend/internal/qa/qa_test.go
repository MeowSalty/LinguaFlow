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
	checker := NewLengthRatioChecker(0.2, 3.0, LengthMethodCharWeight)

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
	checker := NewLengthRatioChecker(0, 3.0, LengthMethodCharWeight)

	segments := []CheckInput{
		{Index: 0, SourceText: "This is a long sentence with many words", TargetText: "短"},
	}

	issues := checker.Check(context.Background(), segments)
	if len(issues) != 0 {
		t.Errorf("minRatio=0 should disable short check, got %d issues", len(issues))
	}
}

func TestLengthRatioChecker_NegativeMinRatioFallsBack(t *testing.T) {
	checker := NewLengthRatioChecker(-1, 3.0, LengthMethodCharWeight)

	segments := []CheckInput{
		{Index: 0, SourceText: "This is a long sentence with many words", TargetText: "短"},
	}

	issues := checker.Check(context.Background(), segments)
	if len(issues) != 1 {
		t.Errorf("negative minRatio should fall back to default, want 1 issue, got %d", len(issues))
	}
}

func TestLengthRatioChecker_ZeroMaxRatioDisablesLongCheck(t *testing.T) {
	checker := NewLengthRatioChecker(0.2, 0, LengthMethodCharWeight)

	segments := []CheckInput{
		{Index: 0, SourceText: "Hello World", TargetText: "这是一个非常非常非常非常非常非常非常非常非常非常非常长的译文"},
	}

	issues := checker.Check(context.Background(), segments)
	if len(issues) != 0 {
		t.Errorf("maxRatio=0 should disable long check, got %d issues", len(issues))
	}
}

func TestLengthRatioChecker_NegativeMaxRatioFallsBack(t *testing.T) {
	checker := NewLengthRatioChecker(0.2, -1, LengthMethodCharWeight)

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

func TestCountWords(t *testing.T) {
	tests := []struct {
		text string
		want int
	}{
		{"Hello", 1},
		{"Hello World", 2},
		{"你好", 2},
		{"你好世界", 4},
		{"Hello你好世界", 5},
		{"", 0},
		{"   ", 0},
		{" a  b  c ", 3},
	}
	for _, tt := range tests {
		got := countWords(tt.text)
		if got != tt.want {
			t.Errorf("countWords(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}

func TestLengthRatioChecker_WordCountMethod(t *testing.T) {
	checker := NewLengthRatioChecker(0.3, 2.5, LengthMethodWordCount)

	tests := []struct {
		name     string
		segments []CheckInput
		want     int
	}{
		{
			name: "zh to en normal",
			segments: []CheckInput{
				{Index: 0, SourceText: "你好世界", TargetText: "Hello World"},
			},
			want: 0,
		},
		{
			name: "en to zh normal",
			segments: []CheckInput{
				{Index: 0, SourceText: "Hello World", TargetText: "你好世界"},
			},
			want: 0,
		},
		{
			name: "zh to en too short",
			segments: []CheckInput{
				{Index: 0, SourceText: "这是一段很长的句子用于测试", TargetText: "test"},
			},
			want: 1,
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

func TestLengthRatioChecker_EmptyMethodDefaultsToCharWeight(t *testing.T) {
	checker := NewLengthRatioChecker(0.2, 3.0, "")

	segments := []CheckInput{
		{Index: 0, SourceText: "Hello World", TargetText: "你好世界"},
	}

	issues := checker.Check(context.Background(), segments)
	if len(issues) != 0 {
		t.Errorf("empty method should default to char_weight, got %d issues", len(issues))
	}
}

func TestFingerprint(t *testing.T) {
	if got := Fingerprint(QualityIssue{Code: "length_ratio"}); got != "length_ratio:" {
		t.Errorf("nil span: got %q", got)
	}
	if got := Fingerprint(QualityIssue{Code: "calque", Span: &Span{MatchedText: ""}}); got != "calque:" {
		t.Errorf("empty matched: got %q", got)
	}
	if got := Fingerprint(QualityIssue{Code: "source_residual", Span: &Span{MatchedText: "テスト"}}); got != "source_residual:テスト" {
		t.Errorf("with span: got %q", got)
	}
}

func TestDedupIssues(t *testing.T) {
	issues := []QualityIssue{
		{Code: "calque", Message: "a", Span: &Span{MatchedText: "foo"}},
		{Code: "calque", Message: "b", Span: &Span{MatchedText: "foo"}}, // dup
		{Code: "calque", Message: "c", Span: &Span{MatchedText: "bar"}},
		{Code: "naturalness", Message: "d"},
		{Code: "naturalness", Message: "e"}, // dup segment-level
	}
	got := DedupIssues(issues)
	if len(got) != 3 {
		t.Fatalf("len=%d want 3: %#v", len(got), got)
	}
	if got[0].Message != "a" || MatchedText(got[1]) != "bar" || got[2].Code != "naturalness" {
		t.Errorf("dedup order/content: %#v", got)
	}
}

func TestLocateSpan(t *testing.T) {
	span := LocateSpan("Hello テスト world", "テスト")
	if span == nil || span.MatchedText != "テスト" {
		t.Fatalf("span=%#v", span)
	}
	if span.TargetStart == nil || span.TargetEnd == nil {
		t.Fatal("expected offsets")
	}
	if *span.TargetStart != 6 || *span.TargetEnd != 9 {
		t.Errorf("offsets start=%d end=%d want 6,9", *span.TargetStart, *span.TargetEnd)
	}

	missing := LocateSpan("hello", "テスト")
	if missing == nil || missing.MatchedText != "テスト" {
		t.Fatalf("missing locate should still store text: %#v", missing)
	}
	if missing.TargetStart != nil || missing.TargetEnd != nil {
		t.Error("missing locate should leave offsets nil")
	}

	if LocateSpan("hello", "") != nil || LocateSpan("hello", "  ") != nil {
		t.Error("empty matched should return nil")
	}
}

func TestLocateSpan_EqualFoldPreservesOriginalByteOffsets(t *testing.T) {
	span := LocateSpan("ȺX", "x")
	if span == nil || span.MatchedText != "X" {
		t.Fatalf("span=%#v", span)
	}
	if span.TargetStart == nil || span.TargetEnd == nil || *span.TargetStart != 1 || *span.TargetEnd != 2 {
		t.Fatalf("offsets=%#v want 1,2", span)
	}

	span = LocateSpan("Ⱥ", "ⱥ")
	if span == nil || span.MatchedText != "Ⱥ" {
		t.Fatalf("case-folded span=%#v", span)
	}
}

// TestZeroConfigDeterministicChecks 锁定白名单内容与顺序，并断言不含需用户阈值/术语表/多段上下文的 checker。
func TestZeroConfigDeterministicChecks(t *testing.T) {
	got := ZeroConfigDeterministicChecks()
	want := []string{
		CheckUntranslated,
		CheckSourceResidual,
		CheckPunctuationPairing,
		CheckPunctuationMissing,
		CheckPunctuationSurplus,
		CheckPunctuationWrapLoss,
		CheckWhitespaceIrregular,
		CheckRepeatedSpace,
		CheckWidthMix,
		CheckNumberMismatch,
		CheckURLEmailMismatch,
		CheckSubtitleLineCount,
		CheckLeftoverPlaceholder,
		CheckXMLTagMismatch,
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d, got=%v", len(got), len(want), got)
	}
	for i, code := range want {
		if got[i] != code {
			t.Fatalf("index %d: got %q want %q (full=%v)", i, got[i], code, got)
		}
	}

	// 排除需要用户阈值、术语表或多段上下文的 checker。
	excluded := []string{
		CheckLengthRatio,              // 需用户阈值，默认值会与执行计划矛盾
		CheckForbiddenTerm,            // 需术语表
		CheckTermInconsistency,        // 需术语表
		CheckDuplicate,                // 需多段输入
		CodeDuplicateSourceDivergence, // 文档级，不走 Engine
	}
	gotSet := make(map[string]struct{}, len(got))
	for _, c := range got {
		gotSet[c] = struct{}{}
	}
	for _, code := range excluded {
		if _, ok := gotSet[code]; ok {
			t.Fatalf("ZeroConfigDeterministicChecks must not include %q, got=%v", code, got)
		}
	}

	// 白名单中的每一项必须是 AllCheckerNames 注册的合法 checker。
	allSet := make(map[string]struct{})
	for _, c := range AllCheckerNames() {
		allSet[c] = struct{}{}
	}
	for _, c := range got {
		if _, ok := allSet[c]; !ok {
			t.Fatalf("ZeroConfigDeterministicChecks contains unknown checker %q", c)
		}
	}
}
