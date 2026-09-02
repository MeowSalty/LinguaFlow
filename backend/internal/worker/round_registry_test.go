package worker

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// newRegistryTestClient 创建内存 SQLite ent 客户端并自动迁移。
// 单连接池：避免并发用例在多连接上触发 SQLite 写锁竞争（BUSY），
// 并保证 :memory: 库在所有查询间共享。
func newRegistryTestClient(t *testing.T) *ent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
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

// registryFixture 创建 minimal User/Project/Resource/Job/JobResource 层级，
// 返回 jobID 与两个 jobResourceID。
func registryFixture(t *testing.T, client *ent.Client) (jobID, jr0, jr1 int) {
	t.Helper()
	ctx := context.Background()

	user, err := client.User.Create().
		SetUsername("registry-user").
		SetPasswordHash("$2a$10$dummyhash").
		SetEmail("registry@test.com").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	project, err := client.Project.Create().
		SetName("registry-proj").
		SetSourceLang("en").
		SetTargetLang("zh").
		SetOwnerUserID(user.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create project: %v", err)
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

	res0, err := client.Resource.Create().
		SetProjectID(project.ID).
		SetPath("a.txt").
		SetFormat("txt").
		SetStoragePath("storage/a.txt").
		Save(ctx)
	if err != nil {
		t.Fatalf("create resource0: %v", err)
	}
	res1, err := client.Resource.Create().
		SetProjectID(project.ID).
		SetPath("b.txt").
		SetFormat("txt").
		SetStoragePath("storage/b.txt").
		Save(ctx)
	if err != nil {
		t.Fatalf("create resource1: %v", err)
	}

	jrA, err := client.JobResource.Create().
		SetStatus("pending").
		SetSegmentCount(3).
		SetJob(job).
		SetResource(res0).
		Save(ctx)
	if err != nil {
		t.Fatalf("create job_resource0: %v", err)
	}
	jrB, err := client.JobResource.Create().
		SetStatus("pending").
		SetSegmentCount(2).
		SetJob(job).
		SetResource(res1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create job_resource1: %v", err)
	}

	return job.ID, jrA.ID, jrB.ID
}

// createJobRoundRow 预建一行 JobRound，返回行 ID。
func createJobRoundRow(t *testing.T, client *ent.Client, jobID, jrID, roundIndex int, mode string) int {
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

// countJobRounds 统计某 jobResource 的 JobRound 行数。
func countJobRounds(t *testing.T, client *ent.Client, jrID int) int {
	t.Helper()
	n, err := client.JobRound.Query().
		Where(jobround.JobResourceIDEQ(jrID)).
		Count(context.Background())
	if err != nil {
		t.Fatalf("count job rounds: %v", err)
	}
	return n
}

// TestLoadJobRounds_BuildsMap loadJobRounds 按 (资源, 轮次) 二维装登记册，
// lookup 命中与未命中语义正确。
func TestLoadJobRounds_BuildsMap(t *testing.T) {
	client := newRegistryTestClient(t)
	ctx := context.Background()
	jobID, jr0, jr1 := registryFixture(t, client)

	createJobRoundRow(t, client, jobID, jr0, 0, "translate")
	createJobRoundRow(t, client, jobID, jr0, 1, "adjudicate")
	createJobRoundRow(t, client, jobID, jr1, 0, "translate")

	reg, err := loadJobRounds(ctx, client, jobID)
	if err != nil {
		t.Fatalf("loadJobRounds: %v", err)
	}

	cases := []struct {
		name          string
		jobResourceID int
		roundIndex    int
	}{
		{"jr0 round0", jr0, 0},
		{"jr0 round1", jr0, 1},
		{"jr1 round0", jr1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := reg.lookup(tc.jobResourceID, tc.roundIndex)
			if info == nil {
				t.Fatalf("lookup(%d,%d) = nil, want 命中", tc.jobResourceID, tc.roundIndex)
			}
			if info.rowID <= 0 {
				t.Fatalf("lookup(%d,%d).rowID = %d, want > 0", tc.jobResourceID, tc.roundIndex, info.rowID)
			}
		})
	}

	// 未命中：不存在的轮次 / 不存在的资源。
	if info := reg.lookup(jr1, 1); info != nil {
		t.Fatalf("lookup(jr1,1) = %+v, want nil（该轮次未建行）", info)
	}
	if info := reg.lookup(99999, 0); info != nil {
		t.Fatalf("lookup(99999,0) = %+v, want nil（未知资源）", info)
	}
}

// TestEnsureLoaded_ExistingAndMissingRows ensureLoaded 双路径：
// 已有行直接返回行 ID 不新建；缺行动态建 pending 行（mode/round_index 正确），
// 且写回注册表后第二次调用幂等返回同一 ID。
func TestEnsureLoaded_ExistingAndMissingRows(t *testing.T) {
	client := newRegistryTestClient(t)
	ctx := context.Background()
	jobID, jr0, _ := registryFixture(t, client)
	existing := createJobRoundRow(t, client, jobID, jr0, 0, "translate")

	reg, err := loadJobRounds(ctx, client, jobID)
	if err != nil {
		t.Fatalf("loadJobRounds: %v", err)
	}

	// 已有行：返回既有 ID，不新建行。
	rowID, err := reg.ensureLoaded(ctx, client, jr0, 0, "translate")
	if err != nil {
		t.Fatalf("ensureLoaded(existing): %v", err)
	}
	if rowID != existing {
		t.Fatalf("ensureLoaded(existing) = %d, want 既有行 ID %d", rowID, existing)
	}
	if n := countJobRounds(t, client, jr0); n != 1 {
		t.Fatalf("JobRound rows = %d, want 1（已有行不应新建）", n)
	}

	// 缺行：动态建 pending 行，mode 与 round_index 正确。
	rowID2, err := reg.ensureLoaded(ctx, client, jr0, 1, "adjudicate")
	if err != nil {
		t.Fatalf("ensureLoaded(missing): %v", err)
	}
	row, err := client.JobRound.Get(ctx, rowID2)
	if err != nil {
		t.Fatalf("get created row: %v", err)
	}
	if row.Mode != "adjudicate" || row.RoundIndex != 1 {
		t.Fatalf("created row mode=%q round_index=%d, want adjudicate/1", row.Mode, row.RoundIndex)
	}
	if row.Status != service.JobRoundStatusPending {
		t.Fatalf("created row status = %q, want %q", row.Status, service.JobRoundStatusPending)
	}
	// 建行后写回注册表：lookup 立即可见。
	if info := reg.lookup(jr0, 1); info == nil || info.rowID != rowID2 {
		t.Fatalf("lookup after ensureLoaded = %+v, want rowID %d", info, rowID2)
	}

	// 幂等：第二次调用返回同一 ID，不再建行。
	rowID3, err := reg.ensureLoaded(ctx, client, jr0, 1, "adjudicate")
	if err != nil {
		t.Fatalf("ensureLoaded(idempotent): %v", err)
	}
	if rowID3 != rowID2 {
		t.Fatalf("second ensureLoaded = %d, want 同一行 ID %d", rowID3, rowID2)
	}
	if n := countJobRounds(t, client, jr0); n != 2 {
		t.Fatalf("JobRound rows = %d, want 2（幂等调用不重复建行）", n)
	}
}

// TestEnsureRoundRow_ConcurrentCreateRace 并发建行命中唯一索引
// (job_resource_id, round_index) 时重查兜底：双方最终拿到同一行 ID，
// 且只落一行。
func TestEnsureRoundRow_ConcurrentCreateRace(t *testing.T) {
	client := newRegistryTestClient(t)
	ctx := context.Background()
	jobID, jr0, _ := registryFixture(t, client)

	const goroutines = 2
	var wg sync.WaitGroup
	ids := make([]int, goroutines)
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i], errs[i] = ensureRoundRow(ctx, client, jobID, jr0, 0, "translate")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("ensureRoundRow#%d: %v", i, err)
		}
	}
	if ids[0] != ids[1] {
		t.Fatalf("并发建行得到不同 ID：%d vs %d, want 同一行", ids[0], ids[1])
	}
	if n := countJobRounds(t, client, jr0); n != 1 {
		t.Fatalf("JobRound rows = %d, want 1（唯一索引竞争只落一行）", n)
	}
}

