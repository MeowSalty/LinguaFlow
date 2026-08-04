package progress

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

// weightedTestClient 创建内存 SQLite ent 客户端并自动迁移。
func weightedTestClient(t *testing.T) *ent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	driver := entsql.OpenDB(dialect.SQLite, db)
	client := ent.NewClient(ent.Driver(driver))
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = db.Close()
	})
	return client
}

// createWeightedFixture 创建 minimal User/Project/Resource/Job/JobResource 层级。
func createWeightedFixture(t *testing.T, client *ent.Client) (jobID, jobResourceID int) {
	t.Helper()
	ctx := context.Background()

	user, err := client.User.Create().
		SetUsername("weighted-user").
		SetPasswordHash("$2a$10$dummyhash").
		SetEmail("weighted@test.com").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	project, err := client.Project.Create().
		SetName("weighted-proj").
		SetSourceLang("en").
		SetTargetLang("zh").
		SetOwnerUserID(user.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

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
		SetResourceCount(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	jr, err := client.JobResource.Create().
		SetStatus("running").
		SetSegmentCount(5).
		SetJob(job).
		SetResource(res).
		Save(ctx)
	if err != nil {
		t.Fatalf("create job_resource: %v", err)
	}

	return job.ID, jr.ID
}

// TestDBReporter_MultiRound_NoJobAccumulation 验证跨多轮进度隔离:
// DBReporter 只累加 JobResource 与 Job 的 weighted_* 字段,
// 绝不写入 Job.completed_segments(该字段仅由 ReconcileJob 聚合)。
func TestDBReporter_MultiRound_NoJobAccumulation(t *testing.T) {
	client := weightedTestClient(t)
	ctx := context.Background()

	jobID, jrID := createWeightedFixture(t, client)

	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second, // 长间隔避免 ticker 干扰
	})
	defer func() {
		_ = r.Close()
	}()

	// Round 1: translate
	r.StageStart("translate", 5)
	for i := 0; i < 5; i++ {
		r.SegmentDone()
	}
	r.StageDone()

	// Round 2: adjudicate
	r.StageStart("adjudicate", 5)
	for i := 0; i < 5; i++ {
		r.SegmentDone()
	}
	r.StageDone()

	// Close 做最终 flush
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	job, err := client.Job.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.CompletedSegments != 0 {
		t.Errorf("Job.CompletedSegments = %d, want 0 (DBReporter must not increment it)", job.CompletedSegments)
	}
	if job.WeightedTotal != 10 {
		t.Errorf("Job.WeightedTotal = %d, want 10", job.WeightedTotal)
	}
	if job.WeightedCompleted != 10 {
		t.Errorf("Job.WeightedCompleted = %d, want 10", job.WeightedCompleted)
	}

	jr, err := client.JobResource.Get(ctx, jrID)
	if err != nil {
		t.Fatalf("reload job_resource: %v", err)
	}
	if jr.StageCompleted != 5 {
		t.Errorf("JobResource.StageCompleted = %d, want 5 (last round only)", jr.StageCompleted)
	}
	if jr.StageTotal != 5 {
		t.Errorf("JobResource.StageTotal = %d, want 5", jr.StageTotal)
	}
	if jr.WeightedTotal != 10 {
		t.Errorf("JobResource.WeightedTotal = %d, want 10", jr.WeightedTotal)
	}
	if jr.WeightedCompleted != 10 {
		t.Errorf("JobResource.WeightedCompleted = %d, want 10", jr.WeightedCompleted)
	}
}
