package service

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
)

// TestJobRound_FKCascadeOnResourceDelete FK 级联回归：DeleteResource 的
// 手动级联先删 JobResource，job_rounds 的 FK 曾为 NoAction——创建过任务的
// 资源/项目删除必然报 FOREIGN KEY constraint failed。级联修复后：
// 删 JobResource 应连带清理其全部轮次行，删 Job 同理（两条 FK 均覆盖）。
func TestJobRound_FKCascadeOnResourceDelete(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "cascade-user")
	project := createTestProject(t, client, "cascade-proj", user.ID)
	res := createTestResource(t, client, project.ID, "cascade.txt")

	job, err := client.Job.Create().
		SetProjectID(project.ID).
		SetExecutionPlanID(1).
		SetStatus("completed").
		SetResourceCount(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	jr, err := client.JobResource.Create().
		SetStatus("completed").
		SetJob(job).
		SetResource(res).
		Save(ctx)
	if err != nil {
		t.Fatalf("create jr: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := client.JobRound.Create().
			SetJobID(job.ID).
			SetJobResourceID(jr.ID).
			SetRoundIndex(i).
			SetMode("translate").
			SetStatus("completed").
			Save(ctx); err != nil {
			t.Fatalf("create round[%d]: %v", i, err)
		}
	}

	// 模拟 DeleteResource 的第一步（手动级联先删 JobResource）：
	// NoAction 时代此处报 FOREIGN KEY constraint failed；级联后成功且行被清。
	if n, err := client.JobResource.Delete().Where(jobresource.IDEQ(jr.ID)).Exec(ctx); err != nil {
		t.Fatalf("删除 JobResource 应级联成功，而非 FK 约束失败: %v", err)
	} else if n != 1 {
		t.Fatalf("deleted = %d, want 1", n)
	}
	if n, err := client.JobRound.Query().Where(jobround.JobResourceIDEQ(jr.ID)).Count(ctx); err != nil {
		t.Fatalf("count rounds: %v", err)
	} else if n != 0 {
		t.Errorf("JobResource 删除后残留 %d 条轮次行, want 0（级联清理）", n)
	}
}

// TestJobRound_FKCascadeOnJobDelete 第二条 FK：直接删 Job（DeleteProject
// 的 Step 2）应级联清理其全部轮次行。
func TestJobRound_FKCascadeOnJobDelete(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "cascade-job-user")
	project := createTestProject(t, client, "cascade-job-proj", user.ID)
	res := createTestResource(t, client, project.ID, "cascade-job.txt")

	job, err := client.Job.Create().
		SetProjectID(project.ID).
		SetExecutionPlanID(1).
		SetStatus("failed").
		SetResourceCount(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	jr, err := client.JobResource.Create().
		SetStatus("failed").
		SetJob(job).
		SetResource(res).
		Save(ctx)
	if err != nil {
		t.Fatalf("create jr: %v", err)
	}
	if _, err := client.JobRound.Create().
		SetJobID(job.ID).
		SetJobResourceID(jr.ID).
		SetRoundIndex(0).
		SetMode("translate").
		SetStatus("failed").
		Save(ctx); err != nil {
		t.Fatalf("create round: %v", err)
	}

	// 模拟 DeleteProject 的真实顺序（Step 1 删 JobResource → Step 2 删 Job）：
	// NoAction 时代任一步都会报 FOREIGN KEY constraint failed；级联后全程成功
	// 且轮次行被清。注：保留 JobResource 直接删 Job 会先触发 job_resources→jobs
	// 这条 NoAction FK 的约束（真实删除链不会出现该顺序）。
	if _, err := client.JobResource.Delete().Where(jobresource.IDEQ(jr.ID)).Exec(ctx); err != nil {
		t.Fatalf("删除 JobResource 应级联成功，而非 FK 约束失败: %v", err)
	}
	if err := client.Job.DeleteOneID(job.ID).Exec(ctx); err != nil {
		t.Fatalf("删除 Job 应级联成功，而非 FK 约束失败: %v", err)
	}
	if n, err := client.JobRound.Query().Where(jobround.JobIDEQ(job.ID)).Count(ctx); err != nil {
		t.Fatalf("count rounds: %v", err)
	} else if n != 0 {
		t.Errorf("Job 删除后残留 %d 条轮次行, want 0（级联清理）", n)
	}
}

// TestMarkJobResourceCompleted_ConvergesStrandedRounds 资源 completed 收尾
// 收敛回归：带 unresolved 的轮次行在 runner 成功分支保持 running（不置
// completed），若其工作被后续同模式轮补齐、资源正常收尾，残留 running 行
// 须由 MarkJobResourceCompleted 兜底终态化——否则矩阵展示与资源终态矛盾，
// 且 RetryJob 的 job 级重置会误翻它。
func TestMarkJobResourceCompleted_ConvergesStrandedRounds(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "converge-user")
	project := createTestProject(t, client, "converge-proj", user.ID)
	projects := NewProjectService(client, NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client))))
	svc := &JobService{client: client, projects: projects}
	job, jrs := seedJobCancelRetry(t, client, project.ID, JobStatusRunning, []string{JobResourceStatusRunning})

	// 种子：stranded running 轮（部分失败的 semantic_qa 轮）+ 正常 completed 轮。
	stranded, err := client.JobRound.Create().
		SetJobID(job.ID).
		SetJobResourceID(jrs[0].ID).
		SetRoundIndex(0).
		SetMode("semantic_qa").
		SetStatus(JobRoundStatusRunning).
		Save(ctx)
	if err != nil {
		t.Fatalf("create stranded round: %v", err)
	}
	completed, err := client.JobRound.Create().
		SetJobID(job.ID).
		SetJobResourceID(jrs[0].ID).
		SetRoundIndex(1).
		SetMode("translate").
		SetStatus(JobRoundStatusCompleted).
		Save(ctx)
	if err != nil {
		t.Fatalf("create completed round: %v", err)
	}

	if err := svc.MarkJobResourceCompleted(ctx, job.ID, jrs[0].ID, "out.txt", 10, 0, ""); err != nil {
		t.Fatalf("MarkJobResourceCompleted: %v", err)
	}
	after, err := client.JobRound.Get(ctx, stranded.ID)
	if err != nil {
		t.Fatalf("get stranded round: %v", err)
	}
	if after.Status != JobRoundStatusCompleted {
		t.Errorf("stranded running 轮收尾后 = %q, want %q（资源 completed 须收敛矩阵）", after.Status, JobRoundStatusCompleted)
	}
	if after.FinishedAt == nil {
		t.Errorf("stranded 轮收敛后应记录 finished_at")
	}
	still, err := client.JobRound.Get(ctx, completed.ID)
	if err != nil {
		t.Fatalf("get completed round: %v", err)
	}
	if still.Status != JobRoundStatusCompleted {
		t.Errorf("completed 轮被误改动 = %q, want %q", still.Status, JobRoundStatusCompleted)
	}
}

