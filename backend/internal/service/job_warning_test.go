package service

import (
	"context"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
)

func TestMarkJobResourceCompleted_SetsWarningAndEvents(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	broker := event.NewBroker(nil)
	svc := &JobService{client: client, broker: broker}

	user := createTestUser(t, client, "warn-user")
	project := createTestProject(t, client, "warn-proj", user.ID)
	res, err := client.Resource.Create().
		SetProjectID(project.ID).
		SetPath("a.txt").
		SetFormat("txt").
		SetStoragePath("storage/a.txt").
		Save(ctx)
	if err != nil {
		t.Fatalf("create resource: %v", err)
	}
	job, err := client.Job.Create().
		SetProjectID(project.ID).
		SetExecutionPlanID(1).
		SetStatus(JobStatusRunning).
		SetResourceCount(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	jr, err := client.JobResource.Create().
		SetStatus(JobResourceStatusRunning).
		SetSegmentCount(2).
		SetJob(job).
		SetResource(res).
		Save(ctx)
	if err != nil {
		t.Fatalf("create job_resource: %v", err)
	}

	ch := broker.Subscribe(job.ID)
	defer broker.Unsubscribe(job.ID, ch)

	warning := "语义质检未完全成功：1 个批次、2 个段落扫描失败"
	if err := svc.MarkJobResourceCompleted(ctx, job.ID, jr.ID, "", 2, 0, warning); err != nil {
		t.Fatalf("MarkJobResourceCompleted: %v", err)
	}

	after, err := client.JobResource.Get(ctx, jr.ID)
	if err != nil {
		t.Fatalf("reload job_resource: %v", err)
	}
	if after.Status != JobResourceStatusCompleted {
		t.Fatalf("status=%q want completed", after.Status)
	}
	if after.WarningMessage == nil || *after.WarningMessage != warning {
		t.Fatalf("warning_message=%v want %q", after.WarningMessage, warning)
	}

	gotTypes := drainEventTypes(t, ch, 2)
	if !containsStr(gotTypes, "resource_completed") || !containsStr(gotTypes, "resource_warning") {
		t.Fatalf("events=%v want resource_completed + resource_warning", gotTypes)
	}

	if err := svc.ReconcileJob(ctx, job.ID); err != nil {
		t.Fatalf("ReconcileJob: %v", err)
	}
	gotTypes = drainEventTypes(t, ch, 2)
	if !containsStr(gotTypes, "job_completed") || !containsStr(gotTypes, "job_warning") {
		t.Fatalf("events=%v want job_completed + job_warning", gotTypes)
	}

	if err := svc.MarkJobResourceCompleted(ctx, job.ID, jr.ID, "", 2, 0, ""); err != nil {
		t.Fatalf("clear warning: %v", err)
	}
	after, err = client.JobResource.Get(ctx, jr.ID)
	if err != nil {
		t.Fatalf("reload after clear: %v", err)
	}
	if after.WarningMessage != nil {
		t.Fatalf("warning_message should be cleared, got %v", *after.WarningMessage)
	}
}

func drainEventTypes(t *testing.T, ch <-chan event.Event, n int) []string {
	t.Helper()
	var types []string
	deadline := time.After(2 * time.Second)
	for len(types) < n {
		select {
		case evt := <-ch:
			types = append(types, evt.Type)
		case <-deadline:
			return types
		}
	}
	return types
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
