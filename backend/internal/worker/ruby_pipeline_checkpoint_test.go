package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func TestRubyPipelineSQLiteRoundCloseFailureRetriesWithoutHTTP(t *testing.T) {
	f := newRubyPipelineFixture(t, 1)
	var fail atomic.Bool
	fail.Store(true)
	injected := errors.New("round close unavailable")
	f.client.JobRound.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			status, _ := mutation.Field("status")
			if status == service.JobRoundStatusCompleted && fail.Load() {
				return nil, injected
			}
			return next.Mutate(ctx, mutation)
		})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.runner.processJob(ctx, f.jobID); !errors.Is(err, injected) {
		t.Fatalf("round close error lost: %v", err)
	}
	if f.client.Job.GetX(ctx, f.jobID).Status == service.JobStatusCompleted {
		t.Fatal("Job completed before failed round closure was persisted")
	}
	f.assertAccepted(t, 0, 1)
	f.assertUsage(t, 2)
	fail.Store(false)
	if err := f.runner.processJob(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	if main, align, _, _ := f.calls(); main != 1 || align != 1 {
		t.Fatalf("round closure failure repeated HTTP: %d/%d", main, align)
	}
	if f.client.Job.GetX(ctx, f.jobID).Status != service.JobStatusCompleted {
		t.Fatal("storage recovery did not close Job")
	}
	f.assertUsage(t, 2)
}

func TestRubyPipelineSQLitePausePersistsRawBeforeCandidateCapacityCheck(t *testing.T) {
	f := newRubyPipelineFixture(t, 1)
	f.runner.pipeCfg.Candidates.ItemBytes = 4 << 10
	var pauseOnce atomic.Bool
	f.respond = func(ctx context.Context, request rubyPipelineRequest) (any, error) {
		if pauseOnce.CompareAndSwap(false, true) {
			if _, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID); err != nil {
				return nil, err
			}
			if !f.runner.Pause(f.jobID) {
				return nil, errors.New("Job gate was not registered")
			}
		}
		return map[string]any{"translations": map[string]string{"1": strings.Repeat("x", 8<<10)}}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.runner.processJob(ctx, f.jobID); err != nil {
		t.Fatalf("reliable raw handoff during pause: %v", err)
	}
	job := f.client.Job.GetX(ctx, f.jobID)
	if job.Status != service.JobStatusPaused || !job.PauseRequested {
		t.Fatalf("reliable raw handoff did not finish the pause: %s", job.Status)
	}
	if f.client.WorkCandidate.Query().CountX(ctx) != 0 || f.client.JobRoundSegment.Query().CountX(ctx) != 0 {
		t.Fatal("capacity failure produced accepted work")
	}
	request := f.client.WorkRequest.Query().OnlyX(ctx)
	if request.State != "completed" || request.PromotionState != "capture_pending" || request.PromotionCursor != 0 || request.ArchiveCursor != 0 || request.CaptureBytes == 0 || request.OutputMemberBytes != 0 || request.InputMemberBytes == 0 {
		t.Fatalf("pause lost reliable capture: state=%s phase=%s cursor=%d archive=%d capture=%d input=%d output=%d", request.State, request.PromotionState, request.PromotionCursor, request.ArchiveCursor, request.CaptureBytes, request.InputMemberBytes, request.OutputMemberBytes)
	}
	f.assertUsage(t, 1)
	if _, err := f.jobs.ResumeJob(ctx, f.ownerID, f.jobID); err != nil {
		t.Fatal(err)
	}
	if err := f.runner.processJob(ctx, f.jobID); err != nil && !errors.Is(err, pipeline.ErrCandidateCapacity) {
		t.Fatalf("candidate capacity cause lost on resume: %v", err)
	}
	if main, align, _, _ := f.calls(); main != 3 || align != 0 {
		t.Fatalf("three configured pools did not each reject one bounded member: main=%d align=%d", main, align)
	}
	if f.client.WorkCandidate.Query().CountX(ctx) != 0 || f.client.JobRoundSegment.Query().CountX(ctx) != 0 {
		t.Fatal("over-Q candidate produced accepted work")
	}
	item := f.client.WorkItem.Query().OnlyX(ctx)
	var finalCursor pipeline.WorkCursor
	// Failed-member promotion preserves the successor's pending state. The
	// successor is outside the configured pools, so round results project it as
	// unresolved without inventing a new persistent terminal-state transition.
	if err := json.Unmarshal(item.Cursor, &finalCursor); err != nil || item.PoolIndex != 3 || item.State != "pending" || finalCursor.Pool != 3 || finalCursor.State != "pending" || finalCursor.Attempt != 0 || finalCursor.LogicalAttempt != 0 || finalCursor.NetworkAttempt != 0 || finalCursor.Phase == "terminal_failure" {
		t.Fatalf("three local refusals lost their pool successor: item_pool=%d item_state=%s cursor=%+v err=%v", item.PoolIndex, item.State, finalCursor, err)
	}
	canonical, adapter := f.round(t, 0)
	defer adapter.CloseCanonicalIndex()
	sealed, total, err := adapter.LoadCanonical(ctx, canonical)
	if err != nil || !sealed || total != 1 {
		t.Fatalf("final canonical round unavailable: sealed=%t total=%d err=%v", sealed, total, err)
	}
	work := canonical.Segments[0].RoundWork
	maxPools := max(1, adapter.retryAttempts()+1)
	// An empty successor phase does not overwrite the old SQL prompt phase.
	// Eligibility depends on the persisted pool and attempts, not equality of
	// the retained scalar phase with the new cursor JSON.
	if maxPools != 3 || !work.Member || work.Completed || work.Cursor.Pool != finalCursor.Pool || work.Cursor.Attempt != 0 || work.Cursor.LogicalAttempt != 0 || work.Cursor.NetworkAttempt != 0 || work.Cursor.State != finalCursor.State || work.Cursor.Pool < maxPools || work.Cursor.Phase == "terminal_failure" {
		t.Fatalf("canonical unresolved member became eligible or completed: max_pools=%d member=%t completed=%t cursor=%+v", maxPools, work.Member, work.Completed, work.Cursor)
	}
	round := f.client.JobRound.GetX(ctx, f.roundIDs[0])
	counts, err := workstate.ReadStageCounts(ctx, f.client, f.jobID)
	if err != nil || round.Status != service.JobRoundStatusFailed || round.SegmentTotal != 1 || round.SegmentCompleted != 0 || counts.ConfirmedWork != 0 {
		t.Fatalf("last translate round abandoned its unresolved debt: status=%s total=%d completed=%d err=%v", round.Status, round.SegmentTotal, round.SegmentCompleted, err)
	}
	seenPools := map[int]bool{}
	for _, completed := range f.client.WorkRequest.Query().AllX(ctx) {
		if completed.State != "completed" || completed.PromotionState != "promoted" || completed.PromotionCursor != 1 || seenPools[completed.MemberPool] {
			t.Fatal("a paid request was retried or a business pool was repeated")
		}
		seenPools[completed.MemberPool] = true
	}
	if len(seenPools) != 3 || !seenPools[0] || !seenPools[1] || !seenPools[2] {
		t.Fatalf("configured rejection pools lost: %v", seenPools)
	}
	for _, outcome := range f.client.WorkResponseMember.Query().AllX(ctx) {
		if outcome.OutcomeKind != 1 || outcome.OutcomeLimit != 4<<10 || outcome.OutcomeObserved <= outcome.OutcomeLimit || outcome.State != "failed" || item.LastError == "" {
			t.Fatalf("over-Q member did not retain its typed content limit: state=%s kind=%d limit=%d observed=%d work_error=%s", outcome.State, outcome.OutcomeKind, outcome.OutcomeLimit, outcome.OutcomeObserved, item.LastError)
		}
	}
	if original := f.client.WorkRequest.GetX(ctx, request.ID); original.Identity != request.Identity || original.RetryEpoch != request.RetryEpoch || original.PromotionCursor != 1 {
		t.Fatal("resume replaced or repeated the original captured invocation")
	}
	f.assertUsage(t, 3)
	if err := f.runner.processJob(ctx, f.jobID); err != nil {
		t.Fatal("closed unresolved round replay failed", err)
	}
	if main, align, _, _ := f.calls(); main != 3 || align != 0 {
		t.Fatalf("pool successor was eligible without an explicit retry: main=%d align=%d", main, align)
	}
	f.assertUsage(t, 3)
}

