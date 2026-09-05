package progress

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
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

// createSegments 批量创建真实 Segment 行（join 表 FK 强制，断点引用必须
// 真实存在）。返回段 ID 列表，下标即 docIndex。
func createSegments(t *testing.T, client *ent.Client, n int) []int {
	t.Helper()
	ids := make([]int, 0, n)
	for i := 0; i < n; i++ {
		seg, err := client.Segment.Create().
			SetSegmentIndex(i).
			SetSourceText(fmt.Sprintf("seg-%d", i)).
			Save(context.Background())
		if err != nil {
			t.Fatalf("create segment %d: %v", i, err)
		}
		ids = append(ids, seg.ID)
	}
	return ids
}

// identityMapper 构造 docIndex → 段 ID 的映射函数（越界返回 ok=false）。
func identityMapper(ids []int) func(int) (int, bool) {
	return func(docIndex int) (int, bool) {
		if docIndex < 0 || docIndex >= len(ids) {
			return 0, false
		}
		return ids[docIndex], true
	}
}

// countRoundSegments 统计某轮 join 表行数（断点集合基数）。
func countRoundSegments(t *testing.T, client *ent.Client, roundRowID int) int {
	t.Helper()
	n, err := client.JobRoundSegment.Query().
		Where(jobroundsegment.JobRoundIDEQ(roundRowID)).
		Count(context.Background())
	if err != nil {
		t.Fatalf("count round segments: %v", err)
	}
	return n
}

// TestDBReporter_MultiRound_ProgressAndMatrix 验证新进度模型核心行为：
//  1. progress_total/progress_completed 跨轮累加（段落×轮工作量）；
//  2. JobRound 行写入 segment_total/segment_completed（矩阵即事实源）；
//  3. 每轮 segment_completed 独立计数（第二轮不把第一轮的段算进去）；
//  4. checkpoint 不变式：轮行 segment_completed ≡ 该轮 join 表基数
//     （断点行与计数同一 flush 事务推进；translate 轮也登记断点）。
func TestDBReporter_MultiRound_ProgressAndMatrix(t *testing.T) {
	client := weightedTestClient(t)
	ctx := context.Background()

	jobID, jrID := progressFixture(t, client)
	round0 := createRoundRow(t, client, jobID, jrID, 0, "translate")
	round1 := createRoundRow(t, client, jobID, jrID, 1, "adjudicate")
	segIDs := createSegments(t, client, 5)

	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second, // 长间隔避免 ticker 干扰
	})
	r.SwitchRound(round0, identityMapper(segIDs))
	// Round 1: translate —— 与其余模式同构，断点集合即进度事实源。

	// Round 1: translate
	r.StageStart("translate", 5)
	for i := 0; i < 5; i++ {
		r.SegmentDone()
		r.SegmentResolved(i)
	}
	r.BatchComplete() // 立即 flush

	// Round 2: adjudicate —— SwitchRound 后计数归零、目标行切换，
	// 并注入当轮 docIndex→Segment ID 映射。
	r.SwitchRound(round1, identityMapper(segIDs))
	r.StageStart("adjudicate", 5)
	for i := 0; i < 3; i++ {
		r.SegmentDone()
		r.SegmentResolved(i)
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

	r1, err := client.JobRound.Get(ctx, round1)
	if err != nil {
		t.Fatalf("reload round1: %v", err)
	}
	if r1.SegmentTotal != 5 || r1.SegmentCompleted != 3 {
		t.Errorf("round1 = %d/%d, want 3/5", r1.SegmentCompleted, r1.SegmentTotal)
	}

	// checkpoint 不变式：segment_completed ≡ 该轮断点集合基数。
	if got := countRoundSegments(t, client, round0); got != r0.SegmentCompleted {
		t.Errorf("round0 checkpoint rows = %d, want segment_completed = %d", got, r0.SegmentCompleted)
	}
	if got := countRoundSegments(t, client, round1); got != r1.SegmentCompleted {
		t.Errorf("round1 checkpoint rows = %d, want segment_completed = %d", got, r1.SegmentCompleted)
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

// TestDBReporter_CheckpointSameTxConsistency 验证 checkpoint 不变式的核心
// 路径：SegmentDone×N + SegmentResolved×N（同批）+ BatchComplete 后，
// 行内 segment_completed == join 表基数 == N，且 join 行写入的正是
// mapper 映射出的真实 Segment ID（FK 强制真实行）。
func TestDBReporter_CheckpointSameTxConsistency(t *testing.T) {
	client := weightedTestClient(t)
	ctx := context.Background()

	jobID, jrID := progressFixture(t, client)
	round0 := createRoundRow(t, client, jobID, jrID, 0, "adjudicate")
	segIDs := createSegments(t, client, 5)

	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second,
	})
	defer r.Close()

	r.SwitchRound(round0, identityMapper(segIDs))
	r.StageStart("adjudicate", 5)

	for i := 0; i < 5; i++ {
		r.SegmentDone()
		r.SegmentResolved(i)
	}
	r.BatchComplete()

	row, err := client.JobRound.Get(ctx, round0)
	if err != nil {
		t.Fatalf("reload round row: %v", err)
	}
	if row.SegmentCompleted != 5 {
		t.Errorf("segment_completed = %d, want 5", row.SegmentCompleted)
	}
	if got := countRoundSegments(t, client, round0); got != 5 {
		t.Errorf("checkpoint rows = %d, want 5", got)
	}

	// join 行引用的段必须与 mapper 映射一致。
	gotSegs, err := client.JobRoundSegment.Query().
		Where(jobroundsegment.JobRoundIDEQ(round0)).
		Select(jobroundsegment.FieldSegmentID).
		Ints(ctx)
	if err != nil {
		t.Fatalf("query checkpoint rows: %v", err)
	}
	gotSet := make(map[int]struct{}, len(gotSegs))
	for _, id := range gotSegs {
		gotSet[id] = struct{}{}
	}
	for _, want := range segIDs {
		if _, ok := gotSet[want]; !ok {
			t.Errorf("checkpoint rows missing segment %d", want)
		}
	}

	job, err := client.Job.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.ProgressCompleted != 5 || job.ProgressTotal != 5 {
		t.Errorf("job progress = %d/%d, want 5/5", job.ProgressCompleted, job.ProgressTotal)
	}
}

