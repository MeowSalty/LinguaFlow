package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
)

// 本文件是轮次断点关系化重构的跨层端到端回归：service 层重置语义
// （RetryJob / RecoverPendingJobs / ResumeJob）与 progress.DBReporter
// 重跑语义（StageStart 基线锚定 join 表基数）在真实 ent 内存库上闭环，
// 验证重构的目标不变式：
//
//  1. 重置只翻状态：join 断点行、segment_total、segment_completed 全保留；
//  2. 重跑只处理「扫描 − 断点」段：baseline(join 基数) + 剩余 = segment_total；
//  3. Job.progress_completed ≤ progress_total（不超计）；
//  4. checkpoint 不变式：segment_completed ≡ 该轮 join 表基数。

// ---- e2e 辅助 ----

// e2eEnv 端到端一致性测试环境：内存库 + 属主用户 + 项目 + Job/Resource 服务。
type e2eEnv struct {
	client  *ent.Client
	jobs    *JobService
	res     *ResourceService
	user    *ent.User
	project *ent.Project
}

func newE2EEnv(t *testing.T) *e2eEnv {
	t.Helper()
	client := testClient(t)
	user := createTestUser(t, client, "e2e-user")
	project := createTestProject(t, client, "e2e-project", user.ID)
	projects := NewProjectService(client, NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client))))
	// 真实 fileStore（临时目录）：DeleteResource 事务提交后会删除存储文件，
	// 测试资源 storage 路径不存在时 os.Remove 返回 IsNotExist 被吞掉。
	fs, err := filestore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("new filestore: %v", err)
	}
	return &e2eEnv{
		client:  client,
		jobs:    newJobRoundTestService(client, nil),
		res:     &ResourceService{client: client, projects: projects, fileStore: fs},
		user:    user,
		project: project,
	}
}

// e2eResource 单资源种子（含资源与 JobResource 行）。
type e2eResource struct {
	res *ent.Resource
	jr  *ent.JobResource
}

// seedE2EJob 构造 Job + 资源矩阵，进度缓存预置为 total/completed。
func seedE2EJob(t *testing.T, env *e2eEnv, jobStatus string, progressTotal, progressCompleted int64, specs []jobResourceSpec) (*ent.Job, []*e2eResource) {
	t.Helper()
	ctx := context.Background()
	job, err := env.client.Job.Create().
		SetProjectID(env.project.ID).
		SetExecutionPlanID(1).
		SetStatus(jobStatus).
		SetResourceCount(len(specs)).
		SetProgressTotal(progressTotal).
		SetProgressCompleted(progressCompleted).
		Save(ctx)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	ers := make([]*e2eResource, 0, len(specs))
	for i, spec := range specs {
		res := createTestResource(t, env.client, env.project.ID, fmt.Sprintf("e2e-seed-%d.txt", i))
		jr, err := env.client.JobResource.Create().
			SetStatus(spec.status).
			SetSegmentCount(spec.segmentCount).
			SetJob(job).
			SetResource(res).
			Save(ctx)
		if err != nil {
			t.Fatalf("create jr[%d]: %v", i, err)
		}
		ers = append(ers, &e2eResource{res: res, jr: jr})
	}
	return job, ers
}

