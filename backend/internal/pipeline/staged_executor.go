package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

type stagedHandler interface {
	RoundHandler
	PrepareBatch(context.Context, *Document, []int, int, *slog.Logger) batchResult
}

type stagedEvent struct {
	page         bool
	nextPage     int
	lastPage     bool
	main         bool
	indices      []int
	cursors      map[int]WorkCursor
	candidates   []*Candidate
	completed    *TranslatedSegment
	candidate    *Candidate
	outcome      CommitOutcome
	err          error
	inputTokens  int64
	outputTokens int64
}

// ErrResourceYield suspends an unfinished resource after all its callbacks have
// joined. Its persisted cursors remain authoritative on re-admission.
var ErrResourceYield = errors.New("resource yields to recovered candidates")

// RunStagedRound keeps the pool barrier until every accepted candidate has a
// durable destination. Main workers finish at reliable handoff, not alignment.
// Only this goroutine applies confirmed results to the resource Document.
func RunStagedRound(ctx context.Context, round Round, doc *Document, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger, reporter progress.Reporter) (RunRoundResult, error) {
	h, ok := round.Handler.(stagedHandler)
	if !ok {
		return RunRound(ctx, round, doc, nil, logger, reporter)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if reporter == nil {
		reporter = progress.Nop{}
	}
	if store == nil {
		store = NewMemoryRoundStore(nil)
	}
	state, err := store.Load(ctx)
	if err != nil {
		return RunRoundResult{}, err
	}
	batches, err := h.BuildBatches(ctx, doc, nil, 0)
	if err != nil {
		return RunRoundResult{}, err
	}
	if !state.Sealed {
		var members []int
		for _, batch := range batches {
			members = append(members, batch...)
		}
		state, err = store.Seal(ctx, uniqueSortedInts(members))
		if err != nil {
			return RunRoundResult{}, err
		}
	}
	if len(state.Members) == 0 {
		return RunRoundResult{}, nil
	}
	reporter.StageStart(h.ModeName(), len(state.Members))
	defer reporter.StageDone()
	if checked, ok := reporter.(interface{ StageError() error }); ok {
		if err := checked.StageError(); err != nil {
			return RunRoundResult{}, err
		}
	}
	remaining := make(map[int]bool, len(state.Members))
	completed := map[int]bool{}
	for _, idx := range state.Members {
		remaining[idx] = true
	}
	for _, idx := range state.Completed {
		delete(remaining, idx)
		completed[idx] = true
	}
	cursors := state.Cursors
	if cursors == nil {
		cursors = map[int]WorkCursor{}
	}
	candidates := map[int]*Candidate{}
	// Decode at most one page of recovery payloads. The Job has reconstructed
	// aggregate occupancy from small headers before any resource is admitted.
	pageAfter, pageActive, recoveryDone := 0, false, false
	maxPools := max(1, round.Retry.MaxAttempts+1)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan stagedEvent, 32)
	var wg sync.WaitGroup
	active := map[int]bool{}
	mainActive, candidateActive := 0, 0
	var firstErr error
	yielded := false
	paused := func() bool { return runtime.Gate != nil && runtime.Gate.Paused() }
	apply := func(e stagedEvent) {
		atomic.AddInt64(&doc.InputTokens, e.inputTokens)
		atomic.AddInt64(&doc.OutputTokens, e.outputTokens)
		if e.page {
			pageActive = false
			pageAfter, recoveryDone = e.nextPage, e.lastPage
		} else if e.main {
			mainActive--
		} else {
			candidateActive--
		}
		for _, idx := range e.indices {
			delete(active, idx)
		}
		for idx, cursor := range e.cursors {
			cursors[idx] = cursor
		}
		for _, c := range e.candidates {
			if !remaining[c.Index] {
				if firstErr == nil {
					firstErr = fmt.Errorf("candidate is outside unfinished manifest")
					cancel()
				}
				continue
			}
			candidates[c.Index] = c
		}
		if e.candidate != nil {
			c := e.candidate
			candidates[c.Index] = c
			if e.outcome == CommitAccepted || e.outcome == CommitExisting || e.outcome == CommitNoop {
				if e.completed != nil {
					applied := false
					if local, ok := store.(interface {
						ApplyConfirmed(context.Context, *Candidate, TranslatedSegment) (bool, error)
					}); ok {
						var err error
						applied, err = local.ApplyConfirmed(ctx, c, *e.completed)
						if err != nil {
							if firstErr == nil {
								firstErr = err
								cancel()
							}
							return
						}
					}
					if !applied {
						doc.Segments[c.Index].Target = e.completed.TargetText
						doc.Segments[c.Index].Issues = e.completed.Issues
						doc.Segments[c.Index].Status = c.Segment.Status
						doc.Segments[c.Index].ContentVersion = c.Segment.ContentVersion
						doc.Segments[c.Index].TargetIsNull = false
					}
				}
				completed[c.Index] = true
				delete(remaining, c.Index)
				delete(candidates, c.Index)
				runtime.Window.Release(c.ID)
				reporter.SegmentDone()
			} else if e.outcome == CommitRejected || e.outcome == CommitStale {
				delete(candidates, c.Index)
				runtime.Window.Release(c.ID)
				if _, persisted := e.cursors[c.Index]; !persisted {
					cursors[c.Index] = WorkCursor{Pool: maxPools, State: "unresolved"}
				}
			}
		}
		if e.err != nil && firstErr == nil {
			firstErr = e.err
			cancel()
		}
	}
	for {
		windowChanged := runtime.Window.Changed()
		if runCtx.Err() == nil && !paused() {
			if !recoveryDone && !pageActive && len(candidates) == 0 {
				pageActive = true
				wg.Add(1)
				go func(after int) {
					defer wg.Done()
					page, next, err := store.Candidates(runCtx, after, 16)
					e := stagedEvent{page: true, nextPage: next, lastPage: len(page) == 0, err: err}
					if err == nil && len(page) > 0 && next <= after {
						e.err = fmt.Errorf("candidate page did not advance")
					}
					for _, c := range page {
						bytes := c.StoredBytes
						var err error
						if bytes == 0 {
							var payload []byte
							payload, err = c.Encode()
							bytes = int64(len(payload))
						}
						if err == nil {
							err = runtime.Window.Restore(c.ID, bytes)
						}
						if err != nil {
							e.err = err
							break
						}
						e.candidates = append(e.candidates, c)
					}
					events <- e
				}(pageAfter)
			}
			// Restored/accepted drafts have priority over producing new payloads.
			keys := make([]int, 0, len(candidates))
			for idx := range candidates {
				keys = append(keys, idx)
			}
			sort.Ints(keys)
			for _, idx := range keys {
				if candidateActive >= 16 {
					break
				}
				c := candidates[idx]
				if active[idx] || time.Now().Before(c.NextAttemptAt) {
					continue
				}
				active[idx] = true
				candidateActive++
				wg.Add(1)
				go func(c *Candidate) {
					defer wg.Done()
					events <- processCandidate(runCtx, round, c, store, runtime, logger, reporter)
				}(c)
			}
			for recoveryDone && mainActive < max(1, round.Concurrency) {
				pool := -1
				attempt := 0
				for idx := range remaining {
					cur := cursors[idx]
					if active[idx] || candidates[idx] != nil || cur.Pool >= maxPools || cur.State == "unresolved" || cur.State == "candidate" || time.Now().Before(cur.NextAttemptAt) {
						continue
					}
					if pool < 0 || cur.Pool < pool {
						pool = cur.Pool
						attempt = cur.Attempt
					}
				}
				if pool < 0 {
					break
				}
				// Pools are resource-local barriers, including drafts and callbacks.
				blocked := false
				for idx := range remaining {
					if cursors[idx].Pool < pool && cursors[idx].State != "unresolved" {
						blocked = true
						break
					}
				}
				if blocked {
					break
				}
				var eligible []int
				for idx := range remaining {
					cur := cursors[idx]
					if !active[idx] && candidates[idx] == nil && cur.Pool == pool && cur.Attempt == attempt && cur.State != "unresolved" && cur.State != "candidate" && !time.Now().Before(cur.NextAttemptAt) {
						eligible = append(eligible, idx)
					}
				}
				sort.Ints(eligible)
				built, e := h.BuildBatches(runCtx, doc, eligible, pool)
				if e != nil {
					firstErr = e
					cancel()
					break
				}
				if len(built) == 0 {
					firstErr = fmt.Errorf("unfinished manifest has no executable batch at pool %d", pool)
					cancel()
					break
				}
				idxs := built[0]
				reservation := NewWorkID()
				for len(idxs) > 0 && !runtime.Window.TryReserve(reservation, len(idxs)) {
					if len(idxs) == 1 {
						idxs = nil
						break
					}
					idxs = idxs[:len(idxs)/2]
				}
				if len(idxs) == 0 {
					if mainActive+candidateActive == 0 && len(candidates) == 0 {
						yielded = true
					}
					break
				}
				copyDoc := snapshotDocument(doc, idxs)
				for _, idx := range idxs {
					active[idx] = true
				}
				mainActive++
				wg.Add(1)
				go func(idxs []int, pool, attempt int, snapshot *Document, key string) {
					defer wg.Done()
					events <- prepareMain(runCtx, round, h, snapshot, idxs, pool, attempt, key, store, runtime, logger, reporter)
				}(idxs, pool, attempt, copyDoc, reservation)
			}
		}
		if mainActive+candidateActive == 0 && !pageActive {
			if firstErr != nil || runCtx.Err() != nil || paused() || yielded {
				break
			}
			pending := false
			for idx := range remaining {
				if candidates[idx] != nil || (cursors[idx].Pool < maxPools && cursors[idx].State != "unresolved") {
					pending = true
					break
				}
			}
			if !pending && recoveryDone {
				break
			}
			if pending && recoveryDone && len(candidates) == 0 {
				future := false
				for idx := range remaining {
					if cursors[idx].NextAttemptAt.After(time.Now()) {
						future = true
						break
					}
				}
				if !future {
					firstErr = fmt.Errorf("unfinished manifest has no candidate, runnable batch, or retry deadline")
					break
				}
			}
		}
		// Only real deadlines use a timer; capacity changes wake immediately.
		var deadline time.Time
		for idx := range remaining {
			if active[idx] {
				continue
			}
			at := cursors[idx].NextAttemptAt
			if c := candidates[idx]; c != nil {
				at = c.NextAttemptAt
			}
			if at.After(time.Now()) && (deadline.IsZero() || at.Before(deadline)) {
				deadline = at
			}
		}
		var timer *time.Timer
		var timerC <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(max(time.Until(deadline), time.Nanosecond))
			timerC = timer.C
		}
		changed := windowChanged
		if runCtx.Err() != nil || paused() {
			apply(<-events)
		} else {
			select {
			case e := <-events:
				apply(e)
			case <-changed:
			case <-timerC:
			case <-runtime.Gate.Done():
			case <-runCtx.Done():
			}
		}
		if timer != nil {
			timer.Stop()
		}
	}
	wg.Wait()
	var result RunRoundResult
	for idx := range completed {
		result.Resolved = append(result.Resolved, idx)
	}
	for idx := range remaining {
		result.Unresolved = append(result.Unresolved, idx)
	}
	sort.Ints(result.Resolved)
	sort.Ints(result.Unresolved)
	if firstErr != nil {
		return result, firstErr
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if yielded {
		return result, ErrResourceYield
	}
	if !paused() {
		if err := h.Finalize(ctx, doc, result.Unresolved); err != nil {
			return result, err
		}
	}
	return result, nil
}

