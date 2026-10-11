package service

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
)

func TestRubySnapshotVersionSurvivesRecoveryResumeAndRetry(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		t.Run(map[int]string{1: "legacy_round_shared", 2: "stage_separated", 3: "stage_separated_batch"}[version], func(t *testing.T) {
			legacy := version == 1
			ctx := context.Background()
			env := newJobRoundTestEnv(t, nil)
			row, resources, rounds := seedJobWithRounds(t, env, JobStatusPausing, 1, 0, []jobResourceSpec{{
				status: JobResourceStatusRunning, segmentCount: 1,
				rounds: []jobRoundSpec{{roundIndex: 0, mode: "translate", status: JobRoundStatusRunning, total: 1}},
			}})
			snapshot, err := GetSnapshot(row)
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Strategy.Ruby.Enabled = true
			snapshot.RubyRetry = &ExecutionPlanRubyRetrySnapshot{Enabled: true, Backend: snapshot.Rounds[0].Backend, MaxAttempts: 2, Concurrency: 3, BatchSize: new(4), MaxWordsPerBatch: new(120), BatchWaitMS: new(0)}
			snapshot.SchemaVersion, snapshot.DefaultsVersion = version, version
			if version < 3 {
				snapshot.RubyRetry.BatchSize, snapshot.RubyRetry.MaxWordsPerBatch, snapshot.RubyRetry.BatchWaitMS = nil, nil, nil
				snapshot.RubyBatchProtocolVersion = 0
				snapshot.RubyTemplates.BatchJSON, snapshot.RubyTemplates.BatchText = "", ""
			}
			if legacy {
				snapshot.SchemaVersion, snapshot.DefaultsVersion = 1, 1
				snapshot.RubyProtocolVersion, snapshot.RubyValidatorVersion = 0, 0
				snapshot.RubyRetry.Concurrency = 0
				snapshot.RubyTemplates = execution.RubyTemplates{JSON: prompt.LegacyRubyAlignmentJSONTemplate, Text: prompt.LegacyRubyAlignmentTextTemplate}
			}
			if err := execution.ValidateSpec(snapshot); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var frozen map[string]any
			if err := json.Unmarshal(encoded, &frozen); err != nil {
				t.Fatal(err)
			}
			env.client.Job.UpdateOneID(row.ID).SetExecutionConfig(frozen).SetPauseRequested(true).ExecX(ctx)
			assertFrozen := func() {
				t.Helper()
				stored := env.client.Job.GetX(ctx, row.ID)
				if !reflect.DeepEqual(stored.ExecutionConfig, frozen) {
					t.Fatal("lifecycle operation rewrote the frozen execution configuration")
				}
				restored, err := env.svc.GetExecutionSnapshot(ctx, row.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(restored, snapshot) {
					t.Fatal("reading the snapshot filled defaults or changed frozen templates")
				}
				model, err := execution.ConcurrencyModelForSpec(restored)
				wantModel := execution.StageSeparated
				if legacy {
					wantModel = execution.LegacyRoundShared
				}
				if err != nil || model != wantModel {
					t.Fatalf("concurrency interpretation changed: %s %v", model, err)
				}
				wantBatch := execution.RubyRetryBatchConfig{BatchSize: 1}
				if version == 3 {
					wantBatch = execution.RubyRetryBatchConfig{BatchSize: 4, MaxWordsPerBatch: 120}
				}
				if got := execution.EffectiveRubyRetryBatch(restored); got != wantBatch {
					t.Fatalf("frozen batching changed: %+v want=%+v", got, wantBatch)
				}
			}
			assertFrozen()
			for range 2 {
				if err := env.svc.PrepareRecovery(ctx); err != nil {
					t.Fatal(err)
				}
				assertFrozen()
			}
			if _, err := env.svc.ResumeJob(ctx, env.user.ID, row.ID); err != nil {
				t.Fatal(err)
			}
			assertFrozen()
			env.client.Job.UpdateOneID(row.ID).SetStatus(JobStatusCancelled).ExecX(ctx)
			env.client.JobResource.UpdateOneID(resources[0].ID).SetStatus(JobResourceStatusCancelled).ExecX(ctx)
			env.client.JobRound.UpdateOneID(rounds[0][0].ID).SetStatus(JobRoundStatusFailed).ExecX(ctx)
			if _, err := env.svc.RetryJob(ctx, env.user.ID, row.ID); err != nil {
				t.Fatal(err)
			}
			assertFrozen()
		})
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
		t.Fatal("stored v2 snapshot acquired missing concurrency from current defaults")
	}
}