// seedE2ERound 为资源预建一行 JobRound 与全量真实 Segment（docIndex i ↔ segIDs[i]），
// 并把前 resolvedRows 段写入 join 断点行（模拟 worker 失败/崩溃前 flush 的
// 一致态：扫描全集 T 段，其中 M 段已解决落断点）。
// 返回轮次行 ID 与全部段 ID 列表（重跑 mapper 需要「扫描 − 断点」的完整映射）。
func seedE2ERound(t *testing.T, env *e2eEnv, jobID int, er *e2eResource, roundIndex int, mode, status string, total, completed, resolvedRows int) (roundRowID int, segIDs []int) {
	t.Helper()
	ctx := context.Background()
	row, err := env.client.JobRound.Create().
		SetJobID(jobID).
		SetJobResourceID(er.jr.ID).
		SetRoundIndex(roundIndex).
		SetMode(mode).
		SetStatus(status).
		SetSegmentTotal(total).
		SetSegmentCompleted(completed).
		Save(ctx)
	if err != nil {
		t.Fatalf("create round row: %v", err)
	}
	if resolvedRows > total {
		t.Fatalf("resolvedRows %d > total %d（断点数不得超过扫描全集）", resolvedRows, total)
	}
	ids := make([]int, 0, total)
	for i := 0; i < total; i++ {
		seg, err := env.client.Segment.Create().
			SetResourceID(er.res.ID).
			SetSegmentIndex(i).
			SetSourceText(fmt.Sprintf("e2e-r%d-seg-%d", roundIndex, i)).
			Save(ctx)
		if err != nil {
			t.Fatalf("create segment: %v", err)
		}
		if i < resolvedRows {
			if _, err := env.client.JobRoundSegment.Create().
				SetJobRoundID(row.ID).
				SetSegmentID(seg.ID).
				Save(ctx); err != nil {
				t.Fatalf("create join row: %v", err)
			}
		}
		ids = append(ids, seg.ID)
	}
	return row.ID, ids
}

// rerunRoundWithReporter 模拟 worker 重跑一轮的完整 progress 写入序列：
// NewDBReporter → SwitchRound(rowID, mapper) → StageStart
// → SegmentDone×剩余 + SegmentResolved×剩余 → BatchComplete → Close。
// mapper 按段 ID 下标映射（docIndex i → segIDs[i]，越界拒绝——重跑基线
// 之后的段索引从 baseline 起步，与生产 executor 的 resolved 子集推导一致）。
// 断言最终 segment_completed == segment_total 且 Job.progress_completed ≤ progress_total。
func rerunRoundWithReporter(t *testing.T, env *e2eEnv, jobID, jrID, roundRowID int, segIDs []int, segmentTotal int) {
	t.Helper()
	ctx := context.Background()

	r := progress.NewDBReporter(progress.DBReporterOptions{
		Client:        env.client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        time.Hour, // 长间隔规避 ticker 干扰；flush 由 BatchComplete/Close 显式驱动
	})
	r.SwitchRound(roundRowID, func(docIndex int) (int, bool) {
		if docIndex < 0 || docIndex >= len(segIDs) {
			return 0, false
		}
		return segIDs[docIndex], true
	})
	r.StageStart("adjudicate", segmentTotal)

	// 重跑只处理「扫描 − 断点」段：剩余段 = segmentTotal − join 基数。
	joinBase := countRoundJoinRows(t, env.client, roundRowID)
	// 段索引与 segIDs 对齐：断点行引用 segIDs[0:joinBase]，剩余段从 joinBase 起。
	for i := joinBase; i < segmentTotal; i++ {
		r.SegmentDone()
		r.SegmentResolved(i)
	}
	r.BatchComplete()
	if err := r.Close(); err != nil {
		t.Fatalf("reporter close: %v", err)
	}

	// checkpoint 不变式（终态）：segment_completed == segment_total。
	row, err := env.client.JobRound.Get(ctx, roundRowID)
	if err != nil {
		t.Fatalf("reload round: %v", err)
	}
	if row.SegmentTotal != segmentTotal {
		t.Errorf("round %d segment_total = %d, want %d（重跑不得重置分母）", row.ID, row.SegmentTotal, segmentTotal)
	}
	if row.SegmentCompleted != segmentTotal {
		t.Errorf("round %d segment_completed = %d, want %d（重跑后应精确完成）", row.ID, row.SegmentCompleted, segmentTotal)
	}
	// 不变式本体：计数 ≡ join 基数。
	if got := countRoundJoinRows(t, env.client, roundRowID); got != row.SegmentCompleted {
		t.Errorf("round %d join 行 = %d, want segment_completed = %d", row.ID, got, row.SegmentCompleted)
	}
	// 不超计：Job.progress_completed ≤ progress_total。
	jobRow, err := env.client.Job.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if jobRow.ProgressCompleted > jobRow.ProgressTotal {
		t.Errorf("job progress_completed = %d > progress_total = %d（超计回归）", jobRow.ProgressCompleted, jobRow.ProgressTotal)
	}
	if jobRow.ProgressCompleted < jobRow.ProgressTotal {
		t.Errorf("job progress_completed = %d < progress_total = %d（重跑后应精确补齐）", jobRow.ProgressCompleted, jobRow.ProgressTotal)
	}
}

