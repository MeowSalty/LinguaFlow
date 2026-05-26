package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

type Runner struct {
	baseConfig  *config.Config
	logger      *slog.Logger
	client      *ent.Client
	projects    *service.ProjectService
	jobs        *service.JobService
	store       *filestore.LocalStore
	queue       *Queue
	concurrency int
}

func NewRunner(
	cfg *config.Config,
	logger *slog.Logger,
	client *ent.Client,
	projects *service.ProjectService,
	jobs *service.JobService,
	store *filestore.LocalStore,
	queue *Queue,
) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	concurrency := 1
	if cfg != nil && cfg.Pipeline.Translate.Concurrency > 0 {
		concurrency = cfg.Pipeline.Translate.Concurrency
	}
	return &Runner{
		baseConfig:  cfg,
		logger:      logger,
		client:      client,
		projects:    projects,
		jobs:        jobs,
		store:       store,
		queue:       queue,
		concurrency: concurrency,
	}
}

func (r *Runner) Recover(ctx context.Context) error {
	jobIDs, err := r.jobs.RecoverPendingJobs(ctx)
	if err != nil {
		return err
	}
	for _, jobID := range jobIDs {
		if err := r.queue.Enqueue(ctx, jobID); err != nil {
			return err
		}
	}
	r.logger.Info("worker recovery completed", "jobs", len(jobIDs))
	return nil
}

