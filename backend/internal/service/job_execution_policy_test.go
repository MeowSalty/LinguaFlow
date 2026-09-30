package service

import (
	"context"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestGetSnapshotRejectsMissingConfiguration(t *testing.T) {
	if _, err := GetSnapshot(&ent.Job{}); err == nil {
		t.Fatal("missing snapshot was accepted")
	}
}

func TestJobRestartRejectsInvalidExecutionBeforeReset(t *testing.T) {
	ctx := context.Background()
	for _, operation := range []string{"recover", "resume", "retry"} {
		for _, invalid := range []string{"missing", "unsupported_version", "incomplete_options", "plaintext", "revoked", "deleted_backend"} {
			t.Run(operation+"/"+invalid, func(t *testing.T) {
				env := newJobRoundTestEnv(t, nil)
				status, resourceStatus := JobStatusRunning, JobResourceStatusRunning
				if operation == "resume" {
					status = JobStatusPaused
				}
				if operation == "retry" {
					status, resourceStatus = JobStatusFailed, JobResourceStatusFailed
				}
				job, resources, rounds := seedJobWithRounds(t, env, status, 10, 3, []jobResourceSpec{{
					status: resourceStatus,
					rounds: []jobRoundSpec{{roundIndex: 0, mode: "translate", status: JobRoundStatusRunning, total: 10, completed: 3}},
				}})
				spec, err := GetSnapshot(job)
				if err != nil {
					t.Fatal(err)
				}
				switch invalid {
				case "missing":
					job.ExecutionConfig = map[string]any{}
				case "unsupported_version":
					job.ExecutionConfig["schema_version"] = 999
				case "incomplete_options", "plaintext":
					round := job.ExecutionConfig["rounds"].([]any)[0].(map[string]any)
					options := round["backend"].(map[string]any)["options"].(map[string]any)
					if invalid == "incomplete_options" {
						delete(options, "timeout")
					} else {
						options["api_key"] = "must-never-run"
					}
				case "revoked":
					if err := env.svc.backends.Credentials().Revoke(ctx, env.user.ID, spec.Rounds[0].Backend.Credential); err != nil {
						t.Fatal(err)
					}
				case "deleted_backend":
					if err := env.svc.backends.Delete(ctx, env.user.ID, spec.Rounds[0].Backend.ID); err != nil {
						t.Fatal(err)
					}
				}
				if err := env.client.Job.UpdateOneID(job.ID).SetExecutionConfig(job.ExecutionConfig).Exec(ctx); err != nil {
					t.Fatal(err)
				}
				switch operation {
				case "recover":
					ids, err := env.svc.RecoverPendingJobs(ctx)
					if err != nil || len(ids) != 0 {
						t.Fatalf("invalid job entered recovery queue: ids=%v err=%v", ids, err)
					}
					after := env.client.Job.GetX(ctx, job.ID)
					if after.Status != JobStatusFailed || after.ErrorMessage == nil || !strings.HasPrefix(*after.ErrorMessage, "execution recovery refused:") {
						t.Fatalf("rejected recovery was not recorded: status=%s error=%v", after.Status, after.ErrorMessage)
					}
				case "resume":
					if _, err := env.svc.ResumeJob(ctx, env.user.ID, job.ID); err == nil {
						t.Fatal("invalid job resumed")
					}
				case "retry":
					if _, err := env.svc.RetryJob(ctx, env.user.ID, job.ID); err == nil {
						t.Fatal("invalid job retried")
					}
				}
				if operation != "recover" && env.client.Job.GetX(ctx, job.ID).Status != status {
					t.Fatal("rejected execution changed job status")
				}
				if env.client.JobResource.GetX(ctx, resources[0].ID).Status != resourceStatus {
					t.Fatal("rejected execution reset a resource")
				}
				if env.client.JobRound.GetX(ctx, rounds[0][0].ID).Status != JobRoundStatusRunning {
					t.Fatal("rejected execution reset a round")
				}
				assertJobProgress(t, env.client.Job.GetX(ctx, job.ID), 10, 3)
			})
		}
	}
}
