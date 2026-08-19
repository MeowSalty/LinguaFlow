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
// filterOutCodes 保留 dismissed 记录（避免未来以 pending 复活）。
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
			{ID: "0", Source: "「a」", Target: "a", Status: "translated", Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing}}},
			{ID: "1", Source: "「b」", Target: "b", Status: "edited", Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing}}},
			{ID: "2", Source: "「c」", Target: "c", Status: "pending", Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing}}},
			{ID: "3", Source: "「d」", Target: "", Status: "translated", Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing}}},
			{ID: "4", Source: "「e」", Target: "e", Status: "translated", Issues: []qa.QualityIssue{{Code: qa.CheckPunctuationMissing}}},
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
