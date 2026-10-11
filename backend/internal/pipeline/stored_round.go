package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

// runStoredRound keeps the existing non-candidate handlers while giving every
// external request the same admission, accounting, and durable attempt boundary.
func runStoredRound(ctx context.Context, round Round, doc *Document, batchHandler func(context.Context, BatchResult) error, logger *slog.Logger, reporter progress.Reporter) (RunRoundResult, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if reporter == nil {
		reporter = progress.Nop{}
	}
	if round.Store == nil {
		round.Store = NewMemoryRoundStore(nil)
	}
	round.Gate = round.Runtime.Gate
	round.Slots = nil
	if setter, ok := round.Handler.(interface{ SetGate(*PauseGate) }); ok {
		setter.SetGate(round.Gate)
	}
	state, err := round.Store.Load(ctx)
	if err != nil {
		return RunRoundResult{}, &StorageError{Err: err}
	}
	if !state.Sealed {
		batches, err := round.Handler.BuildBatches(ctx, doc, nil, 0)
		if err != nil {
			return RunRoundResult{}, err
		}
		var members []int
		for _, batch := range batches {
			members = append(members, batch...)
		}
		err = round.Runtime.Save(ctx, func(saveCtx context.Context) error {
			var sealErr error
			state, sealErr = round.Store.Seal(saveCtx, uniqueSortedInts(members))
			return sealErr
		})
		if err != nil {
			return RunRoundResult{}, err
		}
	}
	state = storedRoundSnapshot(state)
	if len(state.Members) == 0 {
		return RunRoundResult{}, nil
	}
	for _, idx := range state.Members {
		if idx < 0 || idx >= len(doc.Segments) {
			return RunRoundResult{}, fmt.Errorf("stored round member is outside the resource document")
		}
	}
	if h, ok := round.Handler.(*ExtractHandler); ok {
		h.scannedSegments.Store(int64(len(state.Members)))
	}
	reporter.StageStart(round.Handler.ModeName(), len(state.Members))
	defer reporter.StageDone()
	if checked, ok := reporter.(interface{ StageError() error }); ok {
		if err := checked.StageError(); err != nil {
			return RunRoundResult{}, err
		}
	}
	completed := make(map[int]bool, len(state.Completed))
	for _, idx := range state.Completed {
		completed[idx] = true
	}
	maxPools := max(1, round.Retry.MaxAttempts+1)
	budget := transientBudgetFor(round.Retry)
	failed := map[int]bool{}
	failedBatches := 0
	for pool := 0; pool < maxPools; pool++ {
		if ctx.Err() != nil || round.Gate.Paused() {
			break
		}
		groups := map[int][]int{}
		for _, idx := range state.Members {
			if completed[idx] {
				continue
			}
			cursor := state.Cursors[idx]
			if cursor.Phase == "terminal_failure" {
				failed[idx] = true
				continue
			}
			if cursor.State == "unresolved" || cursor.State == "stale" || cursor.State == "rejected" || cursor.Pool != pool {
				continue
			}
			if cursor.Attempt >= budget {
				cursor = WorkCursor{Pool: pool + 1, State: "pending", NextAttemptAt: cursor.NextAttemptAt}
				if cursor.Pool >= maxPools {
					cursor.State = "unresolved"
				}
				if err := round.Runtime.Save(ctx, func(saveCtx context.Context) error { return round.Store.Cursor(saveCtx, idx, cursor) }); err != nil {
					return RunRoundResult{}, err
				}
				if state.Cursors == nil {
					state.Cursors = map[int]WorkCursor{}
				}
				state.Cursors[idx] = cursor
				continue
			}
			groups[cursor.Attempt] = append(groups[cursor.Attempt], idx)
		}
		var batches [][]int
		for attempt := 0; attempt < budget; attempt++ {
			pending := groups[attempt]
			if len(pending) == 0 {
				continue
			}
			built, err := round.Handler.BuildBatches(ctx, doc, pending, pool)
			if err != nil {
				return RunRoundResult{}, err
			}
			if err := validateStoredBatches(pending, built); err != nil {
				return RunRoundResult{}, err
			}
			batches = append(batches, built...)
		}
		if len(batches) == 0 {
			continue
		}
		batchAt := func(batch []int) time.Time {
			var at time.Time
			for _, idx := range batch {
				if next := state.Cursors[idx].NextAttemptAt; next.After(at) {
					at = next
				}
			}
			return at
		}
		sort.SliceStable(batches, func(i, j int) bool { return batchAt(batches[i]).Before(batchAt(batches[j])) })
		emitPoolEvent(reporter, progress.PoolEvent{Mode: round.Handler.ModeName(), PoolIndex: pool, MaxPools: maxPools, Batches: len(batches), Pending: len(computeResolved(state.Members, mapIndices(completed), mapIndices(failed))), ShrinkRate: 1, Phase: "pool_start"})
		result, err := runPool(ctx, round, round.Handler, doc, batches, budget, batchHandler, logger, reporter, pool, state.Cursors)
		if err != nil {
			return RunRoundResult{}, err
		}
		for _, idx := range result.resolved {
			completed[idx] = true
		}
		for _, idx := range result.failedSegments {
			failed[idx] = true
		}
		failedBatches += result.failedBatches
		state, err = round.Store.Load(ctx)
		if err != nil {
			return RunRoundResult{}, &StorageError{Err: err}
		}
		state = storedRoundSnapshot(state)
		for _, idx := range state.Completed {
			completed[idx] = true
		}
	}
	unresolved := computeResolved(state.Members, mapIndices(completed), mapIndices(failed))
	result := RunRoundResult{Resolved: mapIndices(completed), Unresolved: unresolved, FailedSegments: mapIndices(failed), FailedBatches: failedBatches}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if round.Gate.Paused() {
		return result, nil
	}
	if err := round.Handler.Finalize(ctx, doc, unresolved); err != nil {
		return result, err
	}
	return result, nil
}

