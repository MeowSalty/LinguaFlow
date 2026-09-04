package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/sysmem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// JobRunner 任务执行器，实现 TaskRunner 接口。
type JobRunner struct {
	logger      *slog.Logger
	client      *ent.Client
	jobs        *service.JobService
	store       *filestore.LocalStore
	queue       *Queue
	eventBroker *event.Broker
	limiterPool *backend.LimiterPool
	resMutex    *ResourceMutex
	// dbDriver 标识数据库驱动（config.DatabaseDriverPostgres /
	// DatabaseDriverSQLite），用于 batchHandler 中的写入错误分级。
	dbDriver string

	// pipeCfg 流水线准入预算（字节配额与资源数上限）。
	pipeCfg PipelineConfig
	// rssFuse 进程级 RSS 双水位保险丝；nil 时禁用。
	rssFuse *sysmem.Gate

	// per-job 取消注册表：jobID → 取消句柄（包装结构体使 defer 可按值
	// 身份比较删除——cancel 函数本身只能与 nil 比较）
	mu         sync.Mutex
	activeJobs map[int]*jobCancelEntry
	// per-job 暂停闸门注册表：jobID → gate
	gates map[int]*pipeline.PauseGate
}

// jobCancelEntry 包装 per-job cancel：指针可比较，供旧执行的 defer
// 判别「注册表里还是不是本次注册」，避免误删 resume 后新执行的注册项。
type jobCancelEntry struct {
	cancel context.CancelFunc
}

// PipelineConfig 流水线准入配置（由 server 从 WorkerConfig 注入）。
type PipelineConfig struct {
	// MaxInflightWeight 在途工作配额上限（源文本字节；0 = 不限制）。
	MaxInflightWeight int64
	// MaxInflightResources 在途资源数上限（0 = 不限制）。
	MaxInflightResources int
}

// NewJobRunner 创建一个新的任务执行器。
func NewJobRunner(
	logger *slog.Logger,
	client *ent.Client,
	jobs *service.JobService,
	store *filestore.LocalStore,
	queue *Queue,
	eventBroker *event.Broker,
	limiterPool *backend.LimiterPool,
	resMutex *ResourceMutex,
	dbDriver string,
	pipeCfg PipelineConfig,
	rssFuse *sysmem.Gate,
) *JobRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &JobRunner{
		logger:      logger,
		client:      client,
		jobs:        jobs,
		store:       store,
		queue:       queue,
		eventBroker: eventBroker,
		limiterPool: limiterPool,
		resMutex:    resMutex,
		dbDriver:    dbDriver,
		pipeCfg:     pipeCfg,
		rssFuse:     rssFuse,
		activeJobs:  make(map[int]*jobCancelEntry),
		gates:       make(map[int]*pipeline.PauseGate),
	}
}

// Type 返回任务类型标识。
func (r *JobRunner) Type() string {
	return "translation"
}

// Queue 返回此 Runner 的任务队列。
func (r *JobRunner) Queue() *Queue {
	return r.queue
}

// ProcessOne 处理单个翻译任务，不负责 Dequeue/Done。
func (r *JobRunner) ProcessOne(ctx context.Context, jobID int) error {
	return r.processJob(ctx, jobID)
}

// Run 从队列中取任务并执行，直到 ctx 取消。
func (r *JobRunner) Run(ctx context.Context) error {
	for {
		jobID, err := r.queue.Dequeue(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		if err := r.processJob(ctx, jobID); err != nil {
			r.logger.Error("job worker: process job failed", "job_id", jobID, "err", err)
		}
		r.queue.Done(jobID)
	}
}

// Cancel 通知运行中的翻译任务立即停止。
func (r *JobRunner) Cancel(taskID int) {
	r.mu.Lock()
	entry, ok := r.activeJobs[taskID]
	r.mu.Unlock()
	if ok {
		r.logger.Info("cancelling running job", "job_id", taskID)
		entry.cancel()
	}
}

// Recover 从数据库恢复挂起的任务并重新入队。
func (r *JobRunner) Recover(ctx context.Context) ([]int, error) {
	jobIDs, err := r.jobs.RecoverPendingJobs(ctx)
	if err != nil {
		return nil, err
	}
	return jobIDs, nil
}

// processJob 处理单个翻译任务（流水线模式）：所有 pending 资源并发入线，
// 每资源一个 goroutine 按序执行自己的轮次；同轮所有在途资源共享该轮
// 站位并发预算（CPU 工位模型）。准入控制（字节配额 + 资源数上限 + RSS
// 保险丝）约束同时在途的资源；pause gate 在安全点优雅排空。
func (r *JobRunner) processJob(ctx context.Context, jobID int) error {
	// 创建 per-job context，支持外部取消
	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()

	// 注册到 activeJobs，使 Cancel 能触发取消。按值身份删除：与
	// unregisterGate 同理，避免旧执行的 defer 误删 resume 后新执行注册的 cancel。
	entry := &jobCancelEntry{cancel: jobCancel}
	r.mu.Lock()
	r.activeJobs[jobID] = entry
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.activeJobs[jobID] == entry {
			delete(r.activeJobs, jobID)
		}
		r.mu.Unlock()
	}()

	exec, err := r.jobs.LoadJobExecution(jobCtx, jobID)
	if err != nil {
		return err
	}
	// 二次校验：任务可能在入队后、执行前被取消或暂停
	if exec.Job.Status == service.JobStatusCancelled || exec.Job.Status == service.JobStatusPaused {
		r.logger.Info("job already cancelled or paused, skipping", "job_id", jobID, "status", exec.Job.Status)
		return nil
	}
	pending := make([]*ent.JobResource, 0, len(exec.JobResources))
	for _, item := range exec.JobResources {
		if item.Status == service.JobResourceStatusPending || item.Status == service.JobResourceStatusRunning {
			pending = append(pending, item)
		}
	}

	snapshot, err := r.jobs.GetExecutionSnapshot(jobCtx, jobID)
	if err != nil {
		return err
	}

	if len(pending) > 0 {
		// 流水线编排：轮次行注册表、暂停闸门、准入预算、站位信号量。
		registry, err := loadJobRounds(jobCtx, r.client, jobID)
		if err != nil {
			return err
		}
		// 暂停闸门先于 MarkJobRunning 注册：消除「DB 已 running 但 gate 尚未
		// 注册」窗口内暂停请求的静默丢失（PauseTask 查不到 gate 返回 false）。
		// 翻转前到达的暂停走 pending→paused 直接翻转，随后 MarkJobRunning
		// 条件更新不命中，runner 干净退出（defer 注销 gate）。
		gate := pipeline.NewPauseGate()
		r.registerGate(jobID, gate)
		defer r.unregisterGate(jobID, gate)

		if err := r.jobs.MarkJobRunning(jobCtx, jobID); err != nil {
			// 条件更新未命中：任务在入队后、执行前被并发暂停/取消。
			if errors.Is(err, service.ErrJobNotRunnable) {
				r.logger.Info("job not runnable anymore, skipping", "job_id", jobID)
				return nil
			}
			return err
		}
		// 记录任务开始时间
		_ = r.jobs.MarkJobStarted(jobCtx, jobID)

		adm := newAdmission(r.pipeCfg.MaxInflightWeight, r.pipeCfg.MaxInflightResources)

		// 站位信号量：round_index → Station（容量 = 该轮快照 concurrency）。
		stations := make([]*pipeline.Station, len(snapshot.Rounds))
		for i := range snapshot.Rounds {
			stations[i] = pipeline.NewStation(roundConcurrency(snapshot, i))
		}

		var wg sync.WaitGroup
		for _, item := range pending {
			wg.Add(1)
			go func(item *ent.JobResource) {
				defer wg.Done()
				r.runPipelineResource(jobCtx, exec, snapshot, item, registry, gate, adm, stations)
			}(item)
		}
		wg.Wait()

		// 排空后暂停：所有资源 goroutine 退出（在途请求已返回并持久化），
		// 置任务为 paused（未取消时）。
		if gate.Paused() && jobCtx.Err() == nil {
			if err := r.jobs.MarkJobPaused(jobCtx, jobID); err != nil {
				r.logger.Warn("failed to mark job paused", "job_id", jobID, "err", err)
			}
		}
	}
	// 收尾 reconcile 用独立 ctx：取消路径下 jobCtx 已失效，但终态聚合
	//（矩阵重算 + 状态推导 + 终态事件）仍须落库；超时防悬挂 worker。
	reconcileCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reconcileErr := r.jobs.ReconcileJob(reconcileCtx, jobID)
	r.eventBroker.Purge(jobID)
	return reconcileErr
}

