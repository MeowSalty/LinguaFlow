package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// TranslationRunner 翻译任务的执行器，通过嵌入 BaseRunner 复用公共逻辑。
type TranslationRunner struct {
	*BaseRunner
	jobs *service.TranslationJobService
}

// NewTranslationRunner 创建一个新的翻译任务执行器。
func NewTranslationRunner(
	cfg *config.Config,
	logger *slog.Logger,
	client *ent.Client,
	jobs *service.TranslationJobService,
	store *filestore.LocalStore,
	queue *Queue,
) *TranslationRunner {
	r := &TranslationRunner{
		jobs: jobs,
	}
	r.BaseRunner = newBaseRunner(cfg, logger, client, store, queue, jobs, r.processJob, "translation worker")
	return r
}

// processJob 处理单个翻译任务：加载执行上下文，筛选待处理的资源并依次执行。
func (r *TranslationRunner) processJob(ctx context.Context, jobID int) error {
	exec, err := r.jobs.LoadJobExecution(ctx, jobID)
	if err != nil {
		return err
	}
	pending := make([]*ent.JobResource, 0, len(exec.JobResources))
	for _, item := range exec.JobResources {
		if item.Status == service.JobResourceStatusPending {
			pending = append(pending, item)
		}
	}
	if len(pending) == 0 {
		return r.jobs.ReconcileJob(ctx, jobID)
	}
	if err := r.jobs.MarkJobRunning(ctx, jobID); err != nil {
		return err
	}
	for _, item := range pending {
		if err := r.processJobResource(ctx, exec, item); err != nil {
			r.logger.Warn("translation job resource failed", "job_id", jobID, "job_resource_id", item.ID, "err", err)
		}
	}
	return r.jobs.ReconcileJob(ctx, jobID)
}

// processJobResource 处理单个翻译资源：解析路径、加载片段、从快照构建引擎、调用翻译并更新结果。
func (r *TranslationRunner) processJobResource(ctx context.Context, exec *service.TranslationJobExecution, item *ent.JobResource) error {
	if err := r.jobs.MarkJobResourceRunning(ctx, item.ID); err != nil {
		return err
	}
	res, err := item.Edges.ResourceOrErr()
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	inputPath, err := r.store.Absolute(res.StoragePath)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	outputRel := strings.TrimSpace(item.OutputPath)
	outputPath := ""
	if outputRel == "" {
		outputRel = translationOutputPath(exec.Job.ID, item.ID, resourceOutputName(res))
	}
	outputPath, err = r.store.Absolute(outputRel)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	selectedRows, allRows, err := r.loadSegments(ctx, res.ID, item.SegmentIds)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	if len(selectedRows) == 0 {
		return r.jobs.MarkJobResourceCompleted(ctx, item.ID, outputRel, 0)
	}

	// 从任务快照获取执行参数
	snapshot, err := service.GetSnapshot(exec.Job)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	if snapshot == nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, fmt.Errorf("translation job has no execution snapshot"))
		return nil
	}

	// 从快照构建策略配置（用于运行时资源初始化）
	cfg := buildStrategyConfig(snapshot)

	autoApprove := snapshot.AutoApprove
	runtimeGlossary, err := r.buildRuntimeGlossary(exec.Project, cfg)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	memory, err := r.buildRuntimeTM(exec.Project, cfg)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}

	// 从快照构建引擎
	resources := engine.RuntimeResources{Glossary: runtimeGlossary, TM: memory}
	eng, err := r.buildEngineFromSnapshot(ctx, snapshot, resources)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	defer func() { _ = eng.Close() }()

	selectedIndexes := make([]int, 0, len(selectedRows))
	selectedIDsByIndex := make(map[int]int, len(selectedRows))
	for _, row := range selectedRows {
		selectedIndexes = append(selectedIndexes, row.SegmentIndex)
		selectedIDsByIndex[row.SegmentIndex] = row.ID
	}
	sort.Ints(selectedIndexes)
	existingTargets := make(map[int]string, len(allRows))
	for _, row := range allRows {
		if row.TargetText != nil {
			existingTargets[row.SegmentIndex] = *row.TargetText
		}
	}
	job := engine.FileJob(inputPath, outputPath)
	job.SourceLang = snapshot.SourceLang
	job.TargetLang = snapshot.TargetLang
	job.SegmentIndexes = selectedIndexes
	job.ExistingTargets = existingTargets
	result, err := eng.TranslateWithResult(ctx, job)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	if err := r.updateTranslatedSegments(ctx, selectedIDsByIndex, result.Segments, autoApprove); err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	if err := r.recordUsage(ctx, exec, len(selectedRows)); err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	return r.jobs.MarkJobResourceCompleted(ctx, item.ID, outputRel, len(selectedRows))
}