func storedRoundSnapshot(state RoundRecovery) RoundRecovery {
	copy := RoundRecovery{Sealed: state.Sealed, Members: append([]int(nil), state.Members...), Completed: append([]int(nil), state.Completed...), Cursors: make(map[int]WorkCursor, len(state.Cursors))}
	for idx, cursor := range state.Cursors {
		copy.Cursors[idx] = cursor
	}
	return copy
}

func validateStoredBatches(pending []int, batches [][]int) error {
	want := make(map[int]bool, len(pending))
	for _, idx := range pending {
		want[idx] = true
	}
	for _, batch := range batches {
		for _, idx := range batch {
			if !want[idx] {
				return fmt.Errorf("stored round batching returned a duplicate or non-member index %d", idx)
			}
			delete(want, idx)
		}
	}
	if len(want) > 0 {
		return fmt.Errorf("stored round batching omitted %d unfinished members", len(want))
	}
	return nil
}

func mapIndices(set map[int]bool) []int {
	indices := make([]int, 0, len(set))
	for idx := range set {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	return indices
}

func processStoredBatch(ctx context.Context, round Round, handler RoundHandler, doc *Document, job batchJob, pool, budget int, batchHandler func(context.Context, BatchResult) error, logger *slog.Logger, reporter progress.Reporter) (out batchResult) {
	deferred := batchResult{deferred: true}
	if wait := time.Until(job.notBefore); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return deferred
		case <-round.Gate.Done():
			return deferred
		case <-timer.C:
		}
	}
	if ctx.Err() != nil || round.Gate.Paused() {
		return deferred
	}
	session := &requestSession{runtime: round.Runtime, store: round.Store, reporter: reporter, intent: RequestIntent{Stage: backend.RequestStageMain, RoundIndex: handlerRoundIndex(handler), ResourceID: storeResourceID(round.Store), Indices: append([]int(nil), job.idxs...), Pool: pool, Attempt: job.attempt, Phase: "main"}}
	retained := false
	defer func() {
		if !retained {
			if out.err != nil {
				session.fail(out.err)
			} else if err := ctx.Err(); err != nil {
				session.fail(err)
			}
			if err := session.finish(); err != nil {
				out.err = err
				out.deferred = true
			}
		}
	}()
	callCtx := context.WithValue(ctx, requestSessionKey{}, session)
	result := handler.ProcessBatch(callCtx, doc, job.idxs, job.attempt, logger)
	if session.err != nil {
		deferred.err = session.err
		return deferred
	}
	if result.err != nil {
		deferred.err = result.err
		return deferred
	}
	if batchHandler != nil && result.callbackResult != nil {
		if err := batchHandler(callCtx, *result.callbackResult); err != nil {
			deferred.err = err
			return deferred
		}
	}
	if ctx.Err() != nil {
		return deferred
	}
	retry := result.retry
	if retry != nil && (retry.attempt >= budget || round.Gate.Paused()) {
		result.unresolved = append(result.unresolved, result.retry.idxs...)
		result.retry = nil
	}
	for _, idx := range job.idxs {
		next := WorkCursor{Pool: pool, Attempt: job.attempt, State: "pending"}
		if session.calls > 0 {
			next.Attempt++
		}
		if containsIndex(result.fatalUnresolved, idx) {
			next.Pool = max(1, round.Retry.MaxAttempts+1)
			next.State = "unresolved"
		} else if containsIndex(result.failedSegments, idx) {
			next.Pool = max(1, round.Retry.MaxAttempts+1)
			next.State = "unresolved"
			next.Phase = "terminal_failure"
		} else if retry != nil && containsIndex(retry.idxs, idx) {
			next.Attempt = retry.attempt
			next.NextAttemptAt = retry.notBefore
			if next.Attempt >= budget {
				next.Pool++
				next.Attempt = 0
				if next.Pool >= max(1, round.Retry.MaxAttempts+1) {
					next.State = "unresolved"
				}
			}
		} else if containsIndex(result.unresolved, idx) && (!round.Gate.Paused() || next.Attempt >= budget) {
			next.Pool++
			next.Attempt = 0
			if next.Pool >= max(1, round.Retry.MaxAttempts+1) {
				next.State = "unresolved"
			}
		}
		if err := round.Runtime.Save(ctx, func(saveCtx context.Context) error { return round.Store.Cursor(saveCtx, idx, next) }); err != nil {
			deferred.err = err
			return deferred
		}
	}
	result.finish = session.finish
	result.fail = func(err error) { session.fail(err) }
	retained = true
	return result
}

// storedBackoffRetry moves runtime-managed waiting after durable cursor saving.
// Legacy callers keep their existing inline wait without a request session.
func storedBackoffRetry(ctx context.Context, indices []int, attempt int, wait time.Duration) *batchJob {
	if _, ok := ctx.Value(requestSessionKey{}).(*requestSession); !ok {
		return nil
	}
	return &batchJob{idxs: append([]int(nil), indices...), attempt: attempt + 1, notBefore: time.Now().Add(wait)}
}

func containsIndex(indices []int, index int) bool {
	for _, idx := range indices {
		if idx == index {
			return true
		}
	}
	return false
}

func handlerRoundIndex(handler RoundHandler) int {
	switch h := handler.(type) {
	case *TranslateHandler:
		return h.RoundIndex
	case *ReviseHandler:
		return h.RoundIndex
	case *ExtractHandler:
		return h.RoundIndex
	case *SemanticQAHandler:
		return h.RoundIndex
	case *AdjudicateHandler:
		return h.RoundIndex
	default:
		return 0
	}
}
