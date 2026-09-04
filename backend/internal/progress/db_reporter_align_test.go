package progress

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

// errAlignFault 是注入给断点对齐查询的模拟故障。
var errAlignFault = errors.New("injected align query fault")

// faultDriver 包裹 dialect.Driver，仅在 Query 上按需注入故障：命中 substr 的
// 查询直接返回注入的错误而不触库，其余全部透传。刻意不包裹 Tx——事务内查询
// 与所有写入必须原样通过，注入目标是 alignResolved 发出的非事务断点 SELECT。
// armed 状态用互斥锁保护：reporter 的 ticker 协程可能并发 flush。
type faultDriver struct {
	dialect.Driver

	mu     sync.Mutex
	armed  bool
	substr string
	err    error
}

// arm 开始对 SQL 包含 substr 的查询注入 err。
func (d *faultDriver) arm(substr string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.armed = true
	d.substr = substr
	d.err = err
}

// disarm 停止注入，恢复全部透传。
func (d *faultDriver) disarm() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.armed = false
	d.substr = ""
	d.err = nil
}

func (d *faultDriver) Query(ctx context.Context, query string, args, v any) error {
	d.mu.Lock()
	armed, substr, faultErr := d.armed, d.substr, d.err
	d.mu.Unlock()
	if armed && strings.Contains(query, substr) {
		return faultErr
	}
	return d.Driver.Query(ctx, query, args, v)
}

// alignFaultClient 创建内存 SQLite ent 客户端 + 可注入查询故障的驱动。
// :memory: 数据库是连接私有的；限制单连接确保事务路径与非事务的断点对齐
// 查询看到同一实例。arm 必须在 Schema.Create 之后调用。
func alignFaultClient(t *testing.T) (*ent.Client, *faultDriver) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	fd := &faultDriver{Driver: entsql.OpenDB(dialect.SQLite, db)}
	client := ent.NewClient(ent.Driver(fd))
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = db.Close()
	})
	return client, fd
}

// seedResumedRoundFixture 搭建「上一轮运行完成 60/100 后中断」的恢复现场：
// 100 个真实段；轮次行已有 60 条断点、segment_completed=60；Job 计数器
// 镜像上一次运行（progress_total=100、progress_completed=60）。
func seedResumedRoundFixture(t *testing.T, client *ent.Client) (jobID, jrID, roundID int, segIDs []int) {
	t.Helper()
	ctx := context.Background()

	jobID, jrID = progressFixture(t, client)
	roundID = createRoundRow(t, client, jobID, jrID, 0, "translate")
	segIDs = createSegments(t, client, 100)

	for _, id := range segIDs[:60] {
		if _, err := client.JobRoundSegment.Create().
			SetJobRoundID(roundID).
			SetSegmentID(id).
			Save(ctx); err != nil {
			t.Fatalf("seed checkpoint row: %v", err)
		}
	}
	if _, err := client.JobRound.UpdateOneID(roundID).
		SetSegmentTotal(100).
		SetSegmentCompleted(60).
		Save(ctx); err != nil {
		t.Fatalf("seed round counters: %v", err)
	}
	if _, err := client.Job.UpdateOneID(jobID).
		SetProgressTotal(100).
		SetProgressCompleted(60).
		Save(ctx); err != nil {
		t.Fatalf("seed job progress: %v", err)
	}
	return jobID, jrID, roundID, segIDs
}

// TestDBReporter_SeedFailure_CountStillMatchesCheckpointRows 钉住恢复时断点
// 对齐查询失败不得把 segment_completed 写小：上一轮已解决 60/100（60 条断点
// 行已在库），恢复运行的 StageStart 对齐失败后，最终仍必须以「DB 断点 ∪ 本轮
// 新解决」= 100 收尾。修复前对齐失败会退化为空基线，下一次 flush 把
// segment_completed 写成 40（只含本轮新解决段），永久低于真实断点行数 60。
func TestDBReporter_SeedFailure_CountStillMatchesCheckpointRows(t *testing.T) {
	client, faults := alignFaultClient(t)
	ctx := context.Background()

	jobID, jrID, roundID, segIDs := seedResumedRoundFixture(t, client)

	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second, // 长间隔避免 ticker 干扰
	})

	// 恢复现场：StageStart 的断点对齐查询失败，轮次保持未对齐。
	faults.arm("job_round_segments", errAlignFault)
	r.SwitchRound(roundID, identityMapper(segIDs))
	r.StageStart("translate", 40)
	faults.disarm()

	// 恢复扫描只解决剩余 40 段；Close 的 flush 先以 DB 为准对齐（60 条已有
	// 断点 ∪ 40 条新登记）再写计数。
	for i := 60; i < 100; i++ {
		r.SegmentDone()
		r.SegmentResolved(i)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	row, err := client.JobRound.Get(ctx, roundID)
	if err != nil {
		t.Fatalf("reload round: %v", err)
	}
	if row.SegmentCompleted != 100 {
		t.Errorf("segment_completed = %d, want 100 (修复前对齐失败退化为空基线，会写成 40——只含本轮新解决段)", row.SegmentCompleted)
	}
	if got := countRoundSegments(t, client, roundID); got != 100 {
		t.Errorf("checkpoint rows = %d, want 100", got)
	}
	job, err := client.Job.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.ProgressCompleted > job.ProgressTotal {
		t.Errorf("job progress = %d/%d, completed exceeds total", job.ProgressCompleted, job.ProgressTotal)
	}
}

