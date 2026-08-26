package correct

import (
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// wdIssue 构造一条带 span 的 pending width_mix issue（checker 产出形态）。
func wdIssue(matchedText string) []qa.QualityIssue {
	return []qa.QualityIssue{{
		Code:     qa.CheckWidthMix,
		Severity: qa.SeverityWarning,
		Span:     &qa.Span{MatchedText: matchedText},
	}}
}

func TestWidthMixNormalize_CJKPositive(t *testing.T) {
	seg := &model.Segment{
		Target: "那是什么!?",
		Issues: wdIssue("!"),
	}
	res := (&WidthMixNormalizeRule{}).Apply(seg)
	if !res.Changed {
		t.Fatalf("want changed, got %+v", res)
	}
	if res.NewTarget != "那是什么！？" {
		t.Errorf("NewTarget=%q", res.NewTarget)
	}
	if res.Op != "width_mix.normalize" {
		t.Errorf("Op=%q", res.Op)
	}
	if len(res.ResolvedCodes) != 1 || res.ResolvedCodes[0] != qa.CheckWidthMix {
		t.Errorf("ResolvedCodes=%v", res.ResolvedCodes)
	}
	if seg.Target != "那是什么!?" {
		t.Errorf("Target mutated: %q", seg.Target)
	}
	// 幂等自检：改写结果再喂回 Apply 应 no-op（全角已无可转字符）。
	seg2 := &model.Segment{Target: "那是什么！？", Issues: wdIssue("!")}
	res2 := (&WidthMixNormalizeRule{}).Apply(seg2)
	if res2.Changed || !strings.Contains(res2.Reason, "no convertible") {
		t.Fatalf("second apply should be no-op, got %+v", res2)
	}
}

// 一次 Apply 转换 clean 文本中全部命中字符：规则由文本驱动，不受 first-hit issue 的
// MatchedText 只标记首个命中所限。
func TestWidthMixNormalize_CJKMultipleHitsAllConverted(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{"double bang", "好!好!", "好！好！"},
		{"mixed run", "什么?!", "什么？！"},
	}
	for _, tt := range tests {
		seg := &model.Segment{Target: tt.target, Issues: wdIssue("!")}
		res := (&WidthMixNormalizeRule{}).Apply(seg)
		if !res.Changed || res.NewTarget != tt.want {
			t.Errorf("%s: got %+v, want NewTarget=%q", tt.name, res, tt.want)
		}
	}
}

func TestWidthMixNormalize_CJKPunctsConvert(t *testing.T) {
	tests := []struct {
		name   string
		target string
		hit    string
		want   string
	}{
		{"comma after digit but before letter", "3,但是", ",", "3，但是"},
		{"colon after CJK", "他说:你好", ":", "他说：你好"},
		{"parens have no guard even digit-prefixed", "100(约)", "(", "100（约）"},
		{"semicolon no guard", "对;错", ";", "对；错"},
	}
	for _, tt := range tests {
		seg := &model.Segment{Target: tt.target, Issues: wdIssue(tt.hit)}
		res := (&WidthMixNormalizeRule{}).Apply(seg)
		if !res.Changed || res.NewTarget != tt.want {
			t.Errorf("%s: got %+v, want NewTarget=%q", tt.name, res, tt.want)
		}
	}
}

// 阴性 CJK：数字双侧守卫（, :）与数字前缀 run 守卫（!?）豁免的命中不改写。
func TestWidthMixNormalize_CJKDigitGuardsNoop(t *testing.T) {
	tests := []struct {
		name   string
		target string
		hit    string
	}{
		{"digit flanked colon", "12:30", ":"},
		{"digit flanked comma", "1,000", ","},
		{"digit prefixed bang", "5!", "!"},
		{"digit prefixed mixed run", "100!?", "!"},
	}
	for _, tt := range tests {
		seg := &model.Segment{Target: tt.target, Issues: wdIssue(tt.hit)}
		res := (&WidthMixNormalizeRule{}).Apply(seg)
		if res.Changed {
			t.Errorf("%s: want no-op, got %+v", tt.name, res)
		}
		if !strings.Contains(res.Reason, "no convertible") {
			t.Errorf("%s: Reason=%q, want reason about no convertible char", tt.name, res.Reason)
		}
		if seg.Target != tt.target {
			t.Errorf("%s: Target mutated: %q", tt.name, seg.Target)
		}
	}
}

func TestWidthMixNormalize_LatinPositive(t *testing.T) {
	tests := []struct {
		name   string
		target string
		hit    string
		want   string
	}{
		{"fullwidth letters direction from letter", "ＡＢ！", "Ａ", "AB!"},
		{"direction from fullwidth punct too", "ＡＢ！", "！", "AB!"},
		{"fullwidth digits", "０１", "０", "01"},
	}
	for _, tt := range tests {
		seg := &model.Segment{Target: tt.target, Issues: wdIssue(tt.hit)}
		res := (&WidthMixNormalizeRule{}).Apply(seg)
		if !res.Changed || res.NewTarget != tt.want {
			t.Errorf("%s: got %+v, want NewTarget=%q", tt.name, res, tt.want)
		}
		if res.Op != "width_mix.normalize" {
			t.Errorf("%s: Op=%q", tt.name, res.Op)
		}
	}
	// 拉丁幂等自检：已半角化文本再喂回应 no-op。
	seg := &model.Segment{Target: "AB!", Issues: wdIssue("Ａ")}
	res := (&WidthMixNormalizeRule{}).Apply(seg)
	if res.Changed || !strings.Contains(res.Reason, "no convertible") {
		t.Fatalf("latin second apply should be no-op, got %+v", res)
	}
}

