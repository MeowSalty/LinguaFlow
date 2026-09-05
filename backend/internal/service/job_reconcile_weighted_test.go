package service

import (
	"context"
	"testing"
)

// TestReconcileJob_MatrixAggregationAcrossResources 校验 ReconcileJob 的进度聚合
// 改为 JobRound 矩阵求和口径：progress_total = Σ segment_total（无状态过滤）、
// progress_completed = Σ jobRoundProgress(...)（闭合终态 completed/skipped 取
// segment_total，pending/running/failed 取 segment_completed）。
func TestReconcileJob_MatrixAggregationAcrossResources(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	user := createTestUser(t, client, "reconcile-matrix")
	project := createTestProject(t, client, "reconcile-matrix-proj", user.ID)

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

	// JR1（completed）：两轮，(total=10, completed=10) + (total=6, completed=3)
	jr1, err := client.JobResource.Create().
		SetStatus(JobResourceStatusCompleted).
		SetSegmentCount(5).
		SetCompletedSegments(5).
		SetJob(job).
		SetResource(res).
		Save(ctx)
	if err != nil {
		t.Fatalf("create jr1: %v", err)
	}
	rounds1 := []struct{ total, completed int }{{10, 10}, {6, 3}}
	for i, rd := range rounds1 {
		if _, err := client.JobRound.Create().
			SetJob(job).
			SetJobResource(jr1).
			SetRoundIndex(i).
			SetMode("translate").
			SetStatus(JobRoundStatusCompleted).
			SetSegmentTotal(rd.total).
			SetSegmentCompleted(rd.completed).
			Save(ctx); err != nil {
			t.Fatalf("create jr1 round %d: %v", i, err)
		}
	}

	// JR2（completed）：一轮 pending-after-reset 带历史计数 (total=4, completed=0)，
	// 无状态过滤的求和必须把它计入分母（核心不变式）。
	jr2, err := client.JobResource.Create().
		SetStatus(JobResourceStatusCompleted).
		SetSegmentCount(3).
		SetCompletedSegments(3).
		SetJob(job).
		SetResource(res).
		Save(ctx)
	if err != nil {
		t.Fatalf("create jr2: %v", err)
	}
	// 口径钉子：有缺口的 pending 轮取实际 segment_completed（0），不做闭合
	// 补齐——未完成部分交由重置路径续跑。
	if _, err := client.JobRound.Create().
		SetJob(job).
		SetJobResource(jr2).
		SetRoundIndex(0).
		SetMode("translate").
		SetStatus(JobRoundStatusPending).
		SetSegmentTotal(4).
		SetSegmentCompleted(0).
		Save(ctx); err != nil {
		t.Fatalf("create jr2 round 0: %v", err)
	}
	// 口径钉子：有缺口的 skipped 轮闭合取 segment_total（5 而非 2）——skipped
	// 是闭合终态，其已揭示工作量按定义全额计入；segment_completed 列本身
	// 保持断点集合基数（2）不被改写。
	if _, err := client.JobRound.Create().
		SetJob(job).
		SetJobResource(jr2).
		SetRoundIndex(1).
		SetMode("revise").
		SetStatus(JobRoundStatusSkipped).
		SetSegmentTotal(5).
		SetSegmentCompleted(2).
		Save(ctx); err != nil {
		t.Fatalf("create jr2 round 1: %v", err)
	}

	svc := &JobService{client: client}
	if err := svc.ReconcileJob(ctx, job.ID); err != nil {
		t.Fatalf("ReconcileJob: %v", err)
	}

	after, err := client.Job.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	// total = 10 + 6 + 4 + 5 = 25；completed = 10 + 6(闭合) + 0(缺口 pending 取实际值)
	// + 5(闭合 skipped 取满量) = 21。
	if after.ProgressTotal != 25 {
		t.Errorf("Job.ProgressTotal = %d, want 25", after.ProgressTotal)
	}
	if after.ProgressCompleted != 21 {
		t.Errorf("Job.ProgressCompleted = %d, want 21", after.ProgressCompleted)
	}
	if after.Status != JobStatusCompleted {
		t.Errorf("Job.Status = %q, want %q", after.Status, JobStatusCompleted)
	}
}

// TestReconcileJob_PausedStaysPaused 校验 paused 任务在 reconcile 时保持
// paused：不覆盖派生状态、不发终态事件，但刷新矩阵计数器。
func TestReconcileJob_PausedStaysPaused(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	user := createTestUser(t, client, "reconcile-paused")
	project := createTestProject(t, client, "reconcile-paused-proj", user.ID)

	res, err := client.Resource.Create().
		SetProjectID(project.ID).
		SetPath("b.txt").
		SetFormat("txt").
		SetStoragePath("storage/b.txt").
		Save(ctx)
	if err != nil {
		t.Fatalf("create resource: %v", err)
	}

	job, err := client.Job.Create().
		SetProjectID(project.ID).
		SetExecutionPlanID(1).
		SetStatus(JobStatusPaused).
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
		t.Fatalf("create jr: %v", err)
	}
	if _, err := client.JobRound.Create().
		SetJob(job).
		SetJobResource(jr).
		SetRoundIndex(0).
		SetMode("translate").
		SetStatus(JobRoundStatusRunning).
		SetSegmentTotal(7).
		SetSegmentCompleted(4).
		Save(ctx); err != nil {
		t.Fatalf("create jr round 0: %v", err)
	}

	svc := &JobService{client: client}
	if err := svc.ReconcileJob(ctx, job.ID); err != nil {
		t.Fatalf("ReconcileJob: %v", err)
	}

	after, err := client.Job.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if after.Status != JobStatusPaused {
		t.Errorf("Job.Status = %q, want %q (paused 不可被派生状态覆盖)", after.Status, JobStatusPaused)
	}
	if after.ProgressTotal != 7 {
		t.Errorf("Job.ProgressTotal = %d, want 7", after.ProgressTotal)
	}
	if after.ProgressCompleted != 4 {
		t.Errorf("Job.ProgressCompleted = %d, want 4", after.ProgressCompleted)
	}
}
