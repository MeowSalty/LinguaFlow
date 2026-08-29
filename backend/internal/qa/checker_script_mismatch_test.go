package qa

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// TestScriptMismatch_ChineseVariants 表驱动覆盖 zh-Hans / zh-Hant / sr-Latn
// 目标下的正负用例：兄弟文字专属字符出现即报（≥1），共用字与目标文字不报。
func TestScriptMismatch_ChineseVariants(t *testing.T) {
	cases := []struct {
		name        string
		targetLang  string
		target      string
		wantIssues  int
		wantMatched string   // 非空时断言 span.MatchedText
		msgContains []string // message 必须包含的子串
	}{
		{
			name:        "zh-Hans 纯繁体译文报繁体汉字",
			targetLang:  "zh-Hans",
			target:      "這是一個繁體中文的測試段落",
			wantIssues:  1,
			wantMatched: "這",
			msgContains: []string{"繁体汉字", "zh-Hans", "简体汉字"},
		},
		{
			name:       "zh-Hans 纯简体译文不报",
			targetLang: "zh-Hans",
			target:     "这是一个简体中文的测试段落",
			wantIssues: 0,
		},
		{
			name:       "zh-Hans 全共用字不报",
			targetLang: "zh-Hans",
			target:     "你好,人山人海。",
			wantIssues: 0,
		},
		{
			name:        "zh-Hans 简体混入单个繁体字",
			targetLang:  "zh-Hans",
			target:      "这里是简体,裡面混了一个繁体字。",
			wantIssues:  1,
			wantMatched: "裡",
			msgContains: []string{"繁体汉字", "zh-Hans"},
		},
		{
			name:        "zh-Hant 目标混入简体报简体汉字",
			targetLang:  "zh-Hant",
			target:      "这里应该是繁体",
			wantIssues:  1,
			msgContains: []string{"简体汉字", "zh-Hant", "繁体汉字"},
		},
		{
			name:        "sr-Latn 目标混入西里尔字母",
			targetLang:  "sr-Latn",
			target:      "Dobro došli, Жељко!",
			wantIssues:  1,
			wantMatched: "Жељко",
			msgContains: []string{"西里尔字母", "sr-Latn", "拉丁字母"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewScriptMismatchChecker(tc.targetLang)
			issues := c.Check(context.Background(), []CheckInput{
				{Index: 0, SourceText: "x", TargetText: tc.target},
			})
			if len(issues) != tc.wantIssues {
				t.Fatalf("target=%q want %d issues, got %d: %+v", tc.target, tc.wantIssues, len(issues), issues)
			}
			if tc.wantIssues == 0 {
				return
			}
			issue := issues[0]
			if issue.Code != CheckScriptMismatch {
				t.Errorf("code=%s, want %s", issue.Code, CheckScriptMismatch)
			}
			if issue.Severity != SeverityWarning {
				t.Errorf("severity=%s, want warning", issue.Severity)
			}
			if tc.wantMatched != "" && (issue.Span == nil || issue.Span.MatchedText != tc.wantMatched) {
				t.Errorf("want matched %q, got span %+v", tc.wantMatched, issue.Span)
			}
			if issue.Span == nil || issue.Span.MatchedText == "" {
				t.Errorf("span matched text should be a non-empty run, got %+v", issue.Span)
			}
			for _, sub := range tc.msgContains {
				if !strings.Contains(issue.Message, sub) {
					t.Errorf("message %q should contain %q", issue.Message, sub)
				}
			}
		})
	}
}

// 不活跃目标（单文字系统语言 / auto / 空串 / 垃圾串）对任何译文均静默。
func TestScriptMismatch_InactiveTargetsSilent(t *testing.T) {
	for _, lang := range []string{"en", "ja", "auto", "", "not-a-lang!!"} {
		c := NewScriptMismatchChecker(lang)
		issues := c.Check(context.Background(), []CheckInput{
			{Index: 0, SourceText: "x", TargetText: "這是一個繁體中文的測試段落"},
		})
		if len(issues) != 0 {
			t.Errorf("lang=%q want 0 issues (inactive), got %d: %+v", lang, len(issues), issues)
		}
	}
}

// 空译文与纯空白译文跳过检测。
func TestScriptMismatch_EmptyTargetSkipped(t *testing.T) {
	c := NewScriptMismatchChecker("zh-Hans")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: ""},
		{Index: 1, SourceText: "x", TargetText: "   \n\t "},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0 issues for empty targets, got %d: %+v", len(issues), issues)
	}
}