// roundConcurrency 从快照提取某轮的站位容量（concurrency 字段）。
func roundConcurrency(snapshot *service.JobExecutionSnapshot, roundIdx int) int {
	if roundIdx < 0 || roundIdx >= len(snapshot.Rounds) {
		return 1
	}
	rd := snapshot.Rounds[roundIdx]
	switch rd.Mode {
	case "translate":
		if rd.Translate != nil {
			return rd.Translate.Concurrency
		}
	case "extract":
		if rd.Extract != nil {
			return rd.Extract.Concurrency
		}
	case "adjudicate":
		if rd.Adjudicate != nil {
			return rd.Adjudicate.Concurrency
		}
	case "semantic_qa":
		if rd.SemanticQA != nil {
			return rd.SemanticQA.Concurrency
		}
	case "revise":
		if rd.Revise != nil {
			return rd.Revise.Concurrency
		}
	case "correct":
		if rd.Correct != nil {
			return rd.Correct.Concurrency
		}
	}
	return 1
}

// runPipelineResource 单资源的流水线执行：准入 → 轮次循环（恢复断点 →
// 站位注入 → 执行 → 持久化 resolved）。返回前保证准入预算已归还。
func (r *JobRunner) runPipelineResource(
	jobCtx context.Context,
	exec *service.JobExecution,
	snapshot *service.JobExecutionSnapshot,
	item *ent.JobResource,
	registry *roundRegistry,
	gate *pipeline.PauseGate,
	adm *admission,
	stations []*pipeline.Station,
) {
	// 动态选择资源：首次入线加载选择集时回填 work_weight（准入从首次入线起算）。
	// 权重计算/回填失败时资源以最小单元（weightAllow(0)=1）准入——绕过字节
	// 配额但无功能错误；记 Warn 保持该降级路径可观测，避免瞬时 DB 错误
	// 静默放行大资源且无人知晓。
	weight := item.WorkWeight
	if weight == 0 && len(item.SegmentIds) == 0 {
		w, err := r.computeWorkWeight(jobCtx, item)
		switch {
		case err != nil:
			r.logger.Warn("compute work weight failed, resource admitted with minimal weight",
				"job_id", exec.Job.ID, "job_resource_id", item.ID, "err", err)
		case w > 0:
			weight = w
			if uerr := r.client.JobResource.UpdateOneID(item.ID).SetWorkWeight(w).Exec(jobCtx); uerr != nil {
				r.logger.Warn("persist work weight failed, will recompute on next run",
					"job_id", exec.Job.ID, "job_resource_id", item.ID, "err", uerr)
			}
		}
	}

	// 准入：字节配额 + 资源数上限 + RSS 保险丝（pull 型：每次准入尝试
	// 调用 Allow() 推进双水位状态机；熔断中资源排队、在途继续——只出不进）。
	for {
		if jobCtx.Err() != nil {
			return
		}
		if gate.Paused() {
			return // 暂停：未入线资源直接退出（resume 重新入队）
		}
		if r.rssFuse != nil && !r.rssFuse.Allow() {
			// 熔断中：不占用配额，等待水位回落（1s 轮询）。
			select {
			case <-jobCtx.Done():
				return
			case <-gate.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if err := adm.admit(weight); err == nil {
			if r.pipeCfg.MaxInflightWeight > 0 && weightAllow(weight) > r.pipeCfg.MaxInflightWeight {
				// 超预算单资源独跑（admit 空预算放行）：峰值在途字节短暂
				// 超过配额，内存后盾由进程级 RSS 保险丝兜底。
				r.logger.Warn("resource work weight exceeds inflight budget, admitting solo",
					"job_id", exec.Job.ID, "job_resource_id", item.ID,
					"work_weight_bytes", weight, "budget_bytes", r.pipeCfg.MaxInflightWeight)
			}
			break
		} else if !errAdmissionRetryable(err) {
			r.logger.Error("admission failed permanently, resource aborted",
				"job_id", exec.Job.ID, "job_resource_id", item.ID, "err", err)
			_ = r.jobs.MarkJobResourceFailed(jobCtx, exec.Job.ID, item.ID, err)
			return
		}
		select {
		case <-jobCtx.Done():
			return
		case <-gate.Done():
			return
		case <-time.After(time.Second):
		}
	}
	defer adm.release(weight)

	if err := r.processJobResource(jobCtx, exec, snapshot, item, registry, gate, stations); err != nil {
		r.logger.Warn("job resource failed", "job_id", exec.Job.ID, "job_resource_id", item.ID, "err", err)
	}
}

// computeWorkWeight 聚合动态选择资源（segment_ids 为空）全部段落的
// source_text 字节数，作为准入权重回填。经 service 层 ent 自定义聚合
// 实现字节口径跨 SQLite/PostgreSQL 一致。
func (r *JobRunner) computeWorkWeight(ctx context.Context, item *ent.JobResource) (int64, error) {
	res, err := item.Edges.ResourceOrErr()
	if err != nil {
		return 0, err
	}
	return service.SumResourceWorkWeight(ctx, r.client, res.ID)
}

// registerGate / unregisterGate 维护 jobID → pause gate 注册表。
func (r *JobRunner) registerGate(jobID int, gate *pipeline.PauseGate) {
	r.mu.Lock()
	if r.gates == nil {
		r.gates = make(map[int]*pipeline.PauseGate)
	}
	r.gates[jobID] = gate
	r.mu.Unlock()
}

// unregisterGate 注销本次执行注册的 gate。按值身份删除：MarkJobPaused 之后、
// 本 defer 执行之前的窗口内 resume 可启动新执行并注册新 gate——若按 jobID
// 无条件 delete 会误删新执行的 gate（此后 PauseTask 恒 miss、暂停 API 恒 409）。
func (r *JobRunner) unregisterGate(jobID int, gate *pipeline.PauseGate) {
	r.mu.Lock()
	if r.gates[jobID] == gate {
		delete(r.gates, jobID)
	}
	r.mu.Unlock()
}

// Pause 通知运行中的任务优雅排空（等待在途 LLM 请求返回后冻结）。
// 返回 false 表示任务未在运行（由 service 层处理 pending 态暂停）。
func (r *JobRunner) Pause(taskID int) bool {
	r.mu.Lock()
	gate, ok := r.gates[taskID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	gate.Pause()
	r.logger.Info("pausing running job", "job_id", taskID)
	return true
}

// processJobResource 处理单个翻译资源：从 DB 加载段落、轮次循环翻译、写回 DB。
// registry/gate/stations 为流水线编排注入物（nil 时退化为单资源直跑，
// 供测试与 CLI 路径复用）。
func (r *JobRunner) processJobResource(
	ctx context.Context,
	exec *service.JobExecution,
	snapshot *service.JobExecutionSnapshot,
	item *ent.JobResource,
	registry *roundRegistry,
	gate *pipeline.PauseGate,
	stations []*pipeline.Station,
) error {
	job := exec.Job

	if err := r.jobs.MarkJobResourceRunning(ctx, job.ID, item.ID); err != nil {
		// 条件更新未命中：资源已被并发取消/置终态，静默跳过。
		if errors.Is(err, service.ErrJobResourceNotRunnable) {
			return nil
		}
		return err
	}
	_ = r.jobs.MarkJobResourceStarted(ctx, item.ID)

	reporter := progress.NewDBReporter(progress.DBReporterOptions{
		Client:        r.client,
		JobID:         exec.Job.ID,
		JobResourceID: item.ID,
		Logger:        r.logger,
		Broker:        r.eventBroker,
	})
	defer reporter.Close()

	res, err := item.Edges.ResourceOrErr()
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
		return nil
	}

	// 获取 Resource 级互斥锁
	if r.resMutex != nil {
		release, err := r.resMutex.Acquire(ctx, res.ID)
		if err != nil {
			_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, fmt.Errorf("acquire resource lock: %w", err))
			return nil
		}
		defer release()
	}

	// 断点恢复：从 JobRound 行装载各非翻译轮已解决段集合（DB Segment ID），
	// 转 docIndex 后注入 resolvedByMode（跨同模式轮累积的并集语义）。
	// registry 为 nil（单资源路径）时跳过——无 JobRound 行。
	var resolvedByMode map[string]map[int]struct{} = engine.NewResolvedByMode()
	var dbIDToIndexBootstrap map[int]int
	if registry != nil {
		persisted, err := loadResolved(ctx, r.client, item.ID)
		if err != nil {
			r.logger.Warn("load resolved checkpoints failed, rounds will rescan",
				"job_id", job.ID, "job_resource_id", item.ID, "err", err)
		} else if len(persisted) > 0 {
			// DB ID → docIndex 映射依赖每轮重载（loadSegments 顺序确定），
			// 此处仅用 ID 与顺序建立映射：全列加载（含 source_text 大文本）
			// 读出即弃是启动期读放大，改用单列投影；排序须与轮次循环的
			// loadSegments 一致（Asc(SegmentIndex), Asc(ID)）。
			idRows, loadErr := r.client.Segment.Query().
				Where(segment.ResourceIDEQ(res.ID)).
				Order(ent.Asc(segment.FieldSegmentIndex), ent.Asc(segment.FieldID)).
				Select(segment.FieldID).
				All(ctx)
			if loadErr == nil {
				dbIDToIndexBootstrap = make(map[int]int, len(idRows))
				for i, row := range idRows {
					dbIDToIndexBootstrap[row.ID] = i
				}
				for mode, dbIDs := range persisted {
					if set, ok := resolvedByMode[mode]; ok {
						for dbID := range dbIDs {
							if idx, ok2 := dbIDToIndexBootstrap[dbID]; ok2 {
								set[idx] = struct{}{}
							}
						}
					}
				}
			}
		}
	}

	engineCfg := BuildEngineConfig(snapshot)
	autoApprove := snapshot.AutoApprove

	runtimeGlossary, err := r.buildRuntimeGlossary(ctx, exec.Project, engineCfg.Glossary.Enabled)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
		return nil
	}
	var qaEngine *qa.Engine
	if engineCfg.QA.Enabled {
		qaCfg := engineCfg.QA
		qaCfg.Glossary = runtimeGlossary
		qaCfg.Format = res.Format
		qaEngine = qa.NewEngine(qaCfg, r.logger)
	}
	memory, err := r.buildRuntimeTM(exec.Project, engineCfg.TMEnabled)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
		return nil
	}

	factory := NewEngineFactory(r.logger, r.limiterPool)
	resources := engine.RuntimeResources{Glossary: runtimeGlossary, TM: memory}
	eng, err := factory.BuildEngine(ctx, snapshot, resources, reporter)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
		return nil
	}
	defer func() { _ = eng.Close() }()

	var mu sync.Mutex
	completedCount := 0
	var lastResult pipeline.TranslateResult

	// 瞬时写入失败追踪：段 docIndex → 待下一轮 pending 过滤拾取。
	// 每轮 translate 开始时重置（瞬态段已在下一轮被 pending 过滤拾取并重试，
	// 清空避免跨轮累积误判）。最后一轮后非空则 fail-fast（避免 limbo 段）。
	var persistFailedMu sync.Mutex
	persistFailedIndices := make(map[int]struct{})

	// 段落来源标记：仅 segment_ids 手动选择时跳过默认过滤
	isExplicitSelection := snapshot.ExplicitSegmentSelection
	firstTranslateRoundIdx := -1
	lastTranslateRoundIdx := -1
	translateRoundCount := 0
	// lastRoundIdxByMode：模式 → 计划内最后一轮的 index，覆盖快照中出现的
	// 全部模式（含未知模式，保证承接者判定对任何模式都成立）。轮次终态与
	// 资源收尾都以「是否存在后续同模式轮」为界：最后一轮的欠账无人承接，
	// 是真实放弃；更早轮的欠账由后续同模式轮的池 0 重扫清偿（见 roundAbandoned）。
	lastRoundIdxByMode := make(map[string]int, len(snapshot.Rounds))
	for i := range snapshot.Rounds {
		mode := snapshot.Rounds[i].Mode
		lastRoundIdxByMode[mode] = i
		if mode == pipeline.RoundModeTranslate {
			if firstTranslateRoundIdx == -1 {
				firstTranslateRoundIdx = i
			}
			lastTranslateRoundIdx = i
			translateRoundCount++
		}
	}

	// 残留账本：模式 → 该模式最后一次实际执行后的欠账（未解决 + 终态失败）。
	// 每次执行覆盖写入：后续同模式轮执行后前一轮的欠账已被它解决或重新计入，
	// 故最后一次执行的结果即该模式的欠账。刻意不按「计划内最后一轮」记账——
	// 该轮可能因段过滤为空走 skipped 分支（不执行、不写账本），而前序轮的欠账
	// 此时仍然有效（显式选段让首轮重译已翻译段并失败时，段状态非 pending，
	// 末轮 pending_only 过滤为空），漏记会让资源静默 completed。资源收尾据此
	// 判定 translate 失败与其余模式的软警告，取代「读最后执行轮 lastResult」
	// 的跨模式误取。
	residualByMode := make(map[string]roundResidual)

	// 跨轮增量载体（in-memory）：per-mode 已解决段索引集合。
	// 下一同模式轮的 BuildBatches（池 0）据此排除已解决段，避免跨轮全量重扫。
	// translate 不参与（由 DB status 驱动增量）。恢复时从 job_round_segments
	// 关联行按 mode 并集重建（见上方断点恢复）。
	// resolvedByMode 已在断点恢复处初始化（registry==nil 时为空集合）。
	// 注意：该集合仅承载内存跨轮缓存语义——断点已由 executor 经
	// reporter.SegmentResolved 逐段实时落盘，读写（WithResolvedIndices /
	// AccumulateResolved）均在本轮循环 goroutine 顺序执行，无需加锁。

	// 轮次循环
	for roundIdx := range snapshot.Rounds {
		if ctx.Err() != nil {
			r.logger.Info("context cancelled, stopping round loop", "job_id", job.ID)
			break
		}
		// 轮次启动前暂停检查：退出循环（当前轮保持 running 冻结态，
		// resume 时按批量重置路径处理）。
		if gate != nil && gate.Paused() {
			r.logger.Info("job paused, stopping round loop", "job_id", job.ID)
			break
		}

		round := snapshot.Rounds[roundIdx]

		// 轮次行流转：跳过已完成的轮（断点续传核心——completed 轮直接跳过）。
		roundRowID := 0
		if registry != nil {
			roundRowID, err = registry.ensureLoaded(ctx, r.client, item.ID, roundIdx, round.Mode)
			if err != nil {
				_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
				return nil
			}
			rowStatus, statErr := r.jobs.GetJobRoundStatus(ctx, roundRowID)
			if statErr != nil {
				_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, statErr)
				return nil
			}
			if rowStatus == service.JobRoundStatusCompleted || rowStatus == service.JobRoundStatusSkipped {
				continue // 断点续传：已完成/跳过轮不再执行
			}
			// pending→running 条件更新：progress_total 分母只在该转换时累加
			//（DBReporter.StageStart 的累加条件由 MarkJobRoundRunning 的
			// 条件更新语义保证——仅 pending/skipped 可转 running）。
			if err := r.jobs.MarkJobRoundRunning(ctx, job.ID, roundRowID); err != nil {
				_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
				return nil
			}
		}

		// 每轮从 DB 重新加载段落（Worker 通过 DB 重新加载避免保护态问题）
		selectedRows, allRows, loadErr := r.loadSegments(ctx, res.ID, item.SegmentIds)
		if loadErr != nil {
			_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, loadErr)
			return nil
		}

		// 翻译轮次按 SegmentFilter 过滤
		// 显式选择段落且未被任务级覆盖时，仅首个翻译轮次跳过默认过滤以尊重用户选择；
		// 后续翻译轮次（兜底轮）正常应用 SegmentFilter，避免重译首轮已成功的段。
		if round.Mode == "translate" && round.Translate != nil {
			filter := round.Translate.SegmentFilter
			skipFilter := isExplicitSelection && roundIdx == firstTranslateRoundIdx && (filter == nil || !filter.Overridden)
			if skipFilter {
				r.logger.Debug("explicit segment selection, skipping default filter on first translate round",
					"job_id", job.ID, "resource_id", res.ID)
			} else {
				selectedRows = applyTranslateSegmentFilter(selectedRows, filter)
			}
		}

		if len(selectedRows) == 0 {
			// 本轮无段可处理（如 translate pending_only 已全部译完）：显式标记
			// skipped（进度矩阵可见「跳过」而非静默消失），继续后续轮次。
			if roundRowID > 0 {
				if err := r.jobs.MarkJobRoundSkipped(ctx, roundRowID); err != nil {
					r.logger.Warn("mark round skipped failed", "round_row_id", roundRowID, "err", err)
				}
			}
			if roundIdx == lastTranslateRoundIdx && engineCfg.QA.Enabled && qa.DuplicateSourceDivergenceEnabled(engineCfg.QA.Checks) {
				if err := r.persistDuplicateSourceDivergence(ctx, res.ID); err != nil {
					_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
					return nil
				}
			}
			continue
		}

		// 构建 Document
		inputs := buildSegmentInputs(allRows)
		doc := pipeline.BuildDocumentFromSegments(inputs,
			snapshot.SourceLang, snapshot.TargetLang, res.Format)

		// 构建索引映射
		dbIDToIndex := make(map[int]int, len(allRows))
		for i, row := range allRows {
			dbIDToIndex[row.ID] = i
		}
		segmentIndexes := make([]int, 0, len(selectedRows))
		for _, row := range selectedRows {
			if idx, ok := dbIDToIndex[row.ID]; ok {
				segmentIndexes = append(segmentIndexes, idx)
			}
		}

		docIndexToDBID := make(map[int]int, len(allRows))
		for _, row := range allRows {
			if idx, ok := dbIDToIndex[row.ID]; ok {
				docIndexToDBID[idx] = row.ID
			}
		}

		// 每轮刷新 reporter 目标行，并同点注入当轮断点映射。
		// 顺序约束由 SwitchRound 内部保证：先 flush 上一轮残留缓冲（写入旧行），
		// 再切换目标行——否则上一轮残留登记会按新映射落行，把错误轮次的断点
		// 写进上一轮行。
		//
		// 全部模式（含 translate）都登记断点：轮次行的 segment_completed 由断点
		// 集合基数派生，是恢复/重试时唯一精确的进度基线（见 DBReporter 注释）。
		// translate 轮的跨轮增量仍由 Segment.status 驱动——resolvedByMode 不含
		// translate，loadResolved 也不加载其断点行，断点只承担记账与恢复语义。
		// docIndexToDBID 每轮整体重建，闭包捕获的是当轮快照，无需加锁。
		reporter.SwitchRound(roundRowID, func(docIndex int) (int, bool) {
			dbID, ok := docIndexToDBID[docIndex]
			return dbID, ok
		})

		// 构建 BatchHandler（翻译/裁决轮次用于持久化，抽取轮次不需要）
		var batchHandler func(_ context.Context, batchResult pipeline.BatchResult) error
		switch round.Mode {
		case "translate":
			// 每轮 translate 开始时重置瞬态失败追踪（瞬态段已在下一轮被
			// pending 过滤拾取并重试，清空避免跨轮累积误判）。
			persistFailedMu.Lock()
			persistFailedIndices = make(map[int]struct{})
			persistFailedMu.Unlock()

			batchHandler = func(_ context.Context, batchResult pipeline.BatchResult) error {
				defaultStatus := service.SegmentStatusTranslated
				if autoApprove {
					defaultStatus = service.SegmentStatusApproved
				}

				// --- QA 规则检测 ---
				var allIssues []qa.QualityIssue
				if qaEngine != nil {
					inputs := buildQACheckInputs(batchResult)
					allIssues = qaEngine.Run(ctx, inputs)
				}

				localCompleted := 0
				for _, ts := range batchResult.Segments {
					if ts.TargetText == "" {
						continue
					}
					dbID, ok := docIndexToDBID[ts.Index]
					if !ok {
						continue
					}

					// 合并 pipeline 产出的守恒 issue（如注音还原不完整）与 QA 扫描结果。
					// 新建切片避免对 QA 内部切片的别名依赖；ts.Issues 为 nil 时 append 跳过。
					segIssues := make([]qa.QualityIssue, 0, len(ts.Issues)+4)
					segIssues = append(segIssues, ts.Issues...)
					segIssues = append(segIssues, qa.IssuesFor(ts.Index, allIssues)...)

					segStatus := defaultStatus
					if qa.HasErrors(segIssues) && engineCfg.QA.AutoReject {
						segStatus = service.SegmentStatusRejected
					}

					update := r.client.Segment.UpdateOneID(dbID).
						SetSourceText(firstNonEmpty(ts.SourceText, " ")).
						SetTargetText(ts.TargetText).
						SetStatus(segStatus)
					if autoApprove {
						update.ClearReviewComment()
					}
					// --- 写入 QA 结果（if/else 二选一，避免 ent mutation
					// 对同一列既 Clear 又 Set 导致 PostgreSQL 42601 重复赋值）---
					// 不对账：新译文导致指纹基本全变，旧裁决不跨文本存活；
					// 若未来引入同译文手动重算，须接入 qa.ReconcileIssues。
					if len(segIssues) > 0 {
						update.SetQualityIssues(segIssues)
					} else {
						update.ClearQualityIssues()
					}
					if err := update.Exec(ctx); err != nil {
						// 取消/超时交由 round_executor 的 ctx 检查接管，不归类重试。
						if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							continue
						}
						classified := database.Classify(r.dbDriver, err)
						r.logger.Warn("persist segment failed",
							"segment_id", dbID, "err", err,
							"category", classified.Category, "sqlstate", classified.SQLState)
						switch classified.Category {
						case database.CategoryTransient:
							// 瞬时：DB 状态因原子 UPDATE 失败仍为 pending，
							// 记录 docIndex 供终端态兜底，不计入 completed，不返回 error。
							// 下一轮 pending_only 过滤自动拾取重试。
							persistFailedMu.Lock()
							persistFailedIndices[ts.Index] = struct{}{}
							persistFailedMu.Unlock()
							continue
						default: // Structural 或 Unknown：fail-fast（命中首段即终止当前轮）
							// 对外消息仅含分级摘要，避免把原始驱动错误（可能内嵌
							// 连接元数据）经 error_message → SSE → API 暴露给客户端。
							return fmt.Errorf("persist segment %d failed (structural DB write error, sqlstate %s)",
								dbID, classified.SQLState)
						}
					}
					localCompleted++
				}
				mu.Lock()
				completedCount += localCompleted
				mu.Unlock()
				return nil
			}
		case "correct":
			batchHandler = func(_ context.Context, batchResult pipeline.BatchResult) error {
				for _, ts := range batchResult.Segments {
					dbID, ok := docIndexToDBID[ts.Index]
					if !ok {
						continue
					}
					// correct 改写译文并重写 quality_issues（ResolvedCodes 已剔除）。
					// if/else 二选一避免同一列既 Clear 又 Set（PostgreSQL 42601）。
					// 不对账：新译文导致指纹基本全变，旧裁决不跨文本存活；
					// 若未来引入同译文手动重算，须接入 qa.ReconcileIssues。
					update := r.client.Segment.UpdateOneID(dbID).
						SetTargetText(ts.TargetText)
					if len(ts.Issues) > 0 {
						update.SetQualityIssues(ts.Issues)
					} else {
						update.ClearQualityIssues()
					}
					if err := update.Exec(ctx); err != nil {
						// 取消/超时交由 round_executor 的 ctx 检查接管，不归类重试。
						if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							continue
						}
						classified := database.Classify(r.dbDriver, err)
						r.logger.Warn("persist correct rewrite failed",
							"segment_id", dbID, "err", err,
							"category", classified.Category, "sqlstate", classified.SQLState)
						// correct 无重试机制（改写结果一次性写入，失败即丢失），
						// fail-fast 当前轮，避免末轮静默丢失。
						return fmt.Errorf("persist correct rewrite for segment %d failed (DB write error, category %s, sqlstate %s)",
							dbID, classified.Category, classified.SQLState)
					}
				}
				return nil
			}
		case "adjudicate":
			batchHandler = func(_ context.Context, batchResult pipeline.BatchResult) error {
				completed := 0
				for _, ts := range batchResult.Segments {
					dbID, ok := docIndexToDBID[ts.Index]
					if !ok {
						continue
					}
					// 仅重写 quality_issues，不改 status / target。
					// if/else 二选一避免同一列既 Clear 又 Set（PostgreSQL 42601）。
					// 不对账：adjudicate 不产生新 issue，只是标记已有的
					// （applyVerdicts 写 dismissed）；新译文场景下指纹全变，无需对账。
					update := r.client.Segment.UpdateOneID(dbID)
					if len(ts.Issues) > 0 {
						update.SetQualityIssues(ts.Issues)
					} else {
						update.ClearQualityIssues()
					}
					if err := update.Exec(ctx); err != nil {
						// 取消/超时交由 round_executor 的 ctx 检查接管，不归类重试。
						if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							continue
						}
						classified := database.Classify(r.dbDriver, err)
						r.logger.Warn("persist adjudicated issues failed",
							"segment_id", dbID, "err", err,
							"category", classified.Category, "sqlstate", classified.SQLState)
						switch classified.Category {
						case database.CategoryTransient:
							// adjudicate 无跨轮重试机制（裁决结果一次性写入，失败即丢失），
							// 即使瞬时错误也 fail-fast 当前轮，避免末轮静默丢失导致
							// COMPLETED 时陈旧 quality_issues 被保留且零信号。
							fallthrough
						default: // Structural 或 Unknown：fail-fast
							return fmt.Errorf("persist adjudicated issues for segment %d failed (DB write error, category %s, sqlstate %s)",
								dbID, classified.Category, classified.SQLState)
						}
					}
					completed++
				}
				return nil
			}
		case "semantic_qa":
			batchHandler = func(batchCtx context.Context, batchResult pipeline.BatchResult) error {
				resultsByDBID := make(map[int]pipeline.TranslatedSegment, len(batchResult.Segments))
				dbIDs := make([]int, 0, len(batchResult.Segments))
				for _, ts := range batchResult.Segments {
					if ts.Issues == nil {
						continue
					}
					dbID, ok := docIndexToDBID[ts.Index]
					if !ok {
						continue
					}
					resultsByDBID[dbID] = ts
					dbIDs = append(dbIDs, dbID)
				}
				if len(dbIDs) == 0 {
					return nil
				}

				rows, err := r.client.Segment.Query().Where(segment.IDIn(dbIDs...)).All(batchCtx)
				if err != nil {
					return fmt.Errorf("load semantic_qa batch segments: %w", err)
				}

				completed := 0
				for _, row := range rows {
					ts, ok := resultsByDBID[row.ID]
					if !ok {
						continue
					}
					updated, err := persistSemanticQASegmentIssues(batchCtx, r.client, row, ts.TargetText, ts.Issues)
					if err != nil {
						// 取消/超时交由 round_executor 的 ctx 检查接管，不计入统计。
						if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							continue
						}
						classified := database.Classify(r.dbDriver, err)
						r.logger.Warn("persist semantic_qa issues failed",
							"segment_id", row.ID, "err", err,
							"category", classified.Category, "sqlstate", classified.SQLState)
						// semantic_qa 没有跨轮重试机制（裁决/质检结果一次性写入，
						// 失败即丢失），无论瞬时还是结构性都 fail-fast 当前轮，
						// 由上层 soft warning 路径接管，避免静默吞没导致 COMPLETED
						// 时 QA 结果丢失且零信号。
						return fmt.Errorf("persist semantic_qa issues for segment %d failed (DB write error, category %s, sqlstate %s)",
							row.ID, classified.Category, classified.SQLState)
					}
					if updated == 0 {
						r.logger.Info("skip stale semantic_qa result", "segment_id", row.ID)
						continue
					}
					completed++
				}
				return nil
			}

		case "revise":
			// 写回时移除的 issue 集合与送进 prompt 的目标集合严格一致
			//（计划校验保证只能是语义 code）。
			var reviseCodes []string
			if round.Revise != nil {
				reviseCodes = round.Revise.IssueCodes
			}
			batchHandler = func(batchCtx context.Context, batchResult pipeline.BatchResult) error {
				resultsByDBID := make(map[int]pipeline.TranslatedSegment, len(batchResult.Segments))
				dbIDs := make([]int, 0, len(batchResult.Segments))
				for _, ts := range batchResult.Segments {
					dbID, ok := docIndexToDBID[ts.Index]
					if !ok {
						continue
					}
					resultsByDBID[dbID] = ts
					dbIDs = append(dbIDs, dbID)
				}
				if len(dbIDs) == 0 {
					return nil
				}
				rows, err := r.client.Segment.Query().Where(segment.IDIn(dbIDs...)).All(batchCtx)
				if err != nil {
					return fmt.Errorf("load revise batch segments: %w", err)
				}
				// 仅对译文确有变化的段构造确定性 QA 输入：no-op 段的 QA 结果
				// 本就会在写回循环中被跳过，整批跑是纯浪费。
				changed := make([]pipeline.TranslatedSegment, 0, len(rows))
				for _, row := range rows {
					ts, ok := resultsByDBID[row.ID]
					if !ok {
						continue
					}
					if ts.TargetText != doc.Segments[ts.Index].Target {
						changed = append(changed, ts)
					}
				}
				var allIssues []qa.QualityIssue
				qaRan := qaEngine != nil && len(changed) > 0
				if qaRan {
					allIssues = qaEngine.Run(batchCtx, buildQACheckInputs(pipeline.BatchResult{Segments: changed}))
				}
				completed := 0
				for _, row := range rows {
					ts, ok := resultsByDBID[row.ID]
					if !ok {
						continue
					}
					baseline := doc.Segments[ts.Index].Target
					if ts.TargetText == baseline {
						completed++
						continue
					}
					fresh := qa.IssuesFor(ts.Index, allIssues)
					updated, err := persistReviseSegmentResult(batchCtx, r.client, row, baseline, ts.TargetText, fresh, reviseCodes, qaRan)
					if err != nil {
						if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							continue
						}
						classified := database.Classify(r.dbDriver, err)
						r.logger.Warn("persist revise result failed", "segment_id", row.ID, "err", err,
							"category", classified.Category, "sqlstate", classified.SQLState)
						return fmt.Errorf("persist revise result for segment %d failed (DB write error, category %s, sqlstate %s)", row.ID, classified.Category, classified.SQLState)
					}
					if updated == 0 {
						r.logger.Info("skip stale revise result", "segment_id", row.ID)
						continue
					}
					completed++
				}
				mu.Lock()
				completedCount += completed
				mu.Unlock()
				return nil
			}
		}

		roundIdx := roundIdx // capture for closure
		// 所有轮次统一用 SegmentFilter 限定任务级段落范围；
		// adjudicate handler 的 BuildBatches 在已过滤 doc 上按 status∈{translated,edited}
		// 且含可裁决 issue 进一步筛选，无需清空共享 doc 的 Status/Issues。
		execOpts := []engine.ExecuteOption{
			engine.WithSegmentFilter(segmentIndexes),
			engine.WithBatchHandler(batchHandler),
		}
		// 非翻译轮注入跨轮增量载体：BuildBatches 据此排除上一同模式轮已解决的段。
		if round.Mode != pipeline.RoundModeTranslate {
			execOpts = append(execOpts, engine.WithResolvedIndices(resolvedByMode[round.Mode]))
		}
		// 流水线注入：站位信号量（同轮资源共享并发预算）与暂停闸门
		//（退避重试等待中止信号）。单资源路径（stations/gate 为 nil）跳过。
		if stations != nil && roundIdx < len(stations) {
			execOpts = append(execOpts, engine.WithStation(stations[roundIdx]))
		}
		if gate != nil {
			execOpts = append(execOpts, engine.WithPauseGate(gate))
		}
		result, roundErr := eng.ExecuteRound(ctx, roundIdx, doc, execOpts...)
		if roundErr == nil {
			lastResult = result
			// 累加本轮成功段到对应模式的 resolved 集合（跨轮增量，仅内存）。
			// 注意：跨"不同"模式间不共享（extract 成功不阻止 adjudicate 扫描）。
			// 断点已由 executor 经 SegmentResolved 逐段实时落盘，此集合不再
			// 承载持久化正确性职责，仅驱动下一同模式轮的 BuildBatches 过滤；
			// 轮末无需强制落盘兜底。
			engine.AccumulateResolved(resolvedByMode, round.Mode, result.Resolved)
			if roundIdx == lastTranslateRoundIdx && engineCfg.QA.Enabled && qa.DuplicateSourceDivergenceEnabled(engineCfg.QA.Checks) {
				if err := r.persistDuplicateSourceDivergence(ctx, res.ID); err != nil {
					roundErr = err
				}
			}
			// 残留账本记「该模式最后一次实际执行」的欠账（未解决 + 终态失败），
			// 每轮覆盖写入：后续同模式轮执行后，前一轮的欠账段要么已被它解决、
			// 要么重新计入它自己的欠账，故最后一次执行的结果就是该模式的欠账。
			// 不按「计划内最后一轮」记账——该轮可能因段过滤为空而 skipped（不执行、
			// 不写账本），此时前序轮的欠账仍然有效：显式选段让首轮重译已翻译段
			// 并失败时，段状态仍非 pending，末轮 pending_only 会过滤为空，漏记会
			// 让未译段既被放弃又让资源静默 completed。
			// 警告与 translate 失败判定统一在资源收尾处按账本生成。
			residualByMode[round.Mode] = roundResidual{
				unresolved: len(result.Unresolved),
				failed:     result.FailedSegmentCount,
			}
		}

		if roundErr != nil {
			// 暂停排空的未解决批次不应落 failed：暂停会把退避中/未派发的
			// 批次全部转为 unresolved，extract 等轮次的 Finalize「全未解决」
			// 检查会把它误报为轮次失败——按冻结语义返回（轮次行保持
			// running，resume 重置后续跑），与成功路径的暂停豁免一致。
			if gate != nil && gate.Paused() {
				return nil
			}
			// 轮次失败：轮次行落 failed 终态（供断点续传跳过与进度矩阵展示）。
			if roundRowID > 0 {
				if err := r.jobs.MarkJobRoundFailed(ctx, roundRowID, roundErr); err != nil {
					r.logger.Warn("mark round failed failed", "round_row_id", roundRowID, "err", err)
				}
			}
			if errors.Is(roundErr, context.Canceled) && completedCount > 0 {
				r.logger.Warn("translation cancelled, preserving partial progress",
					"resource_id", item.ID, "completed", completedCount, "total", len(selectedRows))
				_ = r.recordUsage(ctx, exec, completedCount, lastResult.InputTokens, lastResult.OutputTokens)
				_ = r.client.JobResource.UpdateOneID(item.ID).SetCompletedSegments(completedCount).SetSkippedSegments(lastResult.SkippedCount).Exec(ctx)
				_ = r.jobs.MarkJobResourceCancelled(ctx, job.ID, item.ID)
				return nil
			}
			_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, fmt.Errorf("round %d (%s): %w", roundIdx, round.Mode, roundErr))
			return nil
		}
		// 轮次终态：ctx 存活且未暂停时，按「欠账是否有后续同模式轮承接」分流。
		// 欠账 = 未解决段 + 终态失败段（两集合互斥，见 executor 契约）。有承接者
		// 仍闭合为 completed——后续同模式轮的池 0 会重扫欠账段（非翻译轮经
		// resolvedByMode 差集排除已解决段，翻译轮经 Segment.status 过滤；段状态
		// 从不落 failed、未译段保持 pending，欠账段必被重扫），欠账由接力清偿，
		// 其分母的跨轮重复由闭合吸收。真实放弃（最后一轮仍有欠账）必须落
		// failed：completed 会被断点续传永久跳过且重试不重置，欠账段将既被
		// 永久放弃又被进度记成完成；failed 在 retry/resume/recover 的重置集内，
		// 资源重跑时该轮续跑。暂停/取消（RunRound 把取消吞成 unresolved、
		// nil error）仍保持 running 冻结态，由重置路径续跑。
		// 翻译轮次的 SkippedCount 是轮内的结构跳过计数（空文本/占位段），
		// 与轮次行流转的 skipped 状态语义不同。
		if roundRowID > 0 && (gate == nil || !gate.Paused()) && ctx.Err() == nil {
			residual := roundResidual{unresolved: len(result.Unresolved), failed: result.FailedSegmentCount}
			if roundAbandoned(residual, roundIdx != lastRoundIdxByMode[round.Mode]) {
				if err := r.jobs.MarkJobRoundFailed(ctx, roundRowID, fmt.Errorf(
					"轮次有 %d 个未解决段、%d 个终态失败段，无后续同模式轮次承接",
					residual.unresolved, residual.failed)); err != nil {
					r.logger.Warn("mark round failed failed", "round_row_id", roundRowID, "err", err)
				}
			} else if err := r.jobs.MarkJobRoundCompleted(ctx, roundRowID); err != nil {
				r.logger.Warn("mark round completed failed", "round_row_id", roundRowID, "err", err)
			}
		}
		// 不再因 UnresolvedCount==0 提前 break，避免跳过后续 extract/adjudicate 轮
	}
	completedQuery := r.client.Segment.Query().
		Where(
			segment.ResourceIDEQ(res.ID),
			segment.StatusIn(
				service.SegmentStatusTranslated,
				service.SegmentStatusEdited,
				service.SegmentStatusApproved,
			),
		)
	if len(item.SegmentIds) > 0 {
		completedQuery = completedQuery.Where(segment.IDIn(item.SegmentIds...))
	}
	actualCompleted, countErr := completedQuery.Count(ctx)
	if countErr == nil {
		completedCount = actualCompleted
	}
	skippedCount := lastResult.SkippedCount

	// 暂停退出：资源保持 running 冻结态（矩阵行同样冻结），跳过一切终态
	// 判定与 usage 记录——resume 时从断点继续，不会重复计费。
	if gate != nil && gate.Paused() {
		r.logger.Info("job paused, freezing resource state",
			"job_id", job.ID, "job_resource_id", item.ID)
		return nil
	}

	// 最后一轮 translate 后，检查仍有瞬时写入失败的段（历经所有翻译轮仍写不进去）。
	// 这些段既不在 lastResult.UnresolvedCount（那是 LLM 失败）也不在 completed 中，
	// 不处理会被误判 COMPLETED 而静默丢失。无论单轮还是多轮，凡存在持久化失败的段
	// 都必须 fail-fast，避免静默丢失；错误消息按轮数区分"首次失败"与"重试耗尽"，
	// 防止单轮任务被误报为已重试。
	if lastTranslateRoundIdx >= 0 {
		persistFailedMu.Lock()
		persistFailedCount := len(persistFailedIndices)
		persistFailedMu.Unlock()
		if persistFailedCount > 0 {
			r.logger.Warn("segments failed to persist after all translate rounds",
				"resource_id", item.ID, "count", persistFailedCount,
				"translate_rounds", translateRoundCount)
			_ = r.recordUsage(ctx, exec, completedCount, lastResult.InputTokens, lastResult.OutputTokens)
			_ = r.client.JobResource.UpdateOneID(item.ID).
				SetCompletedSegments(completedCount).SetSkippedSegments(skippedCount).Exec(ctx)
			var err error
			if translateRoundCount > 1 {
				err = fmt.Errorf("%d segments failed to persist to database after %d translate rounds (transient DB errors exhausted retries): consider retrying the job",
					persistFailedCount, translateRoundCount)
			} else {
				err = fmt.Errorf("%d segments failed to persist to database (transient DB write error, no subsequent retry round): consider retrying the job",
					persistFailedCount)
			}
			_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
			return nil
		}
	}

	eng.SaveGlossary(ctx)

	// translate 残留判定读残留账本而非 lastResult：doc 每轮重建，
	// lastResult 只反映「最后执行的那一轮」，其 UnresolvedCount 源自
	// doc.Vars _translate_failed_indices（仅 translate 轮有意义）——计划以
	// extract/质检轮收尾时该值恒为 0，translate 轮的未译段会被静默清零，
	// 资源误判 completed。账本记录的是最后一次实际执行的 translate 轮的欠账
	//（translate 轮 failed 恒为 0，总数即未译段数，与原口径一致）。
	if translateResidual := residualByMode[pipeline.RoundModeTranslate]; translateResidual.total() > 0 {
		r.logger.Warn("translation partially failed: some segments could not be resolved",
			"resource_id", item.ID,
			"unresolved_count", translateResidual.total(),
			"completed_count", completedCount,
		)
		_ = r.recordUsage(ctx, exec, completedCount, lastResult.InputTokens, lastResult.OutputTokens)
		_ = r.client.JobResource.UpdateOneID(item.ID).SetCompletedSegments(completedCount).SetSkippedSegments(skippedCount).Exec(ctx)
		err := fmt.Errorf("%d segments failed to translate (completed: %d): LLM could not preserve all protected placeholders after retries",
			translateResidual.total(), completedCount)
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
		return nil
	}

	if err := r.recordUsage(ctx, exec, completedCount, lastResult.InputTokens, lastResult.OutputTokens); err != nil {
		_ = r.client.JobResource.UpdateOneID(item.ID).SetCompletedSegments(completedCount).SetSkippedSegments(skippedCount).Exec(ctx)
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
		return nil
	}

	// 非翻译模式的残留统一在收尾处转化为资源级软警告（不阻塞 completed）。
	// 文案不承诺「可重试任务」：completed 资源不在 RetryJob 的可重试集合内
	// （ErrJobNoFailedResource），只保留「可调整参数后重新发起任务」的诚实
	// 表述。合并顺序固定为 semantic_qa → adjudicate → revise → extract，
	// 不迭代 map，保证 resource_warning 稳定可比。
	roundWarnings := make([]string, 0, 4)
	for _, mode := range []string{
		pipeline.RoundModeSemanticQA,
		pipeline.RoundModeAdjudicate,
		pipeline.RoundModeRevise,
		pipeline.RoundModeExtract,
	} {
		residual := residualByMode[mode]
		if residual.total() == 0 {
			continue
		}
		r.logger.Warn("round mode finished with unresolved or failed segments",
			"mode", mode,
			"resource_id", item.ID,
			"unresolved", residual.unresolved,
			"failed_segments", residual.failed,
		)
		roundWarnings = append(roundWarnings, residualWarning(mode, residual))
	}
	return r.jobs.MarkJobResourceCompleted(ctx, job.ID, item.ID, "", completedCount, skippedCount, mergeWarnings(roundWarnings...))
}

