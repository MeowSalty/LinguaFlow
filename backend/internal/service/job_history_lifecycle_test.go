package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
)

func TestJobHistoryCancellationDrainRetryAndTerminalTime(t *testing.T) {
	ctx := context.Background()
	env := newJobRoundTestEnv(t, nil)
	var lifecycle tasklife.Coordinator
	env.svc.SetLifecycle(&lifecycle)
	jobRow, resources, _ := seedJobWithRounds(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{
		{status: JobResourceStatusCompleted}, {status: JobResourceStatusRunning},
	})
	guard, err := lifecycle.Lock(ctx, "translation", jobRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	finish := guard.Claim()
	guard.Release()
	defer finish()
	notified := false
	env.svc.SetTaskControl(func(id int) {
		notified = id == jobRow.ID
		// A competing control cannot pass before cancellation notification.
		wait, cancel := context.WithTimeout(ctx, time.Millisecond)
		defer cancel()
		if g, err := lifecycle.Lock(wait, "translation", id); err == nil {
			g.Release()
			t.Error("cancel notification escaped guard")
		}
	}, nil)
	cancelled, err := env.svc.CancelJob(ctx, env.user.ID, jobRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !notified || cancelled.FinishedAt == nil || cancelled.RetentionAnchorAt == nil || !cancelled.FinishedAt.Equal(*cancelled.RetentionAnchorAt) {
		t.Fatalf("missing cancellation timestamp or notification: %+v", cancelled)
	}
	first := *cancelled.FinishedAt
	if err := env.svc.ReconcileJob(ctx, jobRow.ID); err != nil {
		t.Fatal(err)
	}
	stable := env.client.Job.GetX(ctx, jobRow.ID)
	if stable.Status != JobStatusCancelled || stable.FinishedAt == nil || !stable.FinishedAt.Equal(first) {
		t.Fatalf("reconcile resurrected cancelled task: %+v", stable)
	}
	if _, err := env.svc.RetryJob(ctx, env.user.ID, jobRow.ID); !errors.Is(err, tasklife.ErrBusy) {
		t.Fatalf("retry during drain=%v", err)
	}
	finish()
	retried, err := env.svc.RetryJob(ctx, env.user.ID, jobRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != JobStatusPending || retried.FinishedAt != nil || retried.RetentionAnchorAt != nil {
		t.Fatalf("retry kept prior timing: %+v", retried)
	}
	if err := env.svc.MarkJobRunning(ctx, jobRow.ID); err != nil {
		t.Fatal(err)
	}
	if err := env.svc.MarkJobResourceFailed(ctx, jobRow.ID, resources[1].ID, errors.New("new execution failed")); err != nil {
		t.Fatal(err)
	}
	if err := env.svc.ReconcileJob(ctx, jobRow.ID); err != nil {
		t.Fatal(err)
	}
	failed := env.client.Job.GetX(ctx, jobRow.ID)
	if failed.Status != JobStatusFailed || failed.FinishedAt == nil || failed.RetentionAnchorAt == nil || !failed.FinishedAt.Equal(*failed.RetentionAnchorAt) || !failed.FinishedAt.After(first) {
		t.Fatalf("new execution did not restart retention time: %+v", failed)
	}
	if err := env.svc.ReconcileJob(ctx, jobRow.ID); err != nil {
		t.Fatal(err)
	}
	if again := env.client.Job.GetX(ctx, jobRow.ID); !again.FinishedAt.Equal(*failed.FinishedAt) {
		t.Fatal("repeated terminal callback moved finish time")
	}
}

func TestJobHistoryResumeRequiresDrainAndRecoveryClearsTimes(t *testing.T) {
	ctx := context.Background()
	env := newJobRoundTestEnv(t, nil)
	var lifecycle tasklife.Coordinator
	env.svc.SetLifecycle(&lifecycle)
	row, _, _ := seedJobWithRounds(t, env, JobStatusPaused, 0, 0, []jobResourceSpec{{status: JobResourceStatusRunning}})
	guard, err := lifecycle.Lock(ctx, "translation", row.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := guard.Claim()
	guard.Release()
	defer done()
	if _, err := env.svc.ResumeJob(ctx, env.user.ID, row.ID); !errors.Is(err, tasklife.ErrBusy) {
		t.Fatalf("resume during drain=%v", err)
	}
	done()
	if _, err := env.svc.ResumeJob(ctx, env.user.ID, row.ID); err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Hour)
	env.client.Job.UpdateOneID(row.ID).SetStatus(JobStatusRunning).SetFinishedAt(past).SetRetentionAnchorAt(past).ExecX(ctx)
	if err := env.svc.PrepareRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	got := env.client.Job.GetX(ctx, row.ID)
	if got.Status != JobStatusPending || got.FinishedAt != nil || got.RetentionAnchorAt != nil {
		t.Fatalf("recovery retained terminal timing: %+v", got)
	}
}

func TestJobHistoryCompletedTimeIsStableAndLegacyTimeUnknown(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{JobStatusRunning, JobStatusCompleted} {
		env := newJobRoundTestEnv(t, nil)
		row, _, _ := seedJobWithRounds(t, env, status, 0, 0, []jobResourceSpec{{status: JobResourceStatusCompleted}})
		if err := env.svc.ReconcileJob(ctx, row.ID); err != nil {
			t.Fatal(err)
		}
		got := env.client.Job.GetX(ctx, row.ID)
		if status == JobStatusCompleted {
			if got.FinishedAt != nil || got.RetentionAnchorAt != nil {
				t.Fatal("reconcile invented legacy finish time")
			}
		} else if got.FinishedAt == nil || got.RetentionAnchorAt == nil || !got.FinishedAt.Equal(*got.RetentionAnchorAt) {
			t.Fatalf("completed without timing: %+v", got)
		}
	}
}

func TestJobHistoryCancellationFailureRollsBackStateAndTime(t *testing.T) {
	ctx := context.Background()
	env := newJobRoundTestEnv(t, nil)
	row, resources, _ := seedJobWithRounds(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{{status: JobResourceStatusRunning}})
	failure := errors.New("resource cancellation failed")
	env.client.JobResource.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			return nil, failure
		})
	})
	if _, err := env.svc.CancelJob(ctx, env.user.ID, row.ID); !errors.Is(err, failure) {
		t.Fatalf("got %v", err)
	}
	got := env.client.Job.GetX(ctx, row.ID)
	if got.Status != JobStatusRunning || got.FinishedAt != nil || got.RetentionAnchorAt != nil {
		t.Fatalf("failed cancel partially committed: %+v", got)
	}
	if resource := env.client.JobResource.GetX(ctx, resources[0].ID); resource.Status != JobResourceStatusRunning {
		t.Fatal("failed cancel changed resource")
	}
}