// 繁体字全部落在 Protected 占位符还原区域内时不报（与 width_mix 的保护区屏蔽同口径）。
func TestScriptMismatch_ProtectedRegionNotReported(t *testing.T) {
	c := NewScriptMismatchChecker("zh-Hans")
	tag := `<a href="p-006.xhtml">這裡是繁體內容</a>`
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "中間試験の結果", TargetText: tag, Protected: map[string]string{"__LF_000001__": tag}},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0 issues (traditional chars fully inside protected region), got %d: %+v", len(issues), issues)
	}
}

// 保护区外的真实繁体字仍应检出，防止过度屏蔽。
func TestScriptMismatch_TraditionalOutsideProtectedReported(t *testing.T) {
	c := NewScriptMismatchChecker("zh-Hans")
	tag := `<a href="p-006.xhtml">期中考试结果</a>`
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: tag + "這裡", Protected: map[string]string{"__LF_000001__": tag}},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 issue for traditional chars outside protected region, got %d: %+v", len(issues), issues)
	}
	if issues[0].Span == nil || issues[0].Span.MatchedText != "這裡" {
		t.Errorf("want matched 這裡 outside protected region, got span %+v", issues[0].Span)
	}
}

// 注册断言：checker 名单、零配置名单、可筛名单与引擎注册表均包含 script_mismatch，
// 且在 AllCheckerNames 中紧跟 width_mix 之后。
func TestScriptMismatch_Registration(t *testing.T) {
	names := AllCheckerNames()
	found := false
	for i, n := range names {
		if n == CheckScriptMismatch {
			found = true
			if i == 0 || names[i-1] != CheckWidthMix {
				t.Errorf("script_mismatch 应紧跟 width_mix 之后，实际前置为 %q", names[i-1])
			}
			break
		}
	}
	if !found {
		t.Errorf("AllCheckerNames 缺少 %q: %v", CheckScriptMismatch, names)
	}

	if !slices.Contains(ZeroConfigDeterministicChecks(), CheckScriptMismatch) {
		t.Errorf("ZeroConfigDeterministicChecks 缺少 %q", CheckScriptMismatch)
	}
	if !slices.Contains(FilterableIssueCodes(), CheckScriptMismatch) {
		t.Errorf("FilterableIssueCodes 缺少 %q", CheckScriptMismatch)
	}
	if !IsFilterableIssueCode(CheckScriptMismatch) {
		t.Errorf("IsFilterableIssueCode(%q) = false", CheckScriptMismatch)
	}

	e := NewEngine(Config{Enabled: true, TargetLang: "zh-Hans"}, nil)
	found = false
	for _, c := range e.checkers {
		if c.Name() == CheckScriptMismatch {
			found = true
			break
		}
	}
	if !found {
		t.Error("NewEngine 未注册 script_mismatch checker")
	}
}

// 引擎集成：zh-Hans 目标对繁体译文 Run 出 script_mismatch issue。
func TestScriptMismatch_EngineRun(t *testing.T) {
	e := NewEngine(Config{Enabled: true, TargetLang: "zh-Hans"}, nil)
	issues := e.Run(context.Background(), []CheckInput{
		{Index: 0, SourceText: "This is a test", TargetText: "這是一個繁體中文的測試段落"},
	})
	found := false
	for _, issue := range issues {
		if issue.Code == CheckScriptMismatch {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Engine.Run 未产出 script_mismatch issue: %+v", issues)
	}
}

// 横跨保护区拼接边界：两个繁体字被保护区域隔开，strip 后在 cleanTgt 中相邻成 run，
// 但该 run 在原文中并不连续；issue 仍应报出，span 退化为可在原文定位的首字符。
func TestScriptMismatch_SampleAcrossProtectedBoundary(t *testing.T) {
	c := NewScriptMismatchChecker("zh-Hans")
	tag := `<b></b>`
	tgt := "這" + tag + "裡"
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: tgt, Protected: map[string]string{"__LF_000001__": tag}},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 issue across protected boundary, got %d: %+v", len(issues), issues)
	}
	span := issues[0].Span
	if span == nil || span.MatchedText != "這" {
		t.Fatalf("span 应退化为原文真实存在的首字符「這」，got %+v", span)
	}
	if !strings.Contains(tgt, span.MatchedText) {
		t.Errorf("MatchedText %q 应是原文 %q 的子串", span.MatchedText, tgt)
	}
}

// 两栖字豁免端到端：繁体目标收到含台/准/游等两栖字的正常繁体文本不报。
func TestScriptMismatch_AmphibiousCharsNoFalsePositive(t *testing.T) {
	c := NewScriptMismatchChecker("zh-Hant")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: "台灣批准游泳的長征路線"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0 issues for legitimate traditional text with amphibious chars, got %d: %+v", len(issues), issues)
	}
}