func mergeWarnings(warnings ...string) string {
	var nonEmpty []string
	for _, w := range warnings {
		if w != "" {
			nonEmpty = append(nonEmpty, w)
		}
	}
	switch len(nonEmpty) {
	case 0:
		return ""
	case 1:
		return nonEmpty[0]
	}
	// 多个非空轮次软警告并列保留，避免后一个模式覆盖前一个模式的提示。
	return strings.Join(nonEmpty, "; ")
}

// roundResidual 一轮执行后的欠账：unresolved 是本轮所有池结束后仍未解决的
// 段数（含致命 401/403，会跨轮传播给下一同模式轮）；failed 是终态失败段数
// （semantic_qa/revise 的 render/protect 失败，经 terminalFailure 计入，不进
// unresolved、不跨轮传播）。两集合互斥（executor 契约：无任何形态把同一 idx
// 同时计入 resolved 与失败/未解决集合），故总量取和而非 max。
type roundResidual struct {
	unresolved int
	failed     int
}

func (r roundResidual) total() int { return r.unresolved + r.failed }

// roundAbandoned 判断欠账是否构成真实放弃：有欠账且无后续同模式轮承接。
// 有承接者就不算放弃——后续同模式轮的池 0 必然重扫欠账段：非翻译轮经
// resolvedByMode 差集排除已解决段（欠账段不在 resolved 集合内），翻译轮经
// Segment.status 过滤（段状态从不落 failed，未译段保持 pending 必被选中），
// 欠账由接力清偿而非放弃。
func roundAbandoned(residual roundResidual, hasSuccessor bool) bool {
	return residual.total() > 0 && !hasSuccessor
}

