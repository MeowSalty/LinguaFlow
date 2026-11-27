package pipeline

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"sync/atomic"
	"testing"
	"time"
)

func TestStoredBusyDescriptorsDoNotConsumeWholeSourcePage(t *testing.T) {
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 4}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewExecutionRuntime(a, DefaultCandidateLimits(), nil)
	defer runtime.Close()
	var calls atomic.Int64
	entered, wake := make(chan int, 128), make(chan struct{})
	process := func(_ context.Context, job batchJob) batchResult {
		calls.Add(1)
		entered <- job.idxs[0]
		return batchResult{deferred: true, waitCapacity: &WorkDeferred{Wait: backend.AdmissionWait{Reason: "test_busy", CapacityChanged: wake}}}
	}
	doc := newTestDoc(128)
	batches := make([][]int, 128)
	for i := range batches {
		batches[i] = []int{i}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan poolResult, 1)
	go func() {
		r, _ := runPool(ctx, Round{Runtime: runtime, Gate: runtime.Gate, Concurrency: 4}, doc, batches, 1, progress.Nop{}, process)
		done <- r
	}()
	seen := make(map[int]bool)
	for i := 0; i < 4; i++ {
		select {
		case index := <-entered:
			if seen[index] {
				cancel()
				<-done
				t.Fatalf("Busy retried source descriptor %d before its wait changed", index)
			}
			seen[index] = true
		case <-time.After(time.Second):
			cancel()
			<-done
			t.Fatal("four descriptors did not reach Busy")
		}
	}
	select {
	case index := <-entered:
		cancel()
		<-done
		t.Fatalf("Busy fifth call index=%d repeated=%v", index, seen[index])
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case r := <-done:
		if calls.Load() != 4 || len(r.unresolved) != 128 {
			t.Fatalf("source lost on join: calls%d unresolved%d", calls.Load(), len(r.unresolved))
		}
	case <-time.After(time.Second):
		t.Fatal("Busy source did not join on cancel")
	}
}
