package pipeline

import (
	"context"
	"math"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

func TestRunRoundConcurrencyBoundedByWork(t *testing.T) {
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
			handler := &resolvedSubsetHandler{
				modeName: "translate", segsPerBatch: 1,
				processFn: func([]int, int) batchResult {
					calls.Add(1)
					return batchResult{}
				},
			}
			reporter := &resolvedCountingReporter{}
			result, err := RunRound(context.Background(), Round{
				Concurrency: tc.concurrency, Handler: handler,
			}, newTestDoc(3), nil, quietLogger(), reporter)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 3 || !reflect.DeepEqual(result.Resolved, []int{0, 1, 2}) || len(result.Unresolved) != 0 {
				t.Fatalf("batches lost or repeated: calls=%d result=%+v", calls.Load(), result)
			}
			dones, resolved, completes := reporter.snapshot()
			if dones != 3 || len(resolved) != 3 || completes != 3 {
				t.Fatalf("progress lost: dones=%d resolved=%v completes=%d", dones, resolved, completes)
			}
		})
	}
}

func TestRunPoolEmptyWorkWithMaxConcurrency(t *testing.T) {
	result, err := runPool(context.Background(), Round{Concurrency: math.MaxInt},
		&resolvedSubsetHandler{modeName: "translate"}, newTestDoc(0), nil, 0,
		nil, quietLogger(), progress.Nop{}, 0, nil)
	if err != nil || !reflect.DeepEqual(result, poolResult{}) {
		t.Fatalf("empty pool: result=%+v err=%v", result, err)
	}
}
