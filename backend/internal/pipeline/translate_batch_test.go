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

func TestProcessBatchAttempt_BackendErrorEmitsBatch(t *testing.T) {
	doc := newTestDoc(1)
	rep := &recordingBatchObserver{}
	err429 := &backend.StatusError{StatusCode: 429, Err: errors.New("too many requests")}
	fb := &fakeBackend{name: "fake", errs: []error{err429}}
	s := &Translate{
		Rounds:   defaultTestRound(fb, 1, 1),
		Renderer: newTestRenderer(t),
		Logger:   quietLogger(),
		Reporter: rep,
		Repair:   defaultRepairOpts(),
	}
	job := batchJob{idxs: []int{0}, attempt: 0}
	result := s.processBatchAttempt(context.Background(), doc, job, s.Rounds[0], quietLogger(), nil, job.idxs)

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