// buildEngineFromSnapshot 从任务快照构建引擎实例。
// 后端实例由快照中的 Type + Options 直接构建，不依赖名称查找。
func (r *TranslationRunner) buildEngineFromSnapshot(
	ctx context.Context,
	snapshot *service.JobExecutionSnapshot,
	resources engine.RuntimeResources,
) (*engine.Engine, error) {
	var rounds []engine.Round
	for _, rs := range snapshot.Rounds {
		// 从快照直接构建后端实例（无需名称匹配）
		cfg := config.BackendConfig{
			Name:    rs.Backend.Name, // 仅用于日志，不用于匹配
			Type:    rs.Backend.Type,
			Enabled: true,
			Options: rs.Backend.Options,
		}
		b, err := backend.Build(cfg)
		if err != nil {
			return nil, fmt.Errorf("round %q build backend: %w", rs.Name, err)
		}

		rounds = append(rounds, engine.Round{
			Backends:        []backend.Backend{b},
			Name:            rs.Name,
			BatchSize:       rs.BatchSize,
			Concurrency:     rs.Concurrency,
			FallbackShrink:  rs.FallbackShrink,
			RateLimitPerSec: rs.RateLimitPerSec,
			Retry: backend.RetryPolicy{
				MaxAttempts: rs.Retry.MaxAttempts,
				Backoff:     time.Duration(rs.Retry.BackoffMs) * time.Millisecond,
				Jitter:      rs.Retry.Jitter,
			},
		})
	}

	// 构建策略配置（不含后端信息）
	cfg := buildStrategyConfig(snapshot)

	return engine.NewWithOptions(engine.Options{
		Rounds:    rounds,
		Config:    cfg,
		Logger:    r.logger,
		Resources: resources,
	})
}

// buildStrategyConfig 从快照构建策略配置。
func buildStrategyConfig(snapshot *service.JobExecutionSnapshot) *config.Config {
	cfg := config.Default()

	// 提示词配置
	if len(snapshot.Rounds) > 0 {
		cfg.Prompt.SystemTemplateContent = snapshot.Rounds[0].Prompt.Content
	}

	// 策略配置
	if len(snapshot.Rounds) > 0 {
		s := snapshot.Rounds[0].Strategy
		cfg.Pipeline.Split = config.SplitConfig{
			Enabled:  s.Split.Enabled,
			Strategy: s.Split.Strategy,
			MaxChars: s.Split.MaxChars,
		}
		cfg.Pipeline.Protect = config.ProtectConfig{
			Enabled: s.Protect.Enabled,
			Rules:   s.Protect.Rules,
		}
		cfg.Pipeline.Postprocess = config.PostprocessConfig{
			Enabled:    s.Postprocess.Enabled,
			TrimSpaces: s.Postprocess.TrimSpaces,
		}
		cfg.Pipeline.Translate.Repair = config.RepairConfig{
			Enabled:              s.Repair.Enabled,
			JSONStructural:       s.Repair.JSONStructural,
			SchemaAliases:        s.Repair.SchemaAliases,
			Partial:              s.Repair.Partial,
			PartialThreshold:     s.Repair.PartialThreshold,
			PlaceholderNormalize: s.Repair.PlaceholderNormalize,
			PromptUpgrade:        s.Repair.PromptUpgrade,
		}
		cfg.Glossary = config.GlossaryConfig{
			Enabled: s.Glossary.Enabled,
			Bootstrap: config.BootstrapConfig{
				Mode:                   s.Glossary.Bootstrap.Mode,
				Save:                   s.Glossary.Bootstrap.Save,
				MaxTermsPerBatch:       s.Glossary.Bootstrap.MaxTermsPerBatch,
				MinSourceLen:           s.Glossary.Bootstrap.MinSourceLen,
				InlineConflictStrategy: s.Glossary.Bootstrap.InlineConflictStrategy,
			},
		}
	}

	return cfg
}

