package service

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
)

func assertOperationLifecycle(t *testing.T, env *jobRoundTestEnv, jobID int, status string) {
	t.Helper()
	ctx := context.Background()
	// Recreate the projection service on every observation: discovery must not
	// depend on a known task ID, a prior list, or an SSE subscription.
	for _, state := range []string{"", "active", "all"} {
		page, err := NewOperationQueryService(env.client).List(ctx, env.user.ID, OperationListOptions{
			AccessibleJobListOptions: AccessibleJobListOptions{State: state},
		})
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if status == JobStatusCancelled && state != "all" {
			want = 0
		}
		if len(page.Items) != want || want == 1 && (page.Items[0].Job == nil || page.Items[0].Job.ID != jobID || page.Items[0].Job.Status != status) {
			t.Fatalf("state=%q status=%s: %+v", state, status, page)
		}
	}
	summary, err := NewOperationQueryService(env.client).Summary(ctx, env.user.ID, OperationSummaryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := OperationCounts{}
	switch status {
	case JobStatusPausing:
		want.Pausing = 1
	case JobStatusPaused:
		want.Paused = 1
	}
	if summary.Total != want || summary.ByType.Translation != want || summary.ByType.GlossarySync != (OperationCounts{}) || summary.ByType.Storage != (OperationCounts{}) {
		t.Fatalf("status=%s counts=%+v, want %+v", status, summary, want)
	}
}

func TestOperationsPausingControlsAndLatePauseAfterCancel(t *testing.T) {
	ctx := context.Background()
	env, row, _, _, _ := seedDurableJobWork(t)
	paused, err := env.svc.PauseJob(ctx, env.user.ID, row.ID)
	if err != nil || !paused.NeedsDrain || paused.Job.Status != JobStatusPausing {
		t.Fatalf("pause=%+v err=%v", paused, err)
	}
	assertOperationLifecycle(t, env, row.ID, JobStatusPausing)
	if detail, err := env.svc.GetJob(ctx, env.user.ID, row.ID); err != nil || detail.Status != JobStatusPausing {
		t.Fatalf("pausing detail=%+v err=%v", detail, err)
	}
	if _, err := env.svc.PauseJob(ctx, env.user.ID, row.ID); !errors.Is(err, ErrJobNotPausable) {
		t.Fatalf("repeat pause=%v", err)
	}
	if _, err := env.svc.ResumeJob(ctx, env.user.ID, row.ID); !errors.Is(err, ErrJobNotResumable) {
		t.Fatalf("resume before drain=%v", err)
	}
	if _, err := env.svc.RetryJob(ctx, env.user.ID, row.ID); !errors.Is(err, ErrJobNotRetryable) {
		t.Fatalf("retry before drain=%v", err)
	}
	history := NewTaskHistoryService(env.client, env.svc.projects, env.svc.lifecycle, nil)
	history.SetReady(true)
	if history.CanDelete(ctx, env.user.ID, OperationTranslation, row.ID, row.ProjectID, JobStatusPausing) {
		t.Fatal("pausing task projected as deletable")
	}
	if err := history.Delete(ctx, env.user.ID, HistoryTarget{Kind: OperationTranslation, ID: strconv.Itoa(row.ID), ProjectID: row.ProjectID}); !errors.Is(err, ErrTaskNotTerminal) {
		t.Fatalf("delete pausing=%v", err)
	}
	outsider := createTestUser(t, env.client, "pausing-outsider")
	if _, err := env.svc.CancelJob(ctx, outsider.ID, row.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unauthorized cancel=%v", err)
	}
	assertOperationLifecycle(t, env, row.ID, JobStatusPausing)
	if _, err := env.svc.CancelJob(ctx, env.user.ID, row.ID); err != nil {
		t.Fatal(err)
	}
	// A late worker completion must not move the cancelled task back to active.
	if err := env.svc.MarkJobPaused(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	assertOperationLifecycle(t, env, row.ID, JobStatusCancelled)
}

func TestOperationsPausingRestartDiscovery(t *testing.T) {
	ctx := context.Background()
	env, row, _, _, _ := seedDurableJobWork(t)
	if _, err := env.svc.PauseJob(ctx, env.user.ID, row.ID); err != nil {
		t.Fatal(err)
	}
	assertOperationLifecycle(t, env, row.ID, JobStatusPausing)
	// Exercise startup recovery with a fresh service and no in-memory task state.
	env.svc = newJobRoundTestService(t, env.client, nil)
	for range 2 {
		if err := env.svc.PrepareRecovery(ctx); err != nil {
			t.Fatal(err)
		}
		assertOperationLifecycle(t, env, row.ID, JobStatusPaused)
	}
	if pending, err := env.svc.PendingTaskIDs(ctx, 0, 128); err != nil || len(pending) != 0 {
		t.Fatalf("recovery dispatched paused task: %v %v", pending, err)
	}
	if current := env.client.Job.GetX(ctx, row.ID); current.RetryEpoch != 7 || !current.PauseRequested {
		t.Fatalf("recovery lost pause intent or changed budget: %+v", current)
	}
	candidate := env.client.WorkCandidate.Query().Where(workcandidate.IdentityEQ("retained-candidate")).OnlyX(ctx)
	if candidate.State != "pending_alignment" || candidate.Version != 1 || len(candidate.Payload) == 0 {
		t.Fatal("recovery lost the durable candidate")
	}
}
