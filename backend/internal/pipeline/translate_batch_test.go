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

func TestProcessBatchInRound_BackendErrorEmitsBatch(t *testing.T) {
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
	idxs := []int{0}
	unresolved, err := s.processBatchInRound(context.Background(), doc, idxs, s.Rounds[0], quietLogger(), nil)
	if err != nil {
		t.Fatalf("processBatchInRound: %v", err)
	}
	if len(unresolved) != 1 {
		t.Fatalf("unresolved=%v", unresolved)
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
	if evt.ShrinkAttempted {
		t.Fatal("expected shrink_attempted false for single pending segment")
	}
}
