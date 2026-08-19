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

	// per-job 取消注册表：jobID → cancel 函数
	mu         sync.Mutex
	activeJobs map[int]context.CancelFunc
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
		activeJobs:  make(map[int]context.CancelFunc),
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
	cancel, ok := r.activeJobs[taskID]
	r.mu.Unlock()
	if ok {
		r.logger.Info("cancelling running job", "job_id", taskID)
		cancel()
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

// processJob 处理单个翻译任务：加载执行上下文，筛选待处理的资源并依次执行。
func (r *JobRunner) processJob(ctx context.Context, jobID int) error {
	// 创建 per-job context，支持外部取消
	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()

	// 注册到 activeJobs，使 Cancel 能触发取消
	r.mu.Lock()
	r.activeJobs[jobID] = jobCancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.activeJobs, jobID)
		r.mu.Unlock()
	}()

	exec, err := r.jobs.LoadJobExecution(jobCtx, jobID)
	if err != nil {
		return err
	}
	// 二次校验：任务可能在入队后、执行前被取消
	if exec.Job.Status == service.JobStatusCancelled {
		r.logger.Info("job already cancelled, skipping", "job_id", jobID)
		return nil
	}
	pending := make([]*ent.JobResource, 0, len(exec.JobResources))
	for _, item := range exec.JobResources {
		if item.Status == service.JobResourceStatusPending {
			pending = append(pending, item)
		}
	}
	if len(pending) > 0 {
		if err := r.jobs.MarkJobRunning(jobCtx, jobID); err != nil {
			return err
		}
		// 记录任务开始时间
		_ = r.jobs.MarkJobStarted(jobCtx, jobID)
		for _, item := range pending {
			// 每次处理资源前检查 context 是否已取消
			if jobCtx.Err() != nil {
				r.logger.Info("job context cancelled, stopping", "job_id", jobID)
				break
			}
			if err := r.processJobResource(jobCtx, exec, item); err != nil {
				r.logger.Warn("job resource failed", "job_id", jobID, "job_resource_id", item.ID, "err", err)
			}
		}
	}
	reconcileErr := r.jobs.ReconcileJob(jobCtx, jobID)
	r.eventBroker.Purge(jobID)
	return reconcileErr
}

