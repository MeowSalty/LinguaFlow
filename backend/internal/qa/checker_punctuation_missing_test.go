package qa

import (
	"context"
	"strings"
	"testing"
)

func TestPunctuationMissing_QuoteAbsent(t *testing.T) {
	c := NewPunctuationMissingChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「それは…ですね？」", TargetText: "是…对吗？"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(issues), issues)
	}
	if issues[0].Code != CheckPunctuationMissing || issues[0].Severity != SeverityWarning {
		t.Errorf("issue=%+v", issues[0])
	}
	matched := ""
	if issues[0].Span != nil {
		matched = issues[0].Span.MatchedText
	}
	if !strings.Contains(matched, "「") || !strings.Contains(matched, "」") {
		t.Errorf("matched=%q", matched)
	}
}

func TestPunctuationMissing_FrenchApostrophesNotReported(t *testing.T) {
	c := NewPunctuationMissingChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{
			Index: 0,
			SourceText: "Gambetta découvre un Paris froid et brumeux et s’inscrit rapidement à la faculté de droit. " +
				"Il songe depuis longtemps à ces études juridiques, sachant confusément qu’elles vont lui permettre de sortir d’un milieu certes aimant, mais étriqué. " +
				"Pour les jeunes gens peu fortunés, le droit est la meilleure porte d’accès à des postes en vue. " +
				"Vingt-cinq ans après Gambetta, Aristide Briand, par exemple, fils d’un tenancier de café-concert nantais, suivra la même voie.",
			TargetText: "甘必大抵达巴黎后，很快进入法学院就读。他期盼攻读法学已久，隐约知道这能让他脱离那个虽充满关爱却狭隘闭塞的成长环境。" +
				"对于家境清贫的年轻人来说，法学是跻身显赫职位的最佳通道。比如比甘必大晚二十五年的阿里斯蒂德·白里安，也走了相同的道路。",
		},
	})
	if len(issues) != 0 {
		t.Fatalf("French apostrophes should not trigger punctuation_missing: %+v", issues)
	}
}

func TestPunctuationMissing_ReportsMatchedQuotePair(t *testing.T) {
	c := NewPunctuationMissingChecker()
	for _, tc := range []struct {
		name   string
		source string
		want   string
	}{
		{name: "French guillemets", source: "Il dit « bonjour ». ", want: "«»"},
		{name: "ASCII double quote", source: `He said "hello".`, want: `""`},
		{name: "apostrophe inside single quotes", source: "Il cite ‘l’homme’. ", want: "‘’"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := c.Check(context.Background(), []CheckInput{{Index: 0, SourceText: tc.source, TargetText: "他说了这句话。"}})
			if len(issues) != 1 {
				t.Fatalf("want one missing quote issue, got %d: %+v", len(issues), issues)
			}
			if got := issues[0].Span.MatchedText; got != tc.want {
				t.Fatalf("matched text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPunctuationMissing_ParenAbsent(t *testing.T) {
	c := NewPunctuationMissingChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "見て（附录）", TargetText: "看附录"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(issues), issues)
	}
	if issues[0].Code != CheckPunctuationMissing {
		t.Errorf("issue=%+v", issues[0])
	}
	matched := ""
	if issues[0].Span != nil {
		matched = issues[0].Span.MatchedText
	}
	if !strings.Contains(matched, "（") || !strings.Contains(matched, "）") {
		t.Errorf("matched=%q", matched)
	}
}

func TestPunctuationMissing_BothAbsent(t *testing.T) {
	c := NewPunctuationMissingChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「見て（附录）」", TargetText: "见附录"},
	})
	if len(issues) != 2 {
		t.Fatalf("want 2, got %d: %+v", len(issues), issues)
	}
	seen := map[string]bool{}
	for _, iss := range issues {
		if iss.Code != CheckPunctuationMissing {
			t.Errorf("issue=%+v", iss)
		}
		if iss.Span != nil {
			seen[iss.Span.MatchedText] = true
		}
	}
	if len(seen) != 2 {
		t.Errorf("expect 2 distinct matched, got %v", seen)
	}
}

