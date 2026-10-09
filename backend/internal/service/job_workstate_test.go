package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func seedDurableJobWork(t *testing.T) (*jobRoundTestEnv, *ent.Job, *ent.JobResource, *ent.JobRound, *ent.WorkItem) {
	t.Helper()
	ctx := context.Background()
	env := newJobRoundTestEnv(t, nil)
	j, resources, rounds := seedJobWithRounds(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{{
		status: JobResourceStatusRunning, segmentCount: 1,
		rounds: []jobRoundSpec{{roundIndex: 0, mode: "translate", status: JobRoundStatusRunning}},
	}})
	jr, round := resources[0], rounds[0][0]
	resource := jr.QueryResource().OnlyX(ctx)
	seg := createTestSegment(t, env.client, resource.ID, 0, "durable source", nil)
	env.client.Job.UpdateOneID(j.ID).SetRetryEpoch(7).ExecX(ctx)
	scope := workstate.Scope{JobID: j.ID, ResourceID: resource.ID, JobResourceID: jr.ID, RoundID: round.ID, RetryEpoch: 7, SourceGeneration: resource.SourceGeneration, SourceRevisionID: resource.CurrentSourceRevisionID}
	store := workstate.NewStore(env.client)
	if _, err := store.SealRound(ctx, scope, []int{seg.ID}); err != nil {
		t.Fatal(err)
	}
	parent := workstate.Request{ID: "retained-parent", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "parent", MainAttempt: true}
	if err := store.ReserveRequest(ctx, parent); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRequest(ctx, parent.ID, workstate.RequestResult{State: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCandidate(ctx, workstate.Candidate{ID: "retained-candidate", ParentRequestID: parent.ID, Version: 1, Scope: scope, SegmentID: seg.ID, DTOVersion: 1, Mode: "translate", SnapshotDigest: "frozen", BaselineVersion: seg.ContentVersion, BaselineStatus: string(seg.Status), State: "pending_alignment", Payload: []byte(`{"valid":true}`)}); err != nil {
		t.Fatal(err)
	}
	item := env.client.WorkItem.Query().Where(workitem.JobRoundIDEQ(round.ID)).OnlyX(ctx)
	env.client.WorkItem.UpdateOneID(item.ID).SetPoolIndex(2).SetMainAttempts(3).SetAlignmentAttempts(2).SetNetworkAttempts(4).SetMainNetworkAttempts(4).SetAlignmentNetworkAttempts(1).SetPromptPhase("repair").SetCursor([]byte(`{"phase":"repair"}`)).SetNextAttemptAt(time.Now().Add(time.Minute)).ExecX(ctx)
	env.client.JobRound.UpdateOneID(round.ID).SetPoolIndex(2).ExecX(ctx)
	env.client.WorkRequest.Create().SetIdentity("unknown-on-recovery").SetJobID(j.ID).SetResourceID(resource.ID).SetJobRoundID(round.ID).SetRetryEpoch(7).SetSegmentIds([]int{seg.ID}).SetStage("alignment").SetBackendID(1).SetBudgetModel("stage_separated").SetInputDigest("request").SetState("received").ExecX(ctx)
	return env, j, jr, round, item
}

func TestDurableRecoveryAndResumePreserveCandidateAndBudgets(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "restart", true: "pause_restart_resume"}[paused], func(t *testing.T) {
			ctx := context.Background()
			env, j, _, round, item := seedDurableJobWork(t)
			if paused {
				env.client.Job.UpdateOneID(j.ID).SetStatus(JobStatusPausing).SetPauseRequested(true).ExecX(ctx)
			}
			for range 2 {
				if err := env.svc.PrepareRecovery(ctx); err != nil {
					t.Fatal(err)
				}
			}
			current := env.client.Job.GetX(ctx, j.ID)
			wantStatus := JobStatusPending
			if paused {
				wantStatus = JobStatusPaused
			}
			if current.Status != wantStatus || current.RetryEpoch != 7 || current.PauseRequested != paused {
				t.Fatalf("recovered job: status=%s epoch=%d pause=%v", current.Status, current.RetryEpoch, current.PauseRequested)
			}
			ids, err := env.svc.PendingTaskIDs(ctx, 0, 128)
			if err != nil {
				t.Fatal(err)
			}
			if paused && len(ids) != 0 {
				t.Fatal("pausing recovery re-enqueued work")
			}
			if paused {
				current, err = env.svc.ResumeJob(ctx, env.user.ID, j.ID)
				if err != nil {
					t.Fatal(err)
				}
				if current.RetryEpoch != 7 || current.PauseRequested || current.Status != JobStatusPending {
					t.Fatal("resume reset the epoch or retained pause intent")
				}
			}
			cursor := env.client.WorkItem.GetX(ctx, item.ID)
			if cursor.RetryEpoch != 7 || cursor.PoolIndex != 2 || cursor.MainAttempts != 3 || cursor.AlignmentAttempts != 2 || cursor.NetworkAttempts != 4 || cursor.MainNetworkAttempts != 4 || cursor.AlignmentNetworkAttempts != 1 || cursor.PromptPhase != "repair" || cursor.NextAttemptAt == nil {
				t.Fatalf("recovery changed attempt cursor: %+v", cursor)
			}
			if env.client.JobRound.GetX(ctx, round.ID).PoolIndex != 2 {
				t.Fatal("recovery returned to pool zero")
			}
			candidate := env.client.WorkCandidate.Query().Where(workcandidate.IdentityEQ("retained-candidate")).OnlyX(ctx)
			if candidate.State != "pending_alignment" || candidate.Version != 1 || len(candidate.Payload) == 0 {
				t.Fatal("recovery discarded candidate progress")
			}
			if env.client.WorkRequest.Query().Where(workrequest.IdentityEQ("unknown-on-recovery")).OnlyX(ctx).State != "unknown" {
				t.Fatal("unresolved request was not marked unknown")
			}
		})
	}
}

