package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
)

// 本文件覆盖 JobRound 轮次矩阵的状态机与派生计数器不变量。
//
// 核心不变量：JobRound 矩阵是唯一事实源；Job.progress_total / progress_completed
// 只是派生缓存。任何批量重置（RecoverPendingJobs / ResumeJob / RetryJob）都必须以
// 矩阵求和口径重算派生缓存（见 jobRoundProgress）：
//
//	progress_total     = Σ segment_total（全部行，含 fresh pending 0/0 行）
//	progress_completed = Σ jobRoundProgress(status, segment_total, segment_completed)
//	                     ——闭合终态（completed/skipped）取 segment_total，
//	                       其余状态取 segment_completed
//
// 闭合只在读侧发生：终态转换不回写 segment_completed（该列 ≡ 断点集合基数、由
// DBReporter 独占写入），只按口径差值维护 Job 缓存。
//
// 且重置不得清除 job_round_segments 关联断点 / segment_total / segment_completed
// 等断点字段：fresh pending 行为 0/0；带历史的行重置后保留全部总量与断点。

// ---- 测试环境与种子辅助 ----

// newJobRoundTestService 构造仅注入 client/projects/broker 的 JobService，
// 与 job_cancel_retry_state_test.go 的做法一致，绕过 NewJobService 的完整依赖树。
func newJobRoundTestService(client *ent.Client, broker *event.Broker) *JobService {
	projects := NewProjectService(client, NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client))))
	return &JobService{client: client, projects: projects, broker: broker}
}

// jobRoundTestEnv 轮次矩阵测试的最小环境：内存库 + 属主用户 + 项目 + 服务。
type jobRoundTestEnv struct {
	client  *ent.Client
	svc     *JobService
	user    *ent.User
	project *ent.Project
}

func newJobRoundTestEnv(t *testing.T, broker *event.Broker) *jobRoundTestEnv {
	t.Helper()
	client := testClient(t)
	user := createTestUser(t, client, "round-user")
	project := createTestProject(t, client, "round-project", user.ID)
	return &jobRoundTestEnv{
		client:  client,
		svc:     newJobRoundTestService(client, broker),
		user:    user,
		project: project,
	}
}

// jobRoundSpec 单轮种子配置。
type jobRoundSpec struct {
	roundIndex int
	mode       string
	status     string
	total      int
	completed  int
	// resolvedCount 预置断点段数：创建 N 条真实 Segment 并写入
	// job_round_segments join 行（join 表双 FK 强制段行真实存在）。
	resolvedCount int
	errMessage    string
}

// jobResourceSpec 单资源种子配置（含其名下轮次行）。
type jobResourceSpec struct {
	status       string
	segmentCount int
	rounds       []jobRoundSpec
}

// seedJobWithRounds 直接经 ent 构造 Job + JobResource + JobRound 矩阵，
// 并把派生计数器预置为指定值（模拟 worker 上次落库的缓存状态）。
func seedJobWithRounds(t *testing.T, env *jobRoundTestEnv, jobStatus string, progressTotal, progressCompleted int64, specs []jobResourceSpec) (*ent.Job, []*ent.JobResource, [][]*ent.JobRound) {
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
	jrs := make([]*ent.JobResource, 0, len(specs))
	roundRows := make([][]*ent.JobRound, 0, len(specs))
	for i, spec := range specs {
		res := createTestResource(t, env.client, env.project.ID, fmt.Sprintf("round-seed-%d.txt", i))
		jr, err := env.client.JobResource.Create().
			SetStatus(spec.status).
			SetSegmentCount(spec.segmentCount).
			SetJob(job).
			SetResource(res).
			Save(ctx)
		if err != nil {
			t.Fatalf("create jr[%d]: %v", i, err)
		}
		jrs = append(jrs, jr)
		row := make([]*ent.JobRound, 0, len(spec.rounds))
		for _, rs := range spec.rounds {
			create := env.client.JobRound.Create().
				SetJobID(job.ID).
				SetJobResourceID(jr.ID).
				SetRoundIndex(rs.roundIndex).
				SetMode(rs.mode).
				SetStatus(rs.status).
				SetSegmentTotal(rs.total).
				SetSegmentCompleted(rs.completed)
			if rs.errMessage != "" {
				create = create.SetErrorMessage(rs.errMessage)
			}
			roundRow, err := create.Save(ctx)
			if err != nil {
				t.Fatalf("create round jr=%d idx=%d: %v", jr.ID, rs.roundIndex, err)
			}
			// 预置断点：创建真实 Segment 行并写 join 行（双 FK 强制段真实存在，
			// 模拟 worker flush 落下的 checkpoint 关联行）。
			for i := 0; i < rs.resolvedCount; i++ {
				seg := createTestSegment(t, env.client, res.ID, i, fmt.Sprintf("round-seed-%d-r%d-seg-%d", i, jr.ID, rs.roundIndex), nil)
				if _, err := env.client.JobRoundSegment.Create().
					SetJobRoundID(roundRow.ID).
					SetSegmentID(seg.ID).
					Save(ctx); err != nil {
					t.Fatalf("create job_round_segment round=%d seg=%d: %v", roundRow.ID, seg.ID, err)
				}
			}
			row = append(row, roundRow)
		}
		roundRows = append(roundRows, row)
	}
	return job, jrs, roundRows
}

