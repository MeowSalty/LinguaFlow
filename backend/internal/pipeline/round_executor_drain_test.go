package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type holdingReporter struct {
	resolvedCountingReporter
	hold     chan struct{}
	firstHit chan struct{}
	once     sync.Once
}

func (r *holdingReporter) BatchComplete() {
	r.resolvedCountingReporter.BatchComplete()
	r.once.Do(func() { close(r.firstHit) })
	<-r.hold
}

func TestRunPoolCleanupDrainsConfirmedBatchesBeforeJoining(t *testing.T) {
	const accepted = 4
	const batches = 12
	runtime := memoryTestRuntime(t, accepted+1, nil)
	reporter := &holdingReporter{hold: make(chan struct{}), firstHit: make(chan struct{})}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var release sync.Once
	unblock := func() { release.Do(func() { close(reporter.hold) }) }
	defer unblock()
	handlerErr := errors.New("confirmation failed")
	produced := make(chan struct{}, accepted)
	fail := make(chan struct{})
	workerContext := make(chan context.Context, 1)
	result := make(chan error, 1)
	go func() {
		_, err := runPool(ctx, Round{Runtime: runtime, Gate: runtime.Gate, Concurrency: accepted + 1}, newTestDoc(batches), singletonTestBatches(batches), 1, reporter, func(ctx context.Context, job batchJob) batchResult {
			switch {
			case job.idxs[0] < accepted:
				produced <- struct{}{}
				return batchResult{}
			case job.idxs[0] == accepted:
				workerContext <- ctx
				select {
				case <-fail:
					return batchResult{err: handlerErr}
				case <-ctx.Done():
					return batchResult{deferred: true}
				}
			default:
				return batchResult{deferred: true}
			}
		})
		result <- err
	}()
	awaitStoredRoundSignal(t, reporter.firstHit)
	for range accepted {
		awaitStoredRoundSignal(t, produced)
	}
	var worker context.Context
	select {
	case worker = <-workerContext:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(fail)
	awaitStoredRoundSignal(t, worker.Done())
	unblock()
	if err := awaitRuntimeResult(t, result); !errors.Is(err, handlerErr) {
		t.Fatalf("pool error=%v, want %v", err, handlerErr)
	}
	done, legacy, completed := reporter.snapshot()
	if done != accepted || completed != accepted || len(legacy) != 0 {
		t.Fatalf("progress for confirmed batches: done=%d legacy=%v completed=%d", done, legacy, completed)
	}
}
