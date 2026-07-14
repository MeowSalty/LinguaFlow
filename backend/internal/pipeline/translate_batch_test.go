package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

type recordingBatchObserver struct {
	events []progress.BatchEvent
}

func (r *recordingBatchObserver) StageStart(string, int) {}
func (r *recordingBatchObserver) SegmentDone()           {}
func (r *recordingBatchObserver) BatchComplete()         {}
func (r *recordingBatchObserver) StageDone()             {}
func (r *recordingBatchObserver) Close() error           { return nil }
func (r *recordingBatchObserver) OnBatchEvent(e progress.BatchEvent) {
	r.events = append(r.events, e)
}

func TestTranslateHandler_ProcessBatch_BackendErrorEmitsBatch(t *testing.T) {
	doc := newTestDoc(1)
	rep := &recordingBatchObserver{}
	err429 := &backend.StatusError{StatusCode: 429, Err: errors.New("too many requests")}
	fb := &fakeBackend{name: "fake", errs: []error{err429}}

	h := &TranslateHandler{
		Backend:   fb,
		BatchSize: 1,
		Renderer:  newTestRenderer(t),
		Reporter:  rep,
		Repair:    defaultRepairOpts(),
		Logger:    quietLogger(),
	}

	// BuildBatches to get the batch structure
	batches, err := h.BuildBatches(context.Background(), doc)
	if err != nil {
		t.Fatalf("build batches: %v", err)
	}
	if len(batches) == 0 {
		t.Fatal("expected at least one batch")
	}

	result := h.ProcessBatch(context.Background(), doc, batches[0], 0, quietLogger())

	// 429 error triggers retry (not unresolved)
	if result.retry == nil {
		t.Fatal("expected retry for 429 error")
	}
	if result.retry.attempt != 1 {
		t.Fatalf("retry.attempt=%d want 1", result.retry.attempt)
	}
	if len(rep.events) != 1 {
		t.Fatalf("batch events=%d want 1", len(rep.events))
	}
	evt := rep.events[0]
	if evt.Status != "failed" || evt.ErrorType != "backend_error" {
		t.Fatalf("evt=%+v", evt)
	}
	if evt.HTTPStatus != 429 {
		t.Fatalf("http_status=%d want 429", evt.HTTPStatus)
	}
}