// loadResourceRounds 按 round_index 升序读取某资源的全部轮次行。
func loadResourceRounds(t *testing.T, client *ent.Client, jobResourceID int) []*ent.JobRound {
	t.Helper()
	rounds, err := client.JobRound.Query().
		Where(jobround.JobResourceIDEQ(jobResourceID)).
		Order(ent.Asc(jobround.FieldRoundIndex)).
		All(context.Background())
	if err != nil {
		t.Fatalf("query rounds: %v", err)
	}
	return rounds
}

// reloadJob 重新读取 Job 行，用于校验状态与派生缓存。
func reloadJob(t *testing.T, client *ent.Client, jobID int) *ent.Job {
	t.Helper()
	row, err := client.Job.Get(context.Background(), jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	return row
}

// assertJobProgress 校验派生缓存 progress_total / progress_completed。
func assertJobProgress(t *testing.T, row *ent.Job, wantTotal, wantCompleted int64) {
	t.Helper()
	if row.ProgressTotal != wantTotal {
		t.Errorf("job %d progress_total = %d, want %d", row.ID, row.ProgressTotal, wantTotal)
	}
	if row.ProgressCompleted != wantCompleted {
		t.Errorf("job %d progress_completed = %d, want %d", row.ID, row.ProgressCompleted, wantCompleted)
	}
}

// assertRoundCheckpoint 校验轮次行的状态与断点字段完整保留。
// 断点集合经 job_round_segments join 表断言：wantResolvedRows 为期望的
// 关联行数（断点基数），0 表示该轮无断点行。
func assertRoundCheckpoint(t *testing.T, client *ent.Client, r *ent.JobRound, wantStatus string, wantTotal, wantCompleted, wantResolvedRows int) {
	t.Helper()
	if r.Status != wantStatus {
		t.Errorf("round %d status = %q, want %q", r.ID, r.Status, wantStatus)
	}
	if r.SegmentTotal != wantTotal {
		t.Errorf("round %d segment_total = %d, want %d（重置不得清总量）", r.ID, r.SegmentTotal, wantTotal)
	}
	if r.SegmentCompleted != wantCompleted {
		t.Errorf("round %d segment_completed = %d, want %d（重置不得清断点）", r.ID, r.SegmentCompleted, wantCompleted)
	}
	if got := countRoundJoinRows(t, client, r.ID); got != wantResolvedRows {
		t.Errorf("round %d join 断点行 = %d, want %d（重置不得清断点集合）", r.ID, got, wantResolvedRows)
	}
}

// countRoundJoinRows 统计某轮 job_round_segments 关联行数（断点集合基数）。
func countRoundJoinRows(t *testing.T, client *ent.Client, roundRowID int) int {
	t.Helper()
	n, err := client.JobRoundSegment.Query().
		Where(jobroundsegment.JobRoundIDEQ(roundRowID)).
		Count(context.Background())
	if err != nil {
		t.Fatalf("count job_round_segments: %v", err)
	}
	return n
}

// roundJoinSegmentIDs 读取某轮 join 表引用的全部段 ID（升序），用于精确集合断言。
func roundJoinSegmentIDs(t *testing.T, client *ent.Client, roundRowID int) []int {
	t.Helper()
	ids, err := client.JobRoundSegment.Query().
		Where(jobroundsegment.JobRoundIDEQ(roundRowID)).
		Select(jobroundsegment.FieldSegmentID).
		Ints(context.Background())
	if err != nil {
		t.Fatalf("query join segment ids: %v", err)
	}
	sort.Ints(ids)
	return ids
}

// intSliceContains 报告 want 是否出现在 ids 中。
func intSliceContains(ids []int, want int) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// ---- 1. 轮次状态机 ----

// TestMarkJobRound_StateMachine 覆盖轮次状态机的主迁移与条件性 no-op：
// pending→running→completed、running→failed、pending→skipped、skipped→running；
// 状态不匹配时调用标记方法必须为良性 no-op（不报错、不改状态）。
func TestMarkJobRound_StateMachine(t *testing.T) {
	translateRound := func(status string) []jobResourceSpec {
		return []jobResourceSpec{{
			status: JobResourceStatusPending,
			rounds: []jobRoundSpec{{roundIndex: 0, mode: "translate", status: status}},
		}}
	}
	tests := []struct {
		name       string
		initial    string
		act        func(context.Context, *JobService, *ent.Job, *ent.JobRound) error
		wantStatus string
		wantErrMsg string // 非空时额外校验 error_message 内容
	}{
		{
			name:    "pending to running",
			initial: JobRoundStatusPending,
			act: func(ctx context.Context, s *JobService, j *ent.Job, r *ent.JobRound) error {
				return s.MarkJobRoundRunning(ctx, j.ID, r.ID)
			},
			wantStatus: JobRoundStatusRunning,
		},
		{
			name:    "skipped to running",
			initial: JobRoundStatusSkipped,
			act: func(ctx context.Context, s *JobService, j *ent.Job, r *ent.JobRound) error {
				return s.MarkJobRoundRunning(ctx, j.ID, r.ID)
			},
			wantStatus: JobRoundStatusRunning,
		},
		{
			name:    "running to completed",
			initial: JobRoundStatusRunning,
			act: func(ctx context.Context, s *JobService, j *ent.Job, r *ent.JobRound) error {
				return s.MarkJobRoundCompleted(ctx, r.ID)
			},
			wantStatus: JobRoundStatusCompleted,
		},
		{
			name:    "running to failed",
			initial: JobRoundStatusRunning,
			act: func(ctx context.Context, s *JobService, j *ent.Job, r *ent.JobRound) error {
				return s.MarkJobRoundFailed(ctx, r.ID, errors.New("boom"))
			},
			wantStatus: JobRoundStatusFailed,
			wantErrMsg: "boom",
		},
		{
			name:    "pending to skipped",
			initial: JobRoundStatusPending,
			act: func(ctx context.Context, s *JobService, j *ent.Job, r *ent.JobRound) error {
				return s.MarkJobRoundSkipped(ctx, r.ID)
			},
			wantStatus: JobRoundStatusSkipped,
		},
		// ---- 条件性 no-op：状态不匹配时必须原样保留 ----
		{
			name:    "noop completed on pending",
			initial: JobRoundStatusPending,
			act: func(ctx context.Context, s *JobService, j *ent.Job, r *ent.JobRound) error {
				return s.MarkJobRoundCompleted(ctx, r.ID)
			},
			wantStatus: JobRoundStatusPending,
		},
		{
			name:    "noop running on completed",
			initial: JobRoundStatusCompleted,
			act: func(ctx context.Context, s *JobService, j *ent.Job, r *ent.JobRound) error {
				return s.MarkJobRoundRunning(ctx, j.ID, r.ID)
			},
			wantStatus: JobRoundStatusCompleted,
		},
		{
			name:    "noop running on running",
			initial: JobRoundStatusRunning,
			act: func(ctx context.Context, s *JobService, j *ent.Job, r *ent.JobRound) error {
				return s.MarkJobRoundRunning(ctx, j.ID, r.ID)
			},
			wantStatus: JobRoundStatusRunning,
		},
		{
			// 裁决（原对称假设已废弃）：runner 先 MarkJobRoundRunning 再做
			// 空段检查，skip 必须同时接受 running——仅 pending 会 0 行命中
			// 且不报错，行将永久停留 running，「跳过」语义从不生效。
			name:    "skip running round (runner marks running before empty-segment check)",
			initial: JobRoundStatusRunning,
			act: func(ctx context.Context, s *JobService, j *ent.Job, r *ent.JobRound) error {
				return s.MarkJobRoundSkipped(ctx, r.ID)
			},
			wantStatus: JobRoundStatusSkipped,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newJobRoundTestEnv(t, nil)
			ctx := context.Background()
			job, jrs, rounds := seedJobWithRounds(t, env, JobStatusPending, 0, 0, translateRound(tt.initial))
			round := rounds[0][0]

			if err := tt.act(ctx, env.svc, job, round); err != nil {
				t.Fatalf("act: %v", err)
			}

			after, err := env.client.JobRound.Get(ctx, round.ID)
			if err != nil {
				t.Fatalf("get round: %v", err)
			}
			if after.Status != tt.wantStatus {
				t.Errorf("round status = %q, want %q", after.Status, tt.wantStatus)
			}
			// GetJobRoundStatus 应与库内状态一致。
			status, err := env.svc.GetJobRoundStatus(ctx, round.ID)
			if err != nil {
				t.Fatalf("GetJobRoundStatus: %v", err)
			}
			if status != tt.wantStatus {
				t.Errorf("GetJobRoundStatus = %q, want %q", status, tt.wantStatus)
			}
			if tt.wantErrMsg != "" {
				if after.ErrorMessage == nil || !strings.Contains(*after.ErrorMessage, tt.wantErrMsg) {
					t.Errorf("error_message = %v, want contains %q", after.ErrorMessage, tt.wantErrMsg)
				}
			}
			_ = jrs
		})
	}
}

