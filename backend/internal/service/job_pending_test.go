package service

import (
	"context"
	"testing"
)

func TestJobPendingDiscoveryIsBoundedAndReadOnly(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	first, _, _ := seedJobWithRounds(t, env, JobStatusPending, 0, 0, nil)
	running, _, _ := seedJobWithRounds(t, env, JobStatusRunning, 0, 0, nil)
	last, _, _ := seedJobWithRounds(t, env, JobStatusPending, 0, 0, nil)
	ids, err := env.svc.PendingTaskIDs(ctx, 0, 1)
	if err != nil || len(ids) != 1 || ids[0] != first.ID {
		t.Fatalf("first page: %v %v", ids, err)
	}
	ids, err = env.svc.PendingTaskIDs(ctx, first.ID, 1)
	if err != nil || len(ids) != 1 || ids[0] != last.ID {
		t.Fatalf("second page: %v %v", ids, err)
	}
	unchanged, err := env.client.Job.Get(ctx, running.ID)
	if err != nil || unchanged.Status != JobStatusRunning {
		t.Fatalf("periodic discovery reset running task: %+v %v", unchanged, err)
	}
	for _, limit := range []int{0, -1, 129} {
		if _, err := env.svc.PendingTaskIDs(ctx, 0, limit); err == nil {
			t.Fatalf("accepted invalid page limit %d", limit)
		}
	}
}

func TestJobPrepareRecoveryPreservesRoundCheckpoint(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	jobRow, resources, rounds := seedJobWithRounds(t, env, JobStatusRunning, 99, 99, []jobResourceSpec{{
		status:       JobResourceStatusRunning,
		segmentCount: 3,
		rounds:       []jobRoundSpec{{roundIndex: 0, mode: "translate", status: JobRoundStatusRunning, total: 3, completed: 1, resolvedCount: 1}},
	}})
	for i := 0; i < 2; i++ {
		if err := env.svc.PrepareRecovery(ctx); err != nil {
			t.Fatal(err)
		}
	}
	recovered, err := env.client.Job.Get(ctx, jobRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != JobStatusPending || recovered.ProgressTotal != 3 || recovered.ProgressCompleted != 1 {
		t.Fatalf("incorrect recovered job: %+v", recovered)
	}
	resource, err := env.client.JobResource.Get(ctx, resources[0].ID)
	if err != nil || resource.Status != JobResourceStatusPending {
		t.Fatalf("resource not recoverable: %+v %v", resource, err)
	}
	round, err := env.client.JobRound.Get(ctx, rounds[0][0].ID)
	if err != nil || round.Status != JobRoundStatusPending || round.SegmentTotal != 3 || round.SegmentCompleted != 1 {
		t.Fatalf("round checkpoint lost: %+v %v", round, err)
	}
	count, err := env.client.JobRoundSegment.Query().Count(ctx)
	if err != nil || count != 1 {
		t.Fatalf("resolved checkpoint count: %d %v", count, err)
	}
}