// TestRetryJob_ResetsRunningRounds 重试重置范围回归：有未解决段的轮次
// 与取消打断的轮次在成功分支保持 running（不再误置 completed），RetryJob
// 必须把 running 轮重置为 pending 才能在重跑时被重新执行。
func TestRetryJob_ResetsRunningRounds(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "retry-running-user")
	project := createTestProject(t, client, "retry-running-proj", user.ID)
	projects := NewProjectService(client, NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client))))
	svc := &JobService{client: client, projects: projects}
	job, jrs := seedJobCancelRetry(t, client, project.ID, JobStatusFailed, []string{JobResourceStatusFailed})

	// 种子两条轮次行：running（未解决段冻结态）与 completed（正常完成）。
	running, err := client.JobRound.Create().
		SetJobID(job.ID).
		SetJobResourceID(jrs[0].ID).
		SetRoundIndex(0).
		SetMode("translate").
		SetStatus(JobRoundStatusRunning).
		Save(ctx)
	if err != nil {
		t.Fatalf("create running round: %v", err)
	}
	completed, err := client.JobRound.Create().
		SetJobID(job.ID).
		SetJobResourceID(jrs[0].ID).
		SetRoundIndex(1).
		SetMode("translate").
		SetStatus(JobRoundStatusCompleted).
		Save(ctx)
	if err != nil {
		t.Fatalf("create completed round: %v", err)
	}

	if _, err := svc.RetryJob(ctx, user.ID, job.ID); err != nil {
		t.Fatalf("RetryJob: %v", err)
	}
	after, err := client.JobRound.Get(ctx, running.ID)
	if err != nil {
		t.Fatalf("get running round: %v", err)
	}
	if after.Status != JobRoundStatusPending {
		t.Errorf("running 轮重试后 = %q, want %q（须重置才会被重跑）", after.Status, JobRoundStatusPending)
	}
	stillCompleted, err := client.JobRound.Get(ctx, completed.ID)
	if err != nil {
		t.Fatalf("get completed round: %v", err)
	}
	if stillCompleted.Status != JobRoundStatusCompleted {
		t.Errorf("completed 轮重试后 = %q, want %q（断点续传应保持跳过）", stillCompleted.Status, JobRoundStatusCompleted)
	}
}