// TestMarkJobRoundCompleted_ClosesJobProgressWithoutRewritingCheckpoint 校验
// completed 终态闭合任务级缺口而不改写断点计数：闭合是状态语义（读侧按
// jobRoundProgress 口径派生为满量），segment_completed 保持 ≡ 断点集合基数
// （DBReporter 的独占写入面、恢复重跑的进度基线）；Job 缓存补齐到 100/100。
func TestMarkJobRoundCompleted_ClosesJobProgressWithoutRewritingCheckpoint(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	job, _, rounds := seedJobWithRounds(t, env, JobStatusRunning, 100, 91, []jobResourceSpec{{
		status: JobResourceStatusRunning,
		rounds: []jobRoundSpec{{
			roundIndex: 0, mode: "translate", status: JobRoundStatusRunning,
			total: 100, completed: 91, resolvedCount: 91,
		}},
	}})
	roundRowID := rounds[0][0].ID
	beforeJoins := countRoundJoinRows(t, env.client, roundRowID)

	if err := env.svc.MarkJobRoundCompleted(ctx, roundRowID); err != nil {
		t.Fatalf("MarkJobRoundCompleted: %v", err)
	}

	// 计数列不被终态闭合改写：保持 100/91（≡ 断点集合基数），join 行数不变。
	assertRoundCheckpoint(t, env.client, env.client.JobRound.GetX(ctx, roundRowID), JobRoundStatusCompleted, 100, 91, beforeJoins)
	round := env.client.JobRound.GetX(ctx, roundRowID)
	if round.FinishedAt == nil {
		t.Fatal("completed round finished_at = nil")
	}
	// 任务级缺口仍被闭合：Job 缓存按闭合口径为 100/100。
	assertJobProgress(t, reloadJob(t, env.client, job.ID), 100, 100)
}

