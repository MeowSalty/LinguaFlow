package service

import (
	"context"
	"testing"
)

func TestReconcileJob_WeightedAggregation(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	user := createTestUser(t, client, "reconcile-wt")
	project := createTestProject(t, client, "reconcile-wt-proj", user.ID)

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
		SetStatus("running").
		SetResourceCount(2).
		Save(ctx)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// JR1: WeightedTotal=10, WeightedCompleted=10, CompletedSegments=5
	_, err = client.JobResource.Create().
		SetStatus(JobResourceStatusCompleted).
		SetSegmentCount(5).
		SetCompletedSegments(5).
		SetWeightedTotal(10).
		SetWeightedCompleted(10).
		SetJob(job).
		SetResource(res).
		Save(ctx)
	if err != nil {
		t.Fatalf("create jr1: %v", err)
	}

	// JR2: WeightedTotal=6, WeightedCompleted=3, CompletedSegments=3
	_, err = client.JobResource.Create().
		SetStatus(JobResourceStatusCompleted).
		SetSegmentCount(3).
		SetCompletedSegments(3).
		SetWeightedTotal(6).
		SetWeightedCompleted(3).
		SetJob(job).
		SetResource(res).
		Save(ctx)
	if err != nil {
		t.Fatalf("create jr2: %v", err)
	}

	svc := &JobService{client: client}
	if err := svc.ReconcileJob(ctx, job.ID); err != nil {
		t.Fatalf("ReconcileJob: %v", err)
	}

	after, err := client.Job.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if after.WeightedTotal != 16 {
		t.Errorf("Job.WeightedTotal = %d, want 16", after.WeightedTotal)
	}
	if after.WeightedCompleted != 13 {
		t.Errorf("Job.WeightedCompleted = %d, want 13", after.WeightedCompleted)
	}
	if after.CompletedSegments != 8 {
		t.Errorf("Job.CompletedSegments = %d, want 8", after.CompletedSegments)
	}
}
