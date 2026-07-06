package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// TranslationRunner 翻译任务的执行器，通过嵌入 BaseRunner 复用公共逻辑。
type TranslationRunner struct {
	*BaseRunner
	jobs        *service.TranslationJobService
	eventBroker *event.Broker
	limiterPool *backend.LimiterPool

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
) *TranslationRunner {
	r := &TranslationRunner{
		jobs:        jobs,
		eventBroker: eventBroker,
		limiterPool: limiterPool,
		activeJobs:  make(map[int]context.CancelFunc),
	}
	r.BaseRunner = newBaseRunner(logger, client, store, queue, jobs, r.processJob, "translation worker")
	return r
}

// CancelRunningJob 通知运行中的翻译任务立即停止。
func (r *TranslationRunner) CancelRunningJob(jobID int) {
	r.mu.Lock()
	cancel, ok := r.activeJobs[jobID]
	r.mu.Unlock()
	if ok {
		r.logger.Info("cancelling running translation job", "job_id", jobID)
		cancel()
	}
}

// processJob 处理单个翻译任务：加载执行上下文，筛选待处理的资源并依次执行。
func (r *TranslationRunner) processJob(ctx context.Context, jobID int) error {
	// 创建 per-job context，支持外部取消
	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()

	// 注册到 activeJobs，使 CancelRunningJob 能触发取消
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
	if len(pending) == 0 {
		return r.jobs.ReconcileJob(jobCtx, jobID)
	}
	if err := r.jobs.MarkJobRunning(jobCtx, jobID); err != nil {
		return err
	}
	// 记录任务开始时间
	_ = r.jobs.MarkJobStarted(jobCtx, jobID)
	for _, item := range pending {
		// 每次处理资源前检查 context 是否已取消
		if jobCtx.Err() != nil {
			r.logger.Info("job context cancelled, stopping", "job_id", jobID)
			return jobCtx.Err()
		}
		if err := r.processJobResource(jobCtx, exec, item); err != nil {
			r.logger.Warn("translation job resource failed", "job_id", jobID, "job_resource_id", item.ID, "err", err)
		}
	}
	return r.jobs.ReconcileJob(jobCtx, jobID)
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

	snapshot, err := r.jobs.GetTranslationSnapshot(ctx, job.ID)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, fmt.Errorf("get translation snapshot: %w", err))
		return nil
	}

	cfg := buildEngineConfig(snapshot)
	autoApprove := snapshot.AutoApprove
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

		// 过滤出 pending 段落
		pendingRows := filterPendingSegments(selectedRows)
		if len(pendingRows) == 0 {
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
		segmentIndexes := make([]int, 0, len(pendingRows))
		for _, row := range pendingRows {
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
			status := service.SegmentStatusTranslated
			if autoApprove {
				status = service.SegmentStatusApproved
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
				update := r.client.Segment.UpdateOneID(dbID).
					SetSourceText(firstNonEmpty(ts.SourceText, " ")).
					SetTargetText(ts.TargetText).
					SetStatus(status)
				if autoApprove {
					update.ClearReviewComment()
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
		lastResult = result

		if translateErr != nil {
			if errors.Is(translateErr, context.Canceled) && completedCount > 0 {
				r.logger.Warn("translation cancelled, preserving partial progress",
					"resource_id", item.ID, "completed", completedCount, "total", len(selectedRows))
				_ = r.recordUsage(ctx, exec, completedCount, lastResult.InputTokens, lastResult.OutputTokens)
				_ = r.client.JobResource.UpdateOneID(item.ID).SetCompletedSegments(completedCount).Exec(ctx)
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

	eng.SaveGlossary(ctx)

	if lastResult.UnresolvedCount > 0 {
		r.logger.Warn("translation partially failed: some segments could not be resolved",
			"resource_id", item.ID,
			"unresolved_count", lastResult.UnresolvedCount,
			"completed_count", completedCount,
		)
		_ = r.recordUsage(ctx, exec, completedCount, lastResult.InputTokens, lastResult.OutputTokens)
		_ = r.client.JobResource.UpdateOneID(item.ID).SetCompletedSegments(completedCount).Exec(ctx)
		err := fmt.Errorf("%d segments failed to translate (completed: %d): LLM could not preserve all protected placeholders after retries",
			lastResult.UnresolvedCount, completedCount)
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
		return nil
	}

	if err := r.recordUsage(ctx, exec, completedCount, lastResult.InputTokens, lastResult.OutputTokens); err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, job.ID, item.ID, err)
		return nil
	}

	return r.jobs.MarkJobResourceCompleted(ctx, job.ID, item.ID, "", completedCount)
}

// filterPendingSegments 过滤出待翻译的段落（status 为 pending）。
func filterPendingSegments(rows []*ent.Segment) []*ent.Segment {
	var pending []*ent.Segment
	for _, row := range rows {
		if row.Status == "pending" {
			pending = append(pending, row)
		}
	}
	return pending
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

// buildEngineFromSnapshot 从任务快照构建引擎实例。
// 后端实例由快照中的 Type + Options 直接构建，不依赖名称查找。
func (r *TranslationRunner) buildEngineFromSnapshot(
	ctx context.Context,
	snapshot *service.JobExecutionSnapshot,
	resources engine.RuntimeResources,
	reporter progress.Reporter,
) (*engine.Engine, error) {
	var rounds []engine.Round
	for _, rs := range snapshot.Rounds {
		// 从快照直接构建后端实例（无需名称匹配）
		bCfg := backend.Config{
			Name:    rs.Backend.Name, // 仅用于日志，不用于匹配
			Type:    rs.Backend.Type,
			Enabled: true,
			Options: rs.Backend.Options,
		}
		b, err := backend.Build(bCfg)
		if err != nil {
			return nil, fmt.Errorf("round %q build backend: %w", rs.Name, err)
		}

		// 使用共享 limiter pool 包装后端
		if r.limiterPool != nil && rs.Backend.RateLimitPerMinute > 0 {
			limiter := r.limiterPool.Get(rs.Backend.ID, rs.Backend.RateLimitPerMinute)
			b = backend.NewRateLimitedBackend(b, limiter)
		}

		// 为每轮构建独立的 Renderer（使用该轮自己的 prompt 模板）
		roundRenderer, err := prompt.NewRenderer(rs.Prompt.Content)
		if err != nil {
			return nil, fmt.Errorf("round %q build renderer: %w", rs.Name, err)
		}

		rounds = append(rounds, engine.Round{
			Backend:          b,
			Name:             rs.Name,
			BatchSize:        rs.BatchSize,
			MaxWordsPerBatch: rs.MaxWordsPerBatch,
			Concurrency:      rs.Concurrency,
			FallbackShrink:   rs.FallbackShrink,
			Retry: backend.RetryPolicy{
				MaxAttempts: rs.Retry.MaxAttempts,
				Backoff:     time.Duration(rs.Retry.BackoffMs) * time.Millisecond,
				Jitter:      rs.Retry.Jitter,
			},
			Renderer:          roundRenderer,
			ResponseMode:      responseModeFromBackendOptions(rs.Backend.Options),
			Mode:              pipeline.RoundModeTranslate,
			ProtectRules:      rs.Strategy.Protect.Rules,
			RubyEnabled:       rs.Strategy.Ruby.Enabled,
			RubyPreserveKinds: rs.Strategy.Ruby.PreserveKinds,
			Context: &pipeline.ContextConfig{
				Enabled:  rs.Strategy.Context.Enabled,
				Before:   rs.Strategy.Context.Before,
				After:    rs.Strategy.Context.After,
				MaxChars: rs.Strategy.Context.MaxChars,
			},
			Postprocess: &pipeline.PostprocessConfig{
				TrimSpaces: rs.Strategy.Postprocess.TrimSpaces,
			},
		})
	}

	// 构建策略配置（不含后端信息）
	cfg := buildEngineConfig(snapshot)

	// 构建注音对齐重试后端
	var rubyRetryBackends []backend.Backend
	if snapshot.RubyRetry != nil && snapshot.RubyRetry.Enabled {
		rrCfg := backend.Config{
			Name:    snapshot.RubyRetry.Backend.Name,
			Type:    snapshot.RubyRetry.Backend.Type,
			Enabled: true,
			Options: snapshot.RubyRetry.Backend.Options,
		}
		rrBackend, err := backend.Build(rrCfg)
		if err != nil {
			return nil, fmt.Errorf("ruby retry backend: %w", err)
		}
		if r.limiterPool != nil && snapshot.RubyRetry.Backend.RateLimitPerMinute > 0 {
			limiter := r.limiterPool.Get(snapshot.RubyRetry.Backend.ID, snapshot.RubyRetry.Backend.RateLimitPerMinute)
			rrBackend = backend.NewRateLimitedBackend(rrBackend, limiter)
		}
		rubyRetryBackends = []backend.Backend{rrBackend}
	}

	// 构建独立自举后端
	var bootstrapBackends []backend.Backend
	if snapshot.Bootstrap != nil && snapshot.Bootstrap.Enabled {
		bsCfg := backend.Config{
			Name:    snapshot.Bootstrap.Backend.Name,
			Type:    snapshot.Bootstrap.Backend.Type,
			Enabled: true,
			Options: snapshot.Bootstrap.Backend.Options,
		}
		bsBackend, err := backend.Build(bsCfg)
		if err != nil {
			return nil, fmt.Errorf("bootstrap backend: %w", err)
		}
		if r.limiterPool != nil && snapshot.Bootstrap.Backend.RateLimitPerMinute > 0 {
			limiter := r.limiterPool.Get(snapshot.Bootstrap.Backend.ID, snapshot.Bootstrap.Backend.RateLimitPerMinute)
			bsBackend = backend.NewRateLimitedBackend(bsBackend, limiter)
		}
		bootstrapBackends = []backend.Backend{bsBackend}
	}

	return engine.NewWithOptions(engine.Options{
		Rounds:            rounds,
		BootstrapBackends: bootstrapBackends,
		RubyRetryBackends: rubyRetryBackends,
		Config:            cfg,
		Logger:            r.logger,
		Resources:         resources,
		Reporter:          reporter,
	})
}

// buildEngineConfig 从快照构建引擎运行时配置。
func buildEngineConfig(snapshot *service.JobExecutionSnapshot) *engine.Config {
	cfg := &engine.Config{
		SourceLang: snapshot.SourceLang,
		TargetLang: snapshot.TargetLang,
		TMEnabled:  snapshot.TMEnabled,
	}

	if len(snapshot.Rounds) > 0 {
		s := snapshot.Rounds[0].Strategy
		rc := repair.Config{
			Enabled:              s.Repair.Enabled,
			JSONStructural:       s.Repair.JSONStructural,
			SchemaAliases:        s.Repair.SchemaAliases,
			Partial:              s.Repair.Partial,
			PartialThreshold:     s.Repair.PartialThreshold,
			PlaceholderNormalize: s.Repair.PlaceholderNormalize,
			PromptUpgrade:        s.Repair.PromptUpgrade,
		}
		cfg.Repair = rc.ToOptions()
		cfg.Ruby = engine.RubyConfig{
			Enabled:       s.Ruby.Enabled,
			PreserveKinds: s.Ruby.PreserveKinds,
		}
		cfg.Glossary = engine.GlossaryConfig{
			Enabled: snapshot.GlossaryEnabled,
			Bootstrap: config.BootstrapConfig{
				MaxTermsPer1000Chars:   s.Glossary.Bootstrap.MaxTermsPer1000Chars,
				MinSourceLen:           s.Glossary.Bootstrap.MinSourceLen,
				InlineConflictStrategy: s.Glossary.Bootstrap.InlineConflictStrategy,
			},
		}
	}

	if snapshot.Bootstrap != nil {
		cfg.Glossary.Standalone = config.StandaloneBootstrapConfig{
			Enabled:          snapshot.Bootstrap.Enabled,
			TemplateContent:  snapshot.Bootstrap.TemplateContent,
			BatchSize:        snapshot.Bootstrap.BatchSize,
			Concurrency:      snapshot.Bootstrap.Concurrency,
			MaxTermsPerBatch: snapshot.Bootstrap.MaxTermsPerBatch,
			MinSourceLen:     snapshot.Bootstrap.MinSourceLen,
		}
	}

	return cfg
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

// responseModeFromBackendOptions 从后端 Options map 中读取 response_format 值。
// 用于设置 engine.Round.ResponseMode，使 pipeline 能区分 json/text 模式。
func responseModeFromBackendOptions(opts map[string]any) string {
	if v, ok := opts["response_format"].(string); ok {
		return v
	}
	return ""
}