// residualWarning 按模式产出资源级软警告文案；欠账为空时返回空串。
// 不承诺「可重试任务」：软警告只出现在资源 completed 路径，而 completed
// 资源不在 RetryJob 的可重试集合内。translate 的残留走资源失败路径（不产
// 软警告）、correct 恒成功、未知模式无对应话术，均返回空串。
func residualWarning(mode string, residual roundResidual) string {
	if residual.total() == 0 {
		return ""
	}
	switch mode {
	case pipeline.RoundModeSemanticQA:
		return fmt.Sprintf("语义质检未完全成功：%d 个段落未能完成（本次结果已保留，可调整参数后重新发起任务）", residual.total())
	case pipeline.RoundModeAdjudicate:
		return fmt.Sprintf("质量裁决未完全成功：%d 个段落未能完成裁决（issue 保持待决，本次结果已保留，可调整参数后重新发起任务）", residual.total())
	case pipeline.RoundModeRevise:
		return fmt.Sprintf("修订未完全成功：%d 个段落未能完成（本次结果已保留，可调整参数后重新发起任务）", residual.total())
	case pipeline.RoundModeExtract:
		return fmt.Sprintf("术语抽取未完全成功：%d 个段落未能完成（本次结果已保留，可调整参数后重新发起任务）", residual.total())
	}
	return ""
}