// ---- 1. RetryJob：failed 轮重置 → 重跑不超计 ----

// TestRetryJob_FailedRound_CheckpointConsistent 覆盖失败重试的端到端一致性：
// 非翻译轮 failed，join M 行 + segment_completed=M（失败前 flush 一致态）。
// RetryJob 后：轮次行重置为 pending、join 行全保留、segment_completed 保留
// == join 基数、segment_total 保留（分母不重置）。再模拟重跑完成后
// segment_completed == segment_total 精确，且 Job.progress_completed 不超计。
func TestRetryJob_FailedRound_CheckpointConsistent(t *testing.T) {
	env := newE2EEnv(t)
	ctx := context.Background()

	const (
		segmentTotal = 10
		checkpointM  = 6 // 失败前已 flush 的一致断点数
	)
	job, ers := seedE2EJob(t, env, JobStatusFailed, segmentTotal, checkpointM, []jobResourceSpec{
		{status: JobResourceStatusFailed, segmentCount: segmentTotal},
	})
	er := ers[0]
	roundID, segIDs := seedE2ERound(t, env, job.ID, er, 0, "adjudicate", JobRoundStatusFailed, segmentTotal, checkpointM, checkpointM)

	// 前置一致态：segment_completed ≡ join 基数。
	if got := countRoundJoinRows(t, env.client, roundID); got != checkpointM {
		t.Fatalf("seed join rows = %d, want %d", got, checkpointM)
	}

	got, err := env.jobs.RetryJob(ctx, env.user.ID, job.ID)
	if err != nil {
		t.Fatalf("RetryJob: %v", err)
	}
	if got.Status != JobStatusPending {
		t.Errorf("job status = %q, want %q", got.Status, JobStatusPending)
	}

	// 重置只翻状态：轮次 pending、资源 pending。
	roundAfter, err := env.client.JobRound.Get(ctx, roundID)
	if err != nil {
		t.Fatalf("reload round: %v", err)
	}
	assertRoundCheckpoint(t, env.client, roundAfter, JobRoundStatusPending, segmentTotal, checkpointM, checkpointM)
	jrAfter, err := env.client.JobResource.Get(ctx, er.jr.ID)
	if err != nil {
		t.Fatalf("reload jr: %v", err)
	}
	if jrAfter.Status != JobResourceStatusPending {
		t.Errorf("resource status = %q, want %q", jrAfter.Status, JobResourceStatusPending)
	}
	// 任务派生缓存经无条件求和重算：仍为 segment_total/checkpointM。
	assertJobProgress(t, reloadJob(t, env.client, job.ID), segmentTotal, checkpointM)

	// 模拟重跑完成：baseline(join 基数) + 剩余 = segment_total。
	rerunRoundWithReporter(t, env, job.ID, er.jr.ID, roundID, segIDs, segmentTotal)
}

// ---- 2. RecoverPendingJobs：running 轮崩溃 → 恢复重跑不超计 ----