// TestDBReporter_SegmentResolvedIdempotent 验证同一 docIndex 重复登记：
// resolvedPending 只入队一次、flush 后 join 行不重复。
func TestDBReporter_SegmentResolvedIdempotent(t *testing.T) {
	client := weightedTestClient(t)

	jobID, jrID := progressFixture(t, client)
	round0 := createRoundRow(t, client, jobID, jrID, 0, "adjudicate")
	segIDs := createSegments(t, client, 2)

	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second,
	})
	defer r.Close()

	r.SwitchRound(round0, identityMapper(segIDs))
	r.StageStart("adjudicate", 2)

	r.SegmentResolved(0)
	r.SegmentResolved(0)
	r.SegmentResolved(0)

	// 白盒：缓冲内只入队一次。
	r.flushMu.Lock()
	pendingLen := len(r.round.pending)
	bufLen := len(r.round.resolved)
	r.flushMu.Unlock()
	if pendingLen != 1 || bufLen != 1 {
		t.Fatalf("pending/resolved = %d/%d, want 1/1", pendingLen, bufLen)
	}

	r.BatchComplete()

	if got := countRoundSegments(t, client, round0); got != 1 {
		t.Errorf("checkpoint rows = %d, want 1", got)
	}

	// flush 后重复登记仍被 resolved 集合去重，不产生第二次 flush 数据。
	r.SegmentResolved(0)
	r.flushMu.Lock()
	pendingLen = len(r.round.pending)
	r.flushMu.Unlock()
	if pendingLen != 0 {
		t.Fatalf("pending after re-resolve = %d, want 0（已被 resolved 集合去重）", pendingLen)
	}
}

