package qa

import (
	"context"
	"strings"
	"testing"
	"unicode"
)

func TestSourceResidual_StrongKana(t *testing.T) {
	c := NewSourceResidualChecker("ja", "en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "これはテストです", TargetText: "This is テスト residual"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 issue, got %d", len(issues))
	}
	assertResidualIssue(t, issues[0], 0)
	if !strings.Contains(issues[0].Message, "テスト") {
		t.Errorf("message should contain テスト, got %q", issues[0].Message)
	}
}

func TestSourceResidual_StrongCyrillic(t *testing.T) {
	c := NewSourceResidualChecker("ru", "zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "Привет мир", TargetText: "你好 Привет"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 issue, got %d", len(issues))
	}
	assertResidualIssue(t, issues[0], 0)
}

func TestSourceResidual_StrongHangul(t *testing.T) {
	c := NewSourceResidualChecker("ko", "en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "안녕하세요", TargetText: "Hello 안 residual"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 issue, got %d", len(issues))
	}
	assertResidualIssue(t, issues[0], 0)
}

func TestSourceResidual_StrongBengali(t *testing.T) {
	c := NewSourceResidualChecker("bn", "en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "নমস্কার", TargetText: "Hello নম residual"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 issue, got %d", len(issues))
	}
	assertResidualIssue(t, issues[0], 0)
}

func TestSourceResidual_SemiStrongHan(t *testing.T) {
	c := NewSourceResidualChecker("zh", "en")

	t.Run("hit minRun 2", func(t *testing.T) {
		// 源侧 run「你好」出现在译文
		issues := c.Check(context.Background(), []CheckInput{
			{Index: 0, SourceText: "你好世界", TargetText: "Hello 你好 world"},
		})
		if len(issues) != 1 {
			t.Fatalf("want 1 issue, got %d", len(issues))
		}
		assertResidualIssue(t, issues[0], 0)
	})

	t.Run("single char skip", func(t *testing.T) {
		issues := c.Check(context.Background(), []CheckInput{
			{Index: 0, SourceText: "你 好", TargetText: "Hello 你 world"},
		})
		if len(issues) != 0 {
			t.Fatalf("single Han run should not hit minRun, got %d", len(issues))
		}
	})

	t.Run("anchor miss", func(t *testing.T) {
		issues := c.Check(context.Background(), []CheckInput{
			{Index: 0, SourceText: "你好世界", TargetText: "Hello 漢字 not in source"},
		})
		if len(issues) != 0 {
			t.Fatalf("Han not in source should not hit, got %d", len(issues))
		}
	})

	t.Run("partial residual from long source", func(t *testing.T) {
		// 源整段 Han 未完整出现，但译文中的「你好」是源的子串
		issues := c.Check(context.Background(), []CheckInput{
			{Index: 0, SourceText: "你好世界", TargetText: "Hello 你好 world"},
		})
		if len(issues) != 1 {
			t.Fatalf("partial residual want 1, got %d", len(issues))
		}
	})
}

func TestSourceResidual_MediumJaZh(t *testing.T) {
	c := NewSourceResidualChecker("ja", "zh")

	t.Run("han anchor hit glued", func(t *testing.T) {
		// CJK 粘连：源侧 Han run「東京」应在译文中被命中
		issues := c.Check(context.Background(), []CheckInput{
			{Index: 0, SourceText: "東京に行く", TargetText: "前往東京"},
		})
		if len(issues) != 1 {
			t.Fatalf("want 1 issue (Han anchor), got %d: %v", len(issues), issues)
		}
		assertResidualIssue(t, issues[0], 0)
		if !strings.Contains(issues[0].Message, "東京") {
			t.Errorf("message should contain 東京, got %q", issues[0].Message)
		}
	})

	t.Run("kana strong hit", func(t *testing.T) {
		issues := c.Check(context.Background(), []CheckInput{
			{Index: 0, SourceText: "テストです", TargetText: "这是テスト"},
		})
		if len(issues) != 1 {
			t.Fatalf("want 1 issue for kana residual, got %d", len(issues))
		}
		assertResidualIssue(t, issues[0], 0)
		if !strings.Contains(issues[0].Message, "テスト") {
			t.Errorf("message should contain テスト, got %q", issues[0].Message)
		}
	})
}

func TestSourceResidual_WeakZhJaDefaultOff(t *testing.T) {
	c := NewSourceResidualChecker("zh", "ja")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "你好世界朋友", TargetText: "こんにちは你好世界朋友です"},
	})
	if len(issues) != 0 {
		t.Fatalf("weak tier should be off by default, got %d issues", len(issues))
	}
	if weakTierEnabled {
		t.Fatal("test assumes weakTierEnabled=false")
	}
}

