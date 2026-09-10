package qa

import (
	"context"
	"testing"
)

// TestUntranslatedChecker_LanguagePairSeverities 表驱动覆盖语言对感知的三分
// severity：源语独有脚本出现在回传文本中即 error，共用文字系统降 warning，
// 语言对不可知（auto/空/未知）保守报 error。
func TestUntranslatedChecker_LanguagePairSeverities(t *testing.T) {
	cases := []struct {
		name         string
		srcLang      string
		tgtLang      string
		source       string
		target       string
		wantIssues   int
		wantSeverity IssueSeverity // wantIssues 为 0 时不校验
		wantMatched  string        // 非空时断言 span.MatchedText
	}{
		{
			name:         "ja→zh 纯汉字 identity 降 warning（逐字保留可为正确译法）",
			srcLang:      "ja",
			tgtLang:      "zh",
			source:       "北海道大学",
			target:       "北海道大学",
			wantIssues:   1,
			wantSeverity: SeverityWarning,
			wantMatched:  "北海道大学",
		},
		{
			name:         "ja→zh 含假名 identity 报 error（假名是真回传的强证据）",
			srcLang:      "ja",
			tgtLang:      "zh",
			source:       "これは日本語です",
			target:       "これは日本語です",
			wantIssues:   1,
			wantSeverity: SeverityError,
			wantMatched:  "これは日本語です",
		},
		{
			name:         "zh→en 汉字 identity 报 error（Han 为源语独有脚本）",
			srcLang:      "zh",
			tgtLang:      "en",
			source:       "你好世界",
			target:       "你好世界",
			wantIssues:   1,
			wantSeverity: SeverityError,
			wantMatched:  "你好世界",
		},
		{
			name:         "en→de identity 降 warning（共用拉丁字母，可能为专有名词）",
			srcLang:      "en",
			tgtLang:      "de",
			source:       "Marketing",
			target:       "Marketing",
			wantIssues:   1,
			wantSeverity: SeverityWarning,
			wantMatched:  "Marketing",
		},
		{
			name:         "auto→zh identity 保守报 error（source_lang 默认 auto，不可降级）",
			srcLang:      "auto",
			tgtLang:      "zh",
			source:       "Marketing",
			target:       "Marketing",
			wantIssues:   1,
			wantSeverity: SeverityError,
			wantMatched:  "Marketing",
		},
		{
			name:         "空 srcLang 保守报 error",
			srcLang:      "",
			tgtLang:      "zh",
			source:       "Marketing",
			target:       "Marketing",
			wantIssues:   1,
			wantSeverity: SeverityError,
			wantMatched:  "Marketing",
		},
		{
			name:         "空 tgtLang 保守报 error",
			srcLang:      "en",
			tgtLang:      "",
			source:       "Marketing",
			target:       "Marketing",
			wantIssues:   1,
			wantSeverity: SeverityError,
			wantMatched:  "Marketing",
		},
		{
			name:         "zh→ko 汉字 identity 报 error（现代韩文正文以谚文书写，且该语言对无其他兜底）",
			srcLang:      "zh",
			tgtLang:      "ko",
			source:       "你好世界",
			target:       "你好世界",
			wantIssues:   1,
			wantSeverity: SeverityError,
			wantMatched:  "你好世界",
		},
		{
			name:         "ko→zh 纯汉字 identity 降 warning（韩文汉字词逐字保留可为正确译法）",
			srcLang:      "ko",
			tgtLang:      "zh",
			source:       "大韓民國",
			target:       "大韓民國",
			wantIssues:   1,
			wantSeverity: SeverityWarning,
			wantMatched:  "大韓民國",
		},
		{
			name:         "ko→zh 含谚文 identity 报 error",
			srcLang:      "ko",
			tgtLang:      "zh",
			source:       "안녕하세요",
			target:       "안녕하세요",
			wantIssues:   1,
			wantSeverity: SeverityError,
			wantMatched:  "안녕하세요",
		},
		{
			name:       "译文与原文不同不报",
			srcLang:    "en",
			tgtLang:    "de",
			source:     "Hello",
			target:     "Bonjour",
			wantIssues: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewUntranslatedChecker(tc.srcLang, tc.tgtLang)
			issues := c.Check(context.Background(), []CheckInput{
				{Index: 0, SourceText: tc.source, TargetText: tc.target},
			})
			if len(issues) != tc.wantIssues {
				t.Fatalf("src=%q tgt=%q want %d issues, got %d: %+v",
					tc.source, tc.target, tc.wantIssues, len(issues), issues)
			}
			if tc.wantIssues == 0 {
				return
			}
			issue := issues[0]
			if issue.Code != CheckUntranslated {
				t.Errorf("code=%s, want %s", issue.Code, CheckUntranslated)
			}
			if issue.Severity != tc.wantSeverity {
				t.Errorf("severity=%s, want %s", issue.Severity, tc.wantSeverity)
			}
			if issue.Span == nil || issue.Span.MatchedText != tc.wantMatched {
				t.Errorf("want matched %q, got span %+v", tc.wantMatched, issue.Span)
			}
		})
	}
}