func mergeSemanticQAIssues(existing, fresh []qa.QualityIssue) []qa.QualityIssue {
	merged := make([]qa.QualityIssue, 0, len(existing)+len(fresh))
	for _, issue := range existing {
		if !prompt.IsSemanticQACode(issue.Code) {
			merged = append(merged, issue)
		}
	}
	return append(merged, fresh...)
}

// persistSemanticQASegmentIssues 对单个段落执行 CAS 写入语义质检结果。
//
// CAS 保护：仅当段落仍处于可质检状态（translated/edited）且译文未被改动
// （当前 targetText 仍等于扫描时看到的 targetText）时才写入，避免覆盖
// 审核态（approved/rejected）或已被改写的新译文。
//
// 不使用 UpdatedAtEQ：SQLite TEXT 时间列在 ent/modernc 往返时存在精度/
// 格式差异，会导致 WHERE 恒匹配 0 行而静默丢弃所有结果。
//
// 返回实际更新行数（0 表示 CAS 未命中，调用方据此跳过并记日志）。
func persistSemanticQASegmentIssues(ctx context.Context, c *ent.Client, row *ent.Segment, targetText string, fresh []qa.QualityIssue) (int, error) {
	// 对账：retry/崩溃恢复时 semantic_qa 会重扫同一译文，fresh 语义 issues 需继承
	// 已有同指纹 issue 的裁决（dismissed 等），避免静默冲掉用户/LLM 的裁决。
	reconciled := qa.ReconcileIssues(fresh, row.QualityIssues)
	merged := mergeSemanticQAIssues(row.QualityIssues, reconciled)
	return c.Segment.Update().
		Where(
			segment.IDEQ(row.ID),
			segment.StatusIn(service.SegmentStatusTranslated, service.SegmentStatusEdited),
			segment.TargetTextEQ(targetText),
		).
		SetQualityIssues(merged).
		Save(ctx)
}

