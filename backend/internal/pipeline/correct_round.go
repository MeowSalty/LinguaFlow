package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

// CorrectRoundStore fences local seals and atomically confirms a local result.
// Local work owns no model request, response or candidate.
type CorrectRoundStore interface {
	SealCorrectCanonical(context.Context, *Document) error
	CommitCorrect(context.Context, string, Segment, TranslatedSegment) (CommitOutcome, int64, error)
}

func runCorrectMembers(ctx context.Context, round Round, doc *Document, total int, batchHandler func(context.Context, BatchResult) error, logger *slog.Logger, reporter progress.Reporter) (RunRoundResult, error) {
	const pageSize = 16
	var indices [pageSize]int
	var batches [pageSize][]int
	limit := min(pageSize, max(1, round.Concurrency))
	emitPoolEvent(reporter, progress.PoolEvent{Mode: RoundModeCorrect, MaxPools: 1, Batches: total, Pending: total, ShrinkRate: round.Shrink, Phase: "pool_start"})
	for next := 0; next < len(doc.Segments); {
		if ctx.Err() != nil || round.Gate.Paused() {
			break
		}
		count := 0
		for count < limit && next < len(doc.Segments) {
			index := next
			next++
			if !storedPlannerEligible(doc.Segments[index], 0, 0) {
				continue
			}
			indices[count] = index
			batches[count] = indices[count : count+1 : count+1]
			count++
		}
		if _, err := runPool(ctx, round, doc, batches[:count], 1, reporter, func(ctx context.Context, job batchJob) batchResult {
			return processCorrectMember(ctx, round, round.Handler.(*CorrectHandler), doc, job, batchHandler, logger)
		}); err != nil {
			return canonicalStoredResult(doc, 0), err
		}
	}
	return canonicalStoredResult(doc, 0), ctx.Err()
}

func processCorrectMember(ctx context.Context, round Round, handler *CorrectHandler, doc *Document, job batchJob, batchHandler func(context.Context, BatchResult) error, logger *slog.Logger) (out batchResult) {
	if ctx.Err() != nil || round.Gate.Paused() {
		return batchResult{deferred: true}
	}
	index := job.idxs[0]
	baseline := doc.Segments[index]
	runtime := round.Runtime
	id := NewWorkID()
	owner := "correct:" + id
	buildCtx := runtime.BuildContext(ctx, owner)
	keepOwner := false
	defer func() {
		if !keepOwner {
			out.err = errors.Join(out.err, runtime.Admission.ReleaseBuild(owner))
		}
	}()
	p := preparedConfirmation{doc: doc, runtime: runtime, owner: owner, callbackOwner: owner + ":callback", lifetime: runtime.CompletionContext(ctx)}
	if err := runtime.Run(buildCtx, func() error {
		bounds, err := backend.NewBuildBoundsFromContext(buildCtx, 0, 0)
		if err != nil {
			return err
		}
		if correctEligible(&baseline) {
			p.result, err = handler.correctSegment(buildCtx, bounds, doc.Format, index, baseline, logger)
			if err != nil {
				return err
			}
		} else {
			p.result = TranslatedSegment{Index: index, ID: baseline.ID, SourceText: baseline.Source, TargetText: baseline.Target, Issues: baseline.Issues, Meta: baseline.Meta, Protected: baseline.Protected}
		}
		p.plan, p.borrowed, err = prepareConfirmedFields(buildCtx, bounds, p.result, false, baseline)
		return err
	}); err != nil {
		if wait := workDeferred(err); wait != nil {
			return batchResult{deferred: true, waitCapacity: wait}
		}
		if storedDispatchDeferred(ctx, err) {
			return batchResult{deferred: true}
		}
		return batchResult{deferred: true, err: err}
	}
	committer := round.Store.(CorrectRoundStore)
	var outcome CommitOutcome
	var version int64
	if err := runtime.SaveNamed(buildCtx, persistence.Metadata{Operation: "commit", Class: persistence.Normal, ResourceID: storeResourceID(round.Store)}, func(saveCtx context.Context) error {
		if round.Gate.Paused() {
			outcome = ""
			return nil
		}
		var err error
		outcome, version, err = committer.CommitCorrect(saveCtx, id, baseline, p.result)
		return err
	}); err != nil {
		if storedDispatchDeferred(ctx, err) {
			return batchResult{deferred: true}
		}
		return batchResult{deferred: true, err: err}
	}
	switch outcome {
	case "":
		return batchResult{deferred: true}
	case CommitStale:
		doc.Segments[index].RoundWork.Cursor.State = "unresolved"
		return batchResult{fatalUnresolved: job.idxs}
	case CommitAccepted, CommitExisting, CommitNoop:
	default:
		return batchResult{deferred: true, err: fmt.Errorf("unexpected local correction outcome %q", outcome)}
	}
	// A commit accepted before cancellation owns its in-memory projection even
	// if the dispatch context has since ended. Recovery reads the same DB fact.
	p.candidate = &Candidate{ID: id, Index: index, Segment: baseline}
	p.candidate.Segment.ContentVersion = version
	p.candidate.Segment.TargetIsNull = baseline.TargetIsNull && p.result.TargetText == baseline.Target
	if batchHandler != nil {
		p.apply = func(ctx context.Context, _ *Candidate, result TranslatedSegment) (bool, error) {
			if err := batchHandler(ctx, BatchResult{Segments: []TranslatedSegment{result}}); err != nil {
				return false, err
			}
			current := &doc.Segments[index]
			return current.Target != result.TargetText || !reflect.DeepEqual(current.Issues, result.Issues), nil
		}
		if memory, ok := round.Store.(interface{ MemoryStore() *MemoryRoundStore }); ok {
			p.memoryStore = memory.MemoryStore()
		}
	}
	if err := p.project(ctx); err != nil {
		keepOwner = true
		if !doc.Segments[index].RoundWork.Completed {
			err = errors.Join(err, runtime.rememberConfirmation(&p))
			return batchResult{deferred: true, err: &ConfirmationProjectionError{Index: index, Cause: err}}
		}
		return batchResult{err: errors.Join(err, p.release())}
	}
	keepOwner = true
	return batchResult{err: p.release()}
}

func (s *MemoryRoundStore) SealCorrectCanonical(ctx context.Context, doc *Document) error {
	return s.SealCanonical(ctx, doc)
}

func (s *MemoryRoundStore) CommitCorrect(ctx context.Context, identity string, baseline Segment, result TranslatedSegment) (CommitOutcome, int64, error) {
	// Use the existing memory confirmation encoding to recover a new document.
	// The envelope is never admitted, saved, or retired as a candidate.
	result.SourceText = rawSource(&baseline)
	c := Candidate{ID: identity, Index: result.Index, Mode: RoundModeCorrect, Segment: baseline}
	defer c.CloseConfirmedRead()
	if result.TargetText != baseline.Target {
		c.Segment.ContentVersion++
	}
	outcome, err := s.Commit(ctx, &c, result)
	if outcome == CommitAccepted && result.TargetText == baseline.Target {
		outcome = CommitNoop
	}
	return outcome, c.Segment.ContentVersion, err
}