// TestRecoverPendingJobs_RunningRound_CheckpointConsistent 覆盖崩溃恢复的
// 端到端一致性：running 轮（join M + completed M，崩溃点一致态）经
// RecoverPendingJobs 重置为 pending 且断点/计数全保留；重跑后
// segment_completed == segment_total 精确且 Job 不超计。
func TestRecoverPendingJobs_RunningRound_CheckpointConsistent(t *testing.T) {
	env := newE2EEnv(t)
	ctx := context.Background()

	const (
		segmentTotal = 8
		checkpointM  = 3 // 崩溃前已 flush 的一致断点数
	)
	job, ers := seedE2EJob(t, env, JobStatusRunning, segmentTotal, checkpointM, []jobResourceSpec{
		{status: JobResourceStatusRunning, segmentCount: segmentTotal},
	})
	er := ers[0]
	roundID, segIDs := seedE2ERound(t, env, job.ID, er, 0, "adjudicate", JobRoundStatusRunning, segmentTotal, checkpointM, checkpointM)

	ids, err := env.jobs.RecoverPendingJobs(ctx)
	if err != nil {
		t.Fatalf("RecoverPendingJobs: %v", err)
	}
	found := false
	for _, id := range ids {
		if id == job.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("recovered ids = %v, want contains %d", ids, job.ID)
	}

	// 轮次行 running → pending，断点/计数全保留。
	roundAfter, err := env.client.JobRound.Get(ctx, roundID)
	if err != nil {
		t.Fatalf("reload round: %v", err)
	}
	assertRoundCheckpoint(t, env.client, roundAfter, JobRoundStatusPending, segmentTotal, checkpointM, checkpointM)
	// 任务 running → pending，派生缓存重算后仍为 segmentTotal/checkpointM。
	assertJobProgress(t, reloadJob(t, env.client, job.ID), segmentTotal, checkpointM)

	// 重跑完成：不超计且精确补齐。
	rerunRoundWithReporter(t, env, job.ID, er.jr.ID, roundID, segIDs, segmentTotal)
}

// ---- 3. ResumeJob：paused 任务恢复 → 重跑不超计 ----

// TestResumeJob_Then_RerunNoOvercount 覆盖暂停恢复的端到端一致性：
// paused 任务（资源 running、轮 running、join M + completed M）经
// ResumeJob 重置为 pending 且计数/断点保留；重跑后 segment_completed
// == segment_total 精确，Job.progress_completed == progress_total。
func TestResumeJob_Then_RerunNoOvercount(t *testing.T) {
	env := newE2EEnv(t)
	ctx := context.Background()

	const (
		segmentTotal = 12
		checkpointM  = 5
	)
	job, ers := seedE2EJob(t, env, JobStatusPaused, segmentTotal, checkpointM, []jobResourceSpec{
		{status: JobResourceStatusRunning, segmentCount: segmentTotal},
	})
	er := ers[0]
	roundID, segIDs := seedE2ERound(t, env, job.ID, er, 0, "adjudicate", JobRoundStatusRunning, segmentTotal, checkpointM, checkpointM)

	got, err := env.jobs.ResumeJob(ctx, env.user.ID, job.ID)
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	if got.Status != JobStatusPending {
		t.Errorf("job status = %q, want %q", got.Status, JobStatusPending)
	}

	// 轮次 pending、计数/断点保留。
	roundAfter, err := env.client.JobRound.Get(ctx, roundID)
	if err != nil {
		t.Fatalf("reload round: %v", err)
	}
	assertRoundCheckpoint(t, env.client, roundAfter, JobRoundStatusPending, segmentTotal, checkpointM, checkpointM)
	assertJobProgress(t, reloadJob(t, env.client, job.ID), segmentTotal, checkpointM)

	// 重跑后精确 12/12，Job 不超计。
	rerunRoundWithReporter(t, env, job.ID, er.jr.ID, roundID, segIDs, segmentTotal)
}

// ---- 4. skipped 补齐轮回退路径（P1-1 回归） ----