// TestMarkJobRoundCompleted_ZeroDeltaIsIdempotent 校验轮次已经完整时完成标记
// 不重复增加 Job.progress_completed，避免终态重试造成缓存超计。
func TestMarkJobRoundCompleted_ZeroDeltaIsIdempotent(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	job, _, rounds := seedJobWithRounds(t, env, JobStatusRunning, 100, 100, []jobResourceSpec{{
		status: JobResourceStatusRunning,
		rounds: []jobRoundSpec{{
			roundIndex: 0, mode: "translate", status: JobRoundStatusRunning,
			total: 100, completed: 100,
		}},
	}})

	if err := env.svc.MarkJobRoundCompleted(ctx, rounds[0][0].ID); err != nil {
		t.Fatalf("MarkJobRoundCompleted: %v", err)
	}
	assertRoundCheckpoint(t, env.client, env.client.JobRound.GetX(ctx, rounds[0][0].ID), JobRoundStatusCompleted, 100, 100, 0)
	assertJobProgress(t, reloadJob(t, env.client, job.ID), 100, 100)
}

// TestMarkJobRoundTerminal_NoOpPreservesProgress 校验状态不匹配时仍保持原有
// 条件更新语义：返回 nil、0 行受影响，不改变轮次字段或 Job 计数器。
func TestMarkJobRoundTerminal_NoOpPreservesProgress(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		mark       func(context.Context, *JobService, int) error
		wantStatus string
	}{
		{
			name:       "completed round marked completed",
			status:     JobRoundStatusCompleted,
			mark:       func(ctx context.Context, svc *JobService, id int) error { return svc.MarkJobRoundCompleted(ctx, id) },
			wantStatus: JobRoundStatusCompleted,
		},
		{
			name:       "pending round marked completed",
			status:     JobRoundStatusPending,
			mark:       func(ctx context.Context, svc *JobService, id int) error { return svc.MarkJobRoundCompleted(ctx, id) },
			wantStatus: JobRoundStatusPending,
		},
		{
			name:       "completed round marked skipped",
			status:     JobRoundStatusCompleted,
			mark:       func(ctx context.Context, svc *JobService, id int) error { return svc.MarkJobRoundSkipped(ctx, id) },
			wantStatus: JobRoundStatusCompleted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newJobRoundTestEnv(t, nil)
			ctx := context.Background()
			job, _, rounds := seedJobWithRounds(t, env, JobStatusRunning, 100, 60, []jobResourceSpec{{
				status: JobResourceStatusRunning,
				rounds: []jobRoundSpec{{
					roundIndex: 0, mode: "translate", status: tt.status,
					total: 100, completed: 60,
				}},
			}})
			before := env.client.JobRound.GetX(ctx, rounds[0][0].ID)
			if err := tt.mark(ctx, env.svc, before.ID); err != nil {
				t.Fatalf("mark terminal: %v", err)
			}
			after := env.client.JobRound.GetX(ctx, before.ID)
			if after.Status != tt.wantStatus {
				t.Errorf("round status = %q, want %q", after.Status, tt.wantStatus)
			}
			if after.SegmentTotal != before.SegmentTotal || after.SegmentCompleted != before.SegmentCompleted {
				t.Errorf("round progress changed from %d/%d to %d/%d", before.SegmentCompleted, before.SegmentTotal, after.SegmentCompleted, after.SegmentTotal)
			}
			if after.FinishedAt != nil {
				t.Errorf("finished_at = %v, want nil for no-op", after.FinishedAt)
			}
			assertJobProgress(t, reloadJob(t, env.client, job.ID), 100, 60)
		})
	}
}

// TestMarkJobRoundSkipped_ClosesFreshAndHistoricalProgress 校验 skipped 终态的边界：
// fresh 轮是 0/0 no-op；带历史进度的轮闭合为满量口径，但闭合只作用于 Job 缓存
// （wantDelta），轮次行的 segment_completed 保持断点集合基数不被改写。
func TestMarkJobRoundSkipped_ClosesFreshAndHistoricalProgress(t *testing.T) {
	tests := []struct {
		name              string
		status            string
		total, completed  int
		jobTotal, jobDone int64
		wantDelta         int64
	}{
		{name: "fresh pending", status: JobRoundStatusPending, total: 0, completed: 0, jobTotal: 0, jobDone: 0, wantDelta: 0},
		{name: "historical running", status: JobRoundStatusRunning, total: 100, completed: 60, jobTotal: 100, jobDone: 60, wantDelta: 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newJobRoundTestEnv(t, nil)
			ctx := context.Background()
			job, _, rounds := seedJobWithRounds(t, env, JobStatusRunning, tt.jobTotal, tt.jobDone, []jobResourceSpec{{
				status: JobResourceStatusRunning,
				rounds: []jobRoundSpec{{
					roundIndex: 0, mode: "translate", status: tt.status,
					total: tt.total, completed: tt.completed,
				}},
			}})
			if err := env.svc.MarkJobRoundSkipped(ctx, rounds[0][0].ID); err != nil {
				t.Fatalf("MarkJobRoundSkipped: %v", err)
			}
			// 轮次行：segment_completed 不被终态闭合改写（保持 seed 值）。
			// wantDelta 只作用于 Job 缓存（闭合口径增量），join 断点基数不变（seed 为 0）。
			assertRoundCheckpoint(t, env.client, env.client.JobRound.GetX(ctx, rounds[0][0].ID), JobRoundStatusSkipped, tt.total, tt.completed, 0)
			assertJobProgress(t, reloadJob(t, env.client, job.ID), tt.jobTotal, tt.jobDone+tt.wantDelta)
		})
	}
}

