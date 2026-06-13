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

// JobController 定义任务生命周期管理的公共接口。
// *service.TranslationJobService 满足此接口。
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
// 返回值：(translateBackends, bootstrapBackends, error)。
// bootstrapBackends 可能为 nil（项目未配置 bootstrap 后端时）。
func (r *BaseRunner) buildBackendConfig(ctx context.Context, cfg *config.Config, actorUserID, projectID int) ([]service.ProjectBackendBinding, []service.ProjectBackendBinding, error) {
	translatePlan, err := r.projects.ResolveStagePlan(ctx, actorUserID, projectID, service.StageTranslate)
	if err != nil {
		return nil, nil, err
	}
	if len(translatePlan) == 0 {
		return nil, nil, fmt.Errorf("worker: project %d has no backend plan", projectID)
	}
	cfg.Backends = make([]config.BackendConfig, 0, len(translatePlan))
	priorityBase := len(translatePlan)
	for i, binding := range translatePlan {
		cfg.Backends = append(cfg.Backends, config.BackendConfig{
			Name:     binding.Name,
			Type:     binding.Type,
			Enabled:  true,
			Priority: priorityBase - i,
			Options:  cloneAnyMap(binding.Options),
		})
	}
	bootstrapPlan, bootstrapErr := r.projects.ResolveStagePlan(ctx, actorUserID, projectID, service.StageBootstrap)
	if bootstrapErr != nil {
		bootstrapPlan = nil
	}
	return translatePlan, bootstrapPlan, nil
}
