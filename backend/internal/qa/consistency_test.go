package qa

import "testing"

func TestCheckDuplicateSourceDivergence_Hit(t *testing.T) {
	issues := CheckDuplicateSourceDivergence([]CheckInput{
		{Index: 0, SourceText: "  Hello   World  ", TargetText: "你好世界"},
		{Index: 1, SourceText: "Hello World", TargetText: "哈罗世界"},
		{Index: 2, SourceText: "Hello World", TargetText: "你好世界"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 divergent, got %d: %+v", len(issues), issues)
	}
	if issues[0].SegmentIndex != 1 {
		t.Errorf("index=%d want 1", issues[0].SegmentIndex)
	}
	if issues[0].Code != CodeDuplicateSourceDivergence {
		t.Errorf("code=%s", issues[0].Code)
	}
	if issues[0].Severity != SeverityWarning {
		t.Errorf("sev=%s", issues[0].Severity)
	}
}

func TestCheckDuplicateSourceDivergence_Consistent(t *testing.T) {
	issues := CheckDuplicateSourceDivergence([]CheckInput{
		{Index: 0, SourceText: "A", TargetText: "甲"},
		{Index: 1, SourceText: "A", TargetText: "甲"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d", len(issues))
	}
}

func TestCheckDuplicateSourceDivergence_UniqueSources(t *testing.T) {
	issues := CheckDuplicateSourceDivergence([]CheckInput{
		{Index: 0, SourceText: "A", TargetText: "甲"},
		{Index: 1, SourceText: "B", TargetText: "乙"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d", len(issues))
	}
}

func TestEngine_ChecksFilterExact(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Checks:  []string{CheckUntranslated, CheckNumberMismatch},
	}
	e := NewEngine(cfg, nil)
	if len(e.checkers) != 2 {
		t.Fatalf("want 2 checkers, got %d", len(e.checkers))
	}
	names := map[string]bool{}
	for _, c := range e.checkers {
		names[c.Name()] = true
	}
	if !names[CheckUntranslated] || !names[CheckNumberMismatch] {
		t.Errorf("names=%v", names)
	}
}

func TestEngine_NilChecksAll(t *testing.T) {
	cfg := Config{Enabled: true}
	e := NewEngine(cfg, nil)
	if len(e.checkers) != len(AllCheckerNames()) {
		t.Fatalf("want %d, got %d", len(AllCheckerNames()), len(e.checkers))
	}
}