// TestMarkJobRoundTerminal_ReconcileRemainsStable 校验终态补齐后矩阵求和与 Job
// 缓存一致：ReconcileJob 只会把同一 100/100 求和重新写回，不产生跳变。
func TestMarkJobRoundTerminal_ReconcileRemainsStable(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	job, _, rounds := seedJobWithRounds(t, env, JobStatusRunning, 100, 91, []jobResourceSpec{{
		status: JobResourceStatusRunning,
		rounds: []jobRoundSpec{{
			roundIndex: 0, mode: "translate", status: JobRoundStatusRunning,
			total: 100, completed: 91,
		}},
	}})

	if err := env.svc.MarkJobRoundCompleted(ctx, rounds[0][0].ID); err != nil {
		t.Fatalf("MarkJobRoundCompleted: %v", err)
	}
	before := reloadJob(t, env.client, job.ID)
	if err := env.svc.ReconcileJob(ctx, job.ID); err != nil {
		t.Fatalf("ReconcileJob: %v", err)
	}
	after := reloadJob(t, env.client, job.ID)
	if after.ProgressTotal != before.ProgressTotal || after.ProgressCompleted != before.ProgressCompleted {
		t.Errorf("progress changed after reconcile from %d/%d to %d/%d", before.ProgressCompleted, before.ProgressTotal, after.ProgressCompleted, after.ProgressTotal)
	}
	assertJobProgress(t, after, 100, 100)
}

// TestMarkJobRoundFailed_PreservesPartialProgress 校验 failed 轮不套用终态闭合
// 语义：失败仍保留部分进度，交由恢复/重试继续执行。
func TestMarkJobRoundFailed_PreservesPartialProgress(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	job, _, rounds := seedJobWithRounds(t, env, JobStatusRunning, 100, 60, []jobResourceSpec{{
		status: JobResourceStatusRunning,
		rounds: []jobRoundSpec{{
			roundIndex: 0, mode: "translate", status: JobRoundStatusRunning,
			total: 100, completed: 60,
		}},
	}})

	if err := env.svc.MarkJobRoundFailed(ctx, rounds[0][0].ID, errors.New("boom")); err != nil {
		t.Fatalf("MarkJobRoundFailed: %v", err)
	}
	assertRoundCheckpoint(t, env.client, env.client.JobRound.GetX(ctx, rounds[0][0].ID), JobRoundStatusFailed, 100, 60, 0)
	assertJobProgress(t, reloadJob(t, env.client, job.ID), 100, 60)
}

// TestGetJobRoundStatus_MissingRound 不存在的轮次行必须返回错误。
func TestGetJobRoundStatus_MissingRound(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	if _, err := env.svc.GetJobRoundStatus(context.Background(), 99999); err == nil {
		t.Fatal("expected error for missing round, got nil")
	}
}

// ---- 2. 暂停 ----

