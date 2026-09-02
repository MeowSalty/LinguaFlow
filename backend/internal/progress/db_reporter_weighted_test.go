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
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
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

// progressFixture 创建 minimal User/Project/Resource/Job/JobResource 层级。
func progressFixture(t *testing.T, client *ent.Client) (jobID, jobResourceID int) {
	t.Helper()
	ctx := context.Background()

	user, err := client.User.Create().
		SetUsername("progress-user").
		SetPasswordHash("$2a$10$dummyhash").
		SetEmail("progress@test.com").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	project, err := client.Project.Create().
		SetName("progress-proj").
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

// createRoundRow 为 (job, jobResource, roundIndex) 预建一行 JobRound。
func createRoundRow(t *testing.T, client *ent.Client, jobID, jrID, roundIndex int, mode string) int {
	t.Helper()
	row, err := client.JobRound.Create().
		SetJobID(jobID).
		SetJobResourceID(jrID).
		SetRoundIndex(roundIndex).
		SetMode(mode).
		SetStatus("running").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create job_round: %v", err)
	}
	return row.ID
}

// TestDBReporter_MultiRound_ProgressAndMatrix 验证新进度模型核心行为：
// 1. progress_total/progress_completed 跨轮累加（段落×轮工作量）；
// 2. JobRound 行写入 segment_total/segment_completed（矩阵即事实源）；
// 3. 每轮 segment_completed 独立计数（第二轮不把第一轮的段算进去）；
// 4. 断点集合在同一 flush 事务持久化到当前行。
func TestDBReporter_MultiRound_ProgressAndMatrix(t *testing.T) {
	client := weightedTestClient(t)
	ctx := context.Background()

	jobID, jrID := progressFixture(t, client)
	round0 := createRoundRow(t, client, jobID, jrID, 0, "translate")
	round1 := createRoundRow(t, client, jobID, jrID, 1, "adjudicate")

	resolved := []int{101, 102}
	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second, // 长间隔避免 ticker 干扰
	})
	r.SwitchRound(round0)
	r.SetResolvedSource(func() []int {
		out := make([]int, len(resolved))
		copy(out, resolved)
		return out
	})

	// Round 1: translate
	r.StageStart("translate", 5)
	for i := 0; i < 5; i++ {
		r.SegmentDone()
	}
	r.BatchComplete() // 立即 flush（含断点持久化）

	// Round 2: adjudicate —— SwitchRound 后计数归零、目标行切换。
	r.SwitchRound(round1)
	resolved = nil // translate 轮后的 adjudicate 断点此时为空
	r.StageStart("adjudicate", 5)
	for i := 0; i < 3; i++ {
		r.SegmentDone()
	}
	r.StageDone() // flush

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	job, err := client.Job.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.ProgressTotal != 10 {
		t.Errorf("Job.ProgressTotal = %d, want 10", job.ProgressTotal)
	}
	if job.ProgressCompleted != 8 {
		t.Errorf("Job.ProgressCompleted = %d, want 8", job.ProgressCompleted)
	}

	r0, err := client.JobRound.Get(ctx, round0)
	if err != nil {
		t.Fatalf("reload round0: %v", err)
	}
	if r0.SegmentTotal != 5 || r0.SegmentCompleted != 5 {
		t.Errorf("round0 = %d/%d, want 5/5", r0.SegmentCompleted, r0.SegmentTotal)
	}
	if len(r0.ResolvedSegmentIds) != 2 || r0.ResolvedSegmentIds[0] != 101 {
		t.Errorf("round0 resolved = %v, want [101 102]", r0.ResolvedSegmentIds)
	}

	r1, err := client.JobRound.Get(ctx, round1)
	if err != nil {
		t.Fatalf("reload round1: %v", err)
	}
	if r1.SegmentTotal != 5 || r1.SegmentCompleted != 3 {
		t.Errorf("round1 = %d/%d, want 3/5", r1.SegmentCompleted, r1.SegmentTotal)
	}
}

