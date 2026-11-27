package pipeline

import (
	"context"
	"math"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

func singletonTestBatches(n int) [][]int {
	batches := make([][]int, n)
	for i := range batches {
		batches[i] = []int{i}
	}
	return batches
}

func TestRunPoolConcurrencyBoundedByWork(t *testing.T) {
	for _, tc := range []struct {
		name        string
		concurrency int
	}{
		{"negative", -1},
		{"zero", 0},
		{"one", 1},
		{"more_than_batches", 8},
		{"doubling_would_overflow", math.MaxInt/2 + 1},
		{"max_int", math.MaxInt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			runtime := memoryTestRuntime(t, 3, nil)
			reporter := &resolvedCountingReporter{}
			result, err := runPool(t.Context(), Round{
				Concurrency: tc.concurrency, Runtime: runtime, Gate: runtime.Gate,
			}, newTestDoc(3), singletonTestBatches(3), 1, reporter, func(context.Context, batchJob) batchResult {
				calls.Add(1)
				return batchResult{}
			})
			if err != nil || calls.Load() != 3 || !reflect.DeepEqual(result.resolved, []int{0, 1, 2}) || len(result.unresolved) != 0 {
				t.Fatalf("batches lost or repeated: calls=%d result=%+v err=%v", calls.Load(), result, err)
			}
			dones, legacy, completes := reporter.snapshot()
			if dones != 3 || len(legacy) != 0 || completes != 3 {
				t.Fatalf("progress lost: dones=%d legacy=%v completes=%d", dones, legacy, completes)
			}
		})
	}
}

func TestRunPoolEmptyWorkWithMaxConcurrency(t *testing.T) {
	result, err := runPool(t.Context(), Round{Concurrency: math.MaxInt}, newTestDoc(0), nil, 0, progress.Nop{}, nil)
	if err != nil || !reflect.DeepEqual(result, poolResult{}) {
		t.Fatalf("empty pool: result=%+v err=%v", result, err)
	}
}

func TestRunPoolAllowsConfiguredWorkConcurrency(t *testing.T) {
	runtime := memoryTestRuntime(t, 2, nil)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_, err := runPool(ctx, Round{Runtime: runtime, Gate: runtime.Gate, Concurrency: 2}, newTestDoc(2), singletonTestBatches(2), 1, progress.Nop{}, func(ctx context.Context, _ batchJob) batchResult {
			started <- struct{}{}
			select {
			case <-release:
				return batchResult{}
			case <-ctx.Done():
				return batchResult{deferred: true}
			}
		})
		done <- err
	}()
	awaitStoredRoundSignal(t, started)
	awaitStoredRoundSignal(t, started)
	close(release)
	if err := awaitRuntimeResult(t, done); err != nil {
		t.Fatal(err)
	}
}
