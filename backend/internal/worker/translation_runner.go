package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	projects *service.ProjectService,
	jobs *service.TranslationJobService,
	store *filestore.LocalStore,
	queue *Queue,
) *TranslationRunner {
	r := &TranslationRunner{
		jobs: jobs,
	}
	r.BaseRunner = newBaseRunner(cfg, logger, client, projects, store, queue, jobs, r.processJob, "translation worker")
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

// processJobResource 处理单个翻译资源：解析路径、加载片段、构建配置、调用引擎翻译并更新结果。
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
	jobCfg, err := r.buildJobConfig(ctx, exec)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	autoApprove := false
	if exec.Job.TranslationConfig != nil {
		if v, ok := exec.Job.TranslationConfig["auto_approve"].(bool); ok {
			autoApprove = v
		}
	}
	runtimeGlossary, err := r.buildRuntimeGlossary(exec.Project, jobCfg)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	memory, err := r.buildRuntimeTM(exec.Project, jobCfg)
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	eng, err := engine.NewWithRuntime(jobCfg, r.logger, nil, engine.RuntimeResources{Glossary: runtimeGlossary, TM: memory})
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
	job.SourceLang = jobCfg.SourceLang
	job.TargetLang = jobCfg.TargetLang
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

// buildJobConfig 构建翻译任务配置：合并翻译配置、解析后端计划、校验配置。
func (r *TranslationRunner) buildJobConfig(ctx context.Context, exec *service.TranslationJobExecution) (*config.Config, error) {
	cfg, err := service.MergeTranslationConfig(r.baseConfig, nil, exec.Job.TranslationConfig)
	if err != nil {
		return nil, err
	}
	if err := r.buildBackendConfig(ctx, cfg, exec.ActorUserID, exec.Project.ID); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
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