func TestSourceResidual_WeakZhKoDefaultOff(t *testing.T) {
	c := NewSourceResidualChecker("zh", "ko")
	if len(c.rules) != 0 {
		t.Fatalf("zh→ko weak Han should be off by default, got %d rules", len(c.rules))
	}
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "你好世界朋友", TargetText: "안녕 你好世界朋友"},
	})
	if len(issues) != 0 {
		t.Fatalf("zh→ko should not report Han residual when weak off, got %d", len(issues))
	}
}

func TestResolveRules_ZhKoIsWeakNotSemi(t *testing.T) {
	// ko 含 Han 时 zh→ko 不应进入准强档
	rules := resolveRules("zh", "ko")
	if weakTierEnabled {
		if len(rules) != 1 || rules[0].tier != tierWeak {
			t.Fatalf("when weak on, want 1 weak rule, got %+v", rules)
		}
		return
	}
	if len(rules) != 0 {
		t.Fatalf("when weak off, zh→ko want 0 rules, got %d", len(rules))
	}
}

func TestSourceResidual_NotApplicable(t *testing.T) {
	t.Run("en to fr", func(t *testing.T) {
		c := NewSourceResidualChecker("en", "fr")
		if len(c.rules) != 0 {
			t.Fatalf("en→fr should have no rules, got %d", len(c.rules))
		}
		issues := c.Check(context.Background(), []CheckInput{
			{Index: 0, SourceText: "hello", TargetText: "bonjour テスト leftover"},
		})
		if len(issues) != 0 {
			t.Fatalf("want 0 issues, got %d", len(issues))
		}
	})

	t.Run("source auto", func(t *testing.T) {
		c := NewSourceResidualChecker("auto", "en")
		if len(c.rules) != 0 {
			t.Fatalf("source=auto should have no rules, got %d", len(c.rules))
		}
	})
}

func TestSourceResidual_SrcEqTgtDedup(t *testing.T) {
	c := NewSourceResidualChecker("ja", "en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "これはテストです", TargetText: "これはテストです"},
	})
	if len(issues) != 0 {
		t.Fatalf("src==tgt should skip residual (untranslated owns it), got %d", len(issues))
	}
}

func TestSourceResidual_PlaceholderStripped(t *testing.T) {
	c := NewSourceResidualChecker("ja", "en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "テスト __LF_PH_0__", TargetText: "test テスト __LF_PH_0__"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 issue after placeholder strip, got %d", len(issues))
	}
	if strings.Contains(issues[0].Message, "__LF_") {
		t.Errorf("message should not contain placeholder, got %q", issues[0].Message)
	}
}

func TestSourceResidual_MinRunCyrillicSingleSkip(t *testing.T) {
	c := NewSourceResidualChecker("ru", "en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "Привет", TargetText: "Hello П"},
	})
	if len(issues) != 0 {
		t.Fatalf("single cyrillic should not hit (minRun=2), got %d", len(issues))
	}
}

func TestResolveRules_JaEn(t *testing.T) {
	rules := resolveRules("ja", "en")
	// strong kana + semi-strong Han
	if len(rules) < 2 {
		t.Fatalf("ja→en want >=2 rules, got %d", len(rules))
	}
	var hasKana, hasHan bool
	for _, r := range rules {
		for _, s := range r.script {
			if s == unicode.Hiragana || s == unicode.Katakana {
				hasKana = true
			}
			if s == unicode.Han {
				hasHan = true
			}
		}
	}
	if !hasKana {
		t.Error("ja→en should have kana rule")
	}
	if !hasHan {
		t.Error("ja→en should have Han rule")
	}
}

func TestNormalizeLang(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"zh-CN", "zh"},
		{"ja_JP", "ja"},
		{"EN", "en"},
		{"", ""},
		{"  fr-FR  ", "fr"},
	}
	for _, tt := range tests {
		if got := normalizeLang(tt.in); got != tt.want {
			t.Errorf("normalizeLang(%q)=%q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestExtractScriptRuns(t *testing.T) {
	runs := extractScriptRuns("abテストcd漢字ef", []*unicode.RangeTable{unicode.Hiragana, unicode.Katakana})
	if len(runs) != 1 || runs[0] != "テスト" {
		t.Errorf("got %v, want [テスト]", runs)
	}
	runs = extractScriptRuns("你好 world 世界", []*unicode.RangeTable{unicode.Han})
	if len(runs) != 2 || runs[0] != "你好" || runs[1] != "世界" {
		t.Errorf("got %v, want [你好 世界]", runs)
	}
}

func assertResidualIssue(t *testing.T, issue QualityIssue, index int) {
	t.Helper()
	if issue.Code != "source_residual" {
		t.Errorf("code=%q, want source_residual", issue.Code)
	}
	if issue.Severity != SeverityWarning {
		t.Errorf("severity=%q, want warning", issue.Severity)
	}
	if issue.SegmentIndex != index {
		t.Errorf("index=%d, want %d", issue.SegmentIndex, index)
	}
}