// TestMarkJobRoundSkipped_ResetRerun_NoProgressRegression 锁死 skipped 补齐轮
// 重置重跑的计数回退路径：旧实现把终态闭合写进 segment_completed（补齐到
// total），skipped 是唯一会被重置的终态——重跑时 StageStart 锚定断点集合基数
// 做基线、flush 按集合基数绝对值写列，补齐值（非断点派生）会让计数从 total
// 回退、并使 Job 缓存相对矩阵求和超计。新口径下闭合只发生在读侧，全程：
// segment_completed 单调不回退、join 行数 ≡ segment_completed、Job 缓存 ≡
// sumJobRoundProgress 求和、≤ progress_total。
func TestMarkJobRoundSkipped_ResetRerun_NoProgressRegression(t *testing.T) {
	env := newE2EEnv(t)
	ctx := context.Background()

	const (
		segmentTotal   = 100
		checkpointM    = 60 // 首轮崩溃前已 flush 的一致断点数
		rerunNewlyDone = 30 // 重跑只完成一部分（部分重跑）
	)
	job, ers := seedE2EJob(t, env, JobStatusRunning, segmentTotal, checkpointM, []jobResourceSpec{
		{status: JobResourceStatusRunning, segmentCount: segmentTotal},
	})
	er := ers[0]
	roundID, segIDs := seedE2ERound(t, env, job.ID, er, 0, "adjudicate", JobRoundStatusRunning, segmentTotal, checkpointM, checkpointM)

	// 1. skipped 闭合：轮行保持 (skipped, 100, 60, join=60)，闭合只动 Job 缓存
	//    （60 → 100 按闭合口径取 total）。
	if err := env.jobs.MarkJobRoundSkipped(ctx, roundID); err != nil {
		t.Fatalf("MarkJobRoundSkipped: %v", err)
	}
	assertRoundCheckpoint(t, env.client, env.client.JobRound.GetX(ctx, roundID), JobRoundStatusSkipped, segmentTotal, checkpointM, checkpointM)
	assertJobProgress(t, reloadJob(t, env.client, job.ID), segmentTotal, segmentTotal)

	// 2. 真实重置路径：RecoverPendingJobs 把 failed|running|skipped → pending
	//    （skipped 属重置集），断点字段全保留，Job 缓存经矩阵求和回到 100/60。
	if _, err := env.jobs.RecoverPendingJobs(ctx); err != nil {
		t.Fatalf("RecoverPendingJobs: %v", err)
	}
	assertRoundCheckpoint(t, env.client, env.client.JobRound.GetX(ctx, roundID), JobRoundStatusPending, segmentTotal, checkpointM, checkpointM)
	assertJobProgress(t, reloadJob(t, env.client, job.ID), segmentTotal, checkpointM)

	// 3. 部分重跑（真实 DBReporter）：StageStart 锚定断点基数 60，仅对 30 个
	//    未落断点的段 SegmentDone + SegmentResolved，BatchComplete + Close 落库。
	r := progress.NewDBReporter(progress.DBReporterOptions{
		Client:        env.client,
		JobID:         job.ID,
		JobResourceID: er.jr.ID,
		Ticker:        time.Hour, // 长间隔规避 ticker 干扰；flush 由 BatchComplete/Close 显式驱动
	})
	r.SwitchRound(roundID, func(docIndex int) (int, bool) {
		if docIndex < 0 || docIndex >= len(segIDs) {
			return 0, false
		}
		return segIDs[docIndex], true
	})
	r.StageStart("adjudicate", segmentTotal)
	for i := checkpointM; i < checkpointM+rerunNewlyDone; i++ {
		r.SegmentDone()
		r.SegmentResolved(i)
	}
	r.BatchComplete()
	if err := r.Close(); err != nil {
		t.Fatalf("reporter close: %v", err)
	}

	// 4. 断言不回退：计数列 ≥ 首轮基线 60 且精确到 90（60 断点 + 30 新解决）；
	//    join 行数 == segment_completed；Job 缓存 == 90 == sumJobRoundProgress
	//    口径（pending 轮取实际 segment_completed）；≤ progress_total。
	round := env.client.JobRound.GetX(ctx, roundID)
	if round.SegmentCompleted < checkpointM {
		t.Errorf("segment_completed = %d, want ≥ %d（基线断点不得因重置重跑回退）", round.SegmentCompleted, checkpointM)
	}
	wantCompleted := checkpointM + rerunNewlyDone
	if round.SegmentCompleted != wantCompleted {
		t.Errorf("segment_completed = %d, want %d（基线 %d + 新解决 %d）", round.SegmentCompleted, wantCompleted, checkpointM, rerunNewlyDone)
	}
	if got := countRoundJoinRows(t, env.client, roundID); got != round.SegmentCompleted {
		t.Errorf("join 行数 = %d, want == segment_completed = %d", got, round.SegmentCompleted)
	}
	assertJobProgress(t, reloadJob(t, env.client, job.ID), segmentTotal, int64(wantCompleted))
	if wantCompleted > segmentTotal {
		t.Errorf("Job progress_completed = %d > progress_total = %d（超计回归）", wantCompleted, segmentTotal)
	}

	// 5. 最终 completed 闭合：Job 回到 100/100，轮行计数列仍是 90（闭合不改写）。
	if err := env.jobs.MarkJobRoundRunning(ctx, job.ID, roundID); err != nil {
		t.Fatalf("MarkJobRoundRunning: %v", err)
	}
	if err := env.jobs.MarkJobRoundCompleted(ctx, roundID); err != nil {
		t.Fatalf("MarkJobRoundCompleted: %v", err)
	}
	assertRoundCheckpoint(t, env.client, env.client.JobRound.GetX(ctx, roundID), JobRoundStatusCompleted, segmentTotal, wantCompleted, wantCompleted)
	assertJobProgress(t, reloadJob(t, env.client, job.ID), segmentTotal, segmentTotal)
}

