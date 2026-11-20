package pipeline

import (
	"context"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"log/slog"
	"strings"
)

func canonicalStoredHandler(h RoundHandler) bool {
	switch h.(type) {
	case *ExtractHandler, *SemanticQAHandler, *AdjudicateHandler:
		return true
	}
	return false
}
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
		case *ExtractHandler:
			seg.RoundWork.Member = h.Renderer != nil && len(h.Backends) > 0 && h.Glossary != nil && !seg.Skip && strings.TrimSpace(rawSource(seg)) != ""
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
	if !sealed {
		total = prepareStoredCanonicalMembership(round.Handler, doc)
		err = round.Runtime.SaveNamed(ctx, persistence.Metadata{Operation: "seal", Class: persistence.Normal, ResourceID: storeResourceID(round.Store)}, func(saveCtx context.Context) error { return canonical.SealCanonical(saveCtx, doc) })
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
	defer reporter.StageDone()
	if checked, ok := reporter.(interface{ StageError() error }); ok {
		if err := checked.StageError(); err != nil {
			return RunRoundResult{}, err
		}
	}
	maxPools, budget := max(1, round.Retry.MaxAttempts+1), transientBudgetFor(round.Retry)
	failedBatches := 0
	for pool := 0; pool < maxPools; pool++ {
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
				}
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
			result, e := runPool(ctx, round, round.Handler, doc, batches, budget, batchHandler, logger, reporter, pool, nil)
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
			_, _, err = canonical.LoadCanonical(ctx, doc)
			if err != nil {
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
			if len(result.resolved) == 0 && len(result.unresolved) == 0 && len(result.fatalUnresolved) == 0 && len(result.failedSegments) == 0 {
				return canonicalStoredResult(doc, failedBatches), fmt.Errorf("stored round made no durable progress")
			}
		}
	}
	result := canonicalStoredResult(doc, failedBatches)
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
