package qa

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

type memGlossary struct {
	entries []glossary.Entry
}

func (m *memGlossary) Lookup(_ context.Context, text, _, _ string) ([]glossary.Entry, error) {
	var hits []glossary.Entry
	lower := toLower(text)
	for _, e := range m.entries {
		if e.CaseSensitive {
			if containsLiteral(text, e.Source) {
				hits = append(hits, e)
			}
		} else if containsLiteral(lower, toLower(e.Source)) {
			hits = append(hits, e)
		}
	}
	return hits, nil
}

func (m *memGlossary) Add(_ context.Context, entries ...glossary.Entry) (glossary.AddResult, error) {
	m.entries = append(m.entries, entries...)
	return glossary.AddResult{Added: entries}, nil
}

func toLower(s string) string {
	b := make([]byte, len(s))
	copy(b, s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func containsLiteral(hay, needle string) bool {
	return len(needle) > 0 && (hay == needle || len(hay) >= len(needle) && indexOf(hay, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestForbiddenTerm_Hit(t *testing.T) {
	g := &memGlossary{entries: []glossary.Entry{
		{Source: "Foo", Target: "禁译", Forbidden: true, CaseSensitive: false},
	}}
	c := NewForbiddenTermChecker(g, "en", "zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "use Foo carefully", TargetText: "请使用禁译"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Code != CheckForbiddenTerm || issues[0].Severity != SeverityError {
		t.Errorf("issue=%+v", issues[0])
	}
}

func TestForbiddenTerm_NilGlossary(t *testing.T) {
	c := NewForbiddenTermChecker(nil, "en", "zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "b"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d", len(issues))
	}
}

func TestForbiddenTerm_RequiresIndependentLatinSourceMatch(t *testing.T) {
	g := &memGlossary{entries: []glossary.Entry{
		{Source: "AI", Target: "人工智能", Forbidden: true},
	}}
	c := NewForbiddenTermChecker(g, "en", "zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "Please wait", TargetText: "等待人工智能结果"},
	})
	if len(issues) != 0 {
		t.Fatalf("substring source match should not trigger forbidden term: %+v", issues)
	}
}

func TestTermInconsistency_MandatoryError(t *testing.T) {
	g := &memGlossary{entries: []glossary.Entry{
		{Source: "API", Target: "接口", Mandatory: true, CaseSensitive: true},
	}}
	c := NewTermInconsistencyChecker(g, "en", "zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "call the API now", TargetText: "调用端点"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Severity != SeverityError || issues[0].Code != CheckTermInconsistency {
		t.Errorf("issue=%+v", issues[0])
	}
}

func TestTermInconsistency_SoftWarning(t *testing.T) {
	g := &memGlossary{entries: []glossary.Entry{
		{Source: "pipeline", Target: "流水线", Mandatory: false, CaseSensitive: false},
	}}
	c := NewTermInconsistencyChecker(g, "en", "zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "build pipeline", TargetText: "构建管道"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Severity != SeverityWarning {
		t.Errorf("soft want warning, got %s", issues[0].Severity)
	}
}

func TestTermInconsistency_Consistent(t *testing.T) {
	g := &memGlossary{entries: []glossary.Entry{
		{Source: "API", Target: "接口", Mandatory: true, CaseSensitive: true},
	}}
	c := NewTermInconsistencyChecker(g, "en", "zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "call the API", TargetText: "调用接口"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d", len(issues))
	}
}

func TestTermPresent_LatinBoundary(t *testing.T) {
	if termPresent("wait and rain", "ai", true, "en") {
		t.Error("substring ai in wait/rain should not match")
	}
	if !termPresent("use AI carefully", "AI", true, "en") {
		t.Error("independent AI should match")
	}
	if !termPresent("你好世界", "你好", true, "zh") {
		t.Error("CJK contains should match")
	}
}

func TestEngine_FilterTerminologyIndependently(t *testing.T) {
	g := &memGlossary{entries: []glossary.Entry{
		{Source: "Foo", Target: "禁", Forbidden: true},
		{Source: "Bar", Target: "推荐", Mandatory: true, CaseSensitive: false},
	}}
	cfg := Config{
		Enabled:    true,
		Checks:     []string{CheckForbiddenTerm},
		Glossary:   g,
		SourceLang: "en",
		TargetLang: "zh",
	}
	e := NewEngine(cfg, nil)
	if len(e.checkers) != 1 || e.checkers[0].Name() != CheckForbiddenTerm {
		t.Fatalf("checkers=%v", e.checkers)
	}
	issues := e.Run(context.Background(), []CheckInput{
		{Index: 0, SourceText: "Foo and Bar", TargetText: "禁 与 其他"},
	})
	for _, iss := range issues {
		if iss.Code == CheckTermInconsistency {
			t.Fatal("term_inconsistency should be filtered out")
		}
	}
	if len(issues) != 1 || issues[0].Code != CheckForbiddenTerm {
		t.Fatalf("want only forbidden, got %+v", issues)
	}
}