// TestRetryJob_ResetsSkippedRounds skipped 轮重置回归：skipped 是「当时无段
// 可处理」的时点判断——失败期间用户可经段落编辑 API 把段置回 pending 使其
// 失效，重试必须重置 skipped 轮（空段检查会自然重新判定），否则新 pending 段
// 被本任务静默跳过、永不翻译。
func TestRetryJob_ResetsSkippedRounds(t *testing.T) {	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "retry-skipped-user")
	project := createTestProject(t, client, "retry-skipped-proj", user.ID)
	projects := NewProjectService(client, NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client))))
	svc := &JobService{client: client, projects: projects}
	job, jrs := seedJobCancelRetry(t, client, project.ID, JobStatusFailed, []string{JobResourceStatusFailed})

	skipped, err := client.JobRound.Create().
		SetJobID(job.ID).
		SetJobResourceID(jrs[0].ID).
		SetRoundIndex(0).
		SetMode("translate").
		SetStatus(JobRoundStatusSkipped).
		Save(ctx)
	if err != nil {
		t.Fatalf("create skipped round: %v", err)
	}

	if _, err := svc.RetryJob(ctx, user.ID, job.ID); err != nil {
		t.Fatalf("RetryJob: %v", err)
	}
	after, err := client.JobRound.Get(ctx, skipped.ID)
	if err != nil {
		t.Fatalf("get skipped round: %v", err)
	}
	if after.Status != JobRoundStatusPending {
		t.Errorf("skipped 轮重试后 = %q, want %q（须重置才能拾取新 pending 段）", after.Status, JobRoundStatusPending)
	}
}

// TestBulkCreateJobRounds_Chunking CreateBulk 分片回归：超过单批上限的
// 矩阵行分多批全部插入成功。ent 的 CreateBulk 拼单条多行 INSERT，SQLite
// 绑定变量上限 32766（每行 10 列）约 3300 行封顶，不分片时大任务创建
// 整体失败（"too many SQL variables"）。
func TestBulkCreateJobRounds_Chunking(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "bulk-chunk-user")
	project := createTestProject(t, client, "bulk-chunk-proj", user.ID)
	res := createTestResource(t, client, project.ID, "bulk-chunk.txt")
	job, err := client.Job.Create().
		SetProjectID(project.ID).
		SetExecutionPlanID(1).
		SetStatus(JobStatusPending).
		SetResourceCount(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	jr, err := client.JobResource.Create().
		SetStatus(JobResourceStatusPending).
		SetJob(job).
		SetResource(res).
		Save(ctx)
	if err != nil {
		t.Fatalf("create jr: %v", err)
	}

	// 超过 jobRoundBulkChunk（500）一轮，验证分批全部落库。
	const total = 600
	builders := make([]*ent.JobRoundCreate, 0, total)
	for i := 0; i < total; i++ {
		builders = append(builders, client.JobRound.Create().
			SetJobID(job.ID).
			SetJobResourceID(jr.ID).
			SetRoundIndex(i).
			SetMode("translate").
			SetStatus(JobRoundStatusPending))
	}
	if err := bulkCreateJobRounds(ctx, builders, client.JobRound.CreateBulk); err != nil {
		t.Fatalf("bulkCreateJobRounds: %v", err)
	}
	count, err := client.JobRound.Query().Where(jobround.JobResourceIDEQ(jr.ID)).Count(ctx)
	if err != nil {
		t.Fatalf("count rounds: %v", err)
	}
	if count != total {
		t.Errorf("分片插入后轮次行 = %d, want %d（跨批全部落库）", count, total)
	}
}

// TestRecoverPendingJobs_ResetsFailedRounds failed 轮重置回归：runner 的
// MarkJobRoundFailed 与 MarkJobResourceFailed 是两次独立写，两写间隙崩溃
// 会留下「failed 轮 + running 资源」——恢复时轮次重置必须含 failed（与
// RetryJob 对齐、与 runner 重跑行为一致），否则该轮行永久滞留 failed。
func TestRecoverPendingJobs_ResetsFailedRounds(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recover-failed-user")
	project := createTestProject(t, client, "recover-failed-proj", user.ID)
	res := createTestResource(t, client, project.ID, "recover-failed.txt")
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
		SetJob(job).
		SetResource(res).
		Save(ctx)
	if err != nil {
		t.Fatalf("create jr: %v", err)
	}
	// 模拟崩溃窗口：轮已落 failed、资源留 running。
	failedRound, err := client.JobRound.Create().
		SetJobID(job.ID).
		SetJobResourceID(jr.ID).
		SetRoundIndex(0).
		SetMode("translate").
		SetStatus(JobRoundStatusFailed).
		Save(ctx)
	if err != nil {
		t.Fatalf("create failed round: %v", err)
	}

	svc := &JobService{client: client}
	if _, err := svc.RecoverPendingJobs(ctx); err != nil {
		t.Fatalf("RecoverPendingJobs: %v", err)
	}
	after, err := client.JobRound.Get(ctx, failedRound.ID)
	if err != nil {
		t.Fatalf("get failed round: %v", err)
	}
	if after.Status != JobRoundStatusPending {
		t.Errorf("崩溃窗口的 failed 轮恢复后 = %q, want %q（须重置才能重跑，与 RetryJob 对齐）",
			after.Status, JobRoundStatusPending)
	}
}