// TestDBReporter_ResolvedNoopWithoutRoundOrMapper 验证两种退化形态下
// SegmentResolved 均为 no-op、join 表无行：
//  1. 无轮次行（单资源路径 preview/quick-translate/cli）；
//  2. 有轮次行但未注入映射（接线错误，SwitchRound 已 ERROR 点名，
//     此处只保证不写脏数据、不 panic）。
func TestDBReporter_ResolvedNoopWithoutRoundOrMapper(t *testing.T) {
	client := weightedTestClient(t)

	jobID, jrID := progressFixture(t, client)
	round0 := createRoundRow(t, client, jobID, jrID, 0, "translate")
	createSegments(t, client, 2)

	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second,
	})
	defer r.Close()

	// 1. 无轮次行：SegmentResolved 无处可写，静默跳过。
	r.StageStart("translate", 2)
	r.SegmentResolved(0)
	r.BatchComplete()
	if got := countRoundSegments(t, client, round0); got != 0 {
		t.Fatalf("checkpoint rows before SwitchRound = %d, want 0", got)
	}

	// 2. 有轮次行但 mapper 为 nil：无法映射 DB Segment ID，同样跳过。
	r.SwitchRound(round0, nil)
	r.StageStart("translate", 2)
	r.SegmentResolved(0)
	r.SegmentResolved(1)
	r.BatchComplete()
	if got := countRoundSegments(t, client, round0); got != 0 {
		t.Errorf("checkpoint rows without mapper = %d, want 0", got)
	}
}

// TestDBReporter_CheckpointMapperNotOk 验证 mapper 返回 ok=false 的段被
// 跳过登记，join 表只含映射成功的段。
func TestDBReporter_CheckpointMapperNotOk(t *testing.T) {
	client := weightedTestClient(t)
	ctx := context.Background()

	jobID, jrID := progressFixture(t, client)
	round0 := createRoundRow(t, client, jobID, jrID, 0, "adjudicate")
	segIDs := createSegments(t, client, 3)

	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second,
	})
	defer r.Close()

	// 只接受 docIndex 0，其余拒绝映射。
	r.SwitchRound(round0, func(docIndex int) (int, bool) {
		if docIndex != 0 {
			return 0, false
		}
		return segIDs[0], true
	})
	r.StageStart("adjudicate", 3)

	r.SegmentDone()
	r.SegmentResolved(0)
	r.SegmentDone()
	r.SegmentResolved(1) // ok=false，跳过
	r.SegmentDone()
	r.SegmentResolved(2) // ok=false，跳过
	r.BatchComplete()

	row, err := client.JobRound.Get(ctx, round0)
	if err != nil {
		t.Fatalf("reload round row: %v", err)
	}
	if row.SegmentCompleted != 1 {
		t.Errorf("segment_completed = %d, want 1 (计数由集合派生，映射不成功的段不计入)", row.SegmentCompleted)
	}
	if got := countRoundSegments(t, client, round0); got != 1 {
		t.Errorf("checkpoint rows = %d, want 1", got)
	}
}

// TestDBReporter_CheckpointOnlyFlush 验证纯断点 flush：pending 为空但
// resolvedPending 非空时 flush 仍写 join 行（不被空缓冲早退拦截），
// 且跳过段计数与 Job 计数写入。
func TestDBReporter_CheckpointOnlyFlush(t *testing.T) {
	client := weightedTestClient(t)
	ctx := context.Background()

	jobID, jrID := progressFixture(t, client)
	round0 := createRoundRow(t, client, jobID, jrID, 0, "adjudicate")
	segIDs := createSegments(t, client, 2)

	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second,
	})
	defer r.Close()

	r.SwitchRound(round0, identityMapper(segIDs))
	r.StageStart("adjudicate", 2)

	// 只登记断点，不伴 SegmentDone。
	r.SegmentResolved(0)
	r.SegmentResolved(1)
	r.BatchComplete()

	if got := countRoundSegments(t, client, round0); got != 2 {
		t.Fatalf("checkpoint rows = %d, want 2 (pure-checkpoint flush must not early-return)", got)
	}
	row, err := client.JobRound.Get(ctx, round0)
	if err != nil {
		t.Fatalf("reload round row: %v", err)
	}
	if row.SegmentCompleted != 2 {
		t.Errorf("segment_completed = %d, want 2 (纯断点 flush 也推进集合派生的进度)", row.SegmentCompleted)
	}
	job, err := client.Job.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.ProgressCompleted != 2 {
		t.Errorf("job progress_completed = %d, want 2 (纯断点 flush 也推进 Job 进度)", job.ProgressCompleted)
	}
}