func snapshotDocument(doc *Document, idxs []int) *Document {
	copy := &Document{SourceLang: doc.SourceLang, TargetLang: doc.TargetLang, Format: doc.Format, Segments: append([]Segment(nil), doc.Segments...)}
	if doc.Vars != nil {
		copy.Vars = cloneValue(reflect.ValueOf(doc.Vars)).Interface().(map[string]any)
	}
	if doc.ResolvedIndices != nil {
		copy.ResolvedIndices = make(map[int]struct{}, len(doc.ResolvedIndices))
		for idx := range doc.ResolvedIndices {
			copy.ResolvedIndices[idx] = struct{}{}
		}
	}
	for _, idx := range idxs {
		copy.Segments[idx] = cloneSegment(doc.Segments[idx])
	}
	return copy
}

func prepareMain(ctx context.Context, round Round, h stagedHandler, doc *Document, idxs []int, pool, attempt int, reservation string, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger, reporters ...progress.Reporter) (e stagedEvent) {
	e = stagedEvent{main: true, indices: idxs, cursors: map[int]WorkCursor{}}
	defer func() {
		e.inputTokens = atomic.LoadInt64(&doc.InputTokens)
		e.outputTokens = atomic.LoadInt64(&doc.OutputTokens)
	}()
	defer runtime.Window.Release(reservation)
	if attempt >= transientBudgetFor(round.Retry) {
		for _, idx := range idxs {
			cursor := WorkCursor{Pool: pool + 1, State: "pending"}
			if err := runtime.Save(ctx, func(saveCtx context.Context) error { return store.Cursor(saveCtx, idx, cursor) }); err != nil {
				e.err = err
				return e
			}
			e.cursors[idx] = cursor
		}
		return e
	}
	_, _, _, _, roundIndex := alignmentSettings(round.Handler)
	session := &requestSession{runtime: runtime, store: store, intent: RequestIntent{Stage: backend.RequestStageMain, Indices: idxs, Pool: pool, Attempt: attempt, Phase: "main", RoundIndex: roundIndex, ResourceID: storeResourceID(store)}}
	if len(reporters) > 0 {
		session.reporter = reporters[0]
	}
	defer func() {
		if e.err != nil {
			session.fail(e.err)
		}
		if err := session.finish(); err != nil && e.err == nil {
			e.err = err
		}
	}()
	ctx = context.WithValue(ctx, requestSessionKey{}, session)
	result := h.PrepareBatch(ctx, doc, idxs, attempt, logger)
	if session.err != nil {
		e.err = session.err
		return e
	}
	if ctx.Err() != nil {
		e.err = ctx.Err()
		return e
	}
	if result.err != nil {
		e.err = result.err
		return e
	}
	for _, c := range result.candidates {
		c.PoolIndex = pool
		c.MainAttempt = attempt + 1
		c.ParentRequestID = session.lastID
		data, err := c.Encode()
		if err != nil {
			e.err = err
			return e
		}
		if int64(len(data)) > runtime.Window.limits.ItemBytes {
			e.err = ErrCandidateCapacity
			return e
		}
		if err = runtime.Save(ctx, func(saveCtx context.Context) error { return store.Save(saveCtx, c) }); err != nil {
			e.err = err
			return e
		}
		if err = runtime.Window.Transfer(reservation, c.ID, int64(len(data))); err != nil {
			e.err = err
			return e
		}
		e.candidates = append(e.candidates, c)
	}
	accepted := map[int]bool{}
	for _, c := range e.candidates {
		accepted[c.Index] = true
		e.cursors[c.Index] = WorkCursor{Pool: pool, Attempt: attempt + 1, State: "candidate"}
	}
	for _, idx := range idxs {
		if accepted[idx] {
			continue
		}
		cursor := WorkCursor{Pool: pool + 1, State: "pending"}
		if result.retry != nil && attempt+1 < transientBudgetFor(round.Retry) {
			cursor.Pool = pool
			cursor.Attempt = attempt + 1
			cursor.NextAttemptAt = result.retry.notBefore
		}
		if len(result.fatalUnresolved) > 0 {
			cursor.Pool = max(1, round.Retry.MaxAttempts+1)
			cursor.State = "unresolved"
		}
		if runtime.Gate != nil && runtime.Gate.Paused() {
			cursor.Pool = pool
			cursor.Attempt = attempt
			if session.calls > 0 {
				cursor.Attempt++
			}
		}
		if cursor.Attempt >= transientBudgetFor(round.Retry) {
			cursor.Pool++
			cursor.Attempt = 0
		}
		if err := runtime.Save(ctx, func(saveCtx context.Context) error { return store.Cursor(saveCtx, idx, cursor) }); err != nil {
			e.err = err
			return e
		}
		e.cursors[idx] = cursor
	}
	return e
}

