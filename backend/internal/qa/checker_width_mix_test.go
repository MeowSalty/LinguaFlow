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

// ruby 标签的 <> 不在 Protected 时，width_mix 仍应屏蔽整个 <ruby> 元素，不得把 < 当半角标点误报。
func TestWidthMix_RubyTagNotReported(t *testing.T) {
	c := NewWidthMixChecker("zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "雷神皇", TargetText: "<ruby>雷神皇<rt>艾福达尔</rt></ruby>", Protected: nil},
	})
	if len(issues) != 0 {
		t.Fatalf("ruby tags should not trigger width_mix: %+v", issues)
	}
}

// ruby 与 Protected span 同时存在时，两者区域并集屏蔽。
func TestWidthMix_RubyWithProtectedSpan(t *testing.T) {
	c := NewWidthMixChecker("zh")
	span := `<a href="x">連</a>`
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: span + "<ruby>雷<rt>lei</rt></ruby>神", Protected: map[string]string{"__LF_000001__": span}},
	})
	if len(issues) != 0 {
		t.Fatalf("ruby + protected span should not trigger width_mix: %+v", issues)
	}
}

// 真阳性能被检测；不因屏蔽 ruby 而漏掉保护区外的真实半角标点。
func TestWidthMix_RealHalfwidthOutsideRubyReported(t *testing.T) {
	c := NewWidthMixChecker("zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: "你好,世界<ruby>雷<rt>lei</rt></ruby>神", Protected: nil},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 for real halfwidth comma outside ruby, got %d: %+v", len(issues), issues)
	}
}

// 无 Protected 时通用标签屏蔽应兜住 <a href="x">，不得把 < 当半角标点误报（手动编辑从 DB 重载场景）。
func TestWidthMix_UnprotectedTagNotReported(t *testing.T) {
	c := NewWidthMixChecker("zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "雷神皇", TargetText: `<a href="x">連</a>神`, Protected: nil},
	})
	if len(issues) != 0 {
		t.Fatalf("unprotected bare tag should not trigger width_mix: %+v", issues)
	}
}

// 防过度屏蔽：标签外的真实半角逗号仍应检出。
func TestWidthMix_RealHalfwidthOutsideUnprotectedTagReported(t *testing.T) {
	c := NewWidthMixChecker("zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: `你好,世界<a href="x">連</a>`, Protected: nil},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 for real halfwidth comma outside unprotected tag, got %d: %+v", len(issues), issues)
	}
	if issues[0].Code != CheckWidthMix {
		t.Errorf("code=%s", issues[0].Code)
	}
}
