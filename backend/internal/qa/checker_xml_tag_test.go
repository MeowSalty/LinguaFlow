package qa

import (
	"context"
	"strings"
	"testing"
)

func TestXMLTag_Mismatch(t *testing.T) {
	c := NewXMLTagMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: `<b>hi</b>`, TargetText: `<i>hi</i>`},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Code != CheckXMLTagMismatch || issues[0].Severity != SeverityError {
		t.Errorf("issue=%+v", issues[0])
	}
}

func TestXMLTag_Match(t *testing.T) {
	c := NewXMLTagMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: `<span class="x">a</span>`, TargetText: `<span class="x">甲</span>`},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d: %+v", len(issues), issues)
	}
}

func TestXMLTag_SkipWhenSourceNoTags(t *testing.T) {
	c := NewXMLTagMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: `plain text`, TargetText: `<b>plain</b>`},
	})
	if len(issues) != 0 {
		t.Fatalf("source without tags should skip, got %d", len(issues))
	}
}

func TestXMLTag_RubyFilteredNoFalsePositive(t *testing.T) {
	c := NewXMLTagMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: `<ruby>友里花<rt>ゆりか</rt></ruby>。`, TargetText: `友里花。`},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0 after ruby filter exclusion, got %d: %+v", len(issues), issues)
	}
}

func TestXMLTag_RubyPreservedBothSides(t *testing.T) {
	c := NewXMLTagMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: `<ruby>呪<rt>じゅ</rt></ruby>`, TargetText: `<ruby>呪<rt>じゅ</rt></ruby>`},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0 when ruby tags match on both sides, got %d: %+v", len(issues), issues)
	}
}

func TestXMLTag_RubyDoesNotMaskOtherTagMismatch(t *testing.T) {
	c := NewXMLTagMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: `<b><ruby>x<rt>y</rt></ruby></b>`, TargetText: `<i>x</i>`},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 for non-ruby tag mismatch, got %d: %+v", len(issues), issues)
	}
	if issues[0].Code != CheckXMLTagMismatch || issues[0].Severity != SeverityError {
		t.Errorf("issue=%+v", issues[0])
	}
}

func TestXMLTag_BrokenRubyStructureReported(t *testing.T) {
	// 译文缺 </ruby>：标签总数守恒但嵌套非法。修复前 ruby 族被多重集比对
	// 显式排除、多重集又看不见嵌套，此形态完全漏检；结构判定必须抓到。
	c := NewXMLTagMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{
			Index:      0,
			SourceText: `「レベル６：<ruby>劣化雷神皇<rt>レツサーエフタル</rt></ruby>っ！」`,
			TargetText: `「等级６：<ruby>劣化雷神皇<rt>雷瑟</rt>！」`,
		},
	})
	if len(issues) != 1 {
		t.Fatalf("want exactly 1 issue for missing </ruby>, got %d: %+v", len(issues), issues)
	}
	iss := issues[0]
	if iss.Code != CheckXMLTagMismatch {
		t.Errorf("Code = %q, want %q", iss.Code, CheckXMLTagMismatch)
	}
	if iss.Severity != SeverityError {
		t.Errorf("Severity = %q, want %q", iss.Severity, SeverityError)
	}
	if iss.Span == nil || iss.Span.MatchedText == "" {
		t.Errorf("Span 应非 nil 且 MatchedText 非空, got %+v", iss.Span)
	}
}

func TestXMLTag_RubyRemovalToPlainTextNoFalsePositive(t *testing.T) {
	// preserve_kinds 合法移除全部 ruby 后译文是纯文本：结构平衡，不应误报。
	c := NewXMLTagMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{
			Index:      0,
			SourceText: `<ruby>友里花<rt>ゆりか</rt></ruby>が現れた。`,
			TargetText: `友里花出现了。`,
		},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0 after legal ruby removal, got %d: %+v", len(issues), issues)
	}
}

func TestXMLTag_InvalidSourceSkipped(t *testing.T) {
	// 遗留数据的 source_text 含裸 &（提取期未转义）：无法要求译文比原文更严格，
	// 译文同样含裸 & 不应误报。
	c := NewXMLTagMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{
			Index:      0,
			SourceText: `予兆は & <ruby>未知<rt>みち</rt></ruby> のまま`,
			TargetText: `预兆仍是 & 未知`,
		},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0 when source itself is invalid, got %d: %+v", len(issues), issues)
	}
}

func TestXMLTag_PlainTextPseudoTagsNoFalsePositive(t *testing.T) {
	// 纯文本格式（游戏文本）的译文里出现 <color=red> 之类伪标签：
	// 源文不含标签，不受结构约束，不应误报。
	c := NewXMLTagMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: `普通台词`, TargetText: `<color=red>红色台词`},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0 for pseudo tags in plain text format, got %d: %+v", len(issues), issues)
	}
}

func TestXMLTag_StructuralPrecedesMultiset(t *testing.T) {
	// 一段译文既结构损坏（未闭合）又标签数量不一致（<b></b> 换成 <i>）：
	// 只报 1 条 issue（每段最多一条 xml_tag_mismatch），且指向结构问题。
	c := NewXMLTagMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{
			Index:      0,
			SourceText: `<b>加粗</b><ruby>漢字<rt>かんじ</rt></ruby>`,
			TargetText: `<i>加粗<rt>かんじ`,
		},
	})
	if len(issues) != 1 {
		t.Fatalf("want exactly 1 issue, got %d: %+v", len(issues), issues)
	}
	if issues[0].Code != CheckXMLTagMismatch || issues[0].Severity != SeverityError {
		t.Errorf("issue=%+v", issues[0])
	}
	if !strings.Contains(issues[0].Message, "结构") {
		t.Errorf("message 应指向结构问题, got %q", issues[0].Message)
	}
}