func processCandidate(ctx context.Context, round Round, c *Candidate, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger, reporter progress.Reporter) (e stagedEvent) {
	e = stagedEvent{indices: []int{c.Index}, candidate: c}
	defer progress.NotifyWorkState(reporter)
	if validator, ok := store.(interface {
		ValidateCandidate(context.Context, *Candidate) (bool, error)
	}); ok {
		valid, err := validator.ValidateCandidate(ctx, c)
		if err != nil {
			e.err = err
			return e
		}
		if !valid {
			return retireCandidate(ctx, round, c, store, runtime, true)
		}
	}
	if !c.Ready {
		worker := &AlignmentWorker{store: store, runtime: runtime, reporter: reporter}
		defer func() {
			if err := worker.finish(e.err); err != nil && e.err == nil {
				e.err = err
			}
		}()
		update := worker.Run(ctx, round, c)
		e.inputTokens, e.outputTokens = update.inputTokens, update.outputTokens
		if update.err != nil || update.paused {
			e.err = update.err
			return e
		}
	}
	if !c.Ready {
		return e
	}
	result, accepted, err := FinalizeCandidate(c)
	if err != nil {
		e.err = err
		return e
	}
	if !accepted {
		retired := retireCandidate(ctx, round, c, store, runtime, false)
		retired.inputTokens, retired.outputTokens = e.inputTokens, e.outputTokens
		return retired
	}
	var outcome CommitOutcome
	err = runtime.Save(ctx, func(saveCtx context.Context) error {
		var commitErr error
		outcome, commitErr = store.Commit(saveCtx, c, result)
		return commitErr
	})
	e.outcome = outcome
	e.err = err
	if err != nil {
		e.outcome = ""
		return e
	}
	if outcome == CommitAccepted || outcome == CommitNoop || outcome == CommitExisting {
		result.TargetText = c.Segment.Target
		result.Issues = c.Segment.Issues
		e.completed = &result
	}
	return e
}

func retireCandidate(ctx context.Context, round Round, c *Candidate, store RoundStore, runtime *ExecutionRuntime, stale bool) stagedEvent {
	cursor := WorkCursor{Pool: c.PoolIndex + 1, State: "pending"}
	if stale || cursor.Pool >= max(1, round.Retry.MaxAttempts+1) {
		cursor.Pool = max(1, round.Retry.MaxAttempts+1)
		cursor.State = "unresolved"
	}
	e := stagedEvent{indices: []int{c.Index}, candidate: c, cursors: map[int]WorkCursor{c.Index: cursor}, outcome: CommitRejected}
	if stale {
		e.outcome = CommitStale
	}
	e.err = runtime.Save(ctx, func(saveCtx context.Context) error { return store.Retire(saveCtx, c, cursor) })
	if e.err != nil {
		e.outcome = ""
		e.cursors = nil
	}
	return e
}

func storeResourceID(store RoundStore) int {
	if s, ok := store.(interface{ ResourceIdentity() int }); ok {
		return s.ResourceIdentity()
	}
	return 0
}
