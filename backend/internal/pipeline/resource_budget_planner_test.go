package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestBudgetContractStreamingPlannerPreservesPublicBatch(t *testing.T) {
	doc := newTestDoc(24)
	indices := []int{0, 1, 3, 8, 9, 10, 11, 12, 20, 22, 23}
	for _, window := range []int{0, 1, 3} {
		for _, pool := range []int{0, 1, 3} {
			for _, segments := range []int{0, 2, 4} {
				h := &TranslateHandler{BatchSize: segments, MaxWordsPerBatch: 8, FallbackShrink: 0.5, Context: ContextConfig{Enabled: window > 0, Before: window, After: window}}
				want, err := h.BuildBatches(context.Background(), doc, indices, pool)
				if err != nil {
					t.Fatal(err)
				}
				b, _ := backend.NewBuildBounds(1<<20, 1<<20)
				visit := func(yield func(int) bool) {
					for _, idx := range indices {
						if !yield(idx) {
							return
						}
					}
				}
				got, err := PlanNextMainBatchBounded(b, h, doc, visit, pool, 256)
				if err != nil || !reflect.DeepEqual(got, want[0]) {
					t.Fatalf("window=%d pool=%d segments=%d got=%v want=%v err=%v", window, pool, segments, got, want[0], err)
				}
			}
		}
	}
}
func TestBudgetContractPlannerRefusesCapacityWithoutTailBatch(t *testing.T) {
	doc := newTestDoc(16)
	h := &TranslateHandler{BatchSize: 16, MaxWordsPerBatch: 0}
	visit := func(yield func(int) bool) {
		for idx := range doc.Segments {
			if !yield(idx) {
				return
			}
		}
	}
	b, _ := backend.NewBuildBounds(1<<20, 1<<20)
	if got, err := PlanNextMainBatchBounded(b, h, doc, visit, 0, 8); !errors.Is(err, ErrCandidateCapacity) || got != nil {
		t.Fatalf("overlarge public batch became tail: %v %v", got, err)
	}
}
