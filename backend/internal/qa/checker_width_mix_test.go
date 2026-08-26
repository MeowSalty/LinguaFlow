package qa

import (
	"context"
	"fmt"
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

// ---- 收窄后的安全集与守卫 ----

// 安全集逐字符阳性：CJK 散文中安全集（! ? , ; : ( ) [ ]）每个字符都应检出，
// 且报 run/位置的首个命中字符，message 用 %q 包裹。
func TestWidthMix_CJKSafeSetEachReported(t *testing.T) {
	cases := []struct {
		target string
		want   string
	}{
		{"好!", "!"},
		{"他?", "?"},
		{"说,", ","},
		{"也;", ";"},
		{"他:你", ":"},
		{"他(约)", "("},
		{"约)", ")"},
		{"他[", "["},
		{"他]", "]"},
	}
	c := NewWidthMixChecker("zh")
	for _, tc := range cases {
		issues := c.Check(context.Background(), []CheckInput{
			{Index: 0, SourceText: "x", TargetText: tc.target},
		})
		if len(issues) != 1 {
			t.Errorf("target=%q want 1 issue, got %d: %+v", tc.target, len(issues), issues)
			continue
		}
		if issues[0].Span == nil || issues[0].Span.MatchedText != tc.want {
			t.Errorf("target=%q want matched %q, got span %+v", tc.target, tc.want, issues[0].Span)
		}
		wantMsg := fmt.Sprintf("CJK 译文中混入半角标点：%q", tc.want)
		if issues[0].Message != wantMsg {
			t.Errorf("target=%q want message %q, got %q", tc.target, wantMsg, issues[0].Message)
		}
	}
}

// 收窄后移除的字符（. " ' { } 与 @#$%^&*-_ =+\|/~<> 及反引号等 B 组符号）
// 在 CJK 散文中不再触发 width_mix。
func TestWidthMix_CJKNarrowedOutCharsNotReported(t *testing.T) {
	targets := []string{
		"他.说", "他\"话\"", "他'话'", "他{", "他}",
		"他@说", "他#号", "他$元", "他%比", "他^号", "他&和", "他*乘",
		"他-连", "他_线", "他=等", "他+加", "他\\斜", "他|竖",
		"他/斜", "他<小于", "他>大于", "他~约", "他`点",
	}
	c := NewWidthMixChecker("zh")
	for _, tgt := range targets {
		issues := c.Check(context.Background(), []CheckInput{
			{Index: 0, SourceText: "x", TargetText: tgt},
		})
		if len(issues) != 0 {
			t.Errorf("target=%q want 0 issues after narrowing, got %d: %+v", tgt, len(issues), issues)
		}
	}
}

// 数字双侧守卫（, :）：两侧紧邻均为 ASCII 数字时豁免；
// 数字前缀 run 守卫（! ?）：!? 连续 run 前紧邻 ASCII 数字时整个 run 豁免。
func TestWidthMix_GuardExemptions(t *testing.T) {
	targets := []string{"12:30", "1,000", "2:1", "5!", "3!", "100!?"}
	c := NewWidthMixChecker("zh")
	for _, tgt := range targets {
		issues := c.Check(context.Background(), []CheckInput{
			{Index: 0, SourceText: "x", TargetText: tgt},
		})
		if len(issues) != 0 {
			t.Errorf("target=%q want 0 issues (guard exemption), got %d: %+v", tgt, len(issues), issues)
		}
	}
}

// 守卫不豁免的场景：非数字邻接的 , : 照常报；!? run 前邻非数字时报首个字符；括号无守卫。
func TestWidthMix_GuardBoundariesStillReported(t *testing.T) {
	cases := []struct {
		target string
		want   string
	}{
		{"3,但是", ","},   // 双侧守卫仅豁免两侧均为数字
		{"他说:你好", ":"},  // 同上
		{"好!?", "!"},    // run 前邻「好」非数字，整体报首个字符
		{"那是什么!?", "!"}, // 同上
		{"100(约)", "("}, // 括号无守卫，前邻数字也报
	}
	c := NewWidthMixChecker("zh")
	for _, tc := range cases {
		issues := c.Check(context.Background(), []CheckInput{
			{Index: 0, SourceText: "x", TargetText: tc.target},
		})
		if len(issues) != 1 {
			t.Errorf("target=%q want 1 issue, got %d: %+v", tc.target, len(issues), issues)
			continue
		}
		if issues[0].Span == nil || issues[0].Span.MatchedText != tc.want {
			t.Errorf("target=%q want matched %q, got span %+v", tc.target, tc.want, issues[0].Span)
		}
	}
}

// 拉丁分支检出全角字母与全角数字（FF01–FF5E 全段），且无条件应用——不存在 CJK 守卫。
func TestWidthMix_LatinFullwidthLettersAndDigits(t *testing.T) {
	c := NewWidthMixChecker("en")
	for _, tgt := range []string{"Ａpple", "nｏ.1", "v０", "a１2"} {
		issues := c.Check(context.Background(), []CheckInput{
			{Index: 0, SourceText: "x", TargetText: tgt},
		})
		if len(issues) != 1 {
			t.Errorf("latin target=%q want 1 issue for fullwidth char, got %d: %+v", tgt, len(issues), issues)
		}
	}
}

// 拉丁分支不检出 FF5F（｟）/FF60（｠）：它们在 ASCII 中无对应字符。
func TestWidthMix_LatinVerticalFormsNotReported(t *testing.T) {
	c := NewWidthMixChecker("en")
	for _, tgt := range []string{"a｟b", "x｠y"} {
		issues := c.Check(context.Background(), []CheckInput{
			{Index: 0, SourceText: "x", TargetText: tgt},
		})
		if len(issues) != 0 {
			t.Errorf("latin target=%q want 0 issues (FF5F/FF60 excluded), got %d: %+v", tgt, len(issues), issues)
		}
	}
}
