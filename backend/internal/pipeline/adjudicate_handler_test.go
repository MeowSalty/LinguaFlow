package pipeline

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

const testAdjudicationTmpl = `Adjudicate quality issues for {{.SourceLang}} → {{.TargetLang}}. Reply as JSON: {"verdicts":[...]}`

func newAdjudicationRenderer(t *testing.T) *prompt.AdjudicationRenderer {
	t.Helper()
	r, err := prompt.NewAdjudicationRenderer(testAdjudicationTmpl)
	if err != nil {
		t.Fatalf("adjudication renderer: %v", err)
	}
	return r
}

func adjudicableDoc(statuses []string, issues [][]qa.QualityIssue) *Document {
	segs := make([]Segment, len(statuses))
	for i := range segs {
		segs[i] = Segment{
			ID:     strconv.Itoa(i),
			Source: "hello world",
			Target: "你好世界",
			Status: statuses[i],
			Issues: issues[i],
		}
	}
	return &Document{
		SourceLang: "en",
		TargetLang: "zh",
		Segments:   segs,
		Vars:       map[string]any{},
	}
}

func TestAdjudicateHandler_BuildBatches_SelectsTranslatedWithIssues(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated", "approved", "edited", "pending", "rejected"},
		[][]qa.QualityIssue{
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"}},
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"}},
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"}},
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"}},
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"}},
		},
	)
	h := &AdjudicateHandler{
		Backend:   &fakeBackend{name: "fake"},
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}
	// only translated (0) and edited (2); packed batching keeps discontinuous idxs in one batch
	if len(batches) != 1 || len(batches[0]) != 2 || batches[0][0] != 0 || batches[0][1] != 2 {
		t.Fatalf("batches=%v want [[0 2]]", batches)
	}
}

func TestAdjudicateHandler_BuildBatches_SkipsNonAdjudicableCodes(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated", "translated"},
		[][]qa.QualityIssue{
			{{Code: "untranslated", Severity: qa.SeverityError, Message: "empty"}},
			{{Code: "duplicate", Severity: qa.SeverityWarning, Message: "dup"}},
		},
	)
	h := &AdjudicateHandler{
		Backend:   &fakeBackend{name: "fake"},
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}
	if len(batches) != 0 {
		t.Fatalf("batches=%v want empty (hard rules not adjudicable)", batches)
	}
}

func TestAdjudicateHandler_ProcessBatch_FalsePositiveDismissed(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{
				{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"},
				{Code: "untranslated", Severity: qa.SeverityError, Message: "empty"},
			},
		},
	)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"verdicts":[{"id":"0","issue_code":"source_residual","verdict":"false_positive","reason":"proper noun"}]}`},
	}
	h := &AdjudicateHandler{
		Backend:   fb,
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
	// false_positive 不再剔除，而是打 dismissed 标记保留（审计痕迹）
	if len(doc.Segments[0].Issues) != 2 {
		t.Fatalf("issues len=%d want 2 (dismissed kept for audit)", len(doc.Segments[0].Issues))
	}
	var residual, untranslated *qa.QualityIssue
	for i := range doc.Segments[0].Issues {
		iss := &doc.Segments[0].Issues[i]
		switch iss.Code {
		case "source_residual":
			residual = iss
		case "untranslated":
			untranslated = iss
		}
	}
	if residual == nil || !residual.Dismissed() || residual.Disposition != qa.DispositionDismissed {
		t.Fatalf("source_residual not dismissed: %#v", doc.Segments[0].Issues)
	}
	if residual.Note != "proper noun" {
		t.Fatalf("residual note=%q want %q (LLM reason audited)", residual.Note, "proper noun")
	}
	if residual.DecidedBy != nil {
		t.Fatalf("DecidedBy=%v want nil (LLM adjudicated)", residual.DecidedBy)
	}
	if residual.DecidedAt == nil {
		t.Fatal("DecidedAt must be set for dismissed issue")
	}
	if untranslated == nil || untranslated.Dismissed() || untranslated.Disposition != qa.DispositionPending {
		t.Fatalf("untranslated should stay pending: %#v", doc.Segments[0].Issues)
	}
	cb := result.callbackResult.Segments
	if len(cb) != 1 || len(cb[0].Issues) != 2 {
		t.Fatalf("callback issues=%v", cb)
	}
}

func TestAdjudicateHandler_ProcessBatch_RealPreserved(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"}},
		},
	)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"verdicts":[{"id":"0","issue_code":"source_residual","verdict":"real","reason":"missed translation"}]}`},
	}
	h := &AdjudicateHandler{
		Backend:   fb,
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
	if len(doc.Segments[0].Issues) != 1 {
		t.Fatalf("issues len=%d want 1", len(doc.Segments[0].Issues))
	}
}

