package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func runCapturedQualityTest(t *testing.T, h RoundHandler, doc *Document, reporter progress.Reporter) (RunRoundResult, *BatchResult) {
	t.Helper()
	var batch *BatchResult
	result, err := runMemoryTestRound(t, t.Context(), Round{Handler: h, Concurrency: 1}, doc, func(_ context.Context, sealed BatchResult) error {
		batch = new(BatchResult)
		return json.Unmarshal(sealed.PreparedPayload, batch)
	}, quietLogger(), reporter)
	if err != nil {
		t.Fatal(err)
	}
	return result, batch
}

func TestCapturedSemanticQAParsesWithoutDispatchOrDocumentMutation(t *testing.T) {
	doc := semanticQADoc([]string{"translated", "edited"}, []string{"你好世界", "后续段落"})
	doc.InputTokens, doc.OutputTokens = 17, 23
	h := &SemanticQAHandler{Backend: &fakeBackend{name: "test"}}
	response := &backend.Response{Text: `{"issues":[{"id":"0","code":"calque","message":"借译","snippet":"世界"}]}`}
	result := h.processStoredResponse(t.Context(), doc, []int{0, 1}, response, 0, quietLogger())
	batch := decodeCapturedQualityBatch(t, result)
	if len(batch.Segments) != 2 || len(batch.Segments[0].Issues) != 1 || batch.Segments[1].Issues == nil || len(batch.Segments[1].Issues) != 0 {
		t.Fatalf("captured partial issue report lost a member or empty scan: %+v", batch.Segments)
	}
	if span := batch.Segments[0].Issues[0].Span; span == nil || span.TargetStart == nil || span.MatchedText != "世界" {
		t.Fatalf("captured issue was not located in the request target: %+v", span)
	}
	if len(doc.Segments[0].Issues) != 0 || doc.InputTokens != 17 || doc.OutputTokens != 23 {
		t.Fatalf("parse changed document or usage: %+v", doc)
	}
	response.Truncated = true
	result = h.processStoredResponse(t.Context(), doc, []int{0, 1}, response, 0, quietLogger())
	if result.callbackResult != nil || len(result.unresolved) != 2 {
		t.Fatalf("truncated response should preserve issues and defer the batch: %+v", result)
	}
}

func TestCapturedAdjudicationPreservesIssuesWithoutVerdicts(t *testing.T) {
	issue := qa.QualityIssue{Code: "source_residual", Message: "residual", Disposition: qa.DispositionPending}
	doc := adjudicableDoc([]string{"translated", "edited"}, [][]qa.QualityIssue{{issue}, {issue}})
	h := &AdjudicateHandler{Backend: &fakeBackend{name: "test"}, AdjudicateCodes: qa.DefaultAdjudicateCodes()}
	response := &backend.Response{Text: `{"verdicts":[{"id":"0","issue_code":"source_residual","verdict":"false_positive","reason":"proper noun"}]}`}
	batch := decodeCapturedQualityBatch(t, h.processStoredResponse(t.Context(), doc, []int{0, 1}, response, 0, quietLogger()))
	if len(batch.Segments) != 2 || len(batch.Segments[0].Issues) != 1 || len(batch.Segments[1].Issues) != 1 {
		t.Fatalf("captured verdict did not preserve both issue records: %+v", batch.Segments)
	}
	dismissed := batch.Segments[0].Issues[0]
	if !dismissed.Dismissed() || dismissed.Note != "proper noun" || dismissed.DecidedAt == nil || !batch.Segments[1].Issues[0].IsPending() || !doc.Segments[0].Issues[0].IsPending() {
		t.Fatalf("captured verdict did not preserve pending issues or audit detail: %+v", batch.Segments)
	}
}

func decodeCapturedQualityBatch(t *testing.T, result batchResult) BatchResult {
	t.Helper()
	if result.callbackResult == nil || len(result.callbackResult.PreparedPayload) == 0 || result.callbackResult.Segments != nil {
		t.Fatalf("expected sealed captured result: %+v", result)
	}
	var batch BatchResult
	if err := json.Unmarshal(result.callbackResult.PreparedPayload, &batch); err != nil {
		t.Fatal(err)
	}
	return batch
}