func TestExplicitRetryAdvancesOneEpochAndRetainsCandidate(t *testing.T) {
	ctx := context.Background()
	env, j, jr, round, item := seedDurableJobWork(t)
	env.client.Job.UpdateOneID(j.ID).SetStatus(JobStatusCancelled).SetPauseRequested(true).ExecX(ctx)
	env.client.JobResource.UpdateOneID(jr.ID).SetStatus(JobResourceStatusCancelled).ExecX(ctx)
	env.client.JobRound.UpdateOneID(round.ID).SetStatus(JobRoundStatusFailed).ExecX(ctx)
	env.client.WorkItem.UpdateOneID(item.ID).SetPromptPhase("terminal_failure").SetCursor([]byte(`{"phase":"terminal_failure"}`)).ExecX(ctx)
	current, err := env.svc.RetryJob(ctx, env.user.ID, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.RetryEpoch != 8 || current.PauseRequested || current.Status != JobStatusPending {
		t.Fatalf("retry state: %+v", current)
	}
	if _, err := env.svc.RetryJob(ctx, env.user.ID, j.ID); !errors.Is(err, ErrJobNotRetryable) {
		t.Fatalf("duplicate retry=%v", err)
	}
	if err := env.svc.PrepareRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	cursor := env.client.WorkItem.GetX(ctx, item.ID)
	if cursor.RetryEpoch != 8 || cursor.PoolIndex != 0 || cursor.MainAttempts != 0 || cursor.AlignmentAttempts != 0 || cursor.NetworkAttempts != 0 || cursor.MainNetworkAttempts != 0 || cursor.AlignmentNetworkAttempts != 0 || cursor.NextAttemptAt != nil || cursor.State != "candidate" || cursor.CandidateID != "retained-candidate" || cursor.PromptPhase != "initial" || len(cursor.Cursor) != 0 {
		t.Fatalf("retry cursor: %+v", cursor)
	}
	candidate := env.client.WorkCandidate.Query().Where(workcandidate.IdentityEQ("retained-candidate")).OnlyX(ctx)
	if candidate.Version != 1 || len(candidate.Payload) == 0 {
		t.Fatal("retry discarded reusable candidate")
	}
	if env.client.Job.GetX(ctx, j.ID).RetryEpoch != 8 {
		t.Fatal("recovery or duplicate retry allocated another epoch")
	}
}

func TestExplicitRetryCandidateAlignmentSavesDuringPause(t *testing.T) {
	ctx := context.Background()
	env, j, jr, round, item := seedDurableJobWork(t)
	env.client.Job.UpdateOneID(j.ID).SetStatus(JobStatusCancelled).ExecX(ctx)
	env.client.JobResource.UpdateOneID(jr.ID).SetStatus(JobResourceStatusCancelled).ExecX(ctx)
	env.client.JobRound.UpdateOneID(round.ID).SetStatus(JobRoundStatusFailed).ExecX(ctx)
	retried, err := env.svc.RetryJob(ctx, env.user.ID, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.svc.MarkJobRunning(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	resource := jr.QueryResource().OnlyX(ctx)
	scope := workstate.Scope{JobID: j.ID, ResourceID: resource.ID, JobResourceID: jr.ID, RoundID: round.ID, RetryEpoch: retried.RetryEpoch, SourceGeneration: resource.SourceGeneration, SourceRevisionID: resource.CurrentSourceRevisionID}
	store := workstate.NewStore(env.client)
	candidates, _, err := store.LoadCandidates(ctx, scope, 0, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("retained candidate: %v, count=%d", err, len(candidates))
	}
	candidate := candidates[0]
	if candidate.ParentRequestID != "retained-parent" || candidate.Scope.RetryEpoch != 8 {
		t.Fatalf("retained attribution: %+v", candidate)
	}
	request := workstate.Request{ID: "retry-alignment", CandidateID: candidate.ID, Scope: scope, SegmentIDs: []int{candidate.SegmentID}, Stage: "ruby_alignment", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "alignment", AlignmentAttempt: true, MaxAlignmentAttempts: 2, MaxNetworkAttempts: 2}
	if err := store.ReserveRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRequest(ctx, request.ID, workstate.RequestResult{State: "sent"}); err != nil {
		t.Fatal(err)
	}
	paused, err := env.svc.PauseJob(ctx, env.user.ID, j.ID)
	if err != nil || !paused.NeedsDrain || paused.Job.Status != JobStatusPausing {
		t.Fatalf("pause drain: %+v, %v", paused, err)
	}
	if err := store.RecordRequest(ctx, request.ID, workstate.RequestResult{State: "received", UsageKnown: true, InputTokens: 5, OutputTokens: 3}); err != nil {
		t.Fatal(err)
	}
	candidate.Version++
	candidate.State = "ready_to_commit"
	candidate.Payload = []byte(`{"valid":true,"aligned":true}`)
	if err := store.SaveCandidate(ctx, candidate); err != nil {
		t.Fatalf("save admitted alignment after Retry and pause: %v", err)
	}
	if err := store.SaveCandidate(ctx, candidate); err != nil {
		t.Fatalf("replay admitted alignment handoff: %v", err)
	}
	saved := env.client.WorkCandidate.Query().Where(workcandidate.IdentityEQ(candidate.ID)).OnlyX(ctx)
	if saved.Version != 2 || saved.ParentRequestID != "retained-parent" || saved.State != "ready_to_commit" {
		t.Fatalf("saved candidate: %+v", saved)
	}
	cursor := env.client.WorkItem.GetX(ctx, item.ID)
	if cursor.RetryEpoch != 8 || cursor.AlignmentAttempts != 1 || cursor.AlignmentNetworkAttempts != 1 || cursor.MainAttempts != 0 {
		t.Fatalf("handoff changed retry budgets: %+v", cursor)
	}
	if got := env.client.Job.GetX(ctx, j.ID); got.Status != JobStatusPausing || !got.PauseRequested || got.ProgressCompleted != 0 {
		t.Fatalf("handoff falsely completed or paused the job: %+v", got)
	}
}
