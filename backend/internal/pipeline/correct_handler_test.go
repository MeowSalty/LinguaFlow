package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/correct"
	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func newCorrectHandler() *CorrectHandler {
	rules := correct.New(correct.Config{
		Rules: []correct.RuleConfig{
			{Name: correct.RulePunctuationMissingWrap, Enabled: true},
		},
	})
	return &CorrectHandler{
		Rules:       rules,
		Idempotency: qa.NewEngine(qa.Config{Enabled: true, Checks: rules.ConsumedIssueCodes()}, nil),
		Logger:      quietLogger(),
	}
}

func TestCorrectHandler_ModeAndFinalize(t *testing.T) {
	h := newCorrectHandler()
	if h.ModeName() != RoundModeCorrect {
		t.Fatalf("ModeName=%q", h.ModeName())
	}
	if err := h.Finalize(context.Background(), &Document{}, nil); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}

func TestCorrectHandler_ProcessBatchAppliesWrap(t *testing.T) {
	h := newCorrectHandler()
	doc := &Document{Segments: []model.Segment{
		{
			ID:     "0",
			Source: "「对话」",
			Target: "对话",
			Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing, Severity: qa.SeverityWarning}},
			Status: "translated",
		},
	}}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
	if got := doc.Segments[0].Target; got != "「对话」" {
		t.Fatalf("Target=%q", got)
	}
	if len(doc.Segments[0].Issues) != 0 {
		t.Fatalf("punctuation_missing should be cleared, got %+v", doc.Segments[0].Issues)
	}
	seg := result.callbackResult.Segments[0]
	if seg.TargetText != "「对话」" {
		t.Errorf("callback TargetText=%q", seg.TargetText)
	}
	if len(seg.Issues) != 0 {
		t.Errorf("callback issues=%+v", seg.Issues)
	}
	if seg.Index != 0 || seg.ID != "0" || seg.SourceText != "「对话」" {
		t.Errorf("callback seg=%+v", seg)
	}
}

func TestCorrectHandler_ProcessBatchNoIssueKeepsTarget(t *testing.T) {
	h := newCorrectHandler()
	doc := &Document{Segments: []model.Segment{
		{ID: "0", Source: "「对话」", Target: "对话", Status: "translated"},
	}}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if got := doc.Segments[0].Target; got != "对话" {
		t.Fatalf("Target mutated: %q", got)
	}
	if len(doc.Segments[0].Issues) != 0 {
		t.Fatalf("issues=%+v", doc.Segments[0].Issues)
	}
	seg := result.callbackResult.Segments[0]
	if seg.TargetText != "对话" || len(seg.Issues) != 0 {
		t.Errorf("callback seg=%+v", seg)
	}
}

// 幂等失败：源含引号+括号两类，包裹只修复引号；重跑 checker 仍报括号类
// punctuation_missing → 回退 Target、保留原 issues。
func TestCorrectHandler_IdempotencyFailReverts(t *testing.T) {
	h := newCorrectHandler()
	issues := []qa.QualityIssue{{Code: qa.CheckPunctuationMissing, Severity: qa.SeverityWarning}}
	doc := &Document{Segments: []model.Segment{
		{
			ID:     "0",
			Source: "「（对话）」",
			Target: "对话",
			Issues: issues,
			Status: "translated",
		},
	}}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if got := doc.Segments[0].Target; got != "对话" {
		t.Fatalf("Target should be reverted, got %q", got)
	}
	if len(doc.Segments[0].Issues) != 1 || doc.Segments[0].Issues[0].Code != qa.CheckPunctuationMissing {
		t.Fatalf("issues should be preserved, got %+v", doc.Segments[0].Issues)
	}
	seg := result.callbackResult.Segments[0]
	if seg.TargetText != "对话" {
		t.Errorf("callback TargetText=%q", seg.TargetText)
	}
	if len(seg.Issues) != 1 {
		t.Errorf("callback issues=%+v", seg.Issues)
	}
}

// 混合状态：引号类 pending（触发包裹）+ 括号类 dismissed（用户已裁决非问题）。
// 包裹后括号类仍缺，但其指纹命中 dismissed 集合 → 幂等检查豁免，不误回滚；
// qa.FilterOutPendingByCodes 保留 dismissed 记录（避免未来以 pending 复活）。
func TestCorrectHandler_DismissedExemptFromIdempotency(t *testing.T) {
	h := newCorrectHandler()
	issues := []qa.QualityIssue{
		{Code: qa.CheckPunctuationMissing, Severity: qa.SeverityWarning},
		{Code: qa.CheckPunctuationMissing, Severity: qa.SeverityWarning,
			Disposition: qa.DispositionDismissed,
			Span:        &qa.Span{MatchedText: "（）"}},
	}
	doc := &Document{Segments: []model.Segment{
		{
			ID:     "0",
			Source: "「（对话）」",
			Target: "对话",
			Issues: issues,
			Status: "translated",
		},
	}}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if got := doc.Segments[0].Target; got != "「对话」" {
		t.Fatalf("Target should be applied (dismissed bracket exempt), got %q", got)
	}
	if len(doc.Segments[0].Issues) != 1 || !doc.Segments[0].Issues[0].Dismissed() {
		t.Fatalf("want only the dismissed issue kept, got %+v", doc.Segments[0].Issues)
	}
	seg := result.callbackResult.Segments[0]
	if seg.TargetText != "「对话」" {
		t.Errorf("callback TargetText=%q", seg.TargetText)
	}
	if len(seg.Issues) != 1 || !seg.Issues[0].Dismissed() {
		t.Errorf("callback issues=%+v, want only dismissed kept", seg.Issues)
	}
}