// ---- 5. 级联删除：join 行随资源删除链清理 ----

// TestJobRoundSegment_CascadeDeleteWithResource 覆盖 join 表的级联清理：
// DeleteResource 手动级联先删 Segment 行，job_round_segments 对 Segment 的
// FK 为 Cascade——删除后 join 行必须被 DB 自动清理（Count==0），而非约束失败。
func TestJobRoundSegment_CascadeDeleteWithResource(t *testing.T) {
	env := newE2EEnv(t)
	ctx := context.Background()

	job, ers := seedE2EJob(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{
		{status: JobResourceStatusRunning, segmentCount: 4},
	})
	er := ers[0]
	roundID, _ := seedE2ERound(t, env, job.ID, er, 0, "adjudicate", JobRoundStatusRunning, 4, 4, 4)
	if got := countRoundJoinRows(t, env.client, roundID); got != 4 {
		t.Fatalf("seed join rows = %d, want 4", got)
	}

	if err := env.res.DeleteResource(ctx, env.user.ID, env.project.ID, er.res.ID); err != nil {
		t.Fatalf("DeleteResource: %v", err)
	}

	// join 行被级联清理（Segment FK Cascade），不得残留。
	if got := countRoundJoinRows(t, env.client, roundID); got != 0 {
		t.Errorf("join rows after DeleteResource = %d, want 0（Segment 删除链级联清理）", got)
	}
	// 资源自身的 JobResource 与其轮次行也被手动级联删除。
	n, err := env.client.JobResource.Query().Where(jobresource.IDEQ(er.jr.ID)).Count(ctx)
	if err != nil {
		t.Fatalf("count jr: %v", err)
	}
	if n != 0 {
		t.Errorf("JobResource rows after delete = %d, want 0", n)
	}
	n, err = env.client.JobRound.Query().Where(jobround.JobResourceIDEQ(er.jr.ID)).Count(ctx)
	if err != nil {
		t.Fatalf("count rounds: %v", err)
	}
	if n != 0 {
		t.Errorf("JobRound rows after delete = %d, want 0", n)
	}
}

// ---- 6. 唯一索引：同 (round, segment) 重复插入被拒绝 ----

