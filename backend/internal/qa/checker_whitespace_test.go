package qa

import (
	"context"
	"testing"
)

func TestWhitespaceIrregular_ZWSP(t *testing.T) {
	c := NewWhitespaceIrregularChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "hello\u200bworld"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Code != CheckWhitespaceIrregular {
		t.Errorf("code=%s", issues[0].Code)
	}
}

func TestWhitespaceIrregular_NBSP(t *testing.T) {
	c := NewWhitespaceIrregularChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "hello\u00a0world"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
}

func TestWhitespaceIrregular_TrimOnlyExempt(t *testing.T) {
	c := NewWhitespaceIrregularChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "  hello world  "},
	})
	if len(issues) != 0 {
		t.Fatalf("trim-only should not flag, got %d", len(issues))
	}
}

func TestRepeatedSpace_Double(t *testing.T) {
	c := NewRepeatedSpaceChecker("en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "hello  world"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Code != CheckRepeatedSpace {
		t.Errorf("code=%s", issues[0].Code)
	}
}

func TestRepeatedSpace_CJKBetween(t *testing.T) {
	c := NewRepeatedSpaceChecker("zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "你 好"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 CJK space, got %d", len(issues))
	}
}

func TestRepeatedSpace_LatinOK(t *testing.T) {
	c := NewRepeatedSpaceChecker("en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "hello world"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d", len(issues))
	}
}

func TestWhitespaceIrregular_ProtectedNBSP(t *testing.T) {
	c := NewWhitespaceIrregularChecker()
	tag := "hello\u00a0world"
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: tag, Protected: map[string]string{"__LF_000001__": tag}},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d: %+v", len(issues), issues)
	}
}

func TestRepeatedSpace_ProtectedDouble(t *testing.T) {
	c := NewRepeatedSpaceChecker("en")
	tag := "hello  world"
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: tag, Protected: map[string]string{"__LF_000001__": tag}},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d: %+v", len(issues), issues)
	}
}

// 保护区两侧各一个空格（原文无连续空格），剔除后拼接不应制造虚假命中。
func TestRepeatedSpace_AcrossProtectedRegion(t *testing.T) {
	c := NewRepeatedSpaceChecker("en")
	tgt := "hello <br/> world"
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: tgt, Protected: map[string]string{"__LF_000001__": "<br/>"}},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0 (原文无连续空格，'  ' 仅为 StripRegions 拼接产物), got %d: %+v", len(issues), issues)
	}
}

// 保护区两侧空格 + CJK 目标语：CJK 间空格跨边界同样不应误报。
func TestRepeatedSpace_AcrossProtectedRegionCJK(t *testing.T) {
	c := NewRepeatedSpaceChecker("zh")
	tgt := "你 <br/> 我"
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: tgt, Protected: map[string]string{"__LF_000001__": "<br/>"}},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0 (CJK 间空格是 StripRegions 拼接产物), got %d: %+v", len(issues), issues)
	}
}

// 保护区外存在真实连续空格时仍应报 issue，且 span 指向保护区外。
func TestRepeatedSpace_RealDoubleOutsideProtected(t *testing.T) {
	c := NewRepeatedSpaceChecker("en")
	// 真实连续空格在 world 后；保护区 <br/> 两侧各一空格（不应误报）
	tgt := "hello<br/> world  end"
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: tgt, Protected: map[string]string{"__LF_000001__": "<br/>"}},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(issues), issues)
	}
	span := issues[0].Span
	if span == nil || span.TargetStart == nil {
		t.Fatalf("want span with offsets, got %+v", span)
	}
	// "hello<br/> world  end"：连续空格在 rune 14 (<br/> 之后 world+空格x2)
	// 保护区 <br/> 在 rune [5,10]，world 在 [11,16]，空格在 [16,18]
	if *span.TargetStart != 16 || *span.TargetEnd != 18 {
		t.Fatalf("want start=16 end=18, got start=%d end=%d", *span.TargetStart, *span.TargetEnd)
	}
}
