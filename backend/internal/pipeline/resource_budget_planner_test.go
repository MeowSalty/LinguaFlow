package pipeline

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

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
func TestBudgetContractPlannerStaticPrefixesPreservePublicPriority(t *testing.T) {
	doc := newTestDoc(65)
	rangeOf := func(lo, hi int) []int {
		indices := make([]int, hi-lo)
		for i := range indices {
			indices[i] = lo + i
		}
		return indices
	}
	cases := []struct {
		name    string
		indices []int
	}{
		{"large_continuous", rangeOf(0, 40)},
		{"split_run_before_short_remainder", append(rangeOf(0, 2), rangeOf(10, 52)...)},
		{"longer_remainder_after_full_prefix", append(rangeOf(0, 18), rangeOf(30, 49)...)},
		{"equal_remainders_use_source_order", append(rangeOf(0, 18), rangeOf(30, 48)...)},
		{"sparse", []int{0, 1, 3, 8, 9, 10, 11, 12, 20, 22, 23}},
	}
	for _, tc := range cases {
		for _, limit := range []int{1, 2, 8, DefaultCandidateLimits().Segments} {
			for _, window := range []int{0, 2} {
				for _, pool := range []int{0, 2} {
					for _, words := range []int{0, 40} {
						t.Run(fmt.Sprintf("%s/limit%d/context%d/pool%d/words%d", tc.name, limit, window, pool, words), func(t *testing.T) {
							h := &TranslateHandler{BatchSize: 20, MaxWordsPerBatch: words, FallbackShrink: 0.5, Context: ContextConfig{Enabled: window > 0, Before: window, After: window}}
							remaining := slices.Clone(tc.indices)
							seen := make(map[int]bool)
							for len(remaining) > 0 {
								want, err := h.BuildBatches(context.Background(), doc, remaining, pool)
								if err != nil {
									t.Fatal(err)
								}
								b, _ := backend.NewBuildBounds(1<<20, 1<<20)
								got, err := PlanNextMainBatchBounded(b, h, doc, plannerIndexVisitor(remaining), pool, limit)
								prefix := want[0][:min(len(want[0]), limit)]
								if err != nil || !slices.Equal(got, prefix) || cap(got) > limit+1 {
									t.Fatalf("got=%v want=%v cap=%d err=%v", got, prefix, cap(got), err)
								}
								for _, idx := range got {
									if seen[idx] {
										t.Fatalf("member repeated: %d", idx)
									}
									seen[idx] = true
								}
								remaining = slices.DeleteFunc(remaining, func(idx int) bool { return seen[idx] })
							}
							if len(seen) != len(tc.indices) {
								t.Fatalf("members lost: %d/%d", len(seen), len(tc.indices))
							}
						})
					}
				}
			}
		}
	}
}