// TestPauseJob_StatePrecondition 覆盖 PauseJob 的状态前置：
// pending → 直接置 paused（NeedsDrain=false，发布 job_paused 事件）；
// running → 仅请求暂停（NeedsDrain=true，任务/资源冻结在 running，等待 drain）；
// completed/failed/cancelled → ErrJobNotPausable。
func TestPauseJob_StatePrecondition(t *testing.T) {
	tests := []struct {
		name       string
		jobStatus  string
		wantErr    error
		wantDrain  bool
		wantJobSts string // 成功用例校验暂停请求后的任务状态
	}{
		{name: "pending pauses immediately", jobStatus: JobStatusPending, wantDrain: false, wantJobSts: JobStatusPaused},
		{name: "running needs drain", jobStatus: JobStatusRunning, wantDrain: true, wantJobSts: JobStatusRunning},
		{name: "completed rejected", jobStatus: JobStatusCompleted, wantErr: ErrJobNotPausable},
		{name: "failed rejected", jobStatus: JobStatusFailed, wantErr: ErrJobNotPausable},
		{name: "cancelled rejected", jobStatus: JobStatusCancelled, wantErr: ErrJobNotPausable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			broker := event.NewBroker(nil)
			env := newJobRoundTestEnv(t, broker)
			ctx := context.Background()
			resStatus := JobResourceStatusPending
			if tt.jobStatus == JobStatusRunning {
				resStatus = JobResourceStatusRunning
			}
			job, jrs, _ := seedJobWithRounds(t, env, tt.jobStatus, 0, 0, []jobResourceSpec{{
				status: resStatus,
				rounds: []jobRoundSpec{{roundIndex: 0, mode: "translate", status: JobRoundStatusPending}},
			}})

			ch := broker.Subscribe(job.ID)
			t.Cleanup(func() { broker.Unsubscribe(job.ID, ch) })

			got, err := env.svc.PauseJob(ctx, env.user.ID, job.ID)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("PauseJob: %v", err)
			}
			if got.NeedsDrain != tt.wantDrain {
				t.Errorf("NeedsDrain = %v, want %v", got.NeedsDrain, tt.wantDrain)
			}
			after := reloadJob(t, env.client, job.ID)
			if after.Status != tt.wantJobSts {
				t.Errorf("job status = %q, want %q", after.Status, tt.wantJobSts)
			}
			// running 用例：暂停请求只做标记，资源冻结在 running（见 job_resource schema 注释）。
			if tt.jobStatus == JobStatusRunning {
				jrAfter, err := env.client.JobResource.Get(ctx, jrs[0].ID)
				if err != nil {
					t.Fatalf("get jr: %v", err)
				}
				if jrAfter.Status != JobResourceStatusRunning {
					t.Errorf("resource status = %q, want %q（暂停期间资源冻结在 running）", jrAfter.Status, JobResourceStatusRunning)
				}
			}
			// pending 用例：直接暂停必须发布 job_paused 事件。
			if tt.jobStatus == JobStatusPending {
				var found bool
				deadline := time.After(2 * time.Second)
				for !found {
					select {
					case evt := <-ch:
						if evt.Type == "job_paused" {
							found = true
						}
					case <-deadline:
						t.Fatal("expected job_paused event after PauseJob(pending), none received")
					}
				}
			}
		})
	}
}

// TestMarkJobPaused_FromRunning 覆盖 drain 完成后的落库标记：running → paused。
func TestMarkJobPaused_FromRunning(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	job, _, _ := seedJobWithRounds(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{
		{status: JobResourceStatusRunning},
	})
	if err := env.svc.MarkJobPaused(context.Background(), job.ID); err != nil {
		t.Fatalf("MarkJobPaused: %v", err)
	}
	after := reloadJob(t, env.client, job.ID)
	if after.Status != JobStatusPaused {
		t.Errorf("job status = %q, want %q", after.Status, JobStatusPaused)
	}
}

// ---- 3. 恢复（Resume） ----

// TestResumeJob_PreservesRoundCheckpoint 覆盖暂停恢复：
// paused 任务 → pending；running 资源/轮次 → pending；
// 断点字段（job_round_segments 关联行 / segment_total / segment_completed）全部保留；
// 派生计数器按矩阵无条件求和重算。
func TestResumeJob_PreservesRoundCheckpoint(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	job, jrs, _ := seedJobWithRounds(t, env, JobStatusPaused, 0, 0, []jobResourceSpec{
		{
			status: JobResourceStatusRunning,
			rounds: []jobRoundSpec{{
				roundIndex: 0, mode: "extract", status: JobRoundStatusRunning,
				total: 40, completed: 12, resolvedCount: 2,
			}},
		},
		{
			status: JobResourceStatusPending,
			rounds: []jobRoundSpec{{roundIndex: 0, mode: "translate", status: JobRoundStatusPending}},
		},
	})

	got, err := env.svc.ResumeJob(ctx, env.user.ID, job.ID)
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	if got.Status != JobStatusPending {
		t.Errorf("job status = %q, want %q", got.Status, JobStatusPending)
	}

	// running 资源重置为 pending；原本 pending 的资源不受影响。
	for i, want := range []string{JobResourceStatusPending, JobResourceStatusPending} {
		jrAfter, err := env.client.JobResource.Get(ctx, jrs[i].ID)
		if err != nil {
			t.Fatalf("get jr[%d]: %v", i, err)
		}
		if jrAfter.Status != want {
			t.Errorf("resource[%d] status = %q, want %q", i, jrAfter.Status, want)
		}
	}

	// 断点字段全部保留（重置只改状态，不清历史）。
	roundsAfter := loadResourceRounds(t, env.client, jrs[0].ID)
	if len(roundsAfter) != 1 {
		t.Fatalf("rounds len = %d, want 1", len(roundsAfter))
	}
	assertRoundCheckpoint(t, env.client, roundsAfter[0], JobRoundStatusPending, 40, 12, 2)

	// 新鲜 pending 轮保持 0/0。
	freshRounds := loadResourceRounds(t, env.client, jrs[1].ID)
	if len(freshRounds) != 1 {
		t.Fatalf("fresh rounds len = %d, want 1", len(freshRounds))
	}
	assertRoundCheckpoint(t, env.client, freshRounds[0], JobRoundStatusPending, 0, 0, 0)

	// 无条件求和：progress_total = 40 + 0 = 40；progress_completed = 12 + 0 = 12。
	assertJobProgress(t, reloadJob(t, env.client, job.ID), 40, 12)
}