// TestDBReporter_SeedFailure_NoUndercountWriteBeforeAlign 钉住更强的性质：
// 未对齐的轮次在 flush 中不产出任何写入（连计数列都不动），而不是先写一个
// 偏小值等以后纠正。StageStart 与一次 BatchComplete 期间对齐查询持续失败，
// segment_completed 与断点行必须原样停在 60；故障解除后的第一次 flush 以 DB
// 为准对齐并自愈到 61。修复前对齐失败会以空集合为真值，同一窗口内就会把
// segment_completed 写成 1。
func TestDBReporter_SeedFailure_NoUndercountWriteBeforeAlign(t *testing.T) {
	client, faults := alignFaultClient(t)
	ctx := context.Background()

	jobID, jrID, roundID, segIDs := seedResumedRoundFixture(t, client)

	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second, // 长间隔避免 ticker 在步骤间触发 flush
	})

	// StageStart 与 BatchComplete 期间对齐均失败：不得有任何写入。
	faults.arm("job_round_segments", errAlignFault)
	r.SwitchRound(roundID, identityMapper(segIDs))
	r.StageStart("translate", 40)
	r.SegmentDone()
	r.SegmentResolved(60)
	r.BatchComplete()
	faults.disarm()

	row, err := client.JobRound.Get(ctx, roundID)
	if err != nil {
		t.Fatalf("reload round after failed align: %v", err)
	}
	if row.SegmentCompleted != 60 {
		t.Errorf("segment_completed = %d, want 60 (未对齐的轮次不产出任何写入，而非写一个偏小值)", row.SegmentCompleted)
	}
	if got := countRoundSegments(t, client, roundID); got != 60 {
		t.Errorf("checkpoint rows = %d, want 60 (未对齐期间 join 表不得新增行)", got)
	}

	// 故障解除后的第一次 flush：对齐成功即自愈，60 条已有断点 + 1 条新登记。
	r.BatchComplete()
	row, err = client.JobRound.Get(ctx, roundID)
	if err != nil {
		t.Fatalf("reload round after align: %v", err)
	}
	if row.SegmentCompleted != 61 {
		t.Errorf("segment_completed = %d, want 61 (首次成功对齐后按集合基数断言计数)", row.SegmentCompleted)
	}
	if got := countRoundSegments(t, client, roundID); got != 61 {
		t.Errorf("checkpoint rows = %d, want 61", got)
	}

	for i := 61; i < 100; i++ {
		r.SegmentDone()
		r.SegmentResolved(i)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
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
}

// TestDBReporter_ConstraintConflict_RealignsAndAssertsCount 钉住唯一索引冲突
// 的自愈路径：内存集合偏离 DB（模拟「断点事务实际已提交但客户端看到错误」的
// 边界）时，撞 (job_round_id, segment_id) 唯一索引的 flush 必须失败且计数列
// 不动；随后 flush 以 DB 为准重建集合，把 pending 过滤为空——即使无任何待插
// 增量，segment_completed 也必须重申为对齐后的集合基数。修复前纯计数申明
// 不存在，冲突后计数列会一直停在 0。
func TestDBReporter_ConstraintConflict_RealignsAndAssertsCount(t *testing.T) {
	client := weightedTestClient(t)
	ctx := context.Background()

	jobID, jrID := progressFixture(t, client)
	roundID := createRoundRow(t, client, jobID, jrID, 0, "adjudicate")
	segIDs := createSegments(t, client, 2)

	r := NewDBReporter(DBReporterOptions{
		Client:        client,
		JobID:         jobID,
		JobResourceID: jrID,
		Ticker:        10 * time.Second,
	})
	defer r.Close()

	r.SwitchRound(roundID, identityMapper(segIDs))
	r.StageStart("adjudicate", 2)
	r.SegmentResolved(0)
	r.SegmentResolved(1)

	// 两段断点此刻都已在库，而内存集合仍自认为未落库。
	for _, id := range segIDs {
		if _, err := client.JobRoundSegment.Create().
			SetJobRoundID(roundID).
			SetSegmentID(id).
			Save(ctx); err != nil {
			t.Fatalf("seed committed checkpoint row: %v", err)
		}
	}

	// CreateBulk 撞唯一索引：本次事务整体回滚，计数列不得推进。
	if err := r.flush(); err == nil {
		t.Fatal("conflicting checkpoint flush unexpectedly succeeded")
	}
	row, err := client.JobRound.Get(ctx, roundID)
	if err != nil {
		t.Fatalf("reload round after conflict: %v", err)
	}
	if row.SegmentCompleted != 0 {
		t.Errorf("segment_completed = %d, want 0 (冲突事务整体回滚)", row.SegmentCompleted)
	}

	// 对齐把 pending 过滤为空，但计数列仍须重申为集合基数。
	if err := r.flush(); err != nil {
		t.Fatalf("realign flush: %v", err)
	}
	row, err = client.JobRound.Get(ctx, roundID)
	if err != nil {
		t.Fatalf("reload round after realign: %v", err)
	}
	if row.SegmentCompleted != 2 {
		t.Errorf("segment_completed = %d, want 2 (对齐后即使无待插增量也须断言计数列)", row.SegmentCompleted)
	}
	if got := countRoundSegments(t, client, roundID); got != 2 {
		t.Errorf("checkpoint rows = %d, want 2", got)
	}

	// Job 计数器是派生缓存，纠偏性计数写入不补偿它，只验证不越界。
	job, err := client.Job.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.ProgressCompleted > job.ProgressTotal {
		t.Errorf("job progress = %d/%d, completed exceeds total", job.ProgressCompleted, job.ProgressTotal)
	}
}