// TestDBReporter_StageStartBaselineAnchoredToCheckpoints 验证 StageStart
// 恢复重跑的基线锚定 join 表基数而非 segment_completed 计数列：
// 预置 join 行 M=2 条、计数列被人为改大（模拟 SegmentDone/SegmentResolved
// 与 ticker flush 交错产生的窗口偏差）→ 基线取 2，后续计数从 2 续加，
// 窗口偏差被重跑自愈。
func TestDBReporter_StageStartBaselineAnchoredToCheckpoints(t *testing.T) {
	client := weightedTestClient(t)
	ctx := context.Background()

	jobID, jrID := progressFixture(t, client)
	round0 := createRoundRow(t, client, jobID, jrID, 0, "adjudicate")
	segIDs := createSegments(t, client, 4)

	// 预置：本轮已有 2 条断点行（上次运行崩溃前的已解决段）。
	for _, id := range segIDs[:2] {
		if _, err := client.JobRoundSegment.Create().
			SetJobRoundID(round0).
			SetSegmentID(id).
			Save(ctx); err != nil {
			t.Fatalf("seed checkpoint row: %v", err)
		}
	}
	// 人为把计数列改大（4 > 基数 2），模拟窗口偏差；
	// 上次运行的首次揭示已把 progress_total 累加进 Job（恢复不重复累加）。
	if _, err := client.JobRound.UpdateOneID(round0).
		SetSegmentTotal(4).
		SetSegmentCompleted(4).
		Save(ctx); err != nil {
		t.Fatalf("inflate segment_completed: %v", err)
	}
	if _, err := client.Job.UpdateOneID(jobID).
		AddProgressTotal(4).
		Save(ctx); err != nil {
		t.Fatalf("seed job progress_total: %v", err)
	}

	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second,
	})
	defer r.Close()

	r.SwitchRound(round0, identityMapper(segIDs))
	r.StageStart("adjudicate", 4) // 恢复重跑：total 已揭示，不重复累加

	// 若基线误取计数列（4），2 次新 SegmentDone 后 lastDone = 6；
	// 锚定断点基数（2）后应为 4。
	for i := 2; i < 4; i++ {
		r.SegmentDone()
		r.SegmentResolved(i)
	}
	r.StageDone()

	row, err := client.JobRound.Get(ctx, round0)
	if err != nil {
		t.Fatalf("reload round row: %v", err)
	}
	if row.SegmentCompleted != 4 {
		t.Errorf("segment_completed = %d, want 4 (baseline anchored to join cardinality 2, not stale counter 4)", row.SegmentCompleted)
	}
	if got := countRoundSegments(t, client, round0); got != 4 {
		t.Errorf("checkpoint rows = %d, want 4", got)
	}

	job, err := client.Job.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	// 恢复重跑不重复累加分母；只累加本轮实际完成的 2 段。
	if job.ProgressTotal != 4 {
		t.Errorf("job progress_total = %d, want 4 (no double add on resume)", job.ProgressTotal)
	}
	if job.ProgressCompleted != 2 {
		t.Errorf("job progress_completed = %d, want 2", job.ProgressCompleted)
	}
}

