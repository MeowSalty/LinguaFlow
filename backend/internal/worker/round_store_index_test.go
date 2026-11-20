package worker

import (
	"context"
	"errors"
	"math/bits"
	"sync"
	"testing"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type canonicalIndexOwnedProbe struct{ current, peak int64 }

func (p *canonicalIndexOwnedProbe) ObserveOwned(category, _ string, bytes int64) {
	if category == "worker_index" {
		p.current, p.peak = bytes, max(p.peak, bytes)
	}
}

func TestCanonicalOrdinalIndexIdentityAndOwnership(t *testing.T) {
	for _, name := range []string{"source_order", "scope", "source_revision", "document_rebind", "unknown_id", "duplicate_id", "cancel_during_build", "capacity"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			doc := &pipeline.Document{Segments: []pipeline.Segment{{DBID: 91}, {DBID: 7}, {DBID: 40}}}
			revision := 2
			s := &roundStore{scope: workstate.Scope{JobID: 1, ResourceID: 2, RoundID: 3, RetryEpoch: 4, SourceGeneration: 5, SourceRevisionID: &revision}, digest: "snapshot"}
			defer s.CloseCanonicalIndex()
			bytes := int64(len(doc.Segments)) * int64(unsafe.Sizeof(canonicalIndexEntry{}))
			limit := bytes
			if name == "capacity" {
				limit--
			}
			var paid, peak int64
			owned := &canonicalIndexOwnedProbe{}
			ctx = backend.WithOwnedObserver(ctx, owned)
			ctx = backend.WithBuildAllocation(ctx, limit, func(n int64) error {
				paid += n
				peak = max(peak, paid)
				return nil
			}, func(n int64) { paid -= n })
			ctx = backend.WithMemberWorkRecorder(ctx, func(e backend.MemberWorkEvent) {
				if name == "cancel_during_build" && e.Phase == "worker_index_build" && e.Operation == "doc_member_visit" {
					cancel()
				}
			})
			if name == "duplicate_id" {
				doc.Segments[2].DBID = 91
			}
			err := s.prepareCanonicalIndex(ctx, doc)
			switch name {
			case "duplicate_id", "cancel_during_build", "capacity":
				want := workstate.ErrManifest
				if name == "cancel_during_build" {
					want = context.Canceled
				} else if name == "capacity" {
					want = backend.ErrBuildCapacity
				}
				if !errors.Is(err, want) || paid != 0 || owned.current != 0 || len(s.canonicalIndex) != 0 {
					t.Fatalf("failed build retained backing/credit: err=%v paid%d owned%d", err, paid, owned.current)
				}
				if name == "capacity" && (peak != 0 || owned.peak != 0) {
					t.Fatal("capacity refusal allocated before admission")
				}
				return
			}
			if err != nil || paid != bytes || owned.current != bytes || cap(s.canonicalIndex) != len(doc.Segments) {
				t.Fatalf("exact standalone bounded ownership: err=%v paid%d/%d owned%d", err, paid, bytes, owned.current)
			}
			for i, id := range []int{91, 7, 40} {
				if at, ok := s.segmentIndex(id); !ok || at != i || doc.Segments[i].DBID != id {
					t.Fatalf("sorted lookup reordered source: id%d at%d want%d", id, at, i)
				}
			}
			switch name {
			case "scope":
				s.scope.RetryEpoch++
				if _, ok := s.segmentIndex(7); ok {
					t.Fatal("scope drift reused an earlier index")
				}
			case "source_revision":
				revision++ // Same pointer, changed source identity.
				if _, ok := s.segmentIndex(7); ok {
					t.Fatal("source revision change reused an earlier index")
				}
			case "document_rebind":
				doc.Segments = append([]pipeline.Segment(nil), doc.Segments...)
				doc.Segments[1].DBID = 8
				if _, ok := s.segmentIndex(7); ok {
					t.Fatal("new canonical backing reused the earlier index")
				}
				if err := s.prepareCanonicalIndex(ctx, doc); err != nil {
					t.Fatal(err)
				}
				if at, ok := s.segmentIndex(8); !ok || at != 1 || peak != bytes {
					t.Fatal("rebinding changed ordinal or retained retired allocation")
				}
			case "unknown_id":
				for _, id := range []int{0, 8, 1000} {
					if _, ok := s.segmentIndex(id); ok {
						t.Fatalf("unknown DBID %d acquired an ordinal", id)
					}
				}
			}
			s.CloseCanonicalIndex()
			s.CloseCanonicalIndex()
			if paid != 0 || owned.current != 0 || s.canonicalIndexBounds != nil || len(s.canonicalIndex) != 0 {
				t.Fatalf("idempotent close leaked exact backing: paid%d owned%d", paid, owned.current)
			}
		})
	}
}

