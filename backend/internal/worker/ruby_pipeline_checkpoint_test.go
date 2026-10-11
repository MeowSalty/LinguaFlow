package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
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

func TestRubyPipelineSQLitePauseRejectsUnsafeCapacityDrain(t *testing.T) {
	f := newRubyPipelineFixture(t, 1)
	f.runner.pipeCfg.Candidates.ItemBytes = 256
	f.respond = func(ctx context.Context, request rubyPipelineRequest) (any, error) {
		if _, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID); err != nil {
			return nil, err
		}
		if !f.runner.Pause(f.jobID) {
			return nil, errors.New("Job gate was not registered")
		}
		return rubyPipelineResponse(request), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.runner.processJob(ctx, f.jobID); !errors.Is(err, pipeline.ErrCandidateCapacity) {
		t.Fatalf("oversized admitted response during pause: %v", err)
	}
	job := f.client.Job.GetX(ctx, f.jobID)
	if job.Status != service.JobStatusPausing || !job.PauseRequested {
		t.Fatalf("failed handoff was reported as a safe pause: %s", job.Status)
	}
	if f.client.WorkCandidate.Query().CountX(ctx) != 0 || f.client.JobRoundSegment.Query().CountX(ctx) != 0 {
		t.Fatal("capacity failure produced accepted work")
	}
	request := f.client.WorkRequest.Query().OnlyX(ctx)
	if request.State != "received" {
		t.Fatalf("unhandled response was marked complete: %s", request.State)
	}
	f.assertUsage(t, 1)
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
