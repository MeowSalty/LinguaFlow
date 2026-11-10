package progress

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestBoundedCheckpointRestoresScalarBaselineAndDiscardsConfirmedIDs(t *testing.T) {
	client, queries := alignFaultClient(t)
	jobID, jrID, roundID, ids := seedResumedRoundFixture(t, client)
	r := NewDBReporter(DBReporterOptions{Client: client, JobID: jobID, JobResourceID: jrID, Ticker: time.Hour})
	t.Cleanup(func() { _ = r.Close() })
	r.SwitchBoundedRound(roundID, identityMapper(ids))
	// Recovery needs the count, never a full-scope ID SELECT. Pending matching
	// is allowed later in fixed pages, after this empty-baseline check.
	queries.arm(`SELECT "job_round_segments"."segment_id"`, errAlignFault)
	r.StageStart("translate", 100)
	queries.disarm()
	if err := r.StageError(); err != nil {
		t.Fatal(err)
	}
	if r.stageDone.Load() != 60 || len(r.round.resolved) != 0 {
		t.Fatalf("recovery retained a second manifest: count=%d identities=%d", r.stageDone.Load(), len(r.round.resolved))
	}
	for start := 60; start < 100; start += 5 {
		for i := start; i < min(start+5, 100); i++ {
			r.SegmentDone()
			r.SegmentResolved(i)
			r.SegmentResolved(i)
		}
		if len(r.round.resolved) != 5 || len(r.round.pending) != 5 {
			t.Fatal("current batch did not deduplicate its finite facts")
		}
		if err := r.FlushCheckpoint(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(r.round.resolved) != 0 || len(r.round.pending) != 0 {
			t.Fatal("durable IDs outlived the bounded checkpoint handoff")
		}
	}
	if countRoundSegments(t, client, roundID) != 100 || client.Job.GetX(context.Background(), jobID).ProgressCompleted != 100 {
		t.Fatal("scalar baseline or confirmed facts were lost")
	}
}

func TestBoundedCheckpointFailureRetainsOnlyUnconfirmedBatch(t *testing.T) {
	ctx := context.Background()
	client := weightedTestClient(t)
	jobID, jrID := progressFixture(t, client)
	roundID := createRoundRow(t, client, jobID, jrID, 0, "semantic_qa")
	ids := createSegments(t, client, 1)
	r := NewDBReporter(DBReporterOptions{Client: client, JobID: jobID, JobResourceID: jrID, Ticker: time.Hour})
	t.Cleanup(func() { _ = r.Close() })
	r.SwitchBoundedRound(roundID, identityMapper(ids))
	r.StageStart("semantic_qa", 1)
	failing := true
	injected := errors.New("bounded checkpoint write failed")
	client.JobRoundSegment.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if failing {
				return nil, injected
			}
			return next.Mutate(ctx, m)
		})
	})
	r.SegmentResolved(0)
	r.SegmentDone()
	if err := r.FlushCheckpoint(ctx); !errors.Is(err, injected) {
		t.Fatalf("lost durable failure cause: %v", err)
	}
	if len(r.round.pending) != 1 || len(r.round.resolved) != 1 {
		t.Fatal("failed checkpoint dropped its batch facts")
	}
	failing = false
	if err := r.FlushCheckpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if len(r.round.pending) != 0 || len(r.round.resolved) != 0 || countRoundSegments(t, client, roundID) != 1 {
		t.Fatal("retry lost or retained durable identities")
	}
}