func TestCorrectHandler_BuildBatchesScan(t *testing.T) {
	h := newCorrectHandler()
	doc := &Document{
		Segments: []model.Segment{
			{ID: "0", Source: "「a」", Target: "a", Status: "translated", Translate: true, Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing}}},
			{ID: "1", Source: "「b」", Target: "b", Status: "edited", Translate: true, Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing}}},
			{ID: "2", Source: "「c」", Target: "c", Status: "pending", Translate: true, Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing}}},
			{ID: "3", Source: "「d」", Target: "", Status: "translated", Translate: true, Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing}}},
			{ID: "4", Source: "「e」", Target: "e", Status: "translated", Translate: true, Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing}}},
		},
		ResolvedIndices: map[int]struct{}{4: {}},
	}
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("want single batch, got %v", batches)
	}
	if !sliceEq(batches[0], []int{0, 1}) {
		t.Fatalf("batches=%v want [0 1]", batches)
	}
}

// 段落选择：未选段（Translate=false）即便 status=translated/edited 且含 issue，
// 也不进入 correct 扫描，避免用户仅选中部分段落时误改未选译文。
func TestCorrectHandler_BuildBatchesRespectsSegmentSelection(t *testing.T) {
	h := newCorrectHandler()
	doc := &Document{
		Segments: []model.Segment{
			{ID: "0", Source: "「a」", Target: "a", Status: "translated", Translate: true, Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing}}},
			{ID: "1", Source: "「b」", Target: "b", Status: "edited", Translate: false, Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing}}},
			{ID: "2", Source: "「c」", Target: "c", Status: "translated", Translate: false, Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing}}},
		},
	}
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}
	if len(batches) != 1 || !sliceEq(batches[0], []int{0}) {
		t.Fatalf("batches=%v want [[0]] (Translate=false excluded)", batches)
	}
}

func TestCorrectHandler_BuildBatchesPendingReslice(t *testing.T) {
	h := newCorrectHandler()
	doc := &Document{Segments: []model.Segment{{ID: "0", Target: "a"}}}
	batches, err := h.BuildBatches(context.Background(), doc, []int{7, 8}, 1)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}
	if len(batches) != 1 || !sliceEq(batches[0], []int{7, 8}) {
		t.Fatalf("batches=%v want [[7 8]]", batches)
	}
}

func TestCorrectHandler_BuildBatchesNoRules(t *testing.T) {
	h := &CorrectHandler{Rules: correct.New(correct.Config{}), Logger: quietLogger()}
	doc := &Document{Segments: []model.Segment{{ID: "0", Target: "a", Status: "translated"}}}
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}
	if len(batches) != 0 {
		t.Fatalf("batches=%v want empty", batches)
	}
}

func TestCorrectHandler_ProcessBatchNoRulesNoop(t *testing.T) {
	h := &CorrectHandler{Rules: correct.New(correct.Config{}), Logger: quietLogger()}
	doc := &Document{Segments: []model.Segment{{ID: "0", Target: "a"}}}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
}

func TestCorrectHandler_PlaceholderWrappedStaysInside(t *testing.T) {
	h := newCorrectHandler()
	doc := &Document{Segments: []model.Segment{
		{
			ID:        "0",
			Source:    "「你好 __LF_1__」",
			Target:    "你好 __LF_1__",
			Protected: map[string]string{"__LF_1__": "ABC"},
			Issues:    []qa.QualityIssue{{Code: qa.CheckPunctuationMissing, Severity: qa.SeverityWarning}},
			Status:    "translated",
		},
	}}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if got := doc.Segments[0].Target; got != "「你好 __LF_1__」" {
		t.Fatalf("Target=%q", got)
	}
	if seg := result.callbackResult.Segments[0]; !strings.Contains(seg.TargetText, "__LF_1__") {
		t.Fatalf("placeholder lost: %q", seg.TargetText)
	}
}