func TestAdjudicateHandler_ProcessBatch_ParseFailureDefers(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"}},
		},
	)
	fb := &fakeBackend{name: "fake", responses: []string{`not json at all`}}
	h := &AdjudicateHandler{
		Backend:   fb,
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	// 解析失败：段交下一池重切（unresolved），不改 doc（原 issue 保留供下池重判）。
	if len(doc.Segments[0].Issues) != 1 {
		t.Fatalf("issues len=%d want 1 preserved in doc", len(doc.Segments[0].Issues))
	}
	if !reflect.DeepEqual(result.unresolved, []int{0}) {
		t.Fatalf("unresolved=%v want [0]", result.unresolved)
	}
	// 不返回 callbackResult：避免 batchHandler 写回陈旧 issue 干扰下一池重判。
	if result.callbackResult != nil {
		t.Fatal("parse failure must not produce callbackResult (would persist stale issues)")
	}
}

func TestAdjudicateHandler_ProcessBatch_BackendErrorDefers(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"}},
		},
	)
	// 400 非 401/403（不致命）、非 ≥500/429（不可重试）→ 命中非致命不可重试分支。
	fb := &fakeBackend{name: "fake", errs: []error{&backend.StatusError{StatusCode: 400, Err: errors.New("bad request")}}}
	h := &AdjudicateHandler{
		Backend:   fb,
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if !reflect.DeepEqual(result.unresolved, []int{0}) {
		t.Fatalf("unresolved=%v want [0]", result.unresolved)
	}
	if result.callbackResult != nil {
		t.Fatal("non-fatal backend error must not produce callbackResult (would persist stale issues)")
	}
	if len(doc.Segments[0].Issues) != 1 {
		t.Fatalf("issues len=%d want 1 preserved", len(doc.Segments[0].Issues))
	}
}

func TestAdjudicateHandler_ProcessBatch_RenderFailureDefers(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"}},
		},
	)
	// 模板解析通过，但 Execute 时 AdjudicationData 无 Missing 字段 → render 必失败。
	renderer, err := prompt.NewAdjudicationRenderer("{{.Missing}}")
	if err != nil {
		t.Fatalf("new renderer: %v", err)
	}
	fb := &fakeBackend{name: "fake", responses: []string{`{}`}}
	h := &AdjudicateHandler{
		Backend:   fb,
		Renderer:  renderer,
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if !reflect.DeepEqual(result.unresolved, []int{0}) {
		t.Fatalf("unresolved=%v want [0]", result.unresolved)
	}
	if result.callbackResult != nil {
		t.Fatal("render failure must not produce callbackResult (would persist stale issues)")
	}
	if len(doc.Segments[0].Issues) != 1 {
		t.Fatalf("issues len=%d want 1 preserved", len(doc.Segments[0].Issues))
	}
}

