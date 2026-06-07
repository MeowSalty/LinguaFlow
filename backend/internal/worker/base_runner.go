package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
)

// JobController 定义任务生命周期管理的公共接口。
// *service.JobService 和 *service.TranslationJobService 均满足此接口。
type JobController interface {
	RecoverPendingJobs(ctx context.Context) ([]int, error)
	ReconcileJob(ctx context.Context, jobID int) error
}

// BaseRunner 包含所有 Worker Runner 共享的字段和逻辑。
type BaseRunner struct {
	baseConfig *config.Config
	logger     *slog.Logger
	client     *ent.Client
	projects   *service.ProjectService
	store      *filestore.LocalStore
	queue      *Queue
	jobCtrl    JobController                              // 任务生命周期控制器
	processFn  func(ctx context.Context, jobID int) error // 具体的任务处理函数，由子类注入
	tag        string                                     // 日志标签，如 "worker" 或 "translation worker"
}

func newBaseRunner(
	cfg *config.Config,
	logger *slog.Logger,
	client *ent.Client,
	projects *service.ProjectService,
	store *filestore.LocalStore,
	queue *Queue,
	jobCtrl JobController,
	processFn func(ctx context.Context, jobID int) error,
	tag string,
) *BaseRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &BaseRunner{
		baseConfig: cfg,
		logger:     logger,
		client:     client,
		projects:   projects,
		store:      store,
		queue:      queue,
		jobCtrl:    jobCtrl,
		processFn:  processFn,
		tag:        tag,
	}
}

// Recover 尝试从数据库中恢复挂起的任务并重新入队。
func (r *BaseRunner) Recover(ctx context.Context) error {
	jobIDs, err := r.jobCtrl.RecoverPendingJobs(ctx)
	if err != nil {
		return err
	}
	for _, jobID := range jobIDs {
		if err := r.queue.Enqueue(ctx, jobID); err != nil {
			return err
		}
	}
	r.logger.Info(r.tag+" recovery completed", "jobs", len(jobIDs))
	return nil
}

// Run 启动主循环：从队列出队任务并处理。
func (r *BaseRunner) Run(ctx context.Context) error {
	for {
		jobID, err := r.queue.Dequeue(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		if err := r.processFn(ctx, jobID); err != nil {
			r.logger.Error(r.tag+" process job failed", "job_id", jobID, "err", err)
		}
		r.queue.Done(jobID)
	}
}

// resolvePath 将路径解析为绝对路径，相对路径通过 store 转换。
func (r *BaseRunner) resolvePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("worker: empty path")
	}
	if filepath.IsAbs(raw) {
		return raw, nil
	}
	return r.store.Absolute(raw)
}

// buildBackendConfig 解析后端计划并填充配置。
// 这是各 Runner 的 buildJobConfig 方法共享的公共逻辑。
func (r *BaseRunner) buildBackendConfig(ctx context.Context, cfg *config.Config, actorUserID, projectID int) error {
	translatePlan, err := r.projects.ResolveStagePlan(ctx, actorUserID, projectID, service.StageTranslate)
	if err != nil {
		return err
	}
	if len(translatePlan) == 0 {
		return fmt.Errorf("worker: project %d has no backend plan", projectID)
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
	bootstrapPlan, bootstrapErr := r.projects.ResolveStagePlan(ctx, actorUserID, projectID, service.StageBootstrap)
	if bootstrapErr == nil && len(bootstrapPlan) > 0 {
		cfg.Glossary.Bootstrap.BackendMode = config.BackendModeRestrict
		cfg.Glossary.Bootstrap.BackendOrder = make([]string, 0, len(bootstrapPlan))
		for _, binding := range bootstrapPlan {
			cfg.Glossary.Bootstrap.BackendOrder = append(cfg.Glossary.Bootstrap.BackendOrder, binding.Name)
		}
	}
	return nil
}