// persistReviseSegmentResult 对单个段落执行 LLM 修订结果的 CAS 写入。
//
// CAS 保护：仅当段落仍处于 translated/edited 且译文仍等于扫描时的 baseline
// 时写入，避免覆盖审核态或并发产生的新译文。
//
// 写回契约（与 correct 轮一致："声明修什么，就移除什么，其余判决不动"）：
//   - targetedCodes 命中且仍 pending 的 issue 视为本轮已修复而移除；
//   - 范围外 pending 与 dismissed 记录一律保留；
//   - qaRan=true 时确定性 issue 以 fresh 重算（ReconcileIssues 按指纹继承旧裁决）；
//     qaRan=false（计划未启用确定性 QA）时确定性 issue 不重算、原样保留。
//
// 修订是声明性修复，无法自证 LLM 实际修复了目标问题：若实际未修复，仅当后续
// semantic_qa 轮会重扫该段（scope=all；with_issues/with_issue_codes 作用域会跳过
// 已无 issue 的段落，且轮次顺序无约束、revise 可为末轮）时才会重新检出；否则与
// 手动编辑/重译清除旧语义 issue 的既有语义一致——译文已变更，旧 issue 视为失效。
func persistReviseSegmentResult(ctx context.Context, c *ent.Client, row *ent.Segment, baseline, revised string, fresh []qa.QualityIssue, targetedCodes []string, qaRan bool) (int, error) {
	final := qa.ReviseFinalIssues(row.QualityIssues, fresh, targetedCodes, qaRan)
	update := c.Segment.Update().Where(
		segment.IDEQ(row.ID),
		segment.StatusIn(service.SegmentStatusTranslated, service.SegmentStatusEdited),
		segment.TargetTextEQ(baseline),
	).SetTargetText(revised)
	if len(final) > 0 {
		update.SetQualityIssues(final)
	} else {
		update.ClearQualityIssues()
	}
	return update.Save(ctx)
}

