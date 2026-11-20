package pipeline

import (
	"context"
	"fmt"
	"math"
	"math/bits"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func schedulerOracleBatch(t *testing.T, doc *Document, h stagedHandler, active map[int]bool, candidates map[int]*Candidate, maxPools, limit int, now time.Time) ([]int, int, int) {
	t.Helper()
	pool, attempt := -1, 0
	for i := range doc.Segments {
		w := doc.Segments[i].RoundWork
		c := w.Cursor
		if !w.Member || w.Completed || active[i] || candidates[i] != nil || c.Pool >= maxPools || c.State == "unresolved" || c.State == "candidate" || c.State == "response_pending" || now.Before(c.NextAttemptAt) {
			continue
		}
		if pool < 0 || c.Pool < pool {
			pool, attempt = c.Pool, c.Attempt
		}
	}
	if pool < 0 {
		return nil, pool, attempt
	}
	for i := range doc.Segments {
		w := doc.Segments[i].RoundWork
		if w.Member && !w.Completed && w.Cursor.Pool < pool && w.Cursor.State != "unresolved" {
			return nil, -1, 0
		}
	}
	eligible := []int{}
	for i := range doc.Segments {
		w := doc.Segments[i].RoundWork
		c := w.Cursor
		if w.Member && !w.Completed && !active[i] && candidates[i] == nil && c.Pool == pool && c.Attempt == attempt && c.State != "unresolved" && c.State != "candidate" && c.State != "response_pending" && !now.Before(c.NextAttemptAt) {
			eligible = append(eligible, i)
		}
	}
	b, err := backend.NewBuildBounds(1<<20, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer b.CloseRetired()
	batch, err := PlanNextMainBatchBounded(b, h, doc, func(yield func(int) bool) {
		for _, i := range eligible {
			if !yield(i) {
				return
			}
		}
	}, pool, limit)
	if err != nil {
		t.Fatal(err)
	}
	return batch, pool, attempt
}
func TestIncrementalMainSchedulerOracle(t *testing.T) {
	for _, name := range []string{"same_pool_source_attempt", "word_split_priority", "oversize_first", "full_leftover_before_S", "fallback_shrink", "future_ready", "pool_barrier", "empty_state", "noncanonical_context", "revision_words_span", "active_candidate_exclusions"} {
		t.Run(name, func(t *testing.T) {
			doc := newTestDoc(12)
			for i := range doc.Segments {
				doc.Segments[i].Source = "two words"
				doc.Segments[i].RoundWork.Member = true
				doc.Segments[i].RoundWork.Cursor = WorkCursor{State: "pending"}
			}
			h := stagedHandler(&TranslateHandler{MaxWordsPerBatch: 100})
			active := map[int]bool{}
			candidates := map[int]*Candidate{}
			limits := DefaultCandidateLimits()
			now := time.Now()
			switch name {
			case "same_pool_source_attempt":
				for i := range doc.Segments {
					doc.Segments[i].RoundWork.Cursor.Attempt = 1
				}
				doc.Segments[0].RoundWork.Cursor.Attempt = 2
				doc.Segments[2].RoundWork.Cursor.Attempt = 2
			case "word_split_priority", "oversize_first":
				h = &TranslateHandler{MaxWordsPerBatch: 4}
				doc.Segments[1].RoundWork.Member = false
				if name == "oversize_first" {
					doc.Segments[2].Source = "one two three four five"
				}
			case "full_leftover_before_S":
				limits.Segments = 2
				for _, i := range []int{3, 4, 11} {
					doc.Segments[i].RoundWork.Member = false
				}
			case "fallback_shrink":
				h = &TranslateHandler{MaxWordsPerBatch: 8, FallbackShrink: .5}
				for i := range doc.Segments {
					doc.Segments[i].RoundWork.Cursor.Pool = 1
				}
			case "future_ready":
				doc.Segments[0].RoundWork.Cursor.Attempt = 2
				doc.Segments[0].RoundWork.Cursor.NextAttemptAt = now.Add(time.Hour)
			case "pool_barrier":
				for i := range doc.Segments {
					doc.Segments[i].RoundWork.Cursor.Pool = 1
				}
				doc.Segments[0].RoundWork.Cursor = WorkCursor{State: "response_pending"}
			case "empty_state":
				doc.Segments[0].RoundWork.Cursor.State = ""
			case "noncanonical_context":
				h = &TranslateHandler{MaxWordsPerBatch: 8, Context: ContextConfig{Enabled: true, Before: 2, After: 1}}
				for _, i := range []int{1, 3, 4, 7} {
					doc.Segments[i].RoundWork.Member = false
				}
				doc.Segments[1].OriginalSource = "original three words"
				doc.Segments[4].Skip = true
				doc.Segments[7].StructuralOnly = true
			case "revision_words_span":
				h = &ReviseHandler{MaxWordsPerBatch: 8, MaxBatchIndexSpan: 3}
				for i := range doc.Segments {
					doc.Segments[i].Target = "target two"
				}
				doc.Segments[1].RoundWork.Member = false
			case "active_candidate_exclusions":
				active[0] = true
				active[5] = true
				candidates[2] = &Candidate{Index: 2}
				doc.Segments[3].RoundWork.Cursor.State = "candidate"
				doc.Segments[4].RoundWork.Cursor.State = "response_pending"
				doc.Segments[6].RoundWork.Cursor.State = "unresolved"
				doc.Segments[7].RoundWork.Completed = true
				doc.Segments[8].RoundWork.Cursor.Pool = 3
				doc.Segments[9].RoundWork.Cursor.NextAttemptAt = now.Add(time.Hour)
			}
			r := stagedTestRuntime(t, limits)
			s, err := newMainScheduler(context.Background(), doc, h, r, 3, "oracle")
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			store := NewMemoryRoundStore(nil)
			defer store.Close()
			for i := range active {
				s.refresh(i, true, candidates[i], now)
			}
			for i, c := range candidates {
				s.refresh(i, active[i], c, now)
			}
			for step := 0; step < len(doc.Segments); step++ {
				want, pool, attempt := schedulerOracleBatch(t, doc, h, active, candidates, 3, limits.Segments, now)
				g := s.selectedGroup()
				if len(want) == 0 {
					if g >= 0 {
						t.Fatal("index bypassed pool barrier")
					}
					break
				}
				if g < 0 || s.groups[g].pool != pool || s.groups[g].attempt != attempt {
					t.Fatalf("selected group=%d want pool%d/attempt%d", g, pool, attempt)
				}
				p, err := s.buildPlan(context.Background(), g, store)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(p.indices, want) {
					t.Fatalf("step%d batch=%v want=%v", step, p.indices, want)
				}
				if name == "same_pool_source_attempt" && step == 0 && attempt != 2 {
					t.Fatal("selected minimum Attempt instead of source order")
				}
				for _, idx := range p.indices {
					doc.Segments[idx].RoundWork.Completed = true
					s.refresh(idx, false, nil, now)
				}
				p.close()
			}
		})
	}
}
func TestMainSchedulerContextRangeExcludesSaturatedMain(t *testing.T) {
	doc := newTestDoc(5)
	for i := range doc.Segments {
		doc.Segments[i].Source = "two words"
		doc.Segments[i].RoundWork.Member = true
	}
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	s, err := newMainScheduler(context.Background(), doc, &TranslateHandler{Context: ContextConfig{Enabled: true, Before: math.MaxInt}}, r, 2, "range")
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	// A scalar cell models an overflow-sized raw source without allocating a
	// document-sized fixture. The selected cells must be excluded before sum.
	s.contextWords[s.contextBase+1] = math.MaxInt
	for i := s.contextBase - 1; i > 0; i-- {
		s.contextWords[i] = scheduleSum(s.contextWords[2*i], s.contextWords[2*i+1])
	}
	if got := s.contextCount([]int{1, 3}, math.MaxInt); got != 6 {
		t.Fatalf("excluded saturated cell spoiled direct gaps: %d", got)
	}
	if got := s.contextCount([]int{3}, math.MaxInt); got != math.MaxInt {
		t.Fatalf("included saturation lost: %d", got)
	}
}
func TestMainSchedulerRunSummarySaturation(t *testing.T) {
	s := &mainScheduler{h: &TranslateHandler{MaxWordsPerBatch: math.MaxInt}, groups: []mainScheduleGroup{{pool: 0}}}
	a := scheduleRun{first: 0, last: 0, count: 1, words: math.MaxInt}
	b := scheduleRun{first: 1, last: 1, count: 1, words: 1}
	r := s.mergeRuns(scheduleRuns{first: a, last: a, best: a}, scheduleRuns{first: b, last: b, best: b}, 0)
	if r.split.count != 2 || !r.split.overflow {
		t.Fatalf("sum overflow lost split precedence: %+v", r)
	}
}
func TestDeferredMainPlanReturnedWaitKeepsSemanticVersion(t *testing.T) {
	for _, name := range []string{"self_only", "self_only_empty_group", "earlier_deadline_ready", "empty_group_rebuilt", "rebuilt_then_empty"} {
		t.Run(name, func(t *testing.T) {
			n := 6
			if name == "self_only_empty_group" {
				n = 2
			}
			if name == "empty_group_rebuilt" || name == "rebuilt_then_empty" {
				n = 3
			}
			doc := newTestDoc(n)
			now := time.Now()
			for i := range doc.Segments {
				doc.Segments[i].Source = "two words"
				doc.Segments[i].RoundWork.Member = true
			}
			changed := name == "earlier_deadline_ready" || name == "empty_group_rebuilt" || name == "rebuilt_then_empty"
			if changed {
				doc.Segments[0].RoundWork.Cursor.NextAttemptAt = now.Add(time.Hour)
			}
			r := stagedTestRuntime(t, DefaultCandidateLimits())
			s, err := newMainScheduler(context.Background(), doc, &TranslateHandler{MaxWordsPerBatch: 4}, r, 2, "return-wait")
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			store := NewMemoryRoundStore(nil)
			defer store.Close()
			g := s.selectedGroup()
			p, err := s.buildPlan(context.Background(), g, store)
			if err != nil {
				t.Fatal(err)
			}
			defer p.close()
			active := map[int]bool{}
			for _, idx := range p.indices {
				active[idx] = true
				s.refresh(idx, true, nil, now)
			}
			p.captureDispatch(s, g)
			if changed {
				s.expire(now.Add(2*time.Hour), active, nil)
				if name == "rebuilt_then_empty" {
					doc.Segments[0].RoundWork.Completed = true
					s.refresh(0, false, nil, now.Add(2*time.Hour))
				}
			}
			resume := p.canRestoreWait(s)
			if resume == changed {
				t.Fatalf("resume=%v after %s", resume, name)
			}
			for _, idx := range p.indices {
				delete(active, idx)
				s.refresh(idx, false, nil, now.Add(2*time.Hour))
			}
			if resume {
				g = s.selectedGroup()
				p.version = s.groups[g].version
			}
			if p.valid(s) != !changed {
				t.Fatalf("returned plan validity=%v after %s", p.valid(s), name)
			}
			if name == "earlier_deadline_ready" || name == "empty_group_rebuilt" {
				next, err := s.buildPlan(context.Background(), s.selectedGroup(), store)
				if err != nil {
					t.Fatal(err)
				}
				defer next.close()
				if len(next.indices) != 2 || next.indices[0] != 0 || reflect.DeepEqual(next.indices, p.indices) {
					t.Fatalf("new earlier public batch=%v old=%v", next.indices, p.indices)
				}
			}
		})
	}
}
func TestIncrementalMainSchedulerWorkBound(t *testing.T) {
	for _, n := range []int{32, 64, 128, 256} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			doc := newTestDoc(n)
			for i := range doc.Segments {
				doc.Segments[i].Source = strings.Repeat("word ", 20)
				doc.Segments[i].RoundWork.Member = true
			}
			r := stagedTestRuntime(t, DefaultCandidateLimits())
			counts := map[string]int64{}
			ctx := backend.WithMemberWorkRecorder(context.Background(), func(e backend.MemberWorkEvent) {
				if e.Phase == "scheduler" {
					counts[e.Operation] += e.Rows
				}
			})
			s, err := newMainScheduler(ctx, doc, &TranslateHandler{MaxWordsPerBatch: 40}, r, 3, "work")
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			store := NewMemoryRoundStore(nil)
			defer store.Close()
			coldWords, coldVisits := counts["word_input"], counts["doc_member_visit"]
			if coldVisits != int64(3*n) || counts["index_build_visit"] != int64(2*s.base) {
				t.Fatalf("cold passes omitted: doc=%d init=%d", coldVisits, counts["index_build_visit"])
			}
			p, err := s.buildPlan(ctx, s.selectedGroup(), store)
			if err != nil {
				t.Fatal(err)
			}
			wakeVisits := counts["index_visit"]
			wakeDeadlines := counts["deadline_probe"]
			for i := 0; i < 128; i++ {
				s.expire(time.Now(), nil, nil)
				if !p.valid(s) {
					t.Fatal("empty wake invalidated plan")
				}
				s.deadline()
			}
			if counts["plan_build"] != 1 || counts["word_input"] != coldWords || counts["doc_member_visit"] != coldVisits {
				t.Fatal("empty wake performed hidden work")
			}
			if counts["index_visit"]-wakeVisits > 3*128 {
				t.Fatal("unchanged wake traversed beyond constant ready root")
			}
			if counts["deadline_probe"]-wakeDeadlines != 128 {
				t.Fatal("unchanged wake did not inspect exactly one heap top")
			}
			p.close()
			beforeUpdates := counts["index_visit"]
			for i := range doc.Segments {
				doc.Segments[i].RoundWork.Completed = true
				s.refresh(i, false, nil, time.Now())
			}
			// Count actual ordinal comparisons, group/ready traversal and choice
			// updates separately from cold initialization and K root probes.
			// An update has logarithmic searches/rotations, not an N-member scan.
			limit := int64(n) * int64(8*bits.Len(uint(n))+8)
			if counts["index_visit"]-beforeUpdates > limit || counts["doc_member_visit"]-coldVisits != int64(n) {
				t.Fatalf("update visits=%d/%d doc=%d n%d", counts["index_visit"]-beforeUpdates, limit, counts["doc_member_visit"]-coldVisits, n)
			}
			if counts["word_input"] != int64(n) {
				t.Fatal("member completion recounted unchanged text")
			}
			doc.Segments[0].RoundWork.Completed = false
			for i := 0; i < 128; i++ {
				doc.Segments[0].RoundWork.Cursor.NextAttemptAt = time.Now().Add(time.Duration(i+1) * time.Hour)
				s.refresh(0, false, nil, time.Now())
				if len(s.heap) != 1 {
					t.Fatal("stale deadline nodes accumulated")
				}
			}
			s.expire(time.Now().Add(200*time.Hour), nil, nil)
			if len(s.heap) != 0 || s.selectedGroup() < 0 {
				t.Fatal("absolute deadline did not restore ready member")
			}
		})
	}
}
func TestColdMainSchedulerSealedRevisionWaitsForBuildStation(t *testing.T) {
	root, err := resourcebudget.New(resourcebudget.Policy{Instance: resourcebudget.Limits{MaxBuildWorkers: 1, MaxPayloadBytes: 64 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	if err = r.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "cold-revision"}); err != nil {
		t.Fatal(err)
	}
	holder := resourcebudget.Scope{ActorID: 2, CallID: "paid-builder"}
	pressure := resourcebudget.Vector{BuildWorkers: 1, PayloadBytes: 1}
	if err = root.Claim(holder, "held", pressure); err != nil {
		t.Fatal(err)
	}
	held := true
	defer func() {
		if held {
			root.Release(holder, "held", pressure)
		}
	}()
	store := NewMemoryRoundStore(nil)
	defer store.Close()
	if _, err = store.Seal(context.Background(), []int{0}); err != nil {
		t.Fatal(err)
	}
	doc := newTestDoc(1)
	doc.Segments[0].Source = "two words"
	doc.Segments[0].Target = "old target"
	doc.Segments[0].Status = "translated"
	work := map[string]int64{}
	ctx := backend.WithMemberWorkRecorder(context.Background(), func(e backend.MemberWorkEvent) { work[e.Phase+"/"+e.Operation] += e.Rows })
	handler := &ReviseHandler{BatchSize: 1, MaxWordsPerBatch: 10}
	_, err = RunStagedRound(ctx, Round{Concurrency: 1, Handler: handler}, doc, store, r, quietLogger(), nil)
	d := workDeferred(err)
	if d == nil || d.Wait.Reason != "aggregate_build" {
		t.Fatalf("sealed revise did not defer occupied build station: %v", err)
	}
	if work["scheduler/word_input"] != 0 || work["scheduler/ready_update"] != 0 || r.Admission.OwnedPayloadSnapshot().Current["scheduler"] != 0 {
		t.Fatal("cold index allocated/count-words before CPU station")
	}
	if err = root.Release(holder, "held", pressure); err != nil {
		t.Fatal(err)
	}
	held = false
	var scheduler *mainScheduler
	err = r.Run(ctx, func() error {
		var buildErr error
		scheduler, buildErr = newMainScheduler(ctx, doc, handler, r, 1, "resumed-cold")
		return buildErr
	})
	if err != nil {
		t.Fatal(err)
	}
	scheduler.close()
	if work["scheduler/word_input"] != 2 {
		t.Fatal("released station did not build source and revise target once")
	}
}
func TestOwnedDeferredMainPlanLifecycle(t *testing.T) {
	doc := newTestDoc(4)
	for i := range doc.Segments {
		doc.Segments[i].Source = "two words"
		doc.Segments[i].RoundWork.Member = true
	}
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	s, err := newMainScheduler(context.Background(), doc, &TranslateHandler{MaxWordsPerBatch: 4}, r, 2, "owned-scheduler")
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	store := NewMemoryRoundStore(nil)
	defer store.Close()
	p, err := s.buildPlan(context.Background(), s.selectedGroup(), store)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	owner := p.owner
	indices, keys := p.indices, p.keys
	firstPointer := unsafe.StringData(keys[0])
	paid := r.Admission.BuildBytes(owner)
	if paid < p.backingBytes() || paid == 0 || cap(indices) != len(indices) {
		t.Fatal("plan has unowned or oversized backing")
	}
	for i := 0; i < 128; i++ {
		if !p.valid(s) || unsafe.StringData(p.keys[0]) != firstPointer || r.Admission.BuildBytes(owner) != paid {
			t.Fatal("defer rebuilt or refunded live plan")
		}
	}
	doc.Segments[3].RoundWork.Cursor.State = "candidate"
	s.refresh(3, false, &Candidate{Index: 3}, time.Now())
	if p.valid(s) {
		t.Fatal("eligible mutation did not invalidate plan")
	}
	if r.Admission.BuildBytes(owner) != paid {
		t.Fatal("invalidation refunded a still borrowed plan")
	}
	indices = nil
	keys = nil
	p.close()
	if r.Admission.BuildBytes(owner) != 0 {
		t.Fatal("joined plan leaked")
	}
	s.close()
	snap := r.Admission.OwnedPayloadSnapshot()
	if snap.Current["scheduler"] != 0 || snap.Current["main_plan"] != 0 {
		t.Fatalf("owned terminal=%+v", snap)
	}
}
