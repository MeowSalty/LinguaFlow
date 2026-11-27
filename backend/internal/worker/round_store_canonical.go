package worker

import (
	"cmp"
	"context"
	"slices"
	"sort"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type canonicalIndexEntry struct{ id, ordinal int }
type canonicalIndexKey struct {
	job, resource, jobResource, round, revision, count int
	epoch, generation                                  int64
	hasRevision                                        bool
	digest                                             string
	document                                           *pipeline.Document
	segments                                           uintptr // Identity only; does not retain a retired source arena.
}

func (s *roundStore) canonicalKey(doc *pipeline.Document) canonicalIndexKey {
	k := canonicalIndexKey{job: s.scope.JobID, resource: s.scope.ResourceID, jobResource: s.scope.JobResourceID, round: s.scope.RoundID, epoch: s.scope.RetryEpoch, generation: s.scope.SourceGeneration, digest: s.digest, document: doc, count: len(doc.Segments), segments: uintptr(unsafe.Pointer(unsafe.SliceData(doc.Segments)))}
	if s.scope.SourceRevisionID != nil {
		k.hasRevision, k.revision = true, *s.scope.SourceRevisionID
	}
	return k
}

// This index belongs to one canonical load/round, not to a job-wide cache.
// Source order stays in the document; only the scalar reverse lookup is sorted.
func (s *roundStore) prepareCanonicalIndex(ctx context.Context, doc *pipeline.Document) error {
	s.CloseCanonicalIndex()
	runtime := pipeline.ExecutionRuntimeFromContext(ctx)
	buildCtx := ctx
	if runtime != nil {
		buildCtx = backend.WithBuildWorkers(runtime.BuildContext(ctx, "worker-index:"+pipeline.NewWorkID()), runtime)
	}
	return backend.RunBuild(buildCtx, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bounds, err := backend.NewBuildBoundsFromContext(buildCtx, 0, 0)
		if err != nil {
			return err
		}
		defer bounds.CloseRetired()
		bytes, err := backend.AllocationBytes(int64(len(doc.Segments)), int64(unsafe.Sizeof(canonicalIndexEntry{})))
		if err != nil {
			return err
		}
		if err = bounds.ReserveAllocation(int64(len(doc.Segments)), int64(unsafe.Sizeof(canonicalIndexEntry{}))); err != nil {
			return err
		}
		entries := make([]canonicalIndexEntry, len(doc.Segments))
		trace := backend.NewOwnedTrace(buildCtx, "worker_index", "canonical-ordinal")
		trace.Set(bytes)
		installed := false
		defer func() {
			if !installed {
				entries = nil
				trace.Clear()
			}
		}()
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "worker_index_build", Operation: "build", Rows: 1})
		for i, seg := range doc.Segments {
			if err := ctx.Err(); err != nil {
				return err
			}
			backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "worker_index_build", Operation: "doc_member_visit", Rows: 1})
			if seg.DBID <= 0 {
				return workstate.ErrManifest
			}
			entries[i] = canonicalIndexEntry{id: seg.DBID, ordinal: i}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		slices.SortFunc(entries, func(a, b canonicalIndexEntry) int {
			backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "worker_index_build", Operation: "index_visit", Rows: 2})
			return cmp.Compare(a.id, b.id)
		})
		if err := ctx.Err(); err != nil {
			return err
		}
		for i := range entries {
			backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "worker_index_build", Operation: "index_build_visit", Rows: 1})
			if i > 0 && entries[i-1].id == entries[i].id {
				return workstate.ErrManifest
			}
		}
		credit, err := bounds.RetainedAllocation(bytes)
		if err != nil {
			return err
		}
		retained, err := credit.DetachToBuild()
		if err != nil {
			return err
		}
		s.canonical, s.indexToID = doc, nil
		s.canonicalIndex, s.canonicalIndexKey = entries, s.canonicalKey(doc)
		s.canonicalIndexBounds, s.canonicalIndexTrace = retained, trace
		installed = true
		return nil
	})
}

// The caller must join all lookups/writers before closing or rebinding.
func (s *roundStore) CloseCanonicalIndex() {
	s.canonicalIndex = nil
	s.canonicalIndexKey = canonicalIndexKey{}
	s.canonicalIndexTrace.Clear()
	s.canonicalIndexTrace = nil
	s.canonicalIndexBounds.CloseRetired()
	s.canonicalIndexBounds = nil
}