// TestJobRoundSegment_UniqueIndexRejectsDuplicate 断言 (job_round_id, segment_id)
// 唯一索引真实生效：同一轮对同一段重复登记断点必须报错（写入方的预查去重
// 之上，唯一索引兜底并发/重跑边界的残余竞态）。
func TestJobRoundSegment_UniqueIndexRejectsDuplicate(t *testing.T) {
	env := newE2EEnv(t)
	ctx := context.Background()

	job, ers := seedE2EJob(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{
		{status: JobResourceStatusRunning, segmentCount: 1},
	})
	er := ers[0]
	roundID, segIDs := seedE2ERound(t, env, job.ID, er, 0, "adjudicate", JobRoundStatusRunning, 1, 1, 1)
	if len(segIDs) != 1 {
		t.Fatalf("seed segments = %d, want 1", len(segIDs))
	}

	// 同 (round, segment) 二次插入必须被唯一索引拒绝。
	_, err := env.client.JobRoundSegment.Create().
		SetJobRoundID(roundID).
		SetSegmentID(segIDs[0]).
		Save(ctx)
	if err == nil {
		t.Fatal("duplicate (job_round_id, segment_id) insert 应报错（唯一索引），got nil")
	}

	// 拒绝后基数不变。
	if got := countRoundJoinRows(t, env.client, roundID); got != 1 {
		t.Errorf("join rows after rejected duplicate = %d, want 1", got)
	}
}

// ---- 7. 跨同模式轮断点并集语义（service 视角回归） ----

// TestJobRoundSegment_SameModeUnionPreserved 覆盖恢复语义对跨同模式轮
// 并集的支撑：completed 前轮的断点行在重置/重跑流程中不被清理，
// 新一轮行独立追加（loadResolved 的并集语义因此成立）。
func TestJobRoundSegment_SameModeUnionPreserved(t *testing.T) {
	env := newE2EEnv(t)
	ctx := context.Background()

	const segmentTotal = 6
	job, ers := seedE2EJob(t, env, JobStatusFailed, segmentTotal, segmentTotal, []jobResourceSpec{
		{status: JobResourceStatusFailed, segmentCount: segmentTotal},
	})
	er := ers[0]
	// r0 completed（全量断点）、r1 failed（无断点——模拟崩溃窗口）。
	r0ID, r0Segs := seedE2ERound(t, env, job.ID, er, 0, "adjudicate", JobRoundStatusCompleted, segmentTotal, segmentTotal, segmentTotal)
	r1ID, _ := seedE2ERound(t, env, job.ID, er, 1, "adjudicate", JobRoundStatusFailed, 0, 0, 0)
	_ = r0Segs // 并集断言经 roundJoinSegmentIDs 重读，种子返回值不直接参与

	if _, err := env.jobs.RetryJob(ctx, env.user.ID, job.ID); err != nil {
		t.Fatalf("RetryJob: %v", err)
	}

	// completed 轮终态不动（含断点行）；failed 轮 → pending 保留 0 断点。
	r0After, err := env.client.JobRound.Get(ctx, r0ID)
	if err != nil {
		t.Fatalf("reload r0: %v", err)
	}
	assertRoundCheckpoint(t, env.client, r0After, JobRoundStatusCompleted, segmentTotal, segmentTotal, segmentTotal)
	r1After, err := env.client.JobRound.Get(ctx, r1ID)
	if err != nil {
		t.Fatalf("reload r1: %v", err)
	}
	assertRoundCheckpoint(t, env.client, r1After, JobRoundStatusPending, 0, 0, 0)

	// 两轮断点并集：r0 全量 ∪ r1 空 = r0 全量（同模式重跑据此跳过已解决段）。
	union := roundJoinSegmentIDs(t, env.client, r0ID)
	if len(union) != segmentTotal {
		t.Errorf("r0 断点集 = %d 行, want %d（重试不得清 completed 轮断点）", len(union), segmentTotal)
	}
	// 与 r1 之间无串扰。
	if got := countRoundJoinRows(t, env.client, r1ID); got != 0 {
		t.Errorf("r1 断点集 = %d 行, want 0", got)
	}
}
