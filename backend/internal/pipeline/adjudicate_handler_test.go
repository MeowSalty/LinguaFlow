package pipeline

import (
	"context"
	"errors"
	"reflect"
	"strconv"
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
	batches, err := h.BuildBatches(context.Background(), doc)
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
	batches, err := h.BuildBatches(context.Background(), doc)
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
	if len(doc.Segments[0].Issues) != 1 || doc.Segments[0].Issues[0].Code != "untranslated" {
		t.Fatalf("issues=%v want only untranslated retained", doc.Segments[0].Issues)
	}
	cb := result.callbackResult.Segments
	if len(cb) != 1 || len(cb[0].Issues) != 1 || cb[0].Issues[0].Code != "untranslated" {
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

func TestAdjudicateHandler_ProcessBatch_ParseFailurePreserves(t *testing.T) {
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
	if len(doc.Segments[0].Issues) != 1 {
		t.Fatalf("issues len=%d want 1 preserved", len(doc.Segments[0].Issues))
	}
	if result.callbackResult == nil || len(result.callbackResult.Segments[0].Issues) != 1 {
		t.Fatal("callback should carry original issues")
	}
}

func TestAdjudicateHandler_ProcessBatch_BackendErrorPreserves(t *testing.T) {
	doc := adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"}},
		},
	)
	fb := &fakeBackend{name: "fake", errs: []error{errors.New("network down")}}
	h := &AdjudicateHandler{
		Backend:   fb,
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	_ = h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if len(doc.Segments[0].Issues) != 1 {
		t.Fatalf("issues len=%d want 1 preserved", len(doc.Segments[0].Issues))
	}
}

func TestAdjudicateHandler_ProcessBatch_ForcesJSONSchema(t *testing.T) {
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
	if fb.requests[0].ResponseFormat != "json_schema" {
		t.Fatalf("ResponseFormat=%q want json_schema", fb.requests[0].ResponseFormat)
	}
	if fb.requests[0].JSONSchema == nil {
		t.Fatal("JSONSchema should be set")
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
	batches, err := h.BuildBatches(context.Background(), doc)
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
	batches, err := h.BuildBatches(context.Background(), doc)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}
	want := [][]int{{0, 1, 2}, {3, 4}}
	if !reflect.DeepEqual(batches, want) {
		t.Fatalf("batches=%v want %v", batches, want)
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
	batches, err := h.BuildBatches(context.Background(), doc)
	if err != nil || len(batches) != 1 {
		t.Fatalf("batches=%v err=%v", batches, err)
	}
	_ = h.ProcessBatch(context.Background(), doc, batches[0], 0, quietLogger())
	// source_residual not adjudicated → kept; length_ratio dismissed
	codes := make([]string, 0, len(doc.Segments[0].Issues))
	for _, iss := range doc.Segments[0].Issues {
		codes = append(codes, iss.Code)
	}
	if len(codes) != 1 || codes[0] != "source_residual" {
		t.Fatalf("issues codes=%v want [source_residual]", codes)
	}
}

// silence unused import if backend package only used via fakeBackend elsewhere
var _ backend.Backend = (*fakeBackend)(nil)