// TestDBReporter_TranslateRoundResume_NoRegression 钉住 translate 轮恢复时
// 基线必须来自断点集合：首轮已解决 60/100，恢复扫描剩余 40 段后，不能用
// 恢复批次大小 40 覆写原有 60，且「segment_completed ≡ 断点集合基数」恒成立。
// 旧实现恢复基线恒为 0，第二次 flush 会把轮次行写成 40/100，重算后任务退回 40。
func TestDBReporter_TranslateRoundResume_NoRegression(t *testing.T) {
	client := weightedTestClient(t)
	ctx := context.Background()

	jobID, jrID := progressFixture(t, client)
	roundID := createRoundRow(t, client, jobID, jrID, 0, "translate")
	segIDs := createSegments(t, client, 100)

	r1 := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second,
	})
	r1.SwitchRound(roundID, identityMapper(segIDs))
	r1.StageStart("translate", 100)
	for i := 0; i < 60; i++ {
		r1.SegmentDone()
		r1.SegmentResolved(i)
	}
	r1.BatchComplete()
	if err := r1.Close(); err != nil {
		t.Fatalf("first reporter Close: %v", err)
	}

	row, err := client.JobRound.Get(ctx, roundID)
	if err != nil {
		t.Fatalf("reload round after pause: %v", err)
	}
	if row.SegmentCompleted != 60 {
		t.Fatalf("paused segment_completed = %d, want 60", row.SegmentCompleted)
	}

	r2 := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second,
	})
	r2.SwitchRound(roundID, identityMapper(segIDs))
	r2.StageStart("translate", 40)

	// 恢复后第一次 flush 先写一段新结果，显式钉住绝不允许从 60 倒退。
	r2.SegmentDone()
	r2.SegmentResolved(60)
	r2.BatchComplete()
	row, err = client.JobRound.Get(ctx, roundID)
	if err != nil {
		t.Fatalf("reload round after resume flush: %v", err)
	}
	if row.SegmentCompleted < 60 {
		t.Fatalf("resume flush regressed segment_completed to %d, want >= 60", row.SegmentCompleted)
	}

	for i := 61; i < 100; i++ {
		r2.SegmentDone()
		r2.SegmentResolved(i)
	}
	if err := r2.Close(); err != nil {
		t.Fatalf("second reporter Close: %v", err)
	}

	row, err = client.JobRound.Get(ctx, roundID)
	if err != nil {
		t.Fatalf("reload final round: %v", err)
	}
	if row.SegmentTotal != 100 || row.SegmentCompleted != 100 {
		t.Errorf("final round = %d/%d, want 100/100", row.SegmentCompleted, row.SegmentTotal)
	}
	if got := countRoundSegments(t, client, roundID); got != 100 {
		t.Errorf("final checkpoint rows = %d, want 100", got)
	}
	job, err := client.Job.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("reload final job: %v", err)
	}
	if job.ProgressTotal != 100 || job.ProgressCompleted != 100 {
		t.Errorf("final job progress = %d/%d, want 100/100", job.ProgressCompleted, job.ProgressTotal)
	}
}

// TestDBReporter_TranslateRoundResume_RescanNoOvercount 钉住恢复重扫集合与
// 已解决集合重叠时的幂等性：重扫 59 以及 60..99 共 41 段，59 已在断点集合内，
// 最终只能新增 40 条断点。旧实现没有集合基线/入缓冲去重时会重复计数，得到 101。
func TestDBReporter_TranslateRoundResume_RescanNoOvercount(t *testing.T) {
	client := weightedTestClient(t)
	ctx := context.Background()

	jobID, jrID := progressFixture(t, client)
	roundID := createRoundRow(t, client, jobID, jrID, 0, "translate")
	segIDs := createSegments(t, client, 100)

	r1 := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second,
	})
	r1.SwitchRound(roundID, identityMapper(segIDs))
	r1.StageStart("translate", 100)
	for i := 0; i < 60; i++ {
		r1.SegmentDone()
		r1.SegmentResolved(i)
	}
	r1.BatchComplete()
	if err := r1.Close(); err != nil {
		t.Fatalf("first reporter Close: %v", err)
	}

	r2 := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second,
	})
	r2.SwitchRound(roundID, identityMapper(segIDs))
	r2.StageStart("translate", 41)
	r2.SegmentDone()
	r2.SegmentResolved(59) // 重扫命中已落库断点，必须幂等跳过。
	for i := 60; i < 100; i++ {
		r2.SegmentDone()
		r2.SegmentResolved(i)
	}
	if err := r2.Close(); err != nil {
		t.Fatalf("second reporter Close: %v", err)
	}

	row, err := client.JobRound.Get(ctx, roundID)
	if err != nil {
		t.Fatalf("reload final round: %v", err)
	}
	if row.SegmentCompleted != 100 {
		t.Errorf("final segment_completed = %d, want 100", row.SegmentCompleted)
	}
	if got := countRoundSegments(t, client, roundID); got != 100 {
		t.Errorf("final checkpoint rows = %d, want 100", got)
	}
	job, err := client.Job.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("reload final job: %v", err)
	}
	if job.ProgressCompleted > job.ProgressTotal {
		t.Errorf("job progress = %d/%d, completed exceeds total", job.ProgressCompleted, job.ProgressTotal)
	}
}

