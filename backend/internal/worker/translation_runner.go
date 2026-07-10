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
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// TranslationRunner 翻译任务的执行器，实现 TaskRunner 接口。
type TranslationRunner struct {
	logger      *slog.Logger
	client      *ent.Client
	jobs        *service.TranslationJobService
	store       *filestore.LocalStore
	queue       *Queue
	eventBroker *event.Broker
	limiterPool *backend.LimiterPool
	resMutex    *ResourceMutex

	// per-job 取消注册表：jobID → cancel 函数
	mu         sync.Mutex
	activeJobs map[int]context.CancelFunc
}

// NewTranslationRunner 创建一个新的翻译任务执行器。
func NewTranslationRunner(
	logger *slog.Logger,
	client *ent.Client,
	jobs *service.TranslationJobService,
	store *filestore.LocalStore,
	queue *Queue,
	eventBroker *event.Broker,
	limiterPool *backend.LimiterPool,
	resMutex *ResourceMutex,
) *TranslationRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &TranslationRunner{
		logger:      logger,
		client:      client,
		jobs:        jobs,
		store:       store,
		queue:       queue,
		eventBroker: eventBroker,
		limiterPool: limiterPool,
		resMutex:    resMutex,
		activeJobs:  make(map[int]context.CancelFunc),
	}
}

// Type 返回任务类型标识。
func (r *TranslationRunner) Type() string {
	return "translation"
}

// Queue 返回此 Runner 的任务队列。
func (r *TranslationRunner) Queue() *Queue {
	return r.queue
}

// ProcessOne 处理单个翻译任务，不负责 Dequeue/Done。
func (r *TranslationRunner) ProcessOne(ctx context.Context, jobID int) error {
	return r.processJob(ctx, jobID)
}

// Run 从队列中取任务并执行，直到 ctx 取消。
func (r *TranslationRunner) Run(ctx context.Context) error {
	for {
		jobID, err := r.queue.Dequeue(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		if err := r.processJob(ctx, jobID); err != nil {
			r.logger.Error("translation worker: process job failed", "job_id", jobID, "err", err)
		}
		r.queue.Done(jobID)
	}
}

// Cancel 通知运行中的翻译任务立即停止。
func (r *TranslationRunner) Cancel(taskID int) {
	r.mu.Lock()
	cancel, ok := r.activeJobs[taskID]
	r.mu.Unlock()
	if ok {
		r.logger.Info("cancelling running translation job", "job_id", taskID)
		cancel()
	}
}

// Recover 从数据库恢复挂起的任务并重新入队。
func (r *TranslationRunner) Recover(ctx context.Context) ([]int, error) {
	jobIDs, err := r.jobs.RecoverPendingJobs(ctx)
	if err != nil {
		return nil, err
	}
	return jobIDs, nil
}

// processJob 处理单个翻译任务：加载执行上下文，筛选待处理的资源并依次执行。
func (r *TranslationRunner) processJob(ctx context.Context, jobID int) error {
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
	if exec.Job.Status == service.TranslationJobStatusCancelled {
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
				r.logger.Warn("translation job resource failed", "job_id", jobID, "job_resource_id", item.ID, "err", err)
			}
		}
	}
	reconcileErr := r.jobs.ReconcileJob(jobCtx, jobID)
	r.eventBroker.Purge(jobID)
	return reconcileErr
}

