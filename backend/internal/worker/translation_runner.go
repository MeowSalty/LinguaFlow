package worker

import (
	"context"
	"errors"
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

type TranslationRunner struct {
	baseConfig *config.Config
	logger     *slog.Logger
	client     *ent.Client
	projects   *service.ProjectService
	jobs       *service.TranslationJobService
	store      *filestore.LocalStore
	queue      *Queue
}

func NewTranslationRunner(
	cfg *config.Config,
	logger *slog.Logger,
	client *ent.Client,
	projects *service.ProjectService,
	jobs *service.TranslationJobService,
	store *filestore.LocalStore,
	queue *Queue,
) *TranslationRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &TranslationRunner{baseConfig: cfg, logger: logger, client: client, projects: projects, jobs: jobs, store: store, queue: queue}
}

func (r *TranslationRunner) Recover(ctx context.Context) error {
	jobIDs, err := r.jobs.RecoverPendingJobs(ctx)
	if err != nil {
		return err
	}
	for _, jobID := range jobIDs {
		if err := r.queue.Enqueue(ctx, jobID); err != nil {
			return err
		}
	}
	r.logger.Info("translation worker recovery completed", "jobs", len(jobIDs))
	return nil
}

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
			r.logger.Error("translation worker process job failed", "job_id", jobID, "err", err)
		}
		r.queue.Done(jobID)
	}
}

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
		outputRel = translationOutputPath(exec.Job.ID, item.ID, res.Filename)
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
	result, err := eng.TranslateWithResult(ctx, engine.TranslateJob{
		InputPath:       inputPath,
		OutputPath:      outputPath,
		SourceLang:      jobCfg.SourceLang,
		TargetLang:      jobCfg.TargetLang,
		SegmentIndexes:  selectedIndexes,
		ExistingTargets: existingTargets,
	})
	if err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	if err := r.updateTranslatedSegments(ctx, selectedIDsByIndex, result.Segments); err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	if err := r.recordUsage(ctx, exec, len(selectedRows)); err != nil {
		_ = r.jobs.MarkJobResourceFailed(ctx, item.ID, err)
		return nil
	}
	return r.jobs.MarkJobResourceCompleted(ctx, item.ID, outputRel, len(selectedRows))
}

func (r *TranslationRunner) buildJobConfig(ctx context.Context, exec *service.TranslationJobExecution) (*config.Config, error) {
	cfg, err := service.MergeTranslationConfig(r.baseConfig, nil, exec.Job.TranslationConfig)
	if err != nil {
		return nil, err
	}
	translatePlan, err := r.projects.ResolveStagePlan(ctx, exec.ActorUserID, exec.Project.ID, service.StageTranslate)
	if err != nil {
		return nil, err
	}
	if len(translatePlan) == 0 {
		return nil, fmt.Errorf("translation worker: project %d has no backend plan", exec.Project.ID)
	}
	cfg.Backends = make([]config.BackendConfig, 0, len(translatePlan))
	cfg.Pipeline.Translate.BackendMode = config.BackendModeRestrict
	cfg.Pipeline.Translate.BackendOrder = make([]string, 0, len(translatePlan))
	priorityBase := len(translatePlan)
	for i, binding := range translatePlan {
		cfg.Backends = append(cfg.Backends, config.BackendConfig{
			Name:     binding.Name,
			Type:     binding.Type,
			Enabled:  true,
			Priority: priorityBase - i,
			Options:  cloneAnyMap(binding.Options),
		})
		cfg.Pipeline.Translate.BackendOrder = append(cfg.Pipeline.Translate.BackendOrder, binding.Name)
	}
	bootstrapPlan, bootstrapErr := r.projects.ResolveStagePlan(ctx, exec.ActorUserID, exec.Project.ID, service.StageBootstrap)
	if bootstrapErr == nil && len(bootstrapPlan) > 0 {
		cfg.Glossary.Bootstrap.BackendMode = config.BackendModeRestrict
		cfg.Glossary.Bootstrap.BackendOrder = make([]string, 0, len(bootstrapPlan))
		for _, binding := range bootstrapPlan {
			cfg.Glossary.Bootstrap.BackendOrder = append(cfg.Glossary.Bootstrap.BackendOrder, binding.Name)
		}
	}
	if exec.Project.SourceLang != "" {
		cfg.SourceLang = exec.Project.SourceLang
	}
	if exec.Project.TargetLang != "" {
		cfg.TargetLang = exec.Project.TargetLang
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (r *TranslationRunner) buildRuntimeGlossary(projectRow *ent.Project, cfg *config.Config) (glossary.Glossary, error) {
	if cfg == nil || !cfg.Glossary.Enabled {
		return glossary.Nop{}, nil
	}
	return service.NewDatabaseGlossary(r.client, projectRow)
}

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

func (r *TranslationRunner) updateTranslatedSegments(ctx context.Context, selectedIDsByIndex map[int]int, segments []engine.SegmentResult) error {
	for _, item := range segments {
		segmentID, ok := selectedIDsByIndex[item.Index]
		if !ok {
			continue
		}
		if err := r.client.Segment.UpdateOneID(segmentID).
			SetSourceText(firstNonEmpty(item.SourceText, " ")).
			SetTargetText(item.TargetText).
			SetStatus(service.SegmentStatusTranslated).
			Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

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

func translationOutputPath(jobID, jobResourceID int, filename string) string {
	cleanName := strings.TrimSpace(filepath.Base(filename))
	if cleanName == "" || cleanName == "." || cleanName == ".." {
		cleanName = "resource"
	}
	return filepath.ToSlash(filepath.Join("translation_outputs", fmt.Sprintf("job-%d", jobID), fmt.Sprintf("resource-%d", jobResourceID), cleanName))
}
