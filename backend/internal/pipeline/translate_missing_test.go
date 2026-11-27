package pipeline

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// A partial model response commits only returned members. A missed member
// retains its accepted translation and issues until a new response is confirmed.
func TestTranslateHandler_ProcessBatch_MissingSegmentDropsStaleState(t *testing.T) {
	doc := newTestDoc(2)
	// 模拟 DB 重载：两段均已有旧译文与旧裁决（含用户 dismissed）。
	doc.Segments[0].Target = "旧译文-0"
	doc.Segments[0].Issues = []qa.QualityIssue{{
		SegmentIndex: 0, Severity: qa.SeverityError, Code: qa.CheckXMLTagMismatch,
		Message: "旧问题-0", Disposition: qa.DispositionDismissed,
	}}
	doc.Segments[1].Target = "旧译文-1"
	doc.Segments[1].Issues = []qa.QualityIssue{{
		SegmentIndex: 1, Severity: qa.SeverityWarning, Code: qa.CheckXMLTagMismatch,
		Message: "旧问题-1", Disposition: qa.DispositionPending,
	}}

	// LLM 只返回段 0 的译文，段 1 缺失（partial response）。
	fb := &fakeBackend{name: "fake", responses: []string{`{"translations":{"1":"新译文-0"}}`}}
	h := &TranslateHandler{
		Backend:   fb,
		BatchSize: 2,
		Renderer:  newTestRenderer(t),
		Repair:    defaultRepairOpts(),
		Logger:    quietLogger(),
	}

	var confirmed []TranslatedSegment
	res, err := runMemoryTestRound(t, t.Context(), Round{Handler: h, Concurrency: 1}, doc,
		func(_ context.Context, batch BatchResult) error {
			confirmed = append(confirmed, batch.Segments...)
			return nil
		}, quietLogger(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0] != 1 || len(confirmed) != 1 {
		t.Fatalf("unresolved=%v confirmed=%+v", res.Unresolved, confirmed)
	}
	if got := confirmed[0]; got.Index != 0 || got.TargetText != "新译文-0" || got.Failed || len(got.Issues) != 0 {
		t.Fatalf("confirmed translation=%+v", got)
	}
	if got := doc.Segments[1]; got.Target != "旧译文-1" || len(got.Issues) != 1 || got.Issues[0].Message != "旧问题-1" {
		t.Fatalf("unresolved member lost its prior data: %+v", got)
	}
}