// Keep the real 300-member public boundary without rebuilding every remaining
// public batch hundreds of times for a one-member window.
func TestBudgetContractPlannerDefaultWindowBoundary(t *testing.T) {
	doc := newTestDoc(650)
	indices := make([]int, 600)
	for i := range indices {
		indices[i] = i
	}
	for _, window := range []int{0, 2} {
		h := &TranslateHandler{BatchSize: 300, Context: ContextConfig{Enabled: window > 0, Before: window, After: window}}
		want, err := h.BuildBatches(t.Context(), doc, indices, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int{1, 2, 8, DefaultCandidateLimits().Segments} {
			b, _ := backend.NewBuildBounds(1<<20, 1<<20)
			got, err := PlanNextMainBatchBounded(b, h, doc, plannerIndexVisitor(indices), 0, limit)
			if err != nil || !slices.Equal(got, want[0][:min(len(want[0]), limit)]) {
				t.Fatalf("context=%d limit=%d got=%v err=%v", window, limit, got, err)
			}
		}
	}
}

func plannerIndexVisitor(indices []int) func(func(int) bool) {
	return func(yield func(int) bool) {
		for _, idx := range indices {
			if !yield(idx) {
				return
			}
		}
	}
}

func TestBudgetContractPlannerRevisionStaticPrefixes(t *testing.T) {
	doc := newTestDoc(320)
	indices := make([]int, 0, 300)
	for idx := range doc.Segments {
		if idx%17 != 0 {
			indices = append(indices, idx)
		}
		doc.Segments[idx].Target = "revision target"
	}
	for _, span := range []int{0, 4} {
		for _, words := range []int{0, 40} {
			h := &ReviseHandler{BatchSize: 300, MaxWordsPerBatch: words, MaxBatchIndexSpan: span}
			want := BuildPackedPendingBatches(doc, indices, BatchConstraint{MaxSegments: 300, MaxWords: words, WordCount: func(seg Segment) int { return CountWords(seg.Source) + CountWords(seg.Target) }}, span)
			for _, limit := range []int{1, 8, 256} {
				b, _ := backend.NewBuildBounds(1<<20, 1<<20)
				got, err := PlanNextMainBatchBounded(b, h, doc, plannerIndexVisitor(indices), 0, limit)
				if err != nil || !slices.Equal(got, want[0][:min(len(want[0]), limit)]) {
					t.Fatalf("span=%d words=%d limit=%d got=%v want=%v err=%v", span, words, limit, got, want[0], err)
				}
			}
		}
	}
}

func TestBudgetContractPlannerRejectsInvalidBoundsBeforeAllocation(t *testing.T) {
	doc := newTestDoc(300)
	h := &TranslateHandler{BatchSize: 300}
	for _, limit := range []int{0, -1, 256} {
		b, _ := backend.NewBuildBounds(1, 1)
		visited := false
		got, err := PlanNextMainBatchBounded(b, h, doc, func(func(int) bool) { visited = true }, 0, limit)
		want := backend.ErrBuildCapacity
		if limit < 1 {
			want = backend.ErrPipelineCapacity
		}
		if !errors.Is(err, want) || got != nil || visited {
			t.Fatalf("limit=%d got=%v visited=%v err=%v", limit, got, visited, err)
		}
	}
}

func TestStagedWindowLargeBatchCompletesAndResumes(t *testing.T) {
	for _, window := range []int{0, 2} {
		t.Run(fmt.Sprintf("context%d", window), func(t *testing.T) {
			limits := DefaultCandidateLimits()
			limits.Segments = 16
			r := stagedTestRuntime(t, limits)
			h := &windowBatchProbe{batchSize: 300}
			handler := h.handler(t, r)
			handler.Context = ContextConfig{Enabled: window > 0, Before: window, After: window}
			// The long run after the three ineligible entries still exceeds S16.
			doc := newTestDoc(24)
			doc.Segments[3].Skip = true
			doc.Segments[4].Translate = false
			doc.Segments[5].Source = " "
			store := NewMemoryRoundStore(nil)
			round := Round{Concurrency: 1, Handler: handler}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := RunStagedRound(ctx, round, doc, store, r, quietLogger(), nil)
			if err != nil || len(out.Resolved) != 21 || len(out.Unresolved) != 0 {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			batches, _ := h.snapshot()
			seen := make(map[int]bool)
			fullPrefix := false
			for _, batch := range batches {
				fullPrefix = fullPrefix || len(batch) == limits.Segments
				if len(batch) > r.Window.limits.Segments || !slices.IsSorted(batch) {
					t.Fatalf("invalid batch: %v", batch)
				}
				for _, idx := range batch {
					if seen[idx] || idx >= 3 && idx <= 5 {
						t.Fatalf("ineligible/repeated member: %d", idx)
					}
					seen[idx] = true
				}
			}
			if !fullPrefix || len(batches) < 2 || len(seen) != 21 {
				t.Fatalf("batches=%d members=%d", len(batches), len(seen))
			}
			if _, err := RunStagedRound(ctx, round, doc, store, r, quietLogger(), nil); err != nil {
				t.Fatal(err)
			}
			after, _ := h.snapshot()
			if len(after) != len(batches) {
				t.Fatal("completed manifest replayed provider")
			}
			if snapshot := r.Window.Snapshot(); snapshot.Segments != 0 || snapshot.Bytes != 0 {
				t.Fatalf("occupancy leaked: %+v", snapshot)
			}
		})
	}
}

func TestStagedWindowLargeBatchPreservesFuturePin(t *testing.T) {
	for _, tc := range []struct{ segments, pins, count int }{{16, 1, 20}, {17, 4, 20}, {17, 16, 4}, {17, 17, 20}, {17, 18, 20}} {
		t.Run(fmt.Sprintf("S%d/pins%d", tc.segments, tc.pins), func(t *testing.T) {
			limits := DefaultCandidateLimits()
			limits.Segments = tc.segments
			r := stagedTestRuntime(t, limits)
			for pin := 0; pin < tc.pins; pin++ {
				if err := r.Window.RestoreScoped(fmt.Sprint("future-", pin), 1024, 1, 2); err != nil {
					t.Fatal(err)
				}
			}
			h := &windowBatchProbe{batchSize: 300}
			store := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1, roundID: 1}
			doc := newTestDoc(tc.count)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: h.handler(t, r)}, doc, store, r, quietLogger(), nil)
			batches, _ := h.snapshot()
			if tc.pins >= tc.segments {
				if !errors.Is(err, ErrCandidateCapacity) || len(batches) != 0 || len(out.Resolved) != 0 {
					t.Fatalf("fully pinned S did not reject before HTTP: out=%+v err=%v", out, err)
				}
			} else {
				if err != nil || len(out.Resolved) != tc.count || len(out.Unresolved) != 0 {
					t.Fatalf("out=%+v err=%v", out, err)
				}
				for _, batch := range batches {
					if len(batch) > tc.segments-tc.pins {
						t.Fatalf("fixed ceiling exceeded: %v", batch)
					}
				}
			}
			if snapshot := r.Window.Snapshot(); snapshot.Segments != tc.pins || snapshot.Bytes != int64(tc.pins)*1024 {
				t.Fatalf("future pins changed: %+v", snapshot)
			}
		})
	}
}

func TestStagedWindowRejectsOversizedSingleton(t *testing.T) {
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	r.HandoffBytes = 64 << 10
	h := &windowBatchProbe{batchSize: 300}
	doc := newTestDoc(1)
	doc.Segments[0].Source = strings.Repeat("source ", 20<<10)
	store := NewMemoryRoundStore(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: h.handler(t, r)}, doc, store, r, quietLogger(), nil)
	batches, _ := h.snapshot()
	if !errors.Is(err, backend.ErrBuildCapacity) || len(batches) != 0 || len(out.Resolved) != 0 {
		t.Fatalf("oversized member bypassed byte budget: out=%+v err=%v requests=%d", out, err, len(batches))
	}
	if snapshot := r.Window.Snapshot(); snapshot.Segments != 0 || snapshot.Bytes != 0 {
		t.Fatalf("failed singleton leaked occupancy: %+v", snapshot)
	}
}