// buildSegmentInputs 将 DB segments 转换为 SegmentInput 切片。
// 额外载入 Target/Issues/Status 供裁决轮使用；translate/extract 无副作用。
func buildSegmentInputs(rows []*ent.Segment) []pipeline.SegmentInput {
	inputs := make([]pipeline.SegmentInput, len(rows))
	for i, row := range rows {
		var meta map[string]any
		if row.Meta != nil {
			_ = json.Unmarshal([]byte(*row.Meta), &meta)
		}
		target := ""
		if row.TargetText != nil {
			target = *row.TargetText
		}
		inputs[i] = pipeline.SegmentInput{
			ID:         strconv.Itoa(row.SegmentIndex),
			SourceText: row.SourceText,
			Meta:       meta,
			TargetText: target,
			Issues:     row.QualityIssues,
			Status:     string(row.Status),
		}
	}
	return inputs
}

// buildQACheckInputs 将 BatchResult 转换为 QA 检测输入。
func buildQACheckInputs(batchResult pipeline.BatchResult) []qa.CheckInput {
	inputs := make([]qa.CheckInput, 0, len(batchResult.Segments))
	for _, ts := range batchResult.Segments {
		inputs = append(inputs, qa.CheckInput{
			Index:      ts.Index,
			SourceText: ts.SourceText,
			TargetText: ts.TargetText,
			Protected:  ts.Protected,
		})
	}
	return inputs
}

