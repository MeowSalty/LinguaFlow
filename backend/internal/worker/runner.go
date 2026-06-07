package worker

import (
	"context"
	"encoding/json"
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

// Runner 文件翻译任务的执行器，通过嵌入 BaseRunner 复用公共逻辑。
type Runner struct {
	*BaseRunner
	jobs        *service.JobService
	concurrency int // 并发处理子任务的协程数
}

// NewRunner 创建一个新的文件翻译任务执行器。
func NewRunner(
	cfg *config.Config,
	logger *slog.Logger,
	client *ent.Client,
	projects *service.ProjectService,
	jobs *service.JobService,
	store *filestore.LocalStore,
	queue *Queue,
) *Runner {
	concurrency := 1
	if cfg != nil && cfg.Pipeline.Translate.Concurrency > 0 {
		concurrency = cfg.Pipeline.Translate.Concurrency
	}
	r := &Runner{
		jobs:        jobs,
		concurrency: concurrency,
	}
	r.BaseRunner = newBaseRunner(cfg, logger, client, projects, store, queue, jobs, r.processJob, "worker")
	return r
}

// processJob 处理单个翻译任务：加载执行上下文，筛选待处理子任务，并发执行。
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

// processSubJob 处理单个子任务：解析路径、构建配置、调用引擎翻译并记录结果。
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
	job := engine.FileJob(inputPath, outputPath)
	job.SourceLang = firstNonEmpty(exec.Job.SourceLang, exec.Project.SourceLang)
	job.TargetLang = firstNonEmpty(exec.Job.TargetLang, exec.Project.TargetLang)
	result, err := eng.TranslateWithResult(ctx, job)
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

// buildJobConfig 构建翻译任务配置：克隆基础配置、合并项目配置、解析后端计划、设置语言。
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
	if err := r.buildBackendConfig(ctx, cfg, exec.ActorUserID, exec.Project.ID); err != nil {
		return nil, err
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

// cloneConfig 深拷贝配置对象，避免并发修改。
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

// cloneAnyMap 浅拷贝 map[string]any。
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

// firstNonEmpty 返回参数中第一个非空白字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