// TestResumeJob_NotResumable 非 paused 任务不可恢复。
func TestResumeJob_NotResumable(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	job, _, _ := seedJobWithRounds(t, env, JobStatusCompleted, 0, 0, []jobResourceSpec{
		{status: JobResourceStatusCompleted},
	})
	if _, err := env.svc.ResumeJob(context.Background(), env.user.ID, job.ID); !errors.Is(err, ErrJobNotResumable) {
		t.Fatalf("err = %v, want %v", err, ErrJobNotResumable)
	}
}

// ---- 4. 恢复（Recover）：防重复累计不变量 ----

// TestRecoverPendingJobs_NoDoubleAccumulation 是矩阵不变量的核心回归测试。
//
// seed：r0 completed(100/100)，r1 running(50/20)，r2 pending(0/0)，
// 任务缓存 progress_total=150 / progress_completed=120（worker 上次落库的值）。
// want：r1 → pending 且断点字段原样保留；任务缓存重算后仍必须是 150/120。
//
// 无条件求和公式：
//
//	progress_total     = Σ segment_total(全部行)     = 100 + 50 + 0 = 150
//	progress_completed = Σ segment_completed(全部行) = 100 + 20 + 0 = 120
//
// 若实现 naive 地把矩阵和再加到旧缓存上会得到 300/240；
// 若按状态过滤（只累计 completed 行）会得到 100/100 或 100/120 —— 两者均为回归。
func TestRecoverPendingJobs_NoDoubleAccumulation(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	job, jrs, _ := seedJobWithRounds(t, env, JobStatusRunning, 150, 120, []jobResourceSpec{{
		status: JobResourceStatusRunning,
		rounds: []jobRoundSpec{
			{roundIndex: 0, mode: "translate", status: JobRoundStatusCompleted, total: 100, completed: 100},
			{roundIndex: 1, mode: "extract", status: JobRoundStatusRunning, total: 50, completed: 20, resolvedCount: 1},
			{roundIndex: 2, mode: "semantic_qa", status: JobRoundStatusPending},
		},
	}})

	ids, err := env.svc.RecoverPendingJobs(ctx)
	if err != nil {
		t.Fatalf("RecoverPendingJobs: %v", err)
	}
	if !intSliceContains(ids, job.ID) {
		t.Errorf("recovered ids = %v, want contains %d", ids, job.ID)
	}

	// running 资源 → pending。
	jrAfter, err := env.client.JobResource.Get(ctx, jrs[0].ID)
	if err != nil {
		t.Fatalf("get jr: %v", err)
	}
	if jrAfter.Status != JobResourceStatusPending {
		t.Errorf("resource status = %q, want %q", jrAfter.Status, JobResourceStatusPending)
	}

	// r1：running → pending，断点字段保留。
	roundsAfter := loadResourceRounds(t, env.client, jrs[0].ID)
	if len(roundsAfter) != 3 {
		t.Fatalf("rounds len = %d, want 3", len(roundsAfter))
	}
	assertRoundCheckpoint(t, env.client, roundsAfter[1], JobRoundStatusPending, 50, 20, 1)
	// r0：completed 终态行不得被重置。
	assertRoundCheckpoint(t, env.client, roundsAfter[0], JobRoundStatusCompleted, 100, 100, 0)

	// 任务状态 running → pending；派生缓存重算后恰为 150/120（求和公式见函数注释）。
	after := reloadJob(t, env.client, job.ID)
	if after.Status != JobStatusPending {
		t.Errorf("job status = %q, want %q", after.Status, JobStatusPending)
	}
	assertJobProgress(t, after, 150, 120)
}

// TestRecoverPendingJobs_LegacyBackfill 覆盖无矩阵历史行（旧版任务）的回填：
// 恢复后该资源必须存在至少一条 pending 轮次行。
// 注意：回填的精确形态（模式、segment_total 取值）以生产实现为准，此处只做宽松断言。
func TestRecoverPendingJobs_LegacyBackfill(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	job, jrs, _ := seedJobWithRounds(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{
		{status: JobResourceStatusRunning, segmentCount: 3},
	})

	if _, err := env.svc.RecoverPendingJobs(ctx); err != nil {
		t.Fatalf("RecoverPendingJobs: %v", err)
	}

	// 任务 running → pending。
	after := reloadJob(t, env.client, job.ID)
	if after.Status != JobStatusPending {
		t.Errorf("job status = %q, want %q", after.Status, JobStatusPending)
	}

	jrAfter, err := env.client.JobResource.Get(ctx, jrs[0].ID)
	if err != nil {
		t.Fatalf("get jr: %v", err)
	}
	if jrAfter.Status != JobResourceStatusPending {
		t.Errorf("resource status = %q, want %q", jrAfter.Status, JobResourceStatusPending)
	}
	rounds := loadResourceRounds(t, env.client, jrs[0].ID)
	if len(rounds) == 0 {
		t.Fatal("legacy job backfill: expected at least one JobRound row, got 0")
	}
	for _, r := range rounds {
		if r.Status != JobRoundStatusPending {
			t.Errorf("backfilled round %d status = %q, want %q", r.ID, r.Status, JobRoundStatusPending)
		}
	}
}

// ---- 5. Reconcile：矩阵聚合 ----