func TestRubyPipelineSQLiteConfirmedSelectionResumesOnlyNextRound(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "all"
		if explicit {
			name = "explicit"
		}
		t.Run(name, func(t *testing.T) {
			f := newRubyPipelineFixture(t, 2)
			f.snapshot.ExplicitSegmentSelection = explicit
			f.snapshot.Rounds[0].Translate.SegmentFilter.StatusFilter = "all"
			if explicit {
				f.snapshot.Rounds[0].Translate.SegmentFilter.StatusFilter = "pending_only"
			}
			data, err := json.Marshal(f.snapshot.Rounds[0])
			if err != nil {
				t.Fatal(err)
			}
			var next service.JobRoundSnapshot
			if err := json.Unmarshal(data, &next); err != nil {
				t.Fatal(err)
			}
			next.Translate.SegmentFilter.StatusFilter = "all"
			f.snapshot.Rounds = append(f.snapshot.Rounds, next)
			f.persistSnapshot(t)
			_, result, err := f.execute(t, 0, nil, nil)
			if err != nil || len(result.Resolved) != 2 {
				t.Fatalf("first round=%+v %v", result, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if f.client.WorkCandidate.Query().Where(workcandidate.StateIn("pending_alignment", "ready_to_commit")).CountX(ctx) != 0 {
				t.Fatal("first round retained drafts after confirmation")
			}
			main, align, _, _ := f.calls()
			if main != 1 || align != 2 {
				t.Fatalf("initial HTTP=%d/%d", main, align)
			}
			// A fresh Job runtime starts at a still-open first round after the
			// last candidate was cleared. It must use its sealed checkpoints,
			// then allow the same segments to execute in the second round.
			if err := f.runner.processJob(ctx, f.jobID); err != nil {
				t.Fatal(err)
			}
			main, align, _, _ = f.calls()
			if main != 2 || align != 4 {
				t.Fatalf("resumed HTTP=%d/%d, want only one new main and two alignments", main, align)
			}
			if f.client.JobRoundSegment.Query().Where(jobroundsegment.HasJobRoundWith(jobround.JobIDEQ(f.jobID))).CountX(ctx) != 4 {
				t.Fatal("round-scoped checkpoints were lost or reused by the next round")
			}
			job := f.client.Job.GetX(ctx, f.jobID)
			if job.Status != service.JobStatusCompleted || job.ProgressCompleted != 4 || job.ProgressTotal != 4 {
				t.Fatalf("resumed progress=%+v", job)
			}
			f.assertUsage(t, 6)
		})
	}
}
