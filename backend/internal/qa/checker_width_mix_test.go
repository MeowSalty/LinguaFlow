package qa

import (
	"context"
	"testing"
)

func TestWidthMix_CJKHalfwidthPunct(t *testing.T) {
	c := NewWidthMixChecker("zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "你好,世界"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Code != CheckWidthMix {
		t.Errorf("code=%s", issues[0].Code)
	}
}

func TestWidthMix_CJKFullwidthOK(t *testing.T) {
	c := NewWidthMixChecker("zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "你好，世界"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d", len(issues))
	}
}

func TestWidthMix_LatinFullwidth(t *testing.T) {
	c := NewWidthMixChecker("en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "Hello，world"}, // fullwidth comma U+FF0C
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
}

func TestWidthMix_LatinOK(t *testing.T) {
	c := NewWidthMixChecker("en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "Hello, world!"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d", len(issues))
	}
}

func TestWidthMix_ProtectedTagNotReported(t *testing.T) {
	c := NewWidthMixChecker("zh")
	tag := `<a href="p-006.xhtml">期中考试结果与安妮玛丽的考察</a>`
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "中間試験結果とアンネマリーの考察", TargetText: tag, Protected: map[string]string{"__LF_000001__": tag}},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d: %+v", len(issues), issues)
	}
}

func TestWidthMix_ProtectedTagHalfwidthOutside(t *testing.T) {
	c := NewWidthMixChecker("zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: "你好,<a href=\"x\">链接</a>", Protected: map[string]string{"__LF_000001__": `<a href="x">链接</a>`}},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 for halfwidth comma outside protected region, got %d: %+v", len(issues), issues)
	}
}

// 多个占位符映射到同一保护值（如重复 <br/>）时，所有副本都必须被屏蔽。
func TestWidthMix_ProtectedTagRepeated(t *testing.T) {
	c := NewWidthMixChecker("zh")
	target := `第一行<br/>第二行<br/>`
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: target, Protected: map[string]string{"__LF_000001__": "<br/>", "__LF_000002__": "<br/>"}},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d: %+v", len(issues), issues)
	}
}

// 保护区内部含半角逗号、译文末尾也有半角逗号时，span 应指向保护区外的真实问题字符。
func TestWidthMix_SpanOutsideProtected(t *testing.T) {
	c := NewWidthMixChecker("zh")
	tgt := `<a,x>你好,`
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: tgt, Protected: map[string]string{"__LF_000001__": `<a,x>`}},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(issues), issues)
	}
	span := issues[0].Span
	if span == nil || span.TargetStart == nil {
		t.Fatalf("want span with offsets, got %+v", span)
	}
	// 真实逗号在 rune 7（<a,x>=5 + 你好=2），保护区内逗号在 rune 2。
	if *span.TargetStart != 7 || *span.TargetEnd != 8 {
		t.Fatalf("want start=7 end=8 (outside protected), got start=%d end=%d", *span.TargetStart, *span.TargetEnd)
	}
}
