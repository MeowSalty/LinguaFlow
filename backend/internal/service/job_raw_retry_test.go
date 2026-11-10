package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func TestRetryRetainsReliableUnpromotedRawAcrossEpoch(t *testing.T) {
	for _, mutation := range []string{"none", "snapshot", "source_generation", "baseline_version", "baseline_null_target", "baseline_status"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			env := newJobRoundTestEnv(t, nil)
			j, resources, rounds := seedJobWithRounds(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{{status: JobResourceStatusRunning, segmentCount: 1, rounds: []jobRoundSpec{{roundIndex: 0, mode: "translate", status: JobRoundStatusRunning}}}})
			jr, round := resources[0], rounds[0][0]
			resource := jr.QueryResource().OnlyX(ctx)
			seg := createTestSegment(t, env.client, resource.ID, 0, "raw source", nil)
			env.client.Job.UpdateOneID(j.ID).SetRetryEpoch(7).ExecX(ctx)
			scope := workstate.Scope{JobID: j.ID, ResourceID: resource.ID, JobResourceID: jr.ID, RoundID: round.ID, RetryEpoch: 7, SourceGeneration: resource.SourceGeneration, SourceRevisionID: resource.CurrentSourceRevisionID}
			store := workstate.NewStore(env.client)
			if _, err := store.SealRound(ctx, scope, []int{seg.ID}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := GetSnapshot(j)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(encoded)
			digest := hex.EncodeToString(sum[:])
			members := []workstate.PromotionMember{{SegmentID: seg.ID, WorkID: workstate.WorkIdentity(scope, seg.ID), CandidateID: "raw-candidate", BaselineVersion: seg.ContentVersion, BaselineTarget: seg.TargetText, BaselineStatus: string(seg.Status), SourceGeneration: scope.SourceGeneration, SourceRevisionID: scope.SourceRevisionID}}
			request := workstate.Request{ID: "raw-before-retry", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "frozen-input", MainAttempt: true, MaxMainAttempts: 3, FreezePayload: []byte("frozen"), SnapshotDigest: digest, PromotionMembers: members}
			if err := store.ReserveRequest(ctx, request); err != nil {
				t.Fatal(err)
			}
			response := []byte("reliable untranslated response")
			if err := store.HandoffResponse(ctx, workstate.ResponseHandoff{RequestID: request.ID, Scope: scope, FreezePayload: request.FreezePayload, ResponsePayload: response, SnapshotDigest: digest, Members: members, Result: workstate.RequestResult{UsageKnown: true, InputTokens: 3, OutputTokens: 4}}); err != nil {
				t.Fatal(err)
			}
			env.client.Job.UpdateOneID(j.ID).SetStatus(JobStatusFailed).ExecX(ctx)
			env.client.JobResource.UpdateOneID(jr.ID).SetStatus(JobResourceStatusFailed).ExecX(ctx)
			env.client.JobRound.UpdateOneID(round.ID).SetStatus(JobRoundStatusFailed).ExecX(ctx)
			switch mutation {
			case "snapshot":
				changed := env.client.Job.GetX(ctx, j.ID).ExecutionConfig
				changed["target_lang"] = "changed-language"
				env.client.Job.UpdateOneID(j.ID).SetExecutionConfig(changed).ExecX(ctx)
			case "source_generation":
				env.client.Resource.UpdateOneID(resource.ID).SetSourceGeneration(resource.SourceGeneration + 1).ExecX(ctx)
			case "baseline_version":
				env.client.Segment.UpdateOneID(seg.ID).AddContentVersion(1).ExecX(ctx)
			case "baseline_null_target":
				env.client.Segment.UpdateOneID(seg.ID).SetTargetText("").ExecX(ctx)
			case "baseline_status":
				env.client.Segment.UpdateOneID(seg.ID).SetStatus(SegmentStatusTranslated).ExecX(ctx)
			}
			retried, err := env.svc.RetryJob(ctx, env.user.ID, j.ID)
			if mutation != "none" {
				if !errors.Is(err, workstate.ErrManifest) && !errors.Is(err, workstate.ErrStale) && !errors.Is(err, ErrSourceRevisionConflict) {
					t.Fatalf("invalid reliable raw accepted: %v", err)
				}
				current := env.client.Job.GetX(ctx, j.ID)
				item := env.client.WorkItem.Query().Where(workitem.SegmentIDEQ(seg.ID)).OnlyX(ctx)
				if current.RetryEpoch != 7 || current.Status != JobStatusFailed || item.RetryEpoch != 7 || item.State != "response_pending" || item.ResponseRequestID != request.ID {
					t.Fatal("invalid raw retry did not roll back the execution reset")
				}
				raw := env.client.WorkRequest.Query().Where(workrequest.IdentityEQ(request.ID)).OnlyX(ctx)
				if raw.RetryEpoch != 7 || raw.State != "completed" || string(raw.ResponsePayload) != string(response) || raw.PromotionCursor != 0 || env.client.UsageRecord.Query().CountX(ctx) != 1 {
					t.Fatal("invalid raw retry changed durable response or accounting")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if retried.RetryEpoch != 8 {
				t.Fatalf("retry epoch=%d", retried.RetryEpoch)
			}
			if err := env.svc.PrepareRecovery(ctx); err != nil {
				t.Fatal(err)
			}
			item := env.client.WorkItem.Query().Where(workitem.SegmentIDEQ(seg.ID)).OnlyX(ctx)
			if item.State != "response_pending" || item.ResponseRequestID != request.ID || item.RetryEpoch != 8 || item.MainAttempts != 0 {
				t.Fatalf("raw member reentered main dispatch: %+v", item)
			}
			raw := env.client.WorkRequest.Query().Where(workrequest.IdentityEQ(request.ID)).OnlyX(ctx)
			if raw.RetryEpoch != 7 || raw.State != "completed" || string(raw.ResponsePayload) != string(response) || raw.PromotionCursor != 0 || env.client.UsageRecord.Query().CountX(ctx) != 1 {
				t.Fatal("retry changed raw identity, payload, cursor, or accounting")
			}
		})
	}
}