func TestJobHistoryCancellationOrganizationPermissions(t *testing.T) {
	for _, role := range []string{"owner", "admin", "member", "nonmember", "revoked", "revoked_in_transaction"} {
		t.Run(role, func(t *testing.T) {
			ctx := context.Background()
			org := newOrganizationFixture(t)
			project := org.client.Project.Create().SetName("job-controls").SetOwnerUserID(org.owner.ID).SaveX(ctx)
			svc := newJobRoundTestService(t, org.client, nil)
			svc.SetLifecycle(&tasklife.Coordinator{})
			env := &jobRoundTestEnv{client: org.client, svc: svc, user: org.owner, project: project}
			row, resources, _ := seedJobWithRounds(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{{status: JobResourceStatusRunning}})
			org.client.Project.UpdateOneID(project.ID).ClearOwnerUserID().SetOwnerOrgID(org.org.ID).ExecX(ctx)
			actor := org.member.ID
			switch role {
			case "owner":
				actor = org.owner.ID
			case "admin":
				actor = org.admin.ID
			case "nonmember":
				actor = org.other.ID
			case "revoked":
				if err := org.svc.RemoveMember(ctx, org.owner.ID, org.org.ID, actor); err != nil {
					t.Fatal(err)
				}
			case "revoked_in_transaction":
				// Change membership after the initial GetJob check, inside the
				// cancellation transaction, to verify its authorization recheck.
				org.client.Project.Use(func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
						client := mutation.(*ent.ProjectMutation).Client()
						if _, err := client.OrgMembership.Delete().Where(orgmembership.HasOrganizationWith(organization.IDEQ(org.org.ID)), orgmembership.HasUserWith(user.IDEQ(actor))).Exec(ctx); err != nil {
							return nil, err
						}
						return next.Mutate(ctx, mutation)
					})
				})
			}
			notified := false
			svc.SetTaskControl(func(id int) { notified = id == row.ID }, nil)
			got, err := svc.CancelJob(ctx, actor, row.ID)
			if role == "nonmember" || role == "revoked" || role == "revoked_in_transaction" {
				if !errors.Is(err, ErrForbidden) || notified {
					t.Fatalf("unauthorized cancellation: err=%v notified=%v", err, notified)
				}
				unchanged := org.client.Job.GetX(ctx, row.ID)
				if unchanged.Status != JobStatusRunning || unchanged.FinishedAt != nil || unchanged.RetentionAnchorAt != nil || org.client.JobResource.GetX(ctx, resources[0].ID).Status != JobResourceStatusRunning {
					t.Fatal("unauthorized cancellation changed task state or terminal time")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !notified || got.Status != JobStatusCancelled || got.FinishedAt == nil || got.RetentionAnchorAt == nil || !got.FinishedAt.Equal(*got.RetentionAnchorAt) || org.client.JobResource.GetX(ctx, resources[0].ID).Status != JobResourceStatusCancelled {
				t.Fatalf("authorized cancellation did not commit state, terminal time and notification: %+v", got)
			}
		})
	}
}