// buildRuntimeGlossary 根据配置构建运行时术语表，未启用则返回空实现。
func (r *TranslationRunner) buildRuntimeGlossary(projectRow *ent.Project, cfg *config.Config) (glossary.Glossary, error) {
	if cfg == nil || !cfg.Glossary.Enabled {
		return glossary.Nop{}, nil
	}
	return service.NewDatabaseGlossary(r.client, projectRow)
}

// buildRuntimeTM 根据配置构建运行时翻译记忆，未启用则返回空实现。
func (r *TranslationRunner) buildRuntimeTM(projectRow *ent.Project, cfg *config.Config) (tm.TranslationMemory, error) {
	if cfg == nil || !cfg.TranslationMemory.Enabled {
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

// updateTranslatedSegments 将翻译结果更新回数据库中的对应片段。
// 当 autoApprove 为 true 时，直接将状态设为 approved 跳过审核。
func (r *TranslationRunner) updateTranslatedSegments(ctx context.Context, selectedIDsByIndex map[int]int, segments []engine.SegmentResult, autoApprove bool) error {
	status := service.SegmentStatusTranslated
	if autoApprove {
		status = service.SegmentStatusApproved
	}
	for _, item := range segments {
		segmentID, ok := selectedIDsByIndex[item.Index]
		if !ok {
			continue
		}
		update := r.client.Segment.UpdateOneID(segmentID).
			SetSourceText(firstNonEmpty(item.SourceText, " ")).
			SetTargetText(item.TargetText).
			SetStatus(status)
		if autoApprove {
			update.ClearReviewComment()
		}
		if err := update.Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

// recordUsage 记录翻译用量到数据库。
func (r *TranslationRunner) recordUsage(ctx context.Context, exec *service.TranslationJobExecution, segmentCount int) error {
	usage := r.client.UsageRecord.Create().
		SetProjectID(exec.Project.ID).
		SetSource("translation_job").
		SetSegmentCount(segmentCount).
		SetAPICalls(segmentCount).
		SetNote(fmt.Sprintf("translation_job:%d", exec.Job.ID))
	if exec.ActorUserID > 0 {
		usage.SetUserID(exec.ActorUserID)
	}
	if exec.Project.OwnerOrgID != nil {
		usage.SetOrganizationID(*exec.Project.OwnerOrgID)
	}
	return usage.Exec(ctx)
}

// translationOutputPath 生成翻译输出文件的相对路径。
func translationOutputPath(jobID, jobResourceID int, filename string) string {
	cleanName := cleanOutputResourcePath(filename)
	return filepath.ToSlash(filepath.Join("translation_outputs", fmt.Sprintf("job-%d", jobID), fmt.Sprintf("resource-%d", jobResourceID), filepath.FromSlash(cleanName)))
}

// resourceOutputName 获取资源的输出文件名，为空时返回默认值 "resource"。
func resourceOutputName(res *ent.Resource) string {
	if res != nil && strings.TrimSpace(res.Path) != "" {
		return res.Path
	}
	return "resource"
}

// cleanOutputResourcePath 清理输出路径中的非法字符，确保路径安全。
func cleanOutputResourcePath(value string) string {
	clean := strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	clean = filepath.ToSlash(filepath.Clean(clean))
	if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "resource"
	}
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." || part == ".." {
			parts[i] = "resource"
			continue
		}
		parts[i] = strings.NewReplacer(":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_").Replace(part)
	}
	return strings.Join(parts, "/")
}