func (s *roundStore) LoadCanonical(ctx context.Context, doc *pipeline.Document) (_ bool, _ int, resultErr error) {
	defer func() {
		if resultErr != nil {
			s.CloseCanonicalIndex()
		}
	}()
	// Only the scalar recorder is retained; no request context or payload tree.
	s.workObserverMu.Lock()
	if s.workObserver == nil {
		s.workObserver = backend.MemberWorkRecorder(ctx)
	}
	s.workObserverMu.Unlock()
	if err := s.prepareCanonicalIndex(ctx, doc); err != nil {
		return false, 0, s.classify(err)
	}
	pipeline.RetainCanonicalIndex(ctx, s.CloseCanonicalIndex)
	header, err := s.store.LoadRoundHeader(ctx, s.scope)
	if err != nil {
		return false, 0, s.classify(err)
	}
	for i := range doc.Segments {
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "canonical_load", Operation: "doc_member_visit", Rows: 1})
		doc.Segments[i].RoundWork = model.RoundWorkRecord{}
	}
	if !header.Sealed {
		return false, header.Total, nil
	}
	after, count := 0, 0
	for {
		page, next, err := s.store.LoadRoundMembersPage(ctx, s.scope, after, 16)
		if err != nil {
			return true, header.Total, s.classify(err)
		}
		if len(page) == 0 {
			break
		}
		if next <= after {
			return true, header.Total, workstate.ErrManifest
		}
		for _, w := range page {
			idx, ok := s.segmentIndexObserved(ctx, w.SegmentID)
			if !ok || idx < 0 || idx >= len(doc.Segments) {
				return true, header.Total, workstate.ErrManifest
			}
			c := w.Cursor
			cursor := pipeline.WorkCursor{Pool: c.PoolIndex, Attempt: c.MainAttempts, LogicalAttempt: c.AlignmentAttempts, NetworkAttempt: c.AlignmentNetworkAttempts, Phase: c.PromptPhase, State: c.State}
			if pipeline.IsPendingPromptUpgrade(cursor.Phase) {
				cursor.NetworkAttempt = c.MainNetworkAttempts
			}
			if c.NextAttemptAt != nil {
				cursor.NextAttemptAt = *c.NextAttemptAt
			}
			if c.State == "stale" || c.State == "rejected" {
				cursor.State = "unresolved"
				cursor.Pool = max(1, s.retryAttempts()+1)
			}
			doc.Segments[idx].RoundWork = model.RoundWorkRecord{Member: true, Completed: c.State == "resolved", Cursor: cursor}
			backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "canonical_load", Operation: "doc_member_visit", Rows: 1})
			count++
		}
		after = next
	}
	if count != header.Total {
		return true, header.Total, workstate.ErrManifest
	}
	return true, count, nil
}
func (s *roundStore) SealCanonical(ctx context.Context, doc *pipeline.Document) error {
	total := 0
	for i := range doc.Segments {
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "canonical_seal", Operation: "doc_member_visit", Rows: 1})
		if doc.Segments[i].RoundWork.Member {
			total++
		}
	}
	walk := func(yield func(int) error) error {
		for i := range doc.Segments {
			backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "canonical_seal", Operation: "doc_member_visit", Rows: 1})
			if doc.Segments[i].RoundWork.Member {
				id, ok := s.segmentID(i)
				if !ok {
					return workstate.ErrManifest
				}
				if err := yield(id); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return s.classify(s.store.SealRoundStream(ctx, s.scope, total, walk))
}
func (s *roundStore) segmentIndex(id int) (int, bool) {
	return s.segmentIndexObserved(nil, id)
}
func (s *roundStore) segmentIndexObserved(ctx context.Context, id int) (int, bool) {
	s.workObserverMu.Lock()
	observe := s.workObserver
	s.workObserverMu.Unlock()
	if fromCtx := backend.MemberWorkRecorder(ctx); fromCtx != nil {
		observe = fromCtx
	}
	if s.canonical != nil {
		if observe != nil {
			observe(backend.MemberWorkEvent{Phase: "worker_index_lookup", Operation: "lookup", Rows: 1})
		}
		if s.canonicalIndexBounds == nil || s.canonicalIndexKey != s.canonicalKey(s.canonical) {
			return 0, false
		}
		at := sort.Search(len(s.canonicalIndex), func(i int) bool {
			if observe != nil {
				observe(backend.MemberWorkEvent{Phase: "worker_index_lookup", Operation: "index_visit", Rows: 1})
			}
			return s.canonicalIndex[i].id >= id
		})
		if at >= len(s.canonicalIndex) || s.canonicalIndex[at].id != id {
			return 0, false
		}
		entry := s.canonicalIndex[at]
		if observe != nil {
			observe(backend.MemberWorkEvent{Phase: "worker_index_lookup", Operation: "doc_member_visit", Rows: 1})
		}
		if entry.ordinal < len(s.canonical.Segments) && s.canonical.Segments[entry.ordinal].DBID == id {
			return entry.ordinal, true
		}
		return 0, false
	}
	for index, value := range s.indexToID {
		if observe != nil {
			observe(backend.MemberWorkEvent{Phase: "worker_index_lookup", Operation: "index_visit", Rows: 1})
		}
		if value == id {
			return index, true
		}
	}
	return 0, false
}
func (s *roundStore) segmentID(index int) (int, bool) {
	if s.canonical != nil {
		return canonicalDBIdentity(s.canonical, index)
	}
	id, ok := s.indexToID[index]
	return id, ok
}
func (s *roundStore) bindCanonical(doc *pipeline.Document) {
	s.CloseCanonicalIndex()
	s.canonical = doc
	s.indexToID = nil
}