func TestCanonicalOrdinalIndexBuildStationAndJoinedLookups(t *testing.T) {
	const n = 128
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r := pipeline.NewExecutionRuntime(a, pipeline.DefaultCandidateLimits(), nil)
	defer r.Close()
	root, err := resourcebudget.New(resourcebudget.Policy{Instance: resourcebudget.Limits{MaxBuildWorkers: 1, MaxPayloadBytes: 1 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "worker-index"}); err != nil {
		t.Fatal(err)
	}
	doc := &pipeline.Document{Segments: make([]pipeline.Segment, n)}
	for i := range doc.Segments {
		doc.Segments[i].DBID = 1 + (67*i)%n
	}
	s := &roundStore{scope: workstate.Scope{ResourceID: 1, RoundID: 1}}
	defer s.CloseCanonicalIndex()
	var mu sync.Mutex
	rows := map[string]int64{}
	ctx := backend.WithMemberWorkRecorder(pipeline.WithExecutionRuntime(t.Context(), r), func(e backend.MemberWorkEvent) {
		mu.Lock()
		rows[e.Phase+"/"+e.Operation] += e.Rows
		mu.Unlock()
	})
	if err = a.ChangeStation(0, 1, 0); err != nil {
		t.Fatal(err)
	}
	err = s.prepareCanonicalIndex(ctx, doc)
	if _, deferred := err.(*pipeline.WorkDeferred); !deferred || len(rows) != 0 || a.OwnedPayloadSnapshot().Current["worker_index"] != 0 {
		t.Fatalf("busy CPU station did index work: %v rows=%v", err, rows)
	}
	if err = a.ChangeStation(0, -1, 0); err != nil {
		t.Fatal(err)
	}
	if err = s.prepareCanonicalIndex(ctx, doc); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errorsFound := make(chan int, 3*n)
	for worker := 0; worker < 3; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i, seg := range doc.Segments {
				if at, ok := s.segmentIndexObserved(ctx, seg.DBID); !ok || at != i {
					errorsFound <- seg.DBID
				}
			}
		}()
	}
	wg.Wait()
	close(errorsFound)
	if len(errorsFound) != 0 || rows["worker_index_lookup/doc_member_visit"] != 3*n || rows["worker_index_lookup/index_visit"] > 3*n*int64(bits.Len(uint(n))+1) {
		t.Fatalf("joined typed lookup mapping/work: errors%d rows=%v", len(errorsFound), rows)
	}
	bytes := int64(n) * int64(unsafe.Sizeof(canonicalIndexEntry{}))
	if got := a.OwnedPayloadSnapshot(); got.Current["worker_index"] != bytes || got.Peak["worker_index"] != bytes {
		t.Fatalf("live index has wrong owned fee: %+v", got)
	}
	s.CloseCanonicalIndex() // All lookup borrowers joined before this refund.
	if a.OwnedPayloadSnapshot().Current["worker_index"] != 0 || len(s.canonicalIndex) != 0 {
		t.Fatal("joined index did not retire backing")
	}
}

func TestCanonicalOrdinalIndexLoadFailureReleasesBacking(t *testing.T) {
	f := newRubyPipelineFixture(t, 1, 1)
	doc, store := f.round(t, 0)
	defer store.CloseCanonicalIndex()
	store.scope.RoundID = 0
	bytes := int64(len(doc.Segments)) * int64(unsafe.Sizeof(canonicalIndexEntry{}))
	var paid int64
	owned := &canonicalIndexOwnedProbe{}
	ctx := backend.WithOwnedObserver(t.Context(), owned)
	ctx = backend.WithBuildAllocation(ctx, bytes, func(n int64) error { paid += n; return nil }, func(n int64) { paid -= n })
	_, _, err := store.LoadCanonical(ctx, doc)
	if !ent.IsNotFound(err) || paid != 0 || owned.current != 0 || owned.peak != bytes || store.canonicalIndexBounds != nil {
		t.Fatalf("header query failure retained canonical index: err=%v paid%d owned%d/%d", err, paid, owned.current, owned.peak)
	}
}