// TestUntranslatedChecker_PlaceholderExemption 回归占位符豁免：豁免判断剥离
// __LF_* token 后看剩余文本；__LF_ 本身含字母，不剥离会令"占位符+纯数字"
// 漏判，而"仅占位符"的段落也无需翻译。
func TestUntranslatedChecker_PlaceholderExemption(t *testing.T) {
	cases := []struct {
		name    string
		srcLang string
		tgtLang string
		text    string
	}{
		{
			name:    "仅占位符 identity 不报",
			srcLang: "en",
			tgtLang: "de",
			text:    "__LF_000001__",
		},
		{
			name:    "占位符+纯数字 identity 不报",
			srcLang: "en",
			tgtLang: "de",
			text:    "__LF_000001__ 123",
		},
		{
			name:    "占位符+实义文本 identity 仍检出且降 warning",
			srcLang: "en",
			tgtLang: "de",
			text:    "__LF_000001__ Marketing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewUntranslatedChecker(tc.srcLang, tc.tgtLang)
			issues := c.Check(context.Background(), []CheckInput{
				{Index: 0, SourceText: tc.text, TargetText: tc.text},
			})
			want := 0
			if tc.text == "__LF_000001__ Marketing" {
				want = 1
			}
			if len(issues) != want {
				t.Fatalf("text=%q want %d issues, got %d: %+v", tc.text, want, len(issues), issues)
			}
			if want == 0 {
				return
			}
			if issues[0].Severity != SeverityWarning {
				t.Errorf("severity=%s, want warning", issues[0].Severity)
			}
			// Span 应定位到完整 trim 后文本（含占位符）。
			if issues[0].Span == nil || issues[0].Span.MatchedText != tc.text {
				t.Errorf("want matched %q, got span %+v", tc.text, issues[0].Span)
			}
		})
	}
}

// TestUntranslatedChecker 基础检出与豁免行为（含 Span 偏移）。
func TestUntranslatedChecker(t *testing.T) {
	checker := NewUntranslatedChecker("en", "de")

	tests := []struct {
		name         string
		source       string
		target       string
		wantIssues   int
		wantSeverity IssueSeverity
		wantMatched  string
		wantOffsets  bool // span 是否应带 rune 偏移
	}{
		{
			name:         "untranslated detected",
			source:       "Hello World",
			target:       "Hello World",
			wantIssues:   1,
			wantSeverity: SeverityWarning,
			wantMatched:  "Hello World",
			wantOffsets:  true,
		},
		{
			name:       "translated passes",
			source:     "Hello",
			target:     "你好",
			wantIssues: 0,
		},
		{
			name:       "pure numbers exempt",
			source:     "123",
			target:     "123",
			wantIssues: 0,
		},
		{
			name:       "pure punctuation exempt",
			source:     "...",
			target:     "...",
			wantIssues: 0,
		},
		{
			name:         "前后空白的 identity 在 trim 后检出，span 定位原文",
			source:       " Hello ",
			target:       "Hello",
			wantIssues:   1,
			wantSeverity: SeverityWarning,
			wantMatched:  "Hello",
			wantOffsets:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := checker.Check(context.Background(), []CheckInput{
				{Index: 0, SourceText: tt.source, TargetText: tt.target},
			})
			if len(issues) != tt.wantIssues {
				t.Fatalf("source=%q target=%q want %d issues, got %d: %+v",
					tt.source, tt.target, tt.wantIssues, len(issues), issues)
			}
			if tt.wantIssues == 0 {
				return
			}
			issue := issues[0]
			if issue.Code != CheckUntranslated {
				t.Errorf("code=%s, want %s", issue.Code, CheckUntranslated)
			}
			if issue.Severity != tt.wantSeverity {
				t.Errorf("severity=%s, want %s", issue.Severity, tt.wantSeverity)
			}
			if issue.Span == nil {
				t.Fatal("expected span on untranslated issue")
			}
			if issue.Span.MatchedText != tt.wantMatched {
				t.Errorf("matched=%q, want %q", issue.Span.MatchedText, tt.wantMatched)
			}
			if tt.wantOffsets && (issue.Span.TargetStart == nil || issue.Span.TargetEnd == nil) {
				t.Errorf("expected rune offsets, got span %+v", issue.Span)
			}
		})
	}
}

// 引擎集成：语言对经 buildAllCheckers 注入——ja→zh 纯汉字 identity 端到端
// 产出 1 条 warning，证明 NewUntranslatedChecker 收到了 Config 的语言对。
func TestUntranslatedChecker_EngineRun(t *testing.T) {
	e := NewEngine(Config{Enabled: true, Checks: []string{CheckUntranslated}, SourceLang: "ja", TargetLang: "zh"}, nil)
	issues := e.Run(context.Background(), []CheckInput{
		{Index: 0, SourceText: "北海道大学", TargetText: "北海道大学"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 issue, got %d: %+v", len(issues), issues)
	}
	if issues[0].Code != CheckUntranslated {
		t.Errorf("code=%s, want %s", issues[0].Code, CheckUntranslated)
	}
	if issues[0].Severity != SeverityWarning {
		t.Errorf("severity=%s, want warning（ja→zh 汉字共用）", issues[0].Severity)
	}
}