// TestDBReporter_SwitchRound_PreservesFailedCheckpoints 钉住失败 flush 后切轮
// 的 stale 保留：第一轮断点先引用不存在的 Segment ID 触发 FK 失败，切轮后再
// 创建该 ID 对应的下一段，后续 flush 必须把旧轮 pending 与当前轮一起写成。
// 旧实现切轮直接清空 resolvedBuf/resolvedPending，旧轮断点会永久丢失。
func TestDBReporter_SwitchRound_PreservesFailedCheckpoints(t *testing.T) {
	client := weightedTestClient(t)
	ctx := context.Background()

	jobID, jrID := progressFixture(t, client)
	round0 := createRoundRow(t, client, jobID, jrID, 0, "adjudicate")
	round1 := createRoundRow(t, client, jobID, jrID, 1, "revise")
	segIDs := createSegments(t, client, 1)
	missingID := segIDs[0] + 1 // 空库中下一次 Segment 自增 ID，当前尚不存在。
	mappedID := missingID

	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second,
	})
	defer r.Close()

	r.SwitchRound(round0, func(int) (int, bool) { return mappedID, true })
	r.StageStart("adjudicate", 1)
	r.SegmentDone()
	r.SegmentResolved(0)
	if err := r.flush(); err == nil {
		t.Fatal("first checkpoint flush unexpectedly succeeded")
	}

	// SwitchRound 会重试失败写入，再将仍 pending 的旧轮整体移入 stale。
	r.SwitchRound(round1, identityMapper(segIDs))
	r.flushMu.Lock()
	if len(r.stale) != 1 || len(r.stale[0].pending) != 1 {
		r.flushMu.Unlock()
		t.Fatalf("stale/pending = %d/%d, want 1/1 after failed flush and switch",
			len(r.stale), len(r.stale[0].pending))
	}
	r.flushMu.Unlock()

	created := createSegments(t, client, 1)
	if created[0] != missingID {
		t.Fatalf("created segment ID = %d, want missing mapped ID %d", created[0], missingID)
	}
	r.StageStart("revise", 1)
	r.SegmentDone()
	r.SegmentResolved(0)
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, roundID := range []int{round0, round1} {
		row, err := client.JobRound.Get(ctx, roundID)
		if err != nil {
			t.Fatalf("reload round %d: %v", roundID, err)
		}
		if row.SegmentCompleted != countRoundSegments(t, client, roundID) {
			t.Errorf("round %d segment_completed = %d, checkpoint rows = %d",
				roundID, row.SegmentCompleted, countRoundSegments(t, client, roundID))
		}
	}
	if got := countRoundSegments(t, client, round0); got != 1 {
		t.Errorf("old round checkpoint rows = %d, want 1", got)
	}
	if got := countRoundSegments(t, client, round1); got != 1 {
		t.Errorf("current round checkpoint rows = %d, want 1", got)
	}

	r.flushMu.Lock()
	residue := len(r.stale)
	if r.round != nil {
		residue += len(r.round.pending)
	}
	for _, c := range r.stale {
		residue += len(c.pending)
	}
	r.flushMu.Unlock()
	if residue != 0 {
		t.Errorf("checkpoint residue after Close = %d, want 0", residue)
	}
}
