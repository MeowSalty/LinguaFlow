package qa

import (
	"context"
	"strings"
	"testing"
)

func TestPunctuationWrapLoss_OuterWrapLostInnerQuotesAdded(t *testing.T) {
	c := NewPunctuationWrapLossChecker()
	issues := c.Check(context.Background(), []CheckInput{{
		Index:      0,
		SourceText: "「最初は全然取りあってくれなかったのに覆面って言葉を出したら、異常に食いついてきたのよ。あとはマーリン様の覆面……すっごい素敵ですねとか適当におだてりゃ……まあこの通りってなもんよ」",
		TargetText: "一开始她根本不肯搭理我，结果我一说出“面具”这两个字，她就异常兴奋地咬了上来。然后再随便夸几句“梅林大人的面具——超漂亮的呢”之类的话……嗯，就变成现在这样了。",
	}})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(issues), issues)
	}
	if issues[0].Code != CheckPunctuationWrapLoss || issues[0].Severity != SeverityWarning {
		t.Errorf("issue=%+v", issues[0])
	}
	matched := MatchedText(issues[0])
	if !strings.Contains(matched, "「") || !strings.Contains(matched, "」") {
		t.Errorf("matched=%q", matched)
	}
}

func TestPunctuationWrapLoss_TotalLossFires(t *testing.T) {
	c := NewPunctuationWrapLossChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「a」", TargetText: "a"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(issues), issues)
	}
	if issues[0].Code != CheckPunctuationWrapLoss || issues[0].Severity != SeverityWarning {
		t.Errorf("issue=%+v", issues[0])
	}

	missing := NewPunctuationMissingChecker()
	missingIssues := missing.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「a」", TargetText: "a"},
	})
	if len(missingIssues) != 1 {
		t.Fatalf("punctuation_missing: want 1, got %d: %+v", len(missingIssues), missingIssues)
	}
	if issues[0].Code == missingIssues[0].Code {
		t.Errorf("overlapping checkers must use different codes: wrap=%q missing=%q", issues[0].Code, missingIssues[0].Code)
	}
}

func TestPunctuationWrapLoss_CategoryReplacementOK(t *testing.T) {
	c := NewPunctuationWrapLossChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「a」", TargetText: "“a”"},
	})
	if len(issues) != 0 {
		t.Fatalf("category replacement should not trigger: %+v", issues)
	}
}

func TestPunctuationWrapLoss_AsciiReplacementOK(t *testing.T) {
	c := NewPunctuationWrapLossChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「a」", TargetText: `"a"`},
	})
	if len(issues) != 0 {
		t.Fatalf("ASCII replacement should not trigger: %+v", issues)
	}
}

func TestPunctuationWrapLoss_GuillemetsOK(t *testing.T) {
	c := NewPunctuationWrapLossChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「a」", TargetText: "«a»"},
	})
	if len(issues) != 0 {
		t.Fatalf("guillemets replacement should not trigger: %+v", issues)
	}
}

func TestPunctuationWrapLoss_SingleSidedDelegatedToPairing(t *testing.T) {
	c := NewPunctuationWrapLossChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「a」", TargetText: "「a"},
		{Index: 1, SourceText: "「a」", TargetText: "a」"},
	})
	if len(issues) != 0 {
		t.Fatalf("single-sided target belongs to pairing, got: %+v", issues)
	}
}

func TestPunctuationWrapLoss_MultiSpanSourceSkipped(t *testing.T) {
	c := NewPunctuationWrapLossChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「你好」「再见」", TargetText: "你好 再见"},
	})
	if len(issues) != 0 {
		t.Fatalf("multi-span source should not trigger: %+v", issues)
	}
}

func TestPunctuationWrapLoss_SourceMidSentenceNotWrapped(t *testing.T) {
	c := NewPunctuationWrapLossChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "这是“测试”消息", TargetText: "这是测试消息"},
	})
	if len(issues) != 0 {
		t.Fatalf("mid-sentence source quotes should not trigger: %+v", issues)
	}
}

func TestPunctuationWrapLoss_EmptyContentNoop(t *testing.T) {
	c := NewPunctuationWrapLossChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「」", TargetText: "x"},
	})
	if len(issues) != 0 {
		t.Fatalf("empty source wrapper should not trigger: %+v", issues)
	}
}

func TestPunctuationWrapLoss_EmptyInputs(t *testing.T) {
	c := NewPunctuationWrapLossChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "", TargetText: ""},
		{Index: 1, SourceText: "「a」", TargetText: "   "},
		{Index: 2, SourceText: "   ", TargetText: "a"},
	})
	if len(issues) != 0 {
		t.Fatalf("empty inputs should not trigger: %+v", issues)
	}
}

func TestPunctuationWrapLoss_ProtectedRegion(t *testing.T) {
	c := NewPunctuationWrapLossChecker()
	tag := `<a href="p-006.xhtml">期中考试结果的考察</a>`
	issues := c.Check(context.Background(), []CheckInput{
		{
			Index:      0,
			SourceText: "「中間試験の考察」",
			TargetText: tag,
			Protected:  map[string]string{"__LF_000001__": tag},
		},
	})
	if len(issues) != 0 {
		t.Fatalf("protected target region should not trigger: %+v", issues)
	}
}

func TestPunctuationWrapLoss_InnerSourceQuotesStillSingleSpan(t *testing.T) {
	c := NewPunctuationWrapLossChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「彼は“行く”と言った」", TargetText: "他说要去"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(issues), issues)
	}
	if issues[0].Code != CheckPunctuationWrapLoss || issues[0].Severity != SeverityWarning {
		t.Errorf("issue=%+v", issues[0])
	}
}

func TestPunctuationWrapLoss_TrimmedTargetEdges(t *testing.T) {
	c := NewPunctuationWrapLossChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「彼は行くと言った」", TargetText: "  他说要去  "},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(issues), issues)
	}
	if issues[0].Code != CheckPunctuationWrapLoss || issues[0].Severity != SeverityWarning {
		t.Errorf("issue=%+v", issues[0])
	}
}

func TestPunctuationWrapLoss_RubyInSourceNotMisreported(t *testing.T) {
	c := NewPunctuationWrapLossChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「見て<ruby>呪<rt>じゅ</rt></ruby>術」", TargetText: "看咒术", Protected: nil},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(issues), issues)
	}
	if issues[0].Code != CheckPunctuationWrapLoss || issues[0].Severity != SeverityWarning {
		t.Errorf("issue=%+v", issues[0])
	}
}
