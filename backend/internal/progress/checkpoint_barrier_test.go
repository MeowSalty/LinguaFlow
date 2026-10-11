package progress

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestCheckpointBarrierReportsFailureAndRetriesOnlyStorage(t *testing.T) {
	ctx := context.Background()
	client := weightedTestClient(t)
	jobID, jrID := progressFixture(t, client)
	round := createRoundRow(t, client, jobID, jrID, 0, "semantic_qa")
	ids := createSegments(t, client, 1)
	r := NewDBReporter(DBReporterOptions{Client: client, JobID: jobID, JobResourceID: jrID, Ticker: time.Hour})
	t.Cleanup(func() { r.Close() })
	r.SwitchRound(round, func(index int) (int, bool) { return ids[0], index == 0 })
	r.StageStart("semantic_qa", 1)
	if err := r.StageError(); err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	fail.Store(true)
	injected := errors.New("checkpoint write unavailable")
	client.JobRoundSegment.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if fail.Load() {
				return nil, injected
			}
			return next.Mutate(ctx, m)
		})
	})
	r.SegmentResolved(0)
	r.SegmentDone()
	r.BatchComplete()
	if !errors.Is(r.StageError(), injected) {
		t.Fatalf("lost checkpoint error: %v", r.StageError())
	}
	if client.JobRoundSegment.Query().CountX(ctx) != 0 {
		t.Fatal("failed checkpoint unexpectedly committed")
	}
	fail.Store(false)
	if err := r.FlushCheckpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.StageError(); err != nil {
		t.Fatalf("successful storage retry retained error: %v", err)
	}
	if client.JobRoundSegment.Query().CountX(ctx) != 1 || client.Job.GetX(ctx, jobID).ProgressCompleted != 1 {
		t.Fatal("storage retry lost or duplicated completion")
	}
}
