package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	entjob "github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

// Three resources with 167 rounds cross the 500-row SQL chunk boundary.
const allocationTestRounds = 167

func failJobRoundAfterFirstChunk(client *ent.Client, failure error) *int {
	calls := new(int)
	client.JobRound.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if mutation.Op().Is(ent.OpCreate) {
				(*calls)++
				if *calls > jobRoundBulkChunk {
					return nil, failure
				}
			}
			return next.Mutate(ctx, mutation)
		})
	})
	return calls
}

func assertAllocationTestMatrix(t *testing.T, client *ent.Client, jobID int) {
	t.Helper()
	ctx := context.Background()
	resources := client.JobResource.Query().Where(jobresource.HasJobWith(entjob.IDEQ(jobID))).AllX(ctx)
	if len(resources) != 3 {
		t.Fatalf("resource matrix: got %d want 3", len(resources))
	}
	for _, resource := range resources {
		rounds := client.JobRound.Query().Where(jobround.JobIDEQ(jobID), jobround.JobResourceIDEQ(resource.ID)).
			Order(ent.Asc(jobround.FieldRoundIndex)).AllX(ctx)
		if len(rounds) != allocationTestRounds {
			t.Fatalf("resource %d has %d rounds want %d", resource.ID, len(rounds), allocationTestRounds)
		}
		for i, round := range rounds {
			if round.RoundIndex != i || round.Mode != "translate" || round.Status != JobRoundStatusPending {
				t.Fatalf("round lost or changed: %+v", round)
			}
		}
	}
}

func TestCreateManualJobRoundChunksCommitAtomically(t *testing.T) {
	for _, failLastChunk := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail_last_chunk=%t", failLastChunk), func(t *testing.T) {
			ctx := context.Background()
			fixture, client, userID, _ := newQuickFixture(t)
			backendID := seedUserBackend(t, client, userID)
			projectID := seedProject(t, client, userID)
			var resourceIDs []int
			for i := 0; i < 3; i++ {
				resource := createTestResource(t, client, projectID, fmt.Sprintf("allocation-%d.txt", i))
				createTestSegment(t, client, resource.ID, 0, "source", nil)
				resourceIDs = append(resourceIDs, resource.ID)
			}
			rounds := make([]schema.ExecutionRoundConfig, allocationTestRounds)
			for i := range rounds {
				rounds[i] = validTranslateRound(backendID)
			}
			plan, err := fixture.executionPlans.Create(ctx, userID, CreateExecutionPlanTemplateInput{
				Name: "allocation chunks", ProfileID: -1, Rounds: rounds,
			})
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("last round chunk failed")
			var calls *int
			if failLastChunk {
				calls = failJobRoundAfterFirstChunk(client, failure)
			}
			created, err := fixture.jobs.CreateManualJob(ctx, userID, projectID, CreateJobInput{
				ExecutionPlanID: plan.ID, ResourceIDs: resourceIDs,
			})
			if failLastChunk {
				if !errors.Is(err, failure) || *calls != 501 {
					t.Fatalf("failure not reached after first chunk: calls=%d err=%v", *calls, err)
				}
				if client.Job.Query().CountX(ctx) != 0 || client.JobResource.Query().CountX(ctx) != 0 || client.JobRound.Query().CountX(ctx) != 0 || client.CredentialJobReference.Query().CountX(ctx) != 0 {
					t.Fatal("failed creation left a partial job or credential reference")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertAllocationTestMatrix(t, client, created.ID)
		})
	}
}

func TestBackfillJobRoundChunksCommitAtomically(t *testing.T) {
	for _, failLastChunk := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail_last_chunk=%t", failLastChunk), func(t *testing.T) {
			ctx := context.Background()
			env := newJobRoundTestEnv(t, nil)
			job, _, _ := seedJobWithRounds(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{
				{status: JobResourceStatusRunning}, {status: JobResourceStatusRunning}, {status: JobResourceStatusRunning},
			})
			snapshot, err := GetSnapshot(job)
			if err != nil {
				t.Fatal(err)
			}
			round := snapshot.Rounds[0]
			snapshot.Rounds = make([]execution.JobRoundSnapshot, allocationTestRounds)
			for i := range snapshot.Rounds {
				snapshot.Rounds[i] = round
			}
			snapshot, err = execution.Resolve(*snapshot)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err := json.Unmarshal(raw, &config); err != nil {
				t.Fatal(err)
			}
			env.client.Job.UpdateOneID(job.ID).SetExecutionConfig(config).ExecX(ctx)
			failure := errors.New("last backfill chunk failed")
			var calls *int
			if failLastChunk {
				calls = failJobRoundAfterFirstChunk(env.client, failure)
			}
			err = env.svc.prepareRecoveredJobState(ctx, job.ID)
			if failLastChunk {
				if !errors.Is(err, failure) || *calls != 501 {
					t.Fatalf("failure not reached after first chunk: calls=%d err=%v", *calls, err)
				}
				if env.client.JobRound.Query().CountX(ctx) != 0 {
					t.Fatal("failed recovery left a partial round matrix")
				}
				if env.client.Job.GetX(ctx, job.ID).Status != JobStatusRunning {
					t.Fatal("failed recovery changed job status")
				}
				for _, resource := range env.client.JobResource.Query().AllX(ctx) {
					if resource.Status != JobResourceStatusRunning {
						t.Fatal("failed recovery changed resource status")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertAllocationTestMatrix(t, env.client, job.ID)
		})
	}
}
