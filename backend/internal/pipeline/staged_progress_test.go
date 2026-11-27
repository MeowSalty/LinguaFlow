package pipeline

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

type stagedProgressReporter struct {
	progress.Nop
	poolEvents   []progress.PoolEvent
	batchDone    []int
	total        int
	segmentsDone int
	stagesDone   int
}

func (r *stagedProgressReporter) StageStart(_ string, total int) { r.total = total }
func (r *stagedProgressReporter) SegmentDone()                   { r.segmentsDone++ }
func (r *stagedProgressReporter) BatchComplete()                 { r.batchDone = append(r.batchDone, r.segmentsDone) }
func (r *stagedProgressReporter) StageDone()                     { r.stagesDone++ }
func (r *stagedProgressReporter) OnPoolEvent(e progress.PoolEvent) {
	r.poolEvents = append(r.poolEvents, e)
}

func TestStagedRoundReportsConfirmedBatchProgressAndPoolPlan(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runtime := stagedTestRuntime(t, DefaultCandidateLimits())
	remote := &runtimeTestBackend{}
	remote.call = func(context.Context) (*backend.Response, error) {
		switch remote.calls.Load() {
		case 1:
			return &backend.Response{Text: `{"translations":{"1":"first"}}`}, nil
		case 2:
			return &backend.Response{Text: `{"translations":{"1":"third"}}`}, nil
		default:
			return &backend.Response{Text: `{"translations":{}}`}, nil
		}
	}
	h := stagedTestHandler(t, runtime, remote)
	h.Retry = backend.RetryPolicy{MaxAttempts: 1}
	h.FallbackShrink = 0.5
	doc := newTestDoc(3)
	reporter := &stagedProgressReporter{}
	var callbackIndices []int
	store := NewMemoryRoundStore(func(_ context.Context, result BatchResult) error {
		for _, segment := range result.Segments {
			callbackIndices = append(callbackIndices, segment.Index)
		}
		return nil
	})
	defer store.Close()
	round := Round{Concurrency: 1, Handler: h, Retry: h.Retry, Shrink: h.FallbackShrink}
	result, err := RunStagedRound(ctx, round, doc, store, runtime, quietLogger(), reporter)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Resolved, []int{0, 2}) || !slices.Equal(result.Unresolved, []int{1}) || result.FailedBatches != 0 || len(result.FailedSegments) != 0 {
		t.Fatalf("round result = %+v", result)
	}
	slices.Sort(callbackIndices)
	if !slices.Equal(callbackIndices, result.Resolved) || remote.calls.Load() != 3 {
		t.Fatalf("callback indices %v, network calls %d", callbackIndices, remote.calls.Load())
	}
	if reporter.total != 3 || reporter.segmentsDone != 2 || reporter.stagesDone != 1 || len(reporter.batchDone) != 3 || reporter.batchDone[0] < 1 || reporter.batchDone[2] != 2 {
		t.Fatalf("progress = %+v", reporter)
	}
	want := []progress.PoolEvent{
		{Mode: RoundModeTranslate, PoolIndex: 0, MaxPools: 2, Batches: 2, Pending: 3, ShrinkRate: 0.5, Phase: "pool_start"},
		{Mode: RoundModeTranslate, PoolIndex: 0, MaxPools: 2, Batches: 2, Pending: 1, ShrinkRate: 0.5, Phase: "pool_advance"},
		{Mode: RoundModeTranslate, PoolIndex: 1, MaxPools: 2, Batches: 1, Pending: 1, ShrinkRate: 0.5, Phase: "pool_start"},
	}
	if !slices.Equal(reporter.poolEvents, want) {
		t.Fatalf("pool events = %+v, want %+v", reporter.poolEvents, want)
	}
}

func TestStagedRoundInFlightRetryReportsOneBatchComplete(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), minRateLimitBackoff+10*time.Second)
	defer cancel()
	runtime := stagedTestRuntime(t, DefaultCandidateLimits())
	remote := &runtimeTestBackend{}
	var startedAt, retriedAt time.Time
	remote.call = func(context.Context) (*backend.Response, error) {
		if remote.calls.Load() == 1 {
			startedAt = time.Now()
			return nil, &backend.StatusError{StatusCode: 503, Err: errors.New("try again")}
		}
		retriedAt = time.Now()
		return &backend.Response{Text: `{"translations":{"1":"translated"}}`}, nil
	}
	h := stagedTestHandler(t, runtime, remote)
	h.Retry = backend.RetryPolicy{MaxAttempts: 1}
	reporter := &stagedProgressReporter{}
	round := Round{Concurrency: 1, Handler: h, Retry: h.Retry}
	result, err := RunStagedRound(ctx, round, newTestDoc(1), nil, runtime, quietLogger(), reporter)
	if err != nil || !slices.Equal(result.Resolved, []int{0}) || remote.calls.Load() != 2 {
		t.Fatalf("result = %+v, calls = %d, err = %v", result, remote.calls.Load(), err)
	}
	if wait := retriedAt.Sub(startedAt); wait < minRateLimitBackoff {
		t.Fatalf("retry wait = %v, want at least %v", wait, minRateLimitBackoff)
	}
	if !slices.Equal(reporter.batchDone, []int{1}) || len(reporter.poolEvents) != 1 || reporter.poolEvents[0].Phase != "pool_start" {
		t.Fatalf("progress = %+v", reporter)
	}
}

func TestStagedReviseTerminalBatchFailurePreservesTargetsAndRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	renderer, err := prompt.NewReviseRenderer(`{{.UnavailableTemplateField}}`)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryRoundStore(nil)
	defer store.Close()
	remote := &runtimeTestBackend{}
	doc := reviseDoc()
	doc.Segments[1].Issues[0].Disposition = qa.DispositionPending
	for i := range 2 {
		runtime := stagedTestRuntime(t, DefaultCandidateLimits())
		h := &ReviseHandler{Backend: BindRequestBackend(remote, 1, 0, runtime), Renderer: renderer, IssueCodes: []string{"calque", "naturalness"}, BatchSize: 2}
		reporter := &stagedProgressReporter{}
		round := Round{Handler: h, Concurrency: 1, Retry: backend.RetryPolicy{MaxAttempts: 1}}
		result, err := RunStagedRound(ctx, round, doc, store, runtime, quietLogger(), reporter)
		if err != nil || !slices.Equal(result.FailedSegments, []int{0, 1}) || result.FailedBatches != 1 || len(result.Resolved) != 0 || len(result.Unresolved) != 0 {
			t.Fatalf("run %d: result = %+v, err = %v", i, result, err)
		}
		if reporter.segmentsDone != 0 || len(reporter.batchDone) != 1-i || remote.calls.Load() != 0 || doc.Segments[0].Target != "你好" || doc.Segments[1].Target != "世界" {
			t.Fatalf("run %d: progress = %+v, provider calls = %d, targets = %q / %q", i, reporter, remote.calls.Load(), doc.Segments[0].Target, doc.Segments[1].Target)
		}
	}
}