func TestAdjudicateHandler_ProcessBatch_NonTextAttachesSchema(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"}},
		},
	)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"verdicts":[{"id":"0","issue_code":"source_residual","verdict":"real","reason":"x"}]}`},
	}
	h := &AdjudicateHandler{
		Backend:   fb,
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	_ = h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if len(fb.requests) != 1 {
		t.Fatalf("requests=%d want 1", len(fb.requests))
	}
	if fb.requests[0].ResponseFormat != "" {
		t.Fatalf("ResponseFormat should be empty (backend default), got %q", fb.requests[0].ResponseFormat)
	}
	if fb.requests[0].JSONSchema == nil {
		t.Fatal("JSONSchema should be set for non-text mode")
	}
}

func TestAdjudicateHandler_ProcessBatch_TextMode(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{
				{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"},
				{Code: "untranslated", Severity: qa.SeverityError, Message: "empty"},
			},
		},
	)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{"[verdicts]\n0 | source_residual | false_positive | proper noun"},
	}
	h := &AdjudicateHandler{
		Backend:      fb,
		Renderer:     newAdjudicationRenderer(t),
		BatchSize:    10,
		ResponseMode: "text",
		Logger:       quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
	if len(fb.requests) != 1 {
		t.Fatalf("requests=%d want 1", len(fb.requests))
	}
	req := fb.requests[0]
	if req.ResponseFormat != "none" {
		t.Fatalf("ResponseFormat=%q want none", req.ResponseFormat)
	}
	if req.JSONSchema != nil {
		t.Fatal("JSONSchema should be nil in text mode")
	}
	if !strings.Contains(req.User, "source_lang:") {
		t.Fatalf("text user missing source_lang:\n%s", req.User)
	}
	if !strings.Contains(req.User, "[segment]") {
		t.Fatalf("text user missing [segment]:\n%s", req.User)
	}
	// source_residual 打 dismissed 标记保留，untranslated（硬规则不可裁决）保持 pending
	if len(doc.Segments[0].Issues) != 2 {
		t.Fatalf("issues len=%d want 2", len(doc.Segments[0].Issues))
	}
	var residual, untranslated *qa.QualityIssue
	for i := range doc.Segments[0].Issues {
		iss := &doc.Segments[0].Issues[i]
		switch iss.Code {
		case "source_residual":
			residual = iss
		case "untranslated":
			untranslated = iss
		}
	}
	if residual == nil || !residual.Dismissed() || residual.Disposition != qa.DispositionDismissed {
		t.Fatalf("source_residual not dismissed: %#v", doc.Segments[0].Issues)
	}
	if residual.Note != "proper noun" {
		t.Fatalf("residual note=%q want %q", residual.Note, "proper noun")
	}
	if untranslated == nil || untranslated.Dismissed() || untranslated.Disposition != qa.DispositionPending {
		t.Fatalf("untranslated should stay pending: %#v", doc.Segments[0].Issues)
	}
}

func TestAdjudicateHandler_ProcessBatch_TextModeJSONFallback(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"}},
		},
	)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"verdicts":[{"id":"0","issue_code":"source_residual","verdict":"false_positive","reason":"ok"}]}`},
	}
	h := &AdjudicateHandler{
		Backend:      fb,
		Renderer:     newAdjudicationRenderer(t),
		BatchSize:    10,
		ResponseMode: "text",
		Logger:       quietLogger(),
	}
	_ = h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if len(doc.Segments[0].Issues) != 1 || !doc.Segments[0].Issues[0].Dismissed() {
		t.Fatalf("issues=%#v want 1 dismissed (false_positive via JSON fallback)", doc.Segments[0].Issues)
	}
	if doc.Segments[0].Issues[0].Note != "ok" {
		t.Fatalf("note=%q want %q", doc.Segments[0].Issues[0].Note, "ok")
	}
}

// TestAdjudicateHandler_ProcessBatch_TextModeTruncatedRefused fail-closed stage
// 对截断响应恒不采纳：adjudicate 的成功路径对所有批次段一律 SegmentDone
// （无「缺失 verdict → 重跑」通道），text 协议逐行解析无完整性信号——截断的
// 已完成行被当作完整裁决会令缺失 verdict 的段永久跳过。后端标记 Truncated 时
// 即使 text 协议命中也必须报 parse_error 走 unresolved → 下一池整批重试。
func TestAdjudicateHandler_ProcessBatch_TextModeTruncatedRefused(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"}},
		},
	)
	rec := &recordingBatchObserver{}
	fb := &truncatedFakeBackend{
		fakeBackend: fakeBackend{
			name:      "fake",
			responses: []string{"[verdicts]\n0 | source_residual | false_positive | proper noun"},
		},
	}
	h := &AdjudicateHandler{
		Backend:      fb,
		Renderer:     newAdjudicationRenderer(t),
		BatchSize:    10,
		ResponseMode: "text",
		Reporter:     rec,
		Logger:       quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if len(result.unresolved) != 1 {
		t.Fatalf("unresolved=%v, want the batch (truncated text response must fail closed)",
			result.unresolved)
	}
	if result.callbackResult != nil {
		t.Fatalf("truncated text response must not be accepted as complete verdicts, got %#v",
			result.callbackResult)
	}
	// fail-closed：issue 保持 pending，段未被裁决
	if doc.Segments[0].Issues[0].Dismissed() {
		t.Fatal("issue must stay pending when truncated response is refused")
	}
	if len(rec.events) != 1 {
		t.Fatalf("emitted %d batch events, want 1", len(rec.events))
	}
	evt := rec.events[0]
	if evt.ErrorType != "parse_error" || !evt.Truncated {
		t.Fatalf("event error_type=%q truncated=%v, want parse_error/true", evt.ErrorType, evt.Truncated)
	}
}

