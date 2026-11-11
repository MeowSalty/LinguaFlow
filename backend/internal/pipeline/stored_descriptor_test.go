package pipeline

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type descriptorBusyHandler struct {
	calls   atomic.Int64
	entered chan struct{}
	wake    chan struct{}
}

func (h *descriptorBusyHandler) ModeName() string { return "descriptor_busy" }
func (h *descriptorBusyHandler) BuildBatches(context.Context, *Document, []int, int) ([][]int, error) {
	return nil, nil
}
func (h *descriptorBusyHandler) Finalize(context.Context, *Document, []int) error { return nil }
func (h *descriptorBusyHandler) ProcessBatch(context.Context, *Document, []int, int, *slog.Logger) batchResult {
	h.calls.Add(1)
	h.entered <- struct{}{}
	return batchResult{deferred: true, waitCapacity: &WorkDeferred{Wait: backend.AdmissionWait{Reason: "test_busy", CapacityChanged: h.wake}}}
}
func TestStoredBusyDescriptorsDoNotConsumeWholeSourcePage(t *testing.T) {
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 4}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewExecutionRuntime(a, DefaultCandidateLimits(), nil)
	defer runtime.Close()
	h := &descriptorBusyHandler{entered: make(chan struct{}, 128), wake: make(chan struct{})}
	doc := newTestDoc(128)
	batches := make([][]int, 128)
	for i := range batches {
		batches[i] = []int{i}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan poolResult, 1)
	go func() {
		r, _ := runPool(ctx, Round{Handler: h, Runtime: runtime, Store: NewMemoryRoundStore(nil), Gate: runtime.Gate, Concurrency: 4}, h, doc, batches, 1, nil, quietLogger(), progress.Nop{}, 0, nil)
		done <- r
	}()
	for i := 0; i < 4; i++ {
		select {
		case <-h.entered:
		case <-time.After(time.Second):
			cancel()
			<-done
			t.Fatal("four descriptors did not reach Busy")
		}
	}
	select {
	case <-h.entered:
		cancel()
		<-done
		t.Fatal("Busy admitted an uncredited fifth source descriptor")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case r := <-done:
		if h.calls.Load() != 4 || len(r.unresolved) != 128 {
			t.Fatalf("source lost on join: calls%d unresolved%d", h.calls.Load(), len(r.unresolved))
		}
	case <-time.After(time.Second):
		t.Fatal("Busy source did not join on cancel")
	}
}
