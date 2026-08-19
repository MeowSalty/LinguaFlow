package qa

import (
	"context"
	"testing"
)

func TestPunctuationSurplus_QuoteAdded(t *testing.T) {
	c := NewPunctuationSurplusChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "いや、初対面の人の首筋に、匂いを嗅ぐのはどうかと思うよ？", TargetText: `"不是，第一次见面就闻味道，这有点不妥吧？"`},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(issues), issues)
	}
	if issues[0].Code != CheckPunctuationSurplus || issues[0].Severity != SeverityWarning {
		t.Errorf("issue=%+v", issues[0])
	}
	if issues[0].Span == nil || issues[0].Span.MatchedText != `""` {
		t.Errorf("matched=%q", MatchedText(issues[0]))
	}
}

func TestPunctuationSurplus_ParenAdded(t *testing.T) {
	c := NewPunctuationSurplusChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "テストです", TargetText: "（测试）"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(issues), issues)
	}
	if issues[0].Span == nil || issues[0].Span.MatchedText != "（）" {
		t.Errorf("matched=%q", MatchedText(issues[0]))
	}
}

func TestPunctuationSurplus_MultiplePairsSingleIssue(t *testing.T) {
	c := NewPunctuationSurplusChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "テスト", TargetText: `"前""后"`},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(issues), issues)
	}
	if issues[0].Code != CheckPunctuationSurplus {
		t.Errorf("issue=%+v", issues[0])
	}
}

func TestPunctuationSurplus_CategoryReplacementNotReported(t *testing.T) {
	c := NewPunctuationSurplusChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「それは…ですね？」", TargetText: `"是…对吧？"`},
	})
	if len(issues) != 0 {
		t.Fatalf("category replacement should not trigger: %+v", issues)
	}
}

func TestPunctuationSurplus_SingleSidedDelegatedToPairing(t *testing.T) {
	c := NewPunctuationSurplusChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "テスト", TargetText: `"テスト`},
	})
	if len(issues) != 0 {
		t.Fatalf("single-sided target belongs to pairing, got: %+v", issues)
	}
}

func TestPunctuationSurplus_ProtectedRegion(t *testing.T) {
	c := NewPunctuationSurplusChecker()
	tag := `<a title="quoted">测试</a>`
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "テスト", TargetText: tag, Protected: map[string]string{"__LF_000001__": tag}},
	})
	if len(issues) != 0 {
		t.Fatalf("protected HTML quotes should not trigger: %+v", issues)
	}
}

func TestPunctuationSurplus_EmptyInputs(t *testing.T) {
	c := NewPunctuationSurplusChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "", TargetText: `"测试"`},
		{Index: 1, SourceText: "テスト", TargetText: "   "},
		{Index: 2, SourceText: "   ", TargetText: "测试"},
	})
	if len(issues) != 0 {
		t.Fatalf("empty inputs should not trigger: %+v", issues)
	}
}