// processJobResource 处理单个翻译资源：从 DB 加载段落、轮次循环翻译、写回 DB。
func (r *JobRunner) processJobResource(ctx context.Context, exec *service.JobExecution, item *ent.JobResource) error {
	job := exec.Job

	if err := r.jobs.MarkJobResourceRunning(ctx, job.ID, item.ID); err != nil {
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

	snapshot, err := r.jobs.GetExecutionSnapshot(ctx, job.ID)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, fmt.Errorf("get execution snapshot: %w", err))
		return nil
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
	var semanticQAWarning string

	// 瞬时写入失败追踪：段 docIndex → 待下一轮 pending 过滤拾取。
	// 每轮 translate 开始时重置（瞬态段已在下一轮被 pending 过滤拾取并重试，
	// 清空避免跨轮累积误判）。最后一轮后非空则 fail-fast（避免 limbo 段）。
	var persistFailedMu sync.Mutex
	persistFailedIndices := make(map[int]struct{})

	// 段落来源标记：仅 segment_ids 手动选择时跳过默认过滤
	isExplicitSelection := snapshot.ExplicitSegmentSelection
	firstTranslateRoundIdx := -1
	lastTranslateRoundIdx := -1
	lastSemanticQARoundIdx := -1
	translateRoundCount := 0
	for i := range snapshot.Rounds {
		switch snapshot.Rounds[i].Mode {
		case "translate":
			if firstTranslateRoundIdx == -1 {
				firstTranslateRoundIdx = i
			}
			lastTranslateRoundIdx = i
			translateRoundCount++
		case "semantic_qa":
			lastSemanticQARoundIdx = i
		}
	}

	// 跨轮增量载体（in-memory）：per-mode 已解决段索引集合。
	// 下一同模式轮的 BuildBatches（池 0）据此排除已解决段，避免跨轮全量重扫。
	// translate 不参与（由 DB status 驱动增量）。崩溃重启则该集合丢失，资源从 round 0 重跑（与现状一致，无回归）。
	resolvedByMode := engine.NewResolvedByMode()

	// 轮次循环
	for roundIdx := range snapshot.Rounds {
		if ctx.Err() != nil {
			r.logger.Info("context cancelled, stopping round loop", "job_id", job.ID)
			break
		}

		round := snapshot.Rounds[roundIdx]

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
			// 本轮无段可处理（如 translate pending_only 已全部译完）；继续后续 extract/adjudicate 轮
			if roundIdx == lastTranslateRoundIdx && duplicateSourceDivergenceEnabled(engineCfg.QA) {
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

					segIssues := qa.IssuesFor(ts.Index, allIssues)

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
		result, roundErr := eng.ExecuteRound(ctx, roundIdx, doc, execOpts...)
		if roundErr == nil {
			lastResult = result
			// 累加本轮成功段到对应模式的 resolved 集合（跨轮增量）。
			// 注意：跨"不同"模式间不共享（extract 成功不阻止 adjudicate 扫描）。
			engine.AccumulateResolved(resolvedByMode, round.Mode, result.Resolved)
			if roundIdx == lastTranslateRoundIdx && duplicateSourceDivergenceEnabled(engineCfg.QA) {
				if err := r.persistDuplicateSourceDivergence(ctx, res.ID); err != nil {
					roundErr = err
				}
			}
			// semantic_qa 最后一轮后仍有失败段，发软警告，不阻塞资源 completed：
			//  - result.Unresolved：解析失败/瞬时错误耗尽/致命 401/403，经历跨轮接力
			//    换 backend 仍未成功（中间轮的 Unresolved 是预期跨轮传播，不发警告）；
			//  - result.FailedSegmentCount：render 失败（确定性配置/模板问题），不进池也不
			//    跨轮传播，经 terminalFailure → failedSegments 累计，不计入 Unresolved，
			//    故需单独检查，否则会静默 completed 且无 resource_warning SSE。
			if round.Mode == "semantic_qa" && roundIdx == lastSemanticQARoundIdx &&
				(len(result.Unresolved) > 0 || result.FailedSegmentCount > 0) {
				failed := len(result.Unresolved)
				if n := result.FailedSegmentCount; n > failed {
					failed = n
				}
				semanticQAWarning = fmt.Sprintf(
					"语义质检未完全成功：%d 个段落未能完成（可重试任务或调整参数后重试）",
					failed,
				)
				r.logger.Warn("semantic_qa finished with unresolved or failed segments",
					"resource_id", item.ID,
					"unresolved", len(result.Unresolved),
					"failed_segments", result.FailedSegmentCount,
				)
			}
		}

		if roundErr != nil {
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

	if lastResult.UnresolvedCount > 0 {
		r.logger.Warn("translation partially failed: some segments could not be resolved",
			"resource_id", item.ID,
			"unresolved_count", lastResult.UnresolvedCount,
			"completed_count", completedCount,
		)
		_ = r.recordUsage(ctx, exec, completedCount, lastResult.InputTokens, lastResult.OutputTokens)
		_ = r.client.JobResource.UpdateOneID(item.ID).SetCompletedSegments(completedCount).SetSkippedSegments(skippedCount).Exec(ctx)
		err := fmt.Errorf("%d segments failed to translate (completed: %d): LLM could not preserve all protected placeholders after retries",
			lastResult.UnresolvedCount, completedCount)
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
		return nil
	}

	if err := r.recordUsage(ctx, exec, completedCount, lastResult.InputTokens, lastResult.OutputTokens); err != nil {
		_ = r.client.JobResource.UpdateOneID(item.ID).SetCompletedSegments(completedCount).SetSkippedSegments(skippedCount).Exec(ctx)
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
		return nil
	}

	return r.jobs.MarkJobResourceCompleted(ctx, job.ID, item.ID, "", completedCount, skippedCount, semanticQAWarning)
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

func duplicateSourceDivergenceEnabled(cfg qa.Config) bool {
	if !cfg.Enabled {
		return false
	}
	if cfg.Checks == nil {
		return true
	}
	for _, name := range cfg.Checks {
		if name == qa.CodeDuplicateSourceDivergence {
			return true
		}
	}
	return false
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