func (r *Runner) Run(ctx context.Context) error {
	for {
		jobID, err := r.queue.Dequeue(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		if err := r.processJob(ctx, jobID); err != nil {
			r.logger.Error("worker process job failed", "job_id", jobID, "err", err)
		}
		r.queue.Done(jobID)
	}
}

func (r *Runner) processJob(ctx context.Context, jobID int) error {
	exec, err := r.jobs.LoadJobExecution(ctx, jobID)
	if err != nil {
		return err
	}
	pending := make([]*ent.SubJob, 0, len(exec.SubJobs))
	for _, sub := range exec.SubJobs {
		if sub.Status == service.SubJobStatusPending {
			pending = append(pending, sub)
		}
	}
	if len(pending) == 0 {
		return r.jobs.ReconcileJob(ctx, jobID)
	}
	if err := r.jobs.MarkJobRunning(ctx, jobID); err != nil {
		return err
	}
	pool := NewPool(r.concurrency)
	for _, sub := range pending {
		sub := sub
		pool.Go(func() error {
			return r.processSubJob(ctx, exec, sub)
		})
	}
	if err := pool.Wait(); err != nil {
		r.logger.Warn("worker subjob pool completed with error", "job_id", jobID, "err", err)
	}
	return r.jobs.ReconcileJob(ctx, jobID)
}

func (r *Runner) processSubJob(ctx context.Context, exec *service.JobExecution, sub *ent.SubJob) error {
	if err := r.jobs.MarkSubJobRunning(ctx, sub.ID); err != nil {
		return err
	}
	inputPath, err := r.resolvePath(firstNonEmpty(strings.TrimSpace(sub.InputPath), strings.TrimSpace(exec.Job.InputPath)))
	if err != nil {
		_ = r.jobs.MarkSubJobFailed(ctx, sub.ID, err)
		return nil
	}
	outputRel := strings.TrimSpace(sub.OutputPath)
	outputPath := outputRel
	if outputRel == "" {
		ref, refErr := r.store.PrepareOutput(exec.Job.ID, sub.ID, firstNonEmpty(sub.InputFilename, filepath.Base(inputPath)))
		if refErr != nil {
			_ = r.jobs.MarkSubJobFailed(ctx, sub.ID, refErr)
			return nil
		}
		outputRel = ref.RelativePath
		outputPath = ref.AbsolutePath
	} else {
		outputPath, err = r.resolvePath(outputRel)
		if err != nil {
			_ = r.jobs.MarkSubJobFailed(ctx, sub.ID, err)
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		_ = r.jobs.MarkSubJobFailed(ctx, sub.ID, err)
		return nil
	}
	jobCfg, err := r.buildJobConfig(ctx, exec)
	if err != nil {
		_ = r.jobs.MarkSubJobFailed(ctx, sub.ID, err)
		return nil
	}
	tmScope, err := tm.ScopeFromProject(exec.Project)
	if err != nil {
		_ = r.jobs.MarkSubJobFailed(ctx, sub.ID, err)
		return nil
	}
	memory, err := tm.NewSQLite(r.client, tmScope)
	if err != nil {
		_ = r.jobs.MarkSubJobFailed(ctx, sub.ID, err)
		return nil
	}
	eng, err := engine.NewWithRuntime(jobCfg, r.logger, nil, engine.RuntimeResources{TM: memory})
	if err != nil {
		_ = r.jobs.MarkSubJobFailed(ctx, sub.ID, err)
		return nil
	}
	defer func() { _ = eng.Close() }()
	result, err := eng.TranslateWithResult(ctx, engine.TranslateJob{
		InputPath:  inputPath,
		OutputPath: outputPath,
		SourceLang: firstNonEmpty(exec.Job.SourceLang, exec.Project.SourceLang),
		TargetLang: firstNonEmpty(exec.Job.TargetLang, exec.Project.TargetLang),
	})
	if err != nil {
		_ = r.jobs.MarkSubJobFailed(ctx, sub.ID, err)
		return nil
	}
	segments := make([]service.CompletedSegment, 0, len(result.Segments))
	for _, item := range result.Segments {
		segments = append(segments, service.CompletedSegment{
			Index:      item.Index,
			SourceText: item.SourceText,
			TargetText: item.TargetText,
		})
	}
	return r.jobs.MarkSubJobCompletedWithSegments(ctx, sub.ID, outputRel, result.SegmentCount, segments)
}

func (r *Runner) buildJobConfig(ctx context.Context, exec *service.JobExecution) (*config.Config, error) {
	if r.baseConfig == nil {
		return nil, fmt.Errorf("worker: nil base config")
	}
	cfg := cloneConfig(r.baseConfig)
	if len(exec.Project.Config) > 0 {
		if raw, err := json.Marshal(exec.Project.Config); err == nil {
			if err := json.Unmarshal(raw, &cfg.Pipeline.Translate); err != nil {
				return nil, fmt.Errorf("worker: decode project translate config: %w", err)
			}
		}
	}
	translatePlan, err := r.projects.ResolveStagePlan(ctx, exec.ActorUserID, exec.Project.ID, service.StageTranslate)
	if err != nil {
		return nil, err
	}
	if len(translatePlan) == 0 {
		return nil, fmt.Errorf("worker: project %d has no backend plan", exec.Project.ID)
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
	if exec.Job.SourceLang != "" {
		cfg.SourceLang = exec.Job.SourceLang
	}
	if exec.Job.TargetLang != "" {
		cfg.TargetLang = exec.Job.TargetLang
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (r *Runner) resolvePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("worker: empty path")
	}
	if filepath.IsAbs(raw) {
		return raw, nil
	}
	return r.store.Absolute(raw)
}

func cloneConfig(in *config.Config) *config.Config {
	copyCfg := *in
	copyCfg.Backends = make([]config.BackendConfig, 0, len(in.Backends))
	for _, backendCfg := range in.Backends {
		backendCopy := backendCfg
		backendCopy.Options = cloneAnyMap(backendCfg.Options)
		copyCfg.Backends = append(copyCfg.Backends, backendCopy)
	}
	copyCfg.Prompt.Vars = cloneAnyMap(in.Prompt.Vars)
	copyCfg.Pipeline.Protect.Rules = append([]string(nil), in.Pipeline.Protect.Rules...)
	copyCfg.Pipeline.Translate.BackendOrder = append([]string(nil), in.Pipeline.Translate.BackendOrder...)
	copyCfg.Pipeline.Translate.Plan = append([]config.TranslateRoundConfig(nil), in.Pipeline.Translate.Plan...)
	copyCfg.Glossary.Bootstrap.BackendOrder = append([]string(nil), in.Glossary.Bootstrap.BackendOrder...)
	copyCfg.Plugins.Scripts = append([]string(nil), in.Plugins.Scripts...)
	copyCfg.Server.CORS.AllowedOrigins = append([]string(nil), in.Server.CORS.AllowedOrigins...)
	return &copyCfg
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
