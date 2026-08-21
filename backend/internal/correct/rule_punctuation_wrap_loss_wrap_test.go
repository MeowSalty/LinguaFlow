package correct

import (
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func TestPunctuationWrapLossWrap_Safe(t *testing.T) {
	seg := &model.Segment{
		Source: "「对话」",
		Target: "对话",
		Issues: pmIssue(qa.CheckPunctuationWrapLoss),
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if !res.Changed {
		t.Fatalf("want changed, got %+v", res)
	}
	if res.NewTarget != "「对话」" {
		t.Errorf("NewTarget=%q", res.NewTarget)
	}
	if res.Op != "punctuation_wrap_loss.wrap" {
		t.Errorf("Op=%q", res.Op)
	}
	if len(res.ResolvedCodes) != 1 || res.ResolvedCodes[0] != qa.CheckPunctuationWrapLoss {
		t.Errorf("ResolvedCodes=%v", res.ResolvedCodes)
	}

	seg.Target = res.NewTarget
	res2 := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if res2.Changed || !strings.Contains(res2.Reason, "quote runes") {
		t.Fatalf("second apply should be no-op, got %+v", res2)
	}
}

func TestPunctuationWrapLossWrap_InnerQuotesStillWrapped(t *testing.T) {
	seg := &model.Segment{
		Source: "「彼は“行く”と言った」",
		Target: "他说“要去”",
		Issues: pmIssue(qa.CheckPunctuationWrapLoss),
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if !res.Changed {
		t.Fatalf("want changed, got %+v", res)
	}
	if res.NewTarget != "「他说“要去”」" {
		t.Errorf("NewTarget=%q", res.NewTarget)
	}
}

func TestPunctuationWrapLossWrap_NoIssueNoop(t *testing.T) {
	seg := &model.Segment{Source: "「对话」", Target: "对话"}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if res.Changed {
		t.Fatalf("want no-op, got %+v", res)
	}
	if res.Reason != "no punctuation_wrap_loss issue" {
		t.Errorf("Reason=%q", res.Reason)
	}
}

func TestPunctuationWrapLossWrap_DismissedIssueInvisible(t *testing.T) {
	seg := &model.Segment{
		Source: "「对话」",
		Target: "对话",
		Issues: []qa.QualityIssue{{
			Code:        qa.CheckPunctuationWrapLoss,
			Severity:    qa.SeverityWarning,
			Disposition: qa.DispositionDismissed,
		}},
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if res.Changed {
		t.Fatalf("dismissed issue must not trigger the rule, got %+v", res)
	}
	if res.Reason != "no punctuation_wrap_loss issue" {
		t.Errorf("Reason=%q", res.Reason)
	}
}

func TestPunctuationWrapLossWrap_MultiSpanNoop(t *testing.T) {
	seg := &model.Segment{
		Source: "「你好」「再见」",
		Target: "你好 再见",
		Issues: pmIssue(qa.CheckPunctuationWrapLoss),
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if res.Changed {
		t.Fatalf("want no-op, got %+v", res)
	}
	if !strings.Contains(res.Reason, "multi-span") {
		t.Errorf("Reason=%q", res.Reason)
	}
	if seg.Target != "你好 再见" {
		t.Errorf("Target mutated: %q", seg.Target)
	}
}

func TestPunctuationWrapLossWrap_NotPairedOpenNoop(t *testing.T) {
	seg := &model.Segment{
		Source: "对话！",
		Target: "对话",
		Issues: pmIssue(qa.CheckPunctuationWrapLoss),
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if res.Changed || !strings.Contains(res.Reason, "paired opening quote") {
		t.Fatalf("res=%+v", res)
	}
}

func TestPunctuationWrapLossWrap_MismatchedCloseNoop(t *testing.T) {
	seg := &model.Segment{
		Source: "「对话”",
		Target: "对话",
		Issues: pmIssue(qa.CheckPunctuationWrapLoss),
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if res.Changed || !strings.Contains(res.Reason, "matching closing") {
		t.Fatalf("res=%+v", res)
	}
}

func TestPunctuationWrapLossWrap_NoContentBetweenNoop(t *testing.T) {
	seg := &model.Segment{
		Source: "「」",
		Target: "x",
		Issues: pmIssue(qa.CheckPunctuationWrapLoss),
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if res.Changed || !strings.Contains(res.Reason, "no content between") {
		t.Fatalf("res=%+v", res)
	}
}

func TestPunctuationWrapLossWrap_PlaceholderStaysInside(t *testing.T) {
	seg := &model.Segment{
		Source:    "「你好 __LF_1__」",
		Target:    "你好 __LF_1__",
		Protected: map[string]string{"__LF_1__": "ABC"},
		Issues:    pmIssue(qa.CheckPunctuationWrapLoss),
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if !res.Changed {
		t.Fatalf("want changed, got %+v", res)
	}
	if res.NewTarget != "「你好 __LF_1__」" {
		t.Errorf("NewTarget=%q, placeholder must stay inside wrap", res.NewTarget)
	}
}

func TestPunctuationWrapLossWrap_EdgeQuoteBlocksWrap(t *testing.T) {
	seg := &model.Segment{
		Source: "「对话」",
		Target: "“对话”",
		Issues: pmIssue(qa.CheckPunctuationWrapLoss),
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if res.Changed || !strings.Contains(res.Reason, "quote runes") {
		t.Fatalf("res=%+v", res)
	}
}

func TestPunctuationWrapLossWrap_SingleSidedEdgeBlocksWrap(t *testing.T) {
	seg := &model.Segment{
		Source: "「对话」",
		Target: "「对话",
		Issues: pmIssue(qa.CheckPunctuationWrapLoss),
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if res.Changed || !strings.Contains(res.Reason, "quote runes") {
		t.Fatalf("res=%+v", res)
	}
}

func TestPunctuationWrapLossWrap_UnbalancedInteriorSamePairNoop(t *testing.T) {
	seg := &model.Segment{
		Source: "「对话」",
		Target: "他「说",
		Issues: pmIssue(qa.CheckPunctuationWrapLoss),
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if res.Changed {
		t.Fatalf("interior unbalanced same-pair quote must not wrap, got %+v", res)
	}
	if !strings.Contains(res.Reason, "interior quotes") {
		t.Errorf("Reason=%q", res.Reason)
	}
}

func TestPunctuationWrapLossWrap_UnbalancedInteriorOtherPairNoop(t *testing.T) {
	seg := &model.Segment{
		Source: "「彼はこう言った」",
		Target: "她说“这样",
		Issues: pmIssue(qa.CheckPunctuationWrapLoss),
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if res.Changed {
		t.Fatalf("interior unbalanced other-pair quote must not wrap, got %+v", res)
	}
	if !strings.Contains(res.Reason, "interior quotes") {
		t.Errorf("Reason=%q", res.Reason)
	}
}

func TestPunctuationWrapLossWrap_BalancedInteriorStillWraps(t *testing.T) {
	seg := &model.Segment{
		Source: "「对话」",
		Target: "他「说」了",
		Issues: pmIssue(qa.CheckPunctuationWrapLoss),
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if !res.Changed {
		t.Fatalf("balanced interior quote should still wrap, got %+v", res)
	}
	if res.NewTarget != "「他「说」了」" {
		t.Errorf("NewTarget=%q", res.NewTarget)
	}
}

func TestPunctuationWrapLossWrap_SourceQuotesInsideMarkupNoop(t *testing.T) {
	seg := &model.Segment{
		Source:    "__LF_1__「你好」__LF_2__",
		Target:    "__LF_1__你好__LF_2__",
		Protected: map[string]string{"__LF_1__": "<b>", "__LF_2__": "</b>"},
		Issues:    pmIssue(qa.CheckPunctuationWrapLoss),
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if res.Changed {
		t.Fatalf("source quotes inside markup must not wrap (would invert structure), got %+v", res)
	}
	if !strings.Contains(res.Reason, "protected markup") {
		t.Errorf("Reason=%q", res.Reason)
	}
}

func TestPunctuationWrapLossWrap_SourceQuotesOutsideMarkupWraps(t *testing.T) {
	seg := &model.Segment{
		Source:    "「__LF_1__你好__LF_2__」",
		Target:    "__LF_1__你好__LF_2__",
		Protected: map[string]string{"__LF_1__": "<b>", "__LF_2__": "</b>"},
		Issues:    pmIssue(qa.CheckPunctuationWrapLoss),
	}
	res := (&PunctuationWrapLossWrapRule{}).Apply(seg)
	if !res.Changed {
		t.Fatalf("source quotes at raw boundary should wrap, got %+v", res)
	}
	if res.NewTarget != "「__LF_1__你好__LF_2__」" {
		t.Errorf("NewTarget=%q", res.NewTarget)
	}
}