// TestReconcileJob_MatrixAggregation 覆盖 ReconcileJob 的无条件矩阵聚合：
//
//	progress_total     = Σ segment_total(全部行)     = 100 + 50 + 0 + 10 = 160
//	progress_completed = Σ segment_completed(全部行) = 100 + 20 + 0 + 4  = 124
//
// failed 轮的总量/完成量同样计入求和（状态只影响任务级状态推导，不影响工作量口径）。
func TestReconcileJob_MatrixAggregation(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	job, _, _ := seedJobWithRounds(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{{
		status: JobResourceStatusRunning,
		rounds: []jobRoundSpec{
			{roundIndex: 0, mode: "translate", status: JobRoundStatusCompleted, total: 100, completed: 100},
			{roundIndex: 1, mode: "translate", status: JobRoundStatusRunning, total: 50, completed: 20},
			{roundIndex: 2, mode: "semantic_qa", status: JobRoundStatusPending},
			{roundIndex: 3, mode: "correct", status: JobRoundStatusFailed, total: 10, completed: 4, errMessage: "boom"},
		},
	}})

	if err := env.svc.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatalf("ReconcileJob: %v", err)
	}
	assertJobProgress(t, reloadJob(t, env.client, job.ID), 160, 124)
}

// TestReconcileJob_PausedPreserved 暂停中的任务经 ReconcileJob 不得被状态推导改写：
// 资源仍冻结在 running，朴素推导会得到 running；paused 必须原样保留。
func TestReconcileJob_PausedPreserved(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	job, _, _ := seedJobWithRounds(t, env, JobStatusPaused, 0, 0, []jobResourceSpec{{
		status: JobResourceStatusRunning,
		rounds: []jobRoundSpec{
			{roundIndex: 0, mode: "translate", status: JobRoundStatusRunning, total: 40, completed: 12},
		},
	}})

	if err := env.svc.ReconcileJob(context.Background(), job.ID); err != nil {
		t.Fatalf("ReconcileJob: %v", err)
	}
	after := reloadJob(t, env.client, job.ID)
	if after.Status != JobStatusPaused {
		t.Errorf("job status = %q, want %q（paused 不得被 ReconcileJob 改写）", after.Status, JobStatusPaused)
	}
	assertJobProgress(t, after, 40, 12)
}

// ---- 6. 取消任务重试（矩阵化） ----

// TestRetryJob_CancelledJobWithMatrix 覆盖取消任务的矩阵化重试：
// cancelled 任务与 failed/cancelled 资源均可重试 → pending；
// failed 轮 → pending 且 job_round_segments 断点关联行保留；completed 轮终态不动；
// 派生计数器按矩阵无条件求和重算（旧缓存值被整体覆盖）：
//
//	progress_total     = 25 + 10 = 35
//	progress_completed = 8 + 10 = 18
func TestRetryJob_CancelledJobWithMatrix(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	// 预置明显失真的旧缓存（999/888），重试后必须被矩阵求和覆盖。
	job, jrs, _ := seedJobWithRounds(t, env, JobStatusCancelled, 999, 888, []jobResourceSpec{
		{
			status: JobResourceStatusFailed,
			rounds: []jobRoundSpec{{
				roundIndex: 0, mode: "extract", status: JobRoundStatusFailed,
				total: 25, completed: 8, resolvedCount: 2, errMessage: "boom",
			}},
		},
		{
			status: JobResourceStatusCancelled,
			rounds: []jobRoundSpec{{
				roundIndex: 0, mode: "translate", status: JobRoundStatusCompleted,
				total: 10, completed: 10,
			}},
		},
	})

	got, err := env.svc.RetryJob(ctx, env.user.ID, job.ID)
	if err != nil {
		t.Fatalf("RetryJob: %v", err)
	}
	if got.Status != JobStatusPending {
		t.Errorf("job status = %q, want %q", got.Status, JobStatusPending)
	}

	// failed 与 cancelled 资源均重置为 pending。
	for i := range jrs {
		jrAfter, err := env.client.JobResource.Get(ctx, jrs[i].ID)
		if err != nil {
			t.Fatalf("get jr[%d]: %v", i, err)
		}
		if jrAfter.Status != JobResourceStatusPending {
			t.Errorf("resource[%d] status = %q, want %q", i, jrAfter.Status, JobResourceStatusPending)
		}
	}

	// failed 轮 → pending，断点保留。
	failedRounds := loadResourceRounds(t, env.client, jrs[0].ID)
	if len(failedRounds) != 1 {
		t.Fatalf("failed-resource rounds len = %d, want 1", len(failedRounds))
	}
	assertRoundCheckpoint(t, env.client, failedRounds[0], JobRoundStatusPending, 25, 8, 2)

	// completed 轮终态不动。
	doneRounds := loadResourceRounds(t, env.client, jrs[1].ID)
	if len(doneRounds) != 1 {
		t.Fatalf("completed-resource rounds len = %d, want 1", len(doneRounds))
	}
	assertRoundCheckpoint(t, env.client, doneRounds[0], JobRoundStatusCompleted, 10, 10, 0)

	// 派生缓存 = 矩阵无条件求和。
	assertJobProgress(t, reloadJob(t, env.client, job.ID), 35, 18)
}
