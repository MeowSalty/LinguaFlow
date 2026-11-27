package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

func prepareStoredCanonicalMembership(h RoundHandler, doc *Document) int {
	count := 0
	for i := range doc.Segments {
		seg := &doc.Segments[i]
		seg.RoundWork.Member = false
		seg.RoundWork.Completed = false
		seg.RoundWork.Cursor = WorkCursor{}
		if _, done := doc.ResolvedIndices[i]; done {
			continue
		}
		if seg.CompletionModes&CompletedModeMask(h.ModeName()) != 0 {
			continue
		}
		switch h := h.(type) {
		case *CorrectHandler:
			seg.RoundWork.Member = h.Rules != nil && h.Rules.Enabled() && correctEligible(seg)
		case *ExtractHandler:
			seg.RoundWork.Member = h.Renderer != nil && h.Backend != nil && h.Glossary != nil && !seg.Skip && strings.TrimSpace(rawSource(seg)) != ""
		case *SemanticQAHandler:
			if h.Renderer == nil || h.Backend == nil || !seg.Translate || (seg.Status != "translated" && seg.Status != "edited") || seg.Target == "" {
				continue
			}
			switch h.SegmentScope {
			case "all":
				seg.RoundWork.Member = true
			case "with_issues":
				seg.RoundWork.Member = len(seg.Issues) > 0
			case "with_issue_codes":
				for _, issue := range seg.Issues {
					for _, code := range h.IssueCodes {
						if issue.Code == code {
							seg.RoundWork.Member = true
						}
					}
				}
			}
		case *AdjudicateHandler:
			if h.Renderer == nil || h.Backend == nil || (seg.Status != "translated" && seg.Status != "edited") {
				continue
			}
			for _, issue := range seg.Issues {
				if issue.Dismissed() {
					continue
				}
				for _, code := range h.AdjudicateCodes {
					if issue.Code == code {
						seg.RoundWork.Member = true
					}
				}
			}
		}
		if seg.RoundWork.Member {
			count++
		}
	}
	return count
}
func runCanonicalStoredRound(ctx context.Context, round Round, doc *Document, batchHandler func(context.Context, BatchResult) error, logger *slog.Logger, reporter progress.Reporter) (RunRoundResult, error) {
	activePlanOwner := ""
	var plans []storedBatchPlan
	defer func() {
		plans = nil
		if activePlanOwner != "" {
			_ = round.Runtime.Admission.ReleaseBuild(activePlanOwner)
		}
	}()
	canonical, ok := round.Store.(CanonicalRoundStore)
	if !ok {
		return RunRoundResult{}, fmt.Errorf("stored round has no canonical membership contract")
	}
	ctx, closeCanonicalIndex := withCanonicalIndexLifetime(ctx)
	defer closeCanonicalIndex()
	sealed, total, err := canonical.LoadCanonical(ctx, doc)
	if err != nil {
		return RunRoundResult{}, &StorageError{Err: err}
	}
	_, localCorrect := round.Handler.(*CorrectHandler)
	seal := canonical.SealCanonical
	if localCorrect {
		local, ok := round.Store.(CorrectRoundStore)
		if !ok {
			return RunRoundResult{}, fmt.Errorf("round store does not support local correction")
		}
		seal = local.SealCorrectCanonical
	}
	if !sealed {
		total = prepareStoredCanonicalMembership(round.Handler, doc)
		pausedBeforeSeal := false
		err = round.Runtime.SaveNamed(ctx, persistence.Metadata{Operation: "seal", Class: persistence.Normal, ResourceID: storeResourceID(round.Store)}, func(saveCtx context.Context) error {
			if localCorrect && round.Gate.Paused() {
				pausedBeforeSeal = true
				return nil
			}
			return seal(saveCtx, doc)
		})
		if localCorrect && (pausedBeforeSeal || errors.Is(err, backend.ErrDispatchPaused)) {
			round.Gate.Pause()
			return canonicalStoredResult(doc, 0), ctx.Err()
		}
		if err != nil {
			return RunRoundResult{}, err
		}
	}
	if total == 0 {
		return RunRoundResult{}, nil
	}
	if h, ok := round.Handler.(*ExtractHandler); ok {
		h.scannedSegments.Store(int64(total))
	}
	reporter.StageStart(round.Handler.ModeName(), total)
	if checked, ok := reporter.(interface{ StageError() error }); ok {
		if err := checked.StageError(); err != nil {
			return RunRoundResult{}, err
		}
	}
	defer reporter.StageDone()
	if localCorrect {
		return runCorrectMembers(ctx, round, doc, total, batchHandler, logger, reporter)
	}
	maxPools, budget := max(1, round.Retry.MaxAttempts+1), transientBudgetFor(round.Retry)
	_, observePools := reporter.(progress.PoolObserver)
	failedBatches := 0
	handler := round.Handler.(capturedRoundHandler)
	if _, ok := round.Store.(StoredResponseStore); !ok {
		return RunRoundResult{}, fmt.Errorf("%s requires a capture settlement store", round.Handler.ModeName())
	}
	if prepareIntent(round.Handler, round.Store, backend.RequestStageMain).BackendID == 0 {
		return RunRoundResult{}, fmt.Errorf("%s requires a runtime-bound backend", round.Handler.ModeName())
	}
	consumer := round.Runtime.Window.EnterScope(storeResourceID(round.Store), storeRoundID(round.Store), handlerRoundIndex(round.Handler))
	defer consumer.Leave()
	ctx = context.WithValue(ctx, storedWindowConsumerKey{}, consumer)
	failedBatches, err = recoverStoredResponses(ctx, round, doc, batchHandler, logger, reporter, consumer)
	if err != nil || round.Gate.Paused() {
		return canonicalStoredResult(doc, failedBatches), err
	}
	if owner, ok := round.Store.(interface {
		RecoverStoredOwnership(context.Context, *WindowConsumer) error
	}); ok {
		if err := owner.RecoverStoredOwnership(ctx, consumer); err != nil {
			return canonicalStoredResult(doc, failedBatches), &StorageError{Operation: "load_stored_member_ownership", Err: err}
		}
	}
	consumer.RecoveryComplete()
	for _, seg := range doc.Segments {
		if seg.RoundWork.Member && !seg.RoundWork.Completed && seg.RoundWork.Cursor.State == "response_pending" {
			return canonicalStoredResult(doc, failedBatches), fmt.Errorf("stored member has no recoverable capture")
		}
	}
	for pool := 0; pool < maxPools; pool++ {
		poolEvent := progress.PoolEvent{Mode: round.Handler.ModeName(), PoolIndex: pool, MaxPools: maxPools, ShrinkRate: round.Shrink, Phase: "pool_start"}
		nextPending := 0
		for {
			if err := ctx.Err(); err != nil {
				return canonicalStoredResult(doc, failedBatches), err
			}
			if round.Gate.Paused() {
				return canonicalStoredResult(doc, failedBatches), nil
			}
			// At most a finite prepared page is materialized. Cursors and manifest stay
			// in the document; completed chunks are not retained by the coordinator.
			metadataID := NewWorkID()
			activePlanOwner = "stored-plan:" + metadataID
			planCtx := round.Runtime.BuildContext(ctx, activePlanOwner)
			members := 0
			e := round.Runtime.Run(planCtx, func() error {
				b, err := backend.NewBuildBoundsFromContext(planCtx, 0, 0)
				if err != nil {
					return err
				}
				plans, members, err = planStoredCanonicalPage(b, doc, round.Handler, pool, budget, round.Concurrency)
				return err
			})
			if e != nil {
				return canonicalStoredResult(doc, failedBatches), e
			}
			limit := max(1, members)
			var pending []int
			var advances []CursorTransition

			err := round.Runtime.Run(planCtx, func() error {
				b, e := backend.NewBuildBoundsFromContext(planCtx, round.Runtime.responseLimit(), round.Runtime.handoffLimit())
				if e != nil {
					return e
				}
				if e = backend.ReserveSlice[int](b, int64(limit)); e != nil {
					return e
				}
				if e = backend.ReserveSlice[CursorTransition](b, int64(min(16, limit))); e != nil {
					return e
				}
				pending = make([]int, 0, limit)
				advances = make([]CursorTransition, 0, min(16, limit))

				for i := range doc.Segments {
					w := doc.Segments[i].RoundWork
					if !w.Member || w.Completed || w.Cursor.Pool != pool || w.Cursor.Phase == "terminal_failure" || w.Cursor.State == "unresolved" || w.Cursor.State == "stale" || w.Cursor.State == "rejected" {
						continue
					}
					if w.Cursor.Attempt >= budget {
						next := WorkCursor{Pool: pool + 1, State: "pending", NextAttemptAt: w.Cursor.NextAttemptAt}
						if next.Pool >= maxPools {
							next.State = "unresolved"
						}
						if len(advances) == cap(advances) {
							break
						}
						advances = append(advances, CursorTransition{Index: i, Cursor: next})
						continue
					}
					selected := false
					for _, plan := range plans {
						if w.Cursor.Attempt == plan.attempt && i >= plan.first && i <= plan.last {
							selected = true
							break
						}
					}
					if !selected {
						continue
					}
					pending = append(pending, i)

					if len(pending) == limit {
						break
					}
				}
				return nil
			})
			if err != nil {
				return RunRoundResult{}, err
			}
			if len(advances) > 0 {
				if err = round.Runtime.SaveNamed(ctx, persistence.Metadata{Operation: "pool_advance", Class: persistence.Normal, ResourceID: storeResourceID(round.Store)}, func(saveCtx context.Context) error { return round.Store.Cursors(saveCtx, advances) }); err != nil {
					return RunRoundResult{}, err
				}
				for _, t := range advances {
					doc.Segments[t.Index].RoundWork.Cursor = t.Cursor
					if t.Cursor.State == "unresolved" {
						for _, id := range memberReservationKeys(round.Store, round.Handler.ModeName(), []int{t.Index}) {
							round.Runtime.Window.Release(id)
						}
					}
				}
				nextPending += len(advances)
			}
			if len(pending) == 0 {
				plans = nil
				_ = round.Runtime.Admission.ReleaseBuild(activePlanOwner)
				activePlanOwner = ""
				if len(advances) > 0 {
					continue
				}
				break
			}
			var batches [][]int
			// Separate attempts retain their remaining network budgets. Each subset is
			// bounded by this page; the full scope is never grouped into private maps.
			for attempt := 0; attempt < budget; attempt++ {
				var group []int
				err = round.Runtime.Run(planCtx, func() error {
					b, e := backend.NewBuildBoundsFromContext(planCtx, round.Runtime.responseLimit(), round.Runtime.handoffLimit())
					if e != nil {
						return e
					}
					if e = backend.ReserveSlice[int](b, int64(len(pending))); e != nil {
						return e
					}
					group = make([]int, 0, len(pending))
					for _, i := range pending {
						if doc.Segments[i].RoundWork.Cursor.Attempt == attempt {
							group = append(group, i)
						}
					}
					return nil
				})
				if err != nil {
					return RunRoundResult{}, err
				}
				if len(group) == 0 {
					continue
				}
				built, e := round.Handler.BuildBatches(planCtx, doc, group, pool)
				if e != nil {
					return RunRoundResult{}, e
				}
				err = round.Runtime.Run(planCtx, func() error {
					b, e := backend.NewBuildBoundsFromContext(planCtx, round.Runtime.responseLimit(), round.Runtime.handoffLimit())
					if e != nil {
						return e
					}
					for _, batch := range built {
						size := max(1, round.Runtime.Window.mainBatchLimit(round.Concurrency)-1)
						if fixed := round.Runtime.Window.fixedBatchLimit(consumer); fixed > 0 {
							size = min(size, fixed)
						}
						for len(batch) > size {
							batches, e = backend.AppendNode(b, batches, batch[:size:size])
							if e != nil {
								return e
							}
							batch = batch[size:]
						}
						batches, e = backend.AppendNode(b, batches, batch)
						if e != nil {
							return e
						}
					}
					return nil
				})
				if err != nil {
					return RunRoundResult{}, err
				}
			}
			pending = nil
			if observePools && poolEvent.Batches == 0 {
				// Count the complete pool without retaining every batch alongside
				// the bounded page of descriptors used for execution.
				err = round.Runtime.Run(planCtx, func() error {
					var countErr error
					poolEvent.Batches, countErr = storedCanonicalDescriptorCount(doc, round.Handler, pool, budget, len(doc.Segments))
					for _, seg := range doc.Segments {
						attempt := seg.RoundWork.Cursor.Attempt
						if attempt < budget && storedPlannerEligible(seg, pool, attempt) {
							poolEvent.Pending++
						}
					}
					return countErr
				})
				if err != nil {
					return canonicalStoredResult(doc, failedBatches), err
				}
				emitPoolEvent(reporter, poolEvent)
			}
			result, e := runPool(ctx, round, doc, batches, budget, reporter, func(ctx context.Context, job batchJob) batchResult {
				return processCapturedStoredBatch(ctx, round, handler, doc, job, pool, batchHandler, logger, reporter)
			})
			batches, advances, plans = nil, nil, nil
			_ = round.Runtime.Admission.ReleaseBuild(activePlanOwner)
			activePlanOwner = ""
			if e != nil {
				return canonicalStoredResult(doc, failedBatches), e
			}
			failedBatches += result.failedBatches
			for _, i := range result.resolved {
				doc.Segments[i].RoundWork.Completed = true
			}
			for _, i := range result.failedSegments {
				doc.Segments[i].RoundWork.Cursor.Phase = "terminal_failure"
			}
			if err := ctx.Err(); err != nil {
				return canonicalStoredResult(doc, failedBatches), err
			}
			if round.Gate.Paused() {
				return canonicalStoredResult(doc, failedBatches), nil
			}
			_, _, err = canonical.LoadCanonical(ctx, doc)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
					return canonicalStoredResult(doc, failedBatches), ctxErr
				}
				return RunRoundResult{}, &StorageError{Err: err}
			}
			// The synchronous store/reporter has no Job checkpoints. Confirmed local
			// results still belong to this same canonical document after cursor reload.
			for _, i := range result.resolved {
				doc.Segments[i].RoundWork.Completed = true
			}
			for _, i := range result.failedSegments {
				doc.Segments[i].RoundWork.Cursor.Phase = "terminal_failure"
			}
			for _, i := range result.unresolved {
				if doc.Segments[i].RoundWork.Cursor.Pool == pool+1 {
					nextPending++
				}
			}
			if len(result.resolved) == 0 && len(result.unresolved) == 0 && len(result.fatalUnresolved) == 0 && len(result.failedSegments) == 0 {
				return canonicalStoredResult(doc, failedBatches), fmt.Errorf("stored round made no durable progress")
			}
		}
		if pool+1 < maxPools {
			emitStoredPoolAdvance(reporter, poolEvent, nextPending)
		}
	}
	result := canonicalStoredResult(doc, failedBatches)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if round.Gate.Paused() {
		return result, nil
	}
	if err = round.Handler.Finalize(ctx, doc, result.Unresolved); err != nil {
		return result, err
	}
	return result, nil
}
func canonicalStoredResult(doc *Document, failedBatches int) RunRoundResult {
	r := RunRoundResult{FailedBatches: failedBatches}
	for i := range doc.Segments {
		w := doc.Segments[i].RoundWork
		if !w.Member {
			continue
		}
		if w.Completed {
			r.Resolved = append(r.Resolved, i)
		} else if w.Cursor.Phase == "terminal_failure" {
			r.FailedSegments = append(r.FailedSegments, i)
		} else {
			r.Unresolved = append(r.Unresolved, i)
		}
	}
	return r
}