// processJobResource 处理单个翻译资源：从 DB 加载段落、轮次循环翻译、写回 DB。
func (r *TranslationRunner) processJobResource(ctx context.Context, exec *service.TranslationJobExecution, item *ent.JobResource) error {
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

	snapshot, err := r.jobs.GetTranslationSnapshot(ctx, job.ID)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, fmt.Errorf("get translation snapshot: %w", err))
		return nil
	}

	cfg := buildEngineConfig(snapshot)
	autoApprove := snapshot.AutoApprove

	var qaEngine *qa.Engine
	if cfg.QA.Enabled {
		qaEngine = qa.NewEngine(cfg.QA, r.logger)
	}

	runtimeGlossary, err := r.buildRuntimeGlossary(exec.Project, cfg.Glossary.Enabled)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
		return nil
	}
	memory, err := r.buildRuntimeTM(exec.Project, cfg.TMEnabled)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
		return nil
	}

	resources := engine.RuntimeResources{Glossary: runtimeGlossary, TM: memory}
	eng, err := r.buildEngineFromSnapshot(ctx, snapshot, resources, reporter)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
		return nil
	}
	defer func() { _ = eng.Close() }()

	var mu sync.Mutex
	completedCount := 0
	var lastResult pipeline.TranslateResult

	// 轮次循环
	for roundIdx := range snapshot.Rounds {
		if ctx.Err() != nil {
			r.logger.Info("context cancelled, stopping round loop", "job_id", job.ID)
			break
		}

		// 每轮从 DB 重新加载段落（Worker 通过 DB 重新加载避免保护态问题）
		selectedRows, allRows, loadErr := r.loadSegments(ctx, res.ID, item.SegmentIds)
		if loadErr != nil {
			_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, loadErr)
			return nil
		}
		if len(selectedRows) == 0 {
			break
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

		batchHandler := func(_ context.Context, batchResult pipeline.BatchResult) error {
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
			failed := 0
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
				if qa.HasErrors(segIssues) && cfg.QA.AutoReject {
					segStatus = service.SegmentStatusRejected
				}

				update := r.client.Segment.UpdateOneID(dbID).
					SetSourceText(firstNonEmpty(ts.SourceText, " ")).
					SetTargetText(ts.TargetText).
					SetStatus(segStatus)
				if autoApprove {
					update.ClearReviewComment()
				}
				// --- 写入 QA 结果 ---
				// 先清除旧的 quality_issues，再按需写入新的
				update.ClearQualityIssues()
				if len(segIssues) > 0 {
					update.SetQualityIssues(segIssues)
				}
				if err := update.Exec(ctx); err != nil {
					r.logger.Warn("persist segment failed", "segment_id", dbID, "err", err)
					failed++
					continue
				}
				localCompleted++
			}
			mu.Lock()
			completedCount += localCompleted
			mu.Unlock()
			if failed > 0 && localCompleted == 0 {
				return fmt.Errorf("batch persist failed: all %d segments failed to write to database", failed)
			}
			return nil
		}

		result, translateErr := eng.TranslateRound(ctx, roundIdx, doc,
			engine.WithSegmentFilter(segmentIndexes),
			engine.WithBatchHandler(batchHandler),
		)
		if translateErr == nil {
			lastResult = result
		}

		if translateErr != nil {
			if errors.Is(translateErr, context.Canceled) && completedCount > 0 {
				r.logger.Warn("translation cancelled, preserving partial progress",
					"resource_id", item.ID, "completed", completedCount, "total", len(selectedRows))
				_ = r.recordUsage(ctx, exec, completedCount, lastResult.InputTokens, lastResult.OutputTokens)
				_ = r.client.JobResource.UpdateOneID(item.ID).SetCompletedSegments(completedCount).SetSkippedSegments(lastResult.SkippedCount).Exec(ctx)
				_ = r.jobs.MarkJobResourceCancelled(ctx, job.ID, item.ID)
				return nil
			}
			_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, fmt.Errorf("translate round %d: %w", roundIdx, translateErr))
			return nil
		}

		if result.UnresolvedCount == 0 {
			break
		}
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

	return r.jobs.MarkJobResourceCompleted(ctx, job.ID, item.ID, "", completedCount, skippedCount)
}

// buildSegmentInputs 将 DB segments 转换为 SegmentInput 切片。
func buildSegmentInputs(rows []*ent.Segment) []pipeline.SegmentInput {
	inputs := make([]pipeline.SegmentInput, len(rows))
	for i, row := range rows {
		var meta map[string]any
		if row.Meta != nil {
			_ = json.Unmarshal([]byte(*row.Meta), &meta)
		}
		inputs[i] = pipeline.SegmentInput{
			ID:         strconv.Itoa(row.SegmentIndex),
			SourceText: row.SourceText,
			Meta:       meta,
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
		})
	}
	return inputs
}

// buildRuntimeGlossary 根据配置构建运行时术语表，未启用则返回空实现。
func (r *TranslationRunner) buildRuntimeGlossary(projectRow *ent.Project, enabled bool) (glossary.Glossary, error) {
	if !enabled {
		return glossary.Nop{}, nil
	}
	return service.NewDatabaseGlossary(r.client, projectRow)
}

// buildRuntimeTM 根据配置构建运行时翻译记忆，未启用则返回空实现。
func (r *TranslationRunner) buildRuntimeTM(projectRow *ent.Project, enabled bool) (tm.TranslationMemory, error) {
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
func (r *TranslationRunner) loadSegments(ctx context.Context, resourceID int, selectedIDs []int) ([]*ent.Segment, []*ent.Segment, error) {
	allRows, err := r.client.Segment.Query().
		Where(segment.ResourceIDEQ(resourceID)).
		Order(ent.Asc(segment.FieldSegmentIndex), ent.Asc(segment.FieldID)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}
	selectedSet := make(map[int]struct{}, len(selectedIDs))
	for _, id := range selectedIDs {
		selectedSet[id] = struct{}{}
	}
	selectedRows := make([]*ent.Segment, 0, len(allRows))
	for _, row := range allRows {
		if len(selectedSet) > 0 {
			if _, ok := selectedSet[row.ID]; !ok {
				continue
			}
		}
		selectedRows = append(selectedRows, row)
	}
	return selectedRows, allRows, nil
}

// recordUsage 记录翻译用量到数据库。
func (r *TranslationRunner) recordUsage(ctx context.Context, exec *service.TranslationJobExecution, segmentCount int, inputTokens, outputTokens int64) error {
	usage := r.client.UsageRecord.Create().
		SetProjectID(exec.Project.ID).
		SetSource("translation_job").
		SetSegmentCount(segmentCount).
		SetAPICalls(segmentCount).
		SetInputTokens(clampInt64ToInt(inputTokens)).
		SetOutputTokens(clampInt64ToInt(outputTokens)).
		SetNote(fmt.Sprintf("translation_job:%d", exec.Job.ID))
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

// resolvePath 将路径解析为绝对路径，相对路径通过 store 转换。
func (r *TranslationRunner) resolvePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("worker: empty path")
	}
	if filepath.IsAbs(raw) {
		return raw, nil
	}
	return r.store.Absolute(raw)
}