// TestLoadResolved_UnionPerMode loadResolved 按 mode 分组恢复各同模式轮
// resolved_segment_ids 的并集；空集合行跳过；不同 mode 互不混入。
func TestLoadResolved_UnionPerMode(t *testing.T) {
	client := newRegistryTestClient(t)
	ctx := context.Background()
	jobID, jr0, _ := registryFixture(t, client)

	// translate 轮恒空（不 Set resolved_segment_ids）→ 跳过。
	createJobRoundRow(t, client, jobID, jr0, 0, "translate")

	r1 := createJobRoundRow(t, client, jobID, jr0, 1, "adjudicate")
	if err := client.JobRound.UpdateOneID(r1).SetResolvedSegmentIds([]int{5, 7}).Exec(ctx); err != nil {
		t.Fatalf("set resolved r1: %v", err)
	}
	r2 := createJobRoundRow(t, client, jobID, jr0, 2, "adjudicate")
	if err := client.JobRound.UpdateOneID(r2).SetResolvedSegmentIds([]int{7, 9}).Exec(ctx); err != nil {
		t.Fatalf("set resolved r2: %v", err)
	}
	// extract 轮同样为空 → 跳过。
	createJobRoundRow(t, client, jobID, jr0, 3, "extract")

	got, err := loadResolved(ctx, client, jr0)
	if err != nil {
		t.Fatalf("loadResolved: %v", err)
	}

	// adjudicate：两轮并集 {5,7,9}。
	adj := got["adjudicate"]
	if len(adj) != 3 {
		t.Fatalf("adjudicate set = %v, want {5,7,9}", adj)
	}
	for _, id := range []int{5, 7, 9} {
		if _, ok := adj[id]; !ok {
			t.Fatalf("adjudicate set 缺少 DB 段 ID %d: %v", id, adj)
		}
	}
	// 空集合的 mode 不应出现键（或至多为空集）。
	if s := got["translate"]; len(s) != 0 {
		t.Fatalf("translate set = %v, want 空（空行跳过）", s)
	}
	if s := got["extract"]; len(s) != 0 {
		t.Fatalf("extract set = %v, want 空（空行跳过）", s)
	}
	if len(got) != 1 {
		t.Fatalf("modes = %d (%v), want 仅 adjudicate", len(got), got)
	}
}