// persistDuplicateSourceDivergence 在最后一个翻译轮次后执行全文同文异译检查。
// 仅合并 quality_issues，不改写译文或审核状态。
func (r *JobRunner) persistDuplicateSourceDivergence(ctx context.Context, resourceID int) error {
	_, rows, err := r.loadSegments(ctx, resourceID, nil)
	if err != nil {
		return fmt.Errorf("load segments for duplicate source QA: %w", err)
	}
	inputs := make([]qa.CheckInput, 0, len(rows))
	for _, row := range rows {
		target := ""
		if row.TargetText != nil {
			target = *row.TargetText
		}
		inputs = append(inputs, qa.CheckInput{
			Index:      row.SegmentIndex,
			SourceText: row.SourceText,
			TargetText: target,
		})
	}

	issuesByIndex := make(map[int][]qa.QualityIssue)
	for _, issue := range qa.CheckDuplicateSourceDivergence(inputs) {
		issuesByIndex[issue.SegmentIndex] = append(issuesByIndex[issue.SegmentIndex], issue)
	}
	for _, row := range rows {
		merged, changed := replaceQualityIssuesByCode(
			row.QualityIssues,
			issuesByIndex[row.SegmentIndex],
			qa.CodeDuplicateSourceDivergence,
		)
		if !changed {
			continue
		}
		// 对账：每次 job 处理（含 retry）都会对整份资源重算 dup-divergence，
		// 重算结果需继承同指纹旧裁决（dismissed 等）。replaceQualityIssuesByCode
		// 已剔除旧的同 code issues，对账会从 row.QualityIssues 找回同指纹裁决；
		// 保留的非该 code 旧 issues 与自身对账是幂等的，因此安全。
		merged = qa.ReconcileIssues(merged, row.QualityIssues)
		if err := r.client.Segment.UpdateOneID(row.ID).SetQualityIssues(merged).Exec(ctx); err != nil {
			return fmt.Errorf("persist duplicate source QA for segment %d: %w", row.ID, err)
		}
	}
	return nil
}

func mergeQualityIssuesByFingerprint(existing, fresh []qa.QualityIssue) []qa.QualityIssue {
	merged := make([]qa.QualityIssue, 0, len(existing)+len(fresh))
	seen := make(map[string]struct{}, len(existing)+len(fresh))
	for _, issue := range append(append([]qa.QualityIssue(nil), existing...), fresh...) {
		fp := qa.Fingerprint(issue)
		if _, ok := seen[fp]; ok {
			continue
		}
		seen[fp] = struct{}{}
		merged = append(merged, issue)
	}
	return merged
}

func replaceQualityIssuesByCode(existing, fresh []qa.QualityIssue, code string) ([]qa.QualityIssue, bool) {
	kept := make([]qa.QualityIssue, 0, len(existing)+len(fresh))
	changed := len(fresh) > 0
	for _, issue := range existing {
		if issue.Code == code {
			changed = true
			continue
		}
		kept = append(kept, issue)
	}
	if !changed {
		return existing, false
	}
	return mergeQualityIssuesByFingerprint(kept, fresh), true
}

// buildRuntimeGlossary 根据配置构建运行时术语表，未启用则返回空实现。
func (r *JobRunner) buildRuntimeGlossary(ctx context.Context, projectRow *ent.Project, enabled bool) (glossary.Glossary, error) {
	if !enabled {
		return glossary.Nop{}, nil
	}
	return service.NewDatabaseGlossary(ctx, r.client, projectRow)
}

// buildRuntimeTM 根据配置构建运行时翻译记忆，未启用则返回空实现。
func (r *JobRunner) buildRuntimeTM(projectRow *ent.Project, enabled bool) (tm.TranslationMemory, error) {
	if !enabled {
		return tm.Nop{}, nil
	}
	scope, err := tm.ScopeFromProject(projectRow)
	if err != nil {
		return nil, err
	}
	return tm.NewSQLite(r.client, scope)
}

// loadSegments 从数据库加载指定资源的所有片段，并按 selectedIDs 过滤。
func (r *JobRunner) loadSegments(ctx context.Context, resourceID int, selectedIDs []int) ([]*ent.Segment, []*ent.Segment, error) {
	allRows, err := r.client.Segment.Query().
		Where(segment.ResourceIDEQ(resourceID)).
		Order(ent.Asc(segment.FieldSegmentIndex), ent.Asc(segment.FieldID)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(selectedIDs) == 0 {
		return allRows, allRows, nil
	}
	selectedRows, err := r.client.Segment.Query().
		Where(segment.IDIn(selectedIDs...), segment.ResourceIDEQ(resourceID)).
		Order(ent.Asc(segment.FieldSegmentIndex), ent.Asc(segment.FieldID)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}
	return selectedRows, allRows, nil
}

// recordUsage 记录任务用量到数据库。
func (r *JobRunner) recordUsage(ctx context.Context, exec *service.JobExecution, segmentCount int, inputTokens, outputTokens int64) error {
	usage := r.client.UsageRecord.Create().
		SetProjectID(exec.Project.ID).
		SetSource("job").
		SetSegmentCount(segmentCount).
		SetAPICalls(segmentCount).
		SetInputTokens(clampInt64ToInt(inputTokens)).
		SetOutputTokens(clampInt64ToInt(outputTokens)).
		SetNote(fmt.Sprintf("job:%d", exec.Job.ID))
	if exec.ActorUserID > 0 {
		usage.SetUserID(exec.ActorUserID)
	}
	if exec.Project.OwnerOrgID != nil {
		usage.SetOrganizationID(*exec.Project.OwnerOrgID)
	}
	return usage.Exec(ctx)
}

// clampInt64ToInt 将 int64 安全地转换为 int，超过 math.MaxInt32 时截断。
func clampInt64ToInt(v int64) int {
	if v > int64(^uint32(0)>>1) {
		return int(^uint32(0) >> 1)
	}
	if v < 0 {
		return 0
	}
	return int(v)
}

// firstNonEmpty 返回参数中第一个非空白字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// translateStatusAllowed 判断某 status 是否被 SegmentFilter 允许进入翻译轮。
// filter==nil 视为 pending_only（与 applyTranslateSegmentFilter 默认一致）。
func translateStatusAllowed(filter *service.SegmentFilterSnapshot, status string) bool {
	sf := "pending_only"
	if filter != nil && filter.StatusFilter != "" {
		sf = filter.StatusFilter
	}
	switch sf {
	case "all":
		return true
	case "skip_approved":
		switch status {
		case string(service.SegmentStatusPending), string(service.SegmentStatusRejected),
			string(service.SegmentStatusTranslated), string(service.SegmentStatusEdited):
			return true
		}
		return false
	default: // "pending_only"
		return status == string(service.SegmentStatusPending) ||
			status == string(service.SegmentStatusRejected)
	}
}

// applyTranslateSegmentFilter 按翻译状态过滤段落。
// 仅翻译轮次使用；抽取轮次不过滤。
func applyTranslateSegmentFilter(segments []*ent.Segment, filter *service.SegmentFilterSnapshot) []*ent.Segment {
	result := make([]*ent.Segment, 0, len(segments))
	for _, seg := range segments {
		if translateStatusAllowed(filter, string(seg.Status)) {
			result = append(result, seg)
		}
	}
	return result
}

// resolvePath 将路径解析为绝对路径，相对路径通过 store 转换。
func (r *JobRunner) resolvePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("worker: empty path")
	}
	if filepath.IsAbs(raw) {
		return raw, nil
	}
	return r.store.Absolute(raw)
}
