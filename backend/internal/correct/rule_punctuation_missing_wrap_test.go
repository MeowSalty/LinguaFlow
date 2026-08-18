package correct

import (
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func pmIssue(code string) []qa.QualityIssue {
	return []qa.QualityIssue{{Code: code, Severity: qa.SeverityWarning}}
}

func TestPunctuationMissingWrap_Safe(t *testing.T) {
	seg := &model.Segment{
		Source: "「对话」",
		Target: "对话",
		Issues: pmIssue(qa.CheckPunctuationMissing),
	}
	res := (&PunctuationMissingWrapRule{}).Apply(seg)
	if !res.Changed {
		t.Fatalf("want changed, got %+v", res)
	}
	if res.NewTarget != "「对话」" {
		t.Errorf("NewTarget=%q", res.NewTarget)
	}
	if res.Op != "punctuation_missing.wrap" {
		t.Errorf("Op=%q", res.Op)
	}
	if len(res.ResolvedCodes) != 1 || res.ResolvedCodes[0] != qa.CheckPunctuationMissing {
		t.Errorf("ResolvedCodes=%v", res.ResolvedCodes)
	}
	// 本次 Apply 仅一次改写：再 Apply 一次（issues 移除后）应 no-op。
	seg2 := &model.Segment{Source: "「对话」", Target: "「对话」", Issues: pmIssue(qa.CheckPunctuationMissing)}
	res2 := (&PunctuationMissingWrapRule{}).Apply(seg2)
	if res2.Changed || !strings.Contains(res2.Reason, "quote runes") {
		t.Fatalf("second apply should be no-op, got %+v", res2)
	}
}

func TestPunctuationMissingWrap_MultiSpanNoop(t *testing.T) {
	seg := &model.Segment{
		Source: "「你好」「再见」",
		Target: "你好 再见",
		Issues: pmIssue(qa.CheckPunctuationMissing),
	}
	res := (&PunctuationMissingWrapRule{}).Apply(seg)
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

func TestPunctuationMissingWrap_NoIssueNoop(t *testing.T) {
	seg := &model.Segment{
		Source: "「对话」",
		Target: "对话",
	}
	res := (&PunctuationMissingWrapRule{}).Apply(seg)
	if res.Changed {
		t.Fatalf("want no-op, got %+v", res)
	}
	if res.Reason != "no punctuation_missing issue" {
		t.Errorf("Reason=%q", res.Reason)
	}
}

// dismissed issue 对规则不可见：即使 code 命中，已被用户裁决为 dismiss 的
// issue 不得触发机械修复，避免推翻裁决。
func TestPunctuationMissingWrap_DismissedIssueInvisible(t *testing.T) {
	seg := &model.Segment{
		Source: "「对话」",
		Target: "对话",
		Issues: []qa.QualityIssue{{
			Code:        qa.CheckPunctuationMissing,
			Severity:    qa.SeverityWarning,
			Disposition: qa.DispositionDismissed,
		}},
	}
	res := (&PunctuationMissingWrapRule{}).Apply(seg)
	if res.Changed {
		t.Fatalf("dismissed issue must not trigger the rule, got %+v", res)
	}
	if res.Reason != "no punctuation_missing issue" {
		t.Errorf("Reason=%q", res.Reason)
	}
	if seg.Target != "对话" {
		t.Errorf("Target mutated: %q", seg.Target)
	}
}

// 混合状态：同 code 一条 dismissed（括号类）+ 一条 pending（引号类）。
// pending 存在时规则应正常触发，且不因 dismissed 条目存在而误判。
func TestPunctuationMissingWrap_MixedDispositionsTriggers(t *testing.T) {
	seg := &model.Segment{
		Source: "「（对话）」",
		Target: "对话",
		Issues: []qa.QualityIssue{
			{Code: qa.CheckPunctuationMissing, Severity: qa.SeverityWarning},
			{Code: qa.CheckPunctuationMissing, Severity: qa.SeverityWarning,
				Disposition: qa.DispositionDismissed,
				Span:        &qa.Span{MatchedText: "（）"}},
		},
	}
	res := (&PunctuationMissingWrapRule{}).Apply(seg)
	if !res.Changed {
		t.Fatalf("want changed, got %+v", res)
	}
	if res.NewTarget != "「对话」" {
		t.Errorf("NewTarget=%q", res.NewTarget)
	}
}

func TestPunctuationMissingWrap_NotPairedOpenNoop(t *testing.T) {
	seg := &model.Segment{
		Source: "对话！",
		Target: "对话",
		Issues: pmIssue(qa.CheckPunctuationMissing),
	}
	res := (&PunctuationMissingWrapRule{}).Apply(seg)
	if res.Changed || !strings.Contains(res.Reason, "paired opening quote") {
		t.Fatalf("res=%+v", res)
	}
}

func TestPunctuationMissingWrap_MismatchedCloseNoop(t *testing.T) {
	seg := &model.Segment{
		Source: "「对话」",
		Target: "对话",
		Issues: pmIssue(qa.CheckPunctuationMissing),
	}
	res := (&PunctuationMissingWrapRule{}).Apply(seg)
	if !res.Changed {
		t.Fatal("need a baseline change for this test setup")
	}
	// 闭合不匹配：源首「末” → no-op。
	seg2 := &model.Segment{
		Source: "「对话”",
		Target: "对话",
		Issues: pmIssue(qa.CheckPunctuationMissing),
	}
	res2 := (&PunctuationMissingWrapRule{}).Apply(seg2)
	if res2.Changed || !strings.Contains(res2.Reason, "matching closing") {
		t.Fatalf("res2=%+v", res2)
	}
}

func TestPunctuationMissingWrap_NoContentBetweenNoop(t *testing.T) {
	seg := &model.Segment{
		Source: "「」",
		Target: "",
		Issues: pmIssue(qa.CheckPunctuationMissing),
	}
	res := (&PunctuationMissingWrapRule{}).Apply(seg)
	if res.Changed || !strings.Contains(res.Reason, "no content between") {
		t.Fatalf("res=%+v", res)
	}
}

func TestPunctuationMissingWrap_PlaceholderStaysInside(t *testing.T) {
	seg := &model.Segment{
		// 占位符在 clean 检查中被剥离（__LF_* → 空），剩余仍是合法单 span。
		Source:    "「你好 __LF_1__」",
		Target:    "你好 __LF_1__",
		Protected: map[string]string{"__LF_1__": "ABC"},
		Issues:    pmIssue(qa.CheckPunctuationMissing),
	}
	res := (&PunctuationMissingWrapRule{}).Apply(seg)
	if !res.Changed {
		t.Fatalf("want changed, got %+v", res)
	}
	// 包裹用原始 Target，占位符保留在内部。
	if res.NewTarget != "「你好 __LF_1__」" {
		t.Errorf("NewTarget=%q, placeholder must stay inside wrap", res.NewTarget)
	}
}

func TestPunctuationMissingWrap_DefensiveTargetRunes(t *testing.T) {
	seg := &model.Segment{
		Source: "「对话」",
		Target: "对话「",
		Issues: pmIssue(qa.CheckPunctuationMissing),
	}
	res := (&PunctuationMissingWrapRule{}).Apply(seg)
	if res.Changed || !strings.Contains(res.Reason, "quote runes") {
		t.Fatalf("res=%+v", res)
	}
}

// 源文含引号但不在首尾（句中包裹）：规则应 no-op，issue 保留给人工。
// 这是与"源文整段被引号包裹"场景的关键边界。
func TestPunctuationMissingWrap_QuoteNotAtEdgesNoop(t *testing.T) {
	seg := &model.Segment{
		Source: "这是一条“测试”消息",
		Target: "这是一条测试消息",
		Issues: pmIssue(qa.CheckPunctuationMissing),
	}
	res := (&PunctuationMissingWrapRule{}).Apply(seg)
	if res.Changed {
		t.Fatalf("want no-op for mid-sentence quote, got %+v", res)
	}
	if !strings.Contains(res.Reason, "paired opening quote") {
		t.Errorf("Reason=%q, want reason about paired opening quote", res.Reason)
	}
	if seg.Target != "这是一条测试消息" {
		t.Errorf("Target mutated: %q", seg.Target)
	}
}