func TestAdjudicateHandler_BuildBatches_PackedDiscontinuous(t *testing.T) {
	// 5 段均 translated+issue，但中间夹着非候选（approved）时仍应与其它 pending 同批
	doc := adjudicableDoc(
		[]string{"translated", "approved", "translated", "pending", "edited"},
		[][]qa.QualityIssue{
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "r0"}},
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "r1"}},
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "r2"}},
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "r3"}},
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "r4"}},
		},
	)
	h := &AdjudicateHandler{
		Backend:   &fakeBackend{name: "fake"},
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}
	if len(batches) != 1 || !reflect.DeepEqual(batches[0], []int{0, 2, 4}) {
		t.Fatalf("batches=%v want [[0 2 4]]", batches)
	}
}

func TestAdjudicateHandler_BuildBatches_MaxBatchIndexSpan(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated", "translated", "translated", "translated", "translated"},
		[][]qa.QualityIssue{
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "r0"}},
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "r1"}},
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "r2"}},
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "r3"}},
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "r4"}},
		},
	)
	// pending 全选 [0..4]；span=2 → [0,1,2](2-0=2), [3,4]
	h := &AdjudicateHandler{
		Backend:           &fakeBackend{name: "fake"},
		Renderer:          newAdjudicationRenderer(t),
		BatchSize:         10,
		MaxBatchIndexSpan: 2,
		Logger:            quietLogger(),
	}
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}
	want := [][]int{{0, 1, 2}, {3, 4}}
	if !reflect.DeepEqual(batches, want) {
		t.Fatalf("batches=%v want %v", batches, want)
	}
}

func TestAdjudicateHandler_ProcessBatch_MatchedTextSelectiveDismiss(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{
				{Code: "source_residual", Severity: qa.SeverityWarning, Message: "a", Span: &qa.Span{MatchedText: "foo"}},
				{Code: "source_residual", Severity: qa.SeverityWarning, Message: "b", Span: &qa.Span{MatchedText: "bar"}},
			},
		},
	)
	fb := &fakeBackend{
		name: "fake",
		responses: []string{`{"verdicts":[
			{"id":"0","issue_code":"source_residual","matched_text":"foo","verdict":"false_positive","reason":"ok"},
			{"id":"0","issue_code":"source_residual","matched_text":"bar","verdict":"real","reason":"keep"}
		]}`},
	}
	h := &AdjudicateHandler{
		Backend:   fb,
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	_ = h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	// foo 打 dismissed 标记保留，bar 保持 pending
	if len(doc.Segments[0].Issues) != 2 {
		t.Fatalf("issues=%#v want 2 kept (foo dismissed, bar pending)", doc.Segments[0].Issues)
	}
	var foo, bar *qa.QualityIssue
	for i := range doc.Segments[0].Issues {
		iss := &doc.Segments[0].Issues[i]
		if qa.MatchedText(*iss) == "foo" {
			foo = iss
		} else if qa.MatchedText(*iss) == "bar" {
			bar = iss
		}
	}
	if foo == nil || !foo.Dismissed() || foo.Disposition != qa.DispositionDismissed || foo.Note != "ok" {
		t.Fatalf("foo not dismissed with reason: %#v", foo)
	}
	if bar == nil || bar.Dismissed() || bar.Disposition != qa.DispositionPending {
		t.Fatalf("bar should stay pending: %#v", doc.Segments[0].Issues)
	}
}