func TestWidthMixNormalize_NoIssueNoop(t *testing.T) {
	seg := &model.Segment{Target: "那是什么!?"}
	res := (&WidthMixNormalizeRule{}).Apply(seg)
	if res.Changed {
		t.Fatalf("want no-op, got %+v", res)
	}
	if res.Reason != "no width_mix issue" {
		t.Errorf("Reason=%q", res.Reason)
	}
	if seg.Target != "那是什么!?" {
		t.Errorf("Target mutated: %q", seg.Target)
	}
}

// dismissed issue 对规则不可见：机械修复不得推翻用户裁决。
func TestWidthMixNormalize_DismissedIssueInvisible(t *testing.T) {
	seg := &model.Segment{
		Target: "那是什么!?",
		Issues: []qa.QualityIssue{{
			Code:        qa.CheckWidthMix,
			Severity:    qa.SeverityWarning,
			Disposition: qa.DispositionDismissed,
			Span:        &qa.Span{MatchedText: "!"},
		}},
	}
	res := (&WidthMixNormalizeRule{}).Apply(seg)
	if res.Changed {
		t.Fatalf("dismissed issue must not trigger the rule, got %+v", res)
	}
	if res.Reason != "no width_mix issue" {
		t.Errorf("Reason=%q", res.Reason)
	}
}

// 混合裁决状态：dismissed 条目存在时，pending 条目仍正常触发。
func TestWidthMixNormalize_MixedDispositionsTriggers(t *testing.T) {
	seg := &model.Segment{
		Target: "好!",
		Issues: []qa.QualityIssue{
			{Code: qa.CheckWidthMix, Severity: qa.SeverityWarning,
				Disposition: qa.DispositionDismissed,
				Span:        &qa.Span{MatchedText: "@"}},
			{Code: qa.CheckWidthMix, Severity: qa.SeverityWarning,
				Span: &qa.Span{MatchedText: "!"}},
		},
	}
	res := (&WidthMixNormalizeRule{}).Apply(seg)
	if !res.Changed || res.NewTarget != "好！" {
		t.Fatalf("got %+v, want NewTarget=好！", res)
	}
}

// span 缺失 / MatchedText 为空 / 首 rune 不属任一方向集合 → 方向不可识别，
// 绝不猜测，no-op 保留 issue 给人工。
func TestWidthMixNormalize_UnreadableSpanNoop(t *testing.T) {
	tests := []struct {
		name  string
		issue qa.QualityIssue
	}{
		{"nil span", qa.QualityIssue{Code: qa.CheckWidthMix, Severity: qa.SeverityWarning}},
		{"empty matched text", qa.QualityIssue{Code: qa.CheckWidthMix, Severity: qa.SeverityWarning,
			Span: &qa.Span{MatchedText: ""}}},
		{"unrecognized char", qa.QualityIssue{Code: qa.CheckWidthMix, Severity: qa.SeverityWarning,
			Span: &qa.Span{MatchedText: "@"}}},
	}
	for _, tt := range tests {
		seg := &model.Segment{Target: "那是什么!?", Issues: []qa.QualityIssue{tt.issue}}
		res := (&WidthMixNormalizeRule{}).Apply(seg)
		if res.Changed {
			t.Errorf("%s: want no-op, got %+v", tt.name, res)
		}
		if !strings.Contains(res.Reason, "unreadable") {
			t.Errorf("%s: Reason=%q, want reason about unreadable span", tt.name, res.Reason)
		}
		if seg.Target != "那是什么!?" {
			t.Errorf("%s: Target mutated: %q", tt.name, seg.Target)
		}
	}
}

// 保护区内的命中不改写：ruby 元素（含基底与注音）整体原样保留，只改区外文末的 `!`。
// 守卫在 clean 基准上判定——基底里的数字 `1` 不参与守卫，故文末 `!` 正常转换。
func TestWidthMixNormalize_ProtectedRubyRegionPreserved(t *testing.T) {
	seg := &model.Segment{
		Source: "漢字（かんじ）!",
		Target: "<ruby>1<rt>one</rt></ruby>!",
		Issues: wdIssue("!"),
	}
	res := (&WidthMixNormalizeRule{}).Apply(seg)
	if !res.Changed {
		t.Fatalf("want changed, got %+v", res)
	}
	if res.NewTarget != "<ruby>1<rt>one</rt></ruby>！" {
		t.Errorf("NewTarget=%q, ruby block must stay verbatim", res.NewTarget)
	}
}

// Protected 映射通道同样屏蔽：保护片段内的半角标点不参与转换（若脱离 clean 基准
// 在原文上改写，(a,b) 内的 ( ) , 都会被误转全角），区外文末 `!` 正常转换。
func TestWidthMixNormalize_ProtectedMapPreserved(t *testing.T) {
	seg := &model.Segment{
		Target:    "(a,b) ok!",
		Protected: map[string]string{"(a,b)": "(a,b)"},
		Issues:    wdIssue("!"),
	}
	res := (&WidthMixNormalizeRule{}).Apply(seg)
	if !res.Changed {
		t.Fatalf("want changed, got %+v", res)
	}
	if res.NewTarget != "(a,b) ok！" {
		t.Errorf("NewTarget=%q, protected region must stay verbatim", res.NewTarget)
	}
}