func TestPunctuationMissing_ReplaceCurlyOK(t *testing.T) {
	c := NewPunctuationMissingChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「a」", TargetText: "“a”"},
	})
	if len(issues) != 0 {
		t.Fatalf("curly replacement should not trigger: %+v", issues)
	}
}

func TestPunctuationMissing_ReplaceASCIIOK(t *testing.T) {
	c := NewPunctuationMissingChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「a」", TargetText: `"a"`},
	})
	if len(issues) != 0 {
		t.Fatalf("ASCII replacement should not trigger: %+v", issues)
	}
}

func TestPunctuationMissing_SingleSidedDelegatedToPairing(t *testing.T) {
	c := NewPunctuationMissingChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "「a」", TargetText: "「a"},
	})
	if len(issues) != 0 {
		t.Fatalf("single-sided target belongs to pairing, got: %+v", issues)
	}
}

func TestPunctuationMissing_SourceUnpairedNotReported(t *testing.T) {
	c := NewPunctuationMissingChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "見て「", TargetText: "看"},
	})
	if len(issues) != 0 {
		t.Fatalf("unpaired source should not trigger: %+v", issues)
	}
}

func TestPunctuationMissing_ProtectedRegion(t *testing.T) {
	c := NewPunctuationMissingChecker()
	tag := `<a href="p-006.xhtml">期中考试结果与安妮玛丽的考察</a>`
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "中間試験結果とアンネマリーの考察", TargetText: tag, Protected: map[string]string{"__LF_000001__": tag}},
	})
	if len(issues) != 0 {
		t.Fatalf("protected HTML quotes should not trigger: %+v", issues)
	}
}

func TestPunctuationMissing_EmptyInputs(t *testing.T) {
	c := NewPunctuationMissingChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "", TargetText: ""},
		{Index: 1, SourceText: "「a」", TargetText: "   "},
		{Index: 2, SourceText: "   ", TargetText: "a"},
	})
	if len(issues) != 0 {
		t.Fatalf("empty inputs should not trigger: %+v", issues)
	}
}

func TestPunctuationMissing_LatinParenAbsent(t *testing.T) {
	c := NewPunctuationMissingChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "see (x)", TargetText: "见x"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(issues), issues)
	}
	if issues[0].Code != CheckPunctuationMissing {
		t.Errorf("issue=%+v", issues[0])
	}
	matched := ""
	if issues[0].Span != nil {
		matched = issues[0].Span.MatchedText
	}
	if !strings.Contains(matched, "(") || !strings.Contains(matched, ")") {
		t.Errorf("matched=%q", matched)
	}
}

// 源文含 <span class="tcy">…</span>，其属性中的两个 ASCII " 不应被当源文引号整类缺失来误报。
// 这是误报1 的回归复现：旧逻辑只在译文侧屏蔽区域、源文未屏蔽，class 属性的两个 " 被计入源文，
// 而译文同形 span 被 StripRegions 删去 → src 计数≥2、tgt==0 → 触发。修复后源文同步屏蔽 → 不触发。
func TestPunctuationMissing_SourceSpanAttributeQuotesNotReported(t *testing.T) {
	c := NewPunctuationMissingChecker()
	span := `<span class="tcy">10</span>`
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: span + "件の表示", TargetText: span + "件的显示", Protected: map[string]string{"__LF_000001__": span}},
	})
	if len(issues) != 0 {
		t.Fatalf("span attribute quotes in source should not trigger punctuation_missing: %+v", issues)
	}
}

// 源文/译文含 ruby 元素时，ruby 不含引号/括号，不应误报。
func TestPunctuationMissing_RubyInSourceNotReported(t *testing.T) {
	c := NewPunctuationMissingChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "見て<ruby>呪<rt>じゅ</rt></ruby>術", TargetText: "看<ruby>咒<rt>zhou</rt></ruby>术", Protected: nil},
	})
	if len(issues) != 0 {
		t.Fatalf("ruby in source should not trigger punctuation_missing: %+v", issues)
	}
}
