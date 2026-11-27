package pipeline

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

type resolvedCountingReporter struct {
	progress.Nop
	mu       sync.Mutex
	done     int
	legacy   []int
	complete int
}

func (r *resolvedCountingReporter) SegmentDone() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done++
}

func (r *resolvedCountingReporter) SegmentResolved(index int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.legacy = append(r.legacy, index)
}

func (r *resolvedCountingReporter) BatchComplete() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.complete++
}

func (r *resolvedCountingReporter) snapshot() (done int, legacy []int, complete int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done, append([]int(nil), r.legacy...), r.complete
}

func TestRunPoolReportsConfirmedSubset(t *testing.T) {
	for _, tc := range []struct {
		name       string
		result     batchResult
		resolved   []int
		unresolved []int
		completes  int
	}{
		{name: "accepted", resolved: []int{0, 1, 2, 3}, completes: 1},
		{name: "partial", result: batchResult{unresolved: []int{1}, failedSegments: []int{2}, fatalUnresolved: []int{3}}, resolved: []int{0}, unresolved: []int{1}, completes: 1},
		{name: "deferred", result: batchResult{deferred: true}, unresolved: []int{0, 1, 2, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := memoryTestRuntime(t, 1, nil)
			reporter := &resolvedCountingReporter{}
			got, err := runPool(t.Context(), Round{Runtime: runtime, Gate: runtime.Gate}, newTestDoc(4), [][]int{{0, 1, 2, 3}}, 1, reporter, func(context.Context, batchJob) batchResult {
				return tc.result
			})
			if err != nil || !reflect.DeepEqual(got.resolved, tc.resolved) || !reflect.DeepEqual(got.unresolved, tc.unresolved) ||
				!reflect.DeepEqual(got.fatalUnresolved, tc.result.fatalUnresolved) || !reflect.DeepEqual(got.failedSegments, tc.result.failedSegments) {
				t.Fatalf("pool outcome = %+v, err=%v", got, err)
			}
			dones, legacy, completes := reporter.snapshot()
			if dones != len(tc.resolved) || len(legacy) != 0 || completes != tc.completes {
				t.Fatalf("progress done=%d legacy=%v completes=%d", dones, legacy, completes)
			}
		})
	}
}

func TestRunPoolRetryDoesNotDoubleCount(t *testing.T) {
	runtime := memoryTestRuntime(t, 1, nil)
	reporter := &resolvedCountingReporter{}
	attempts := make([]int, 0, 2)
	got, err := runPool(t.Context(), Round{Runtime: runtime, Gate: runtime.Gate}, newTestDoc(2), [][]int{{0, 1}}, 2, reporter, func(_ context.Context, job batchJob) batchResult {
		attempts = append(attempts, job.attempt)
		if job.attempt == 0 {
			return batchResult{retry: &batchJob{idxs: job.idxs, attempt: 1}}
		}
		return batchResult{}
	})
	if err != nil || !reflect.DeepEqual(attempts, []int{0, 1}) || !reflect.DeepEqual(got.resolved, []int{0, 1}) || len(got.unresolved) != 0 {
		t.Fatalf("retry outcome = %+v, attempts=%v, err=%v", got, attempts, err)
	}
	dones, legacy, completes := reporter.snapshot()
	if dones != 2 || len(legacy) != 0 || completes != 1 {
		t.Fatalf("progress done=%d legacy=%v completes=%d", dones, legacy, completes)
	}
}
