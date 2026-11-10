package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

func TestOldRubySnapshotsStayHistoryBeforeRecoveryResumeAndRetry(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		ctx := context.Background()
		env := newJobRoundTestEnv(t, nil)
		row, resources, _ := seedJobWithRounds(t, env, JobStatusPausing, 1, 0, []jobResourceSpec{{
			status: JobResourceStatusRunning, segmentCount: 1,
			rounds: []jobRoundSpec{{roundIndex: 0, mode: "translate", status: JobRoundStatusRunning, total: 1}},
		}})
		frozen := row.ExecutionConfig
		frozen["schema_version"], frozen["defaults_version"] = version, version
		env.client.Job.UpdateOneID(row.ID).SetExecutionConfig(frozen).SetPauseRequested(true).ExecX(ctx)
		before := env.client.Job.GetX(ctx, row.ID)
		for range 2 {
			if err := env.svc.PrepareRecovery(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := env.svc.RecoverPendingJobs(ctx); err != nil {
				t.Fatal(err)
			}
			after := env.client.Job.GetX(ctx, row.ID)
			if after.Status != before.Status || after.RetryEpoch != before.RetryEpoch || !reflect.DeepEqual(after.ExecutionConfig, before.ExecutionConfig) {
				t.Fatalf("old snapshot %d changed during recovery", version)
			}
		}
		if _, err := env.svc.GetJob(ctx, env.user.ID, row.ID); err != nil {
			t.Fatalf("history became unreadable: %v", err)
		}
		if _, err := env.svc.GetExecutionSnapshot(ctx, row.ID); !errors.Is(err, execution.ErrUnsupportedExecutionFormat) {
			t.Fatalf("old execution accepted: %v", err)
		}
		env.client.Job.UpdateOneID(row.ID).SetStatus(JobStatusPaused).ExecX(ctx)
		if _, err := env.svc.ResumeJob(ctx, env.user.ID, row.ID); !errors.Is(err, execution.ErrUnsupportedExecutionFormat) {
			t.Fatalf("old resume accepted: %v", err)
		}
		if env.client.Job.GetX(ctx, row.ID).Status != JobStatusPaused {
			t.Fatal("failed resume changed state")
		}
		if _, err := env.svc.CancelJob(ctx, env.user.ID, row.ID); err != nil {
			t.Fatalf("explicit old cancellation refused: %v", err)
		}
		env.client.JobResource.UpdateOneID(resources[0].ID).SetStatus(JobResourceStatusCancelled).ExecX(ctx)
		if _, err := env.svc.RetryJob(ctx, env.user.ID, row.ID); !errors.Is(err, execution.ErrUnsupportedExecutionFormat) {
			t.Fatalf("old retry accepted: %v", err)
		}
		if after := env.client.Job.GetX(ctx, row.ID); after.Status != JobStatusCancelled || !reflect.DeepEqual(after.ExecutionConfig, before.ExecutionConfig) {
			t.Fatal("old retry changed history")
		}
	}
}

func TestRubySnapshotRestoreRejectsMissingCurrentConcurrency(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	row, _, _ := seedJobWithRounds(t, env, JobStatusPending, 0, 0, nil)
	snapshot, err := GetSnapshot(row)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.RubyRetry = &ExecutionPlanRubyRetrySnapshot{Enabled: true, Backend: snapshot.Rounds[0].Backend, MaxAttempts: 1}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &row.ExecutionConfig); err != nil {
		t.Fatal(err)
	}
	if _, err := GetSnapshot(row); err == nil {
		t.Fatal("stored current snapshot acquired missing concurrency from defaults")
	}
}