// TestDBReporter_NoRoundRow_JobOnly 验证单资源路径（roundRowID=0）退化：
// 仅累加 Job 计数器，不触碰 JobRound 行。
func TestDBReporter_NoRoundRow_JobOnly(t *testing.T) {
	client := weightedTestClient(t)
	ctx := context.Background()

	jobID, jrID := progressFixture(t, client)

	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second,
	})
	r.StageStart("translate", 5)
	for i := 0; i < 4; i++ {
		r.SegmentDone()
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	job, err := client.Job.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.ProgressTotal != 5 || job.ProgressCompleted != 4 {
		t.Errorf("job progress = %d/%d, want 4/5", job.ProgressCompleted, job.ProgressTotal)
	}
	// 无 JobRound 行被创建。
	count, err := client.JobRound.Query().Where(jobround.JobResourceIDEQ(jrID)).Count(ctx)
	if err != nil {
		t.Fatalf("count rounds: %v", err)
	}
	if count != 0 {
		t.Errorf("JobRound rows = %d, want 0", count)
	}
}

// TestDBReporter_ResolvedDirtyOnce 断点脏标记行为：轮内 resolved 集合恒定
// （AccumulateResolved 仅轮末执行），首次 flush 搭载写入后不再重复重写；
// PersistResolved 无条件强制写入；SetResolvedSource 重置脏标记后重新搭载。
// 用外部覆写断点列作探针——若实现退化为每次 flush 重写，探针会被冲掉。
func TestDBReporter_ResolvedDirtyOnce(t *testing.T) {
	client := weightedTestClient(t)
	ctx := context.Background()

	jobID, jrID := progressFixture(t, client)
	round0 := createRoundRow(t, client, jobID, jrID, 0, "extract")

	probe := func(t *testing.T) []int {
		t.Helper()
		row, err := client.JobRound.Get(ctx, round0)
		if err != nil {
			t.Fatalf("reload round: %v", err)
		}
		return row.ResolvedSegmentIds
	}
	overwrite := func(t *testing.T, ids []int) {
		t.Helper()
		if _, err := client.JobRound.UpdateOneID(round0).SetResolvedSegmentIds(ids).Save(ctx); err != nil {
			t.Fatalf("overwrite resolved: %v", err)
		}
	}

	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second, // 长间隔避免 ticker 干扰
	})
	r.SwitchRound(round0)
	r.SetResolvedSource(func() []int {
		return []int{101, 102}
	})

	// 首次 flush：SetResolvedSource 已置脏，断点随段计数写入。
	r.StageStart("extract", 4)
	r.SegmentDone()
	r.SegmentDone()
	r.BatchComplete()
	if got := probe(t); len(got) != 2 || got[0] != 101 {
		t.Fatalf("首次 flush 后 resolved = %v, want [101 102]", got)
	}

	// 第二次 flush（脏已清）：段计数照写，断点不应重写——探针存活。
	overwrite(t, []int{999})
	r.SegmentDone()
	r.BatchComplete()
	if got := probe(t); len(got) != 1 || got[0] != 999 {
		t.Fatalf("二次 flush 重写了断点 = %v, want 探针 [999] 存活", got)
	}
	row, err := client.JobRound.Get(ctx, round0)
	if err != nil {
		t.Fatalf("reload round: %v", err)
	}
	if row.SegmentCompleted != 3 {
		t.Errorf("SegmentCompleted = %d, want 3（段计数不受脏标记影响）", row.SegmentCompleted)
	}

	// PersistResolved：无条件强制写入（AccumulateResolved 后的兜底路径）。
	r.PersistResolved()
	if got := probe(t); len(got) != 2 || got[0] != 101 {
		t.Fatalf("PersistResolved 后 resolved = %v, want [101 102]", got)
	}

	// SetResolvedSource 重置脏标记：下一次 flush 重新搭载。
	overwrite(t, []int{999})
	r.SetResolvedSource(func() []int { return []int{101, 102} })
	r.SegmentDone()
	r.BatchComplete()
	if got := probe(t); len(got) != 2 || got[0] != 101 {
		t.Fatalf("SetResolvedSource 后 resolved = %v, want [101 102]（应重新搭载）", got)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