// 规则 no-op（引号在句中非首尾）时：Target 不变、原 issue 原样保留。
func TestCorrectHandler_RuleNoopPreservesIssues(t *testing.T) {
	h := newCorrectHandler()
	doc := &Document{Segments: []model.Segment{
		{
			ID:     "0",
			Source: "这是一条“测试”消息",
			Target: "这是一条测试消息",
			Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing, Severity: qa.SeverityWarning}},
			Status: "translated",
		},
	}}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if got := doc.Segments[0].Target; got != "这是一条测试消息" {
		t.Fatalf("Target should not be mutated, got %q", got)
	}
	if len(doc.Segments[0].Issues) != 1 || doc.Segments[0].Issues[0].Code != qa.CheckPunctuationMissing {
		t.Fatalf("issue should be preserved, got %+v", doc.Segments[0].Issues)
	}
	seg := result.callbackResult.Segments[0]
	if seg.TargetText != "这是一条测试消息" {
		t.Errorf("callback TargetText=%q", seg.TargetText)
	}
	if len(seg.Issues) != 1 || seg.Issues[0].Code != qa.CheckPunctuationMissing {
		t.Errorf("callback issues should be preserved, got %+v", seg.Issues)
	}
}

// newMarkupCorrectHandler 构造启用真实 width_mix_normalize 规则的 CorrectHandler，
// 构造方式与 newWidthMixHandler 同构（Checks=ConsumedIssueCodes、TargetLang="zh"）。
func newMarkupCorrectHandler() *CorrectHandler {
	rules := correct.New(correct.Config{
		Rules: []correct.RuleConfig{
			{Name: correct.RuleWidthMixNormalize, Enabled: true},
		},
	})
	return &CorrectHandler{
		Rules: rules,
		Idempotency: qa.NewEngine(qa.Config{
			Enabled:    true,
			Checks:     rules.ConsumedIssueCodes(),
			TargetLang: "zh",
		}, quietLogger()),
		Logger: quietLogger(),
	}
}

// markupGuardOldTarget 是改写前结构合法的译文：平衡 ruby + 全角字母/全角小于号。
// 全角 ＜（U+FF1C）在 XML CharData 中是普通字符，片段合法。
const markupGuardOldTarget = `<ruby>雷神<rt>らいじん</rt></ruby>Ａ ＜ Ｂ`

// markupGuardNewTarget 是 width_mix 规则把 U+FF01–U+FF5E 全部转回半角后的结果：
// ＜ 变成裸 <，`< ` 在 CharData 中非法，片段损坏。
const markupGuardNewTarget = `<ruby>雷神<rt>らいじん</rt></ruby>A < B`

// markupGuardWidthMixIssues 构造触发 width_mix 规则的 pending issue；
// Span.MatchedText 首 rune 为全角 ＜，规则据此反推为拉丁方向（全角→半角）。
func markupGuardWidthMixIssues() []qa.QualityIssue {
	return []qa.QualityIssue{{
		Code:     qa.CheckWidthMix,
		Severity: qa.SeverityWarning,
		Span:     &qa.Span{MatchedText: "＜"},
	}}
}

// 结构守卫：改写把合法译文改坏（全角 ＜ 被规则转成裸 <，XML 片段损坏）时必须回滚，
// Target 回到改写前的值，issue 以未改写形态保留。
func TestCorrectHandler_MarkupRegressionReverts(t *testing.T) {
	h := newMarkupCorrectHandler()
	doc := &Document{
		Format: "epub",
		Segments: []model.Segment{
			{
				ID:     "0",
				Source: "「雷神Ａ ＜ Ｂ」",
				Target: markupGuardOldTarget,
				Issues: markupGuardWidthMixIssues(),
				Status: "translated",
			},
		},
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if got := doc.Segments[0].Target; got != markupGuardOldTarget {
		t.Fatalf("Target should be reverted to oldTarget, got %q want %q", got, markupGuardOldTarget)
	}
	if len(doc.Segments[0].Issues) != 1 || doc.Segments[0].Issues[0].Code != qa.CheckWidthMix {
		t.Fatalf("issues should be preserved, got %+v", doc.Segments[0].Issues)
	}
	seg := result.callbackResult.Segments[0]
	if seg.TargetText != markupGuardOldTarget {
		t.Errorf("callback TargetText=%q, want %q", seg.TargetText, markupGuardOldTarget)
	}
}

// 结构守卫格式门禁：Format 非 epub 时不做结构校验，同样的改写正常生效
// （width_mix 转换结果对非 XML 格式是合法内容，不应回滚）。
func TestCorrectHandler_MarkupGuardFormatGate(t *testing.T) {
	h := newMarkupCorrectHandler()
	doc := &Document{
		Segments: []model.Segment{
			{
				ID:     "0",
				Source: "「雷神Ａ ＜ Ｂ」",
				Target: markupGuardOldTarget,
				Issues: markupGuardWidthMixIssues(),
				Status: "translated",
			},
		},
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if got := doc.Segments[0].Target; got != markupGuardNewTarget {
		t.Fatalf("Target should be rewritten without guard, got %q want %q", got, markupGuardNewTarget)
	}
	if len(doc.Segments[0].Issues) != 0 {
		t.Fatalf("pending width_mix should be cleared, got %+v", doc.Segments[0].Issues)
	}
	seg := result.callbackResult.Segments[0]
	if seg.TargetText != markupGuardNewTarget {
		t.Errorf("callback TargetText=%q, want %q", seg.TargetText, markupGuardNewTarget)
	}
}

func sliceEq(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