func TestAdjudicateHandler_ProcessBatch_MatchedTextMissingKeepsMultipleIssues(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{
				{Code: "source_residual", Severity: qa.SeverityWarning, Message: "a", Span: &qa.Span{MatchedText: "foo"}},
				{Code: "source_residual", Severity: qa.SeverityWarning, Message: "b", Span: &qa.Span{MatchedText: "bar"}},
			},
		},
	)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"verdicts":[{"id":"0","issue_code":"source_residual","matched_text":"","verdict":"false_positive","reason":"missing identity"}]}`},
	}
	h := &AdjudicateHandler{
		Backend:   fb,
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	_ = h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if len(doc.Segments[0].Issues) != 2 {
		t.Fatalf("issues=%#v want both retained", doc.Segments[0].Issues)
	}
}

func TestAdjudicateHandler_AdjudicateCodes_OnlyLengthRatio(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{
				{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"},
				{Code: "length_ratio", Severity: qa.SeverityWarning, Message: "ratio"},
			},
		},
	)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"verdicts":[{"id":"0","issue_code":"length_ratio","verdict":"false_positive","reason":"ok"}]}`},
	}
	h := &AdjudicateHandler{
		Backend:         fb,
		Renderer:        newAdjudicationRenderer(t),
		BatchSize:       10,
		AdjudicateCodes: []string{"length_ratio"},
		Logger:          quietLogger(),
	}
	// BuildBatches should still select (has length_ratio)
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil || len(batches) != 1 {
		t.Fatalf("batches=%v err=%v", batches, err)
	}
	_ = h.ProcessBatch(context.Background(), doc, batches[0], 0, quietLogger())
	// source_residual not adjudicated → stays pending; length_ratio dismissed (marked, not removed)
	if len(doc.Segments[0].Issues) != 2 {
		t.Fatalf("issues len=%d want 2", len(doc.Segments[0].Issues))
	}
	var residual, ratio *qa.QualityIssue
	for i := range doc.Segments[0].Issues {
		iss := &doc.Segments[0].Issues[i]
		switch iss.Code {
		case "source_residual":
			residual = iss
		case "length_ratio":
			ratio = iss
		}
	}
	if residual == nil || residual.Dismissed() || residual.Disposition != qa.DispositionPending {
		t.Fatalf("source_residual should stay pending: %#v", doc.Segments[0].Issues)
	}
	if ratio == nil || !ratio.Dismissed() || ratio.Disposition != qa.DispositionDismissed || ratio.Note != "ok" {
		t.Fatalf("length_ratio should be dismissed with reason: %#v", doc.Segments[0].Issues)
	}
}

func TestApplyVerdicts_AlreadyDismissedPassedThrough(t *testing.T) {
	issues := []qa.QualityIssue{
		{Code: "source_residual", Severity: qa.SeverityWarning, Message: "already done", Disposition: qa.DispositionDismissed, Note: "user said not an issue"},
		{Code: "source_residual", Severity: qa.SeverityWarning, Message: "pending"},
	}
	codes := map[string]struct{}{"source_residual": {}}
	verdictMap := map[string]prompt.AdjudicationVerdict{
		adjudicationKey("0", "source_residual", ""): {ID: "0", IssueCode: "source_residual", Verdict: "false_positive", Reason: "LLM reason"},
	}
	out, newly := applyVerdicts(issues, "0", codes, verdictMap, quietLogger())
	if newly != 1 {
		t.Fatalf("newlyDismissed=%d want 1", newly)
	}
	if len(out) != 2 {
		t.Fatalf("len(out)=%d want 2 (dismissed passed through, pending dismissed)", len(out))
	}
	// 已 dismissed 的 issue 原样带过，不被重复处理
	if !out[0].Dismissed() || out[0].Disposition != qa.DispositionDismissed || out[0].Note != "user said not an issue" {
		t.Fatalf("already-dismissed issue not passed through untouched: %#v", out[0])
	}
	// 待决 issue 被 LLM 裁决为 dismissed，reason 沉淀到 Note
	if !out[1].Dismissed() || out[1].Disposition != qa.DispositionDismissed || out[1].Note != "LLM reason" || out[1].DecidedBy != nil {
		t.Fatalf("pending issue not dismissed by LLM: %#v", out[1])
	}
	if out[1].DecidedAt == nil {
		t.Fatal("DecidedAt must be set for newly dismissed issue")
	}
}

func TestAdjudicateHandler_ProcessBatch_PunctuationSurplusDefaultAdjudicable(t *testing.T) {
	// 未显式配置 AdjudicateCodes 时回退到 qa.DefaultAdjudicateCodes()，
	// 其中已包含 punctuation_surplus —— 默认配置下即应可裁决。
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{
				{
					Code:     qa.CheckPunctuationSurplus,
					Severity: qa.SeverityWarning,
					Message:  "译文多出源文没有的引号标点：“”",
					Span:     &qa.Span{MatchedText: "“”"},
				},
			},
		},
	)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"verdicts":[{"id":"0","issue_code":"punctuation_surplus","matched_text":"“”","verdict":"false_positive","reason":"内心独白加引号"}]}`},
	}
	h := &AdjudicateHandler{
		Backend:   fb,
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
	if len(doc.Segments[0].Issues) != 1 {
		t.Fatalf("issues len=%d want 1 (dismissed kept for audit)", len(doc.Segments[0].Issues))
	}
	var surplus *qa.QualityIssue
	for i := range doc.Segments[0].Issues {
		iss := &doc.Segments[0].Issues[i]
		if iss.Code == qa.CheckPunctuationSurplus {
			surplus = iss
		}
	}
	if surplus == nil || !surplus.Dismissed() || surplus.Disposition != qa.DispositionDismissed {
		t.Fatalf("punctuation_surplus not dismissed under default codes: %#v", doc.Segments[0].Issues)
	}
	if surplus.Note != "内心独白加引号" {
		t.Fatalf("note=%q want %q (LLM reason audited)", surplus.Note, "内心独白加引号")
	}
	if surplus.DecidedBy != nil {
		t.Fatalf("DecidedBy=%v want nil (LLM adjudicated)", surplus.DecidedBy)
	}
	if surplus.DecidedAt == nil {
		t.Fatal("DecidedAt must be set for dismissed issue")
	}
}

func TestAdjudicateHandler_ProcessBatch_PunctuationSurplusMatchedTextKeyed(t *testing.T) {
	// LLM 裁决未回传 matched_text（空串），但该段该 code 仅一条待决 issue，
	// applyVerdicts 的单实例回退（空 matched_text 键）仍应命中并剔除。
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{
				{
					Code:     qa.CheckPunctuationSurplus,
					Severity: qa.SeverityWarning,
					Message:  "译文多出源文没有的引号标点：“”",
					Span:     &qa.Span{MatchedText: "“”"},
				},
			},
		},
	)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"verdicts":[{"id":"0","issue_code":"punctuation_surplus","matched_text":"","verdict":"false_positive","reason":"拟声词加引号"}]}`},
	}
	h := &AdjudicateHandler{
		Backend:   fb,
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
	var surplus *qa.QualityIssue
	for i := range doc.Segments[0].Issues {
		iss := &doc.Segments[0].Issues[i]
		if iss.Code == qa.CheckPunctuationSurplus {
			surplus = iss
		}
	}
	if surplus == nil || !surplus.Dismissed() || surplus.Disposition != qa.DispositionDismissed {
		t.Fatalf("single-instance fallback should dismiss punctuation_surplus: %#v", doc.Segments[0].Issues)
	}
	if surplus.Note != "拟声词加引号" {
		t.Fatalf("note=%q want %q (LLM reason audited)", surplus.Note, "拟声词加引号")
	}
	if surplus.DecidedAt == nil {
		t.Fatal("DecidedAt must be set for dismissed issue")
	}
}

func TestAdjudicateHandler_ProcessBatch_PunctuationSurplusRealStaysPending(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{
				{
					Code:     qa.CheckPunctuationSurplus,
					Severity: qa.SeverityWarning,
					Message:  "译文多出源文没有的引号标点：“”",
					Span:     &qa.Span{MatchedText: "“”"},
				},
			},
		},
	)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"verdicts":[{"id":"0","issue_code":"punctuation_surplus","matched_text":"“”","verdict":"real","reason":"无明显文体动机"}]}`},
	}
	h := &AdjudicateHandler{
		Backend:   fb,
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	// real 不是错误：批次成功返回 callbackResult，issue 保持 pending 原样保留。
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult (real verdict is not an error)")
	}
	if len(doc.Segments[0].Issues) != 1 {
		t.Fatalf("issues len=%d want 1 (real kept)", len(doc.Segments[0].Issues))
	}
	var surplus *qa.QualityIssue
	for i := range doc.Segments[0].Issues {
		iss := &doc.Segments[0].Issues[i]
		if iss.Code == qa.CheckPunctuationSurplus {
			surplus = iss
		}
	}
	if surplus == nil {
		t.Fatalf("punctuation_surplus missing: %#v", doc.Segments[0].Issues)
	}
	if surplus.Dismissed() || surplus.Disposition != qa.DispositionPending {
		t.Fatalf("real verdict must keep issue pending: %#v", surplus)
	}
	if surplus.Note != "" {
		t.Fatalf("note=%q want empty (only false_positive sets Note)", surplus.Note)
	}
}

// silence unused import if backend package only used via fakeBackend elsewhere
var _ backend.Backend = (*fakeBackend)(nil)
