package worker

import (
	"context"
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// SyncTaskRunner 术语同步任务执行器，通过嵌入 BaseRunner 复用公共逻辑。
type SyncTaskRunner struct {
	*BaseRunner
	syncSvc *service.GlossarySyncService
}

// NewSyncTaskRunner 创建一个新的术语同步任务执行器。
func NewSyncTaskRunner(
	cfg *config.Config,
	logger *slog.Logger,
	client *ent.Client,
	syncSvc *service.GlossarySyncService,
	queue *Queue,
) *SyncTaskRunner {
	r := &SyncTaskRunner{
		syncSvc: syncSvc,
	}
	r.BaseRunner = newBaseRunner(cfg, logger, client, nil, queue, syncSvc, r.processTask, "sync task worker")
	return r
}

// processTask 处理单个术语同步任务。
func (r *SyncTaskRunner) processTask(ctx context.Context, taskID int) error {
	return r.syncSvc.ExecuteSyncTask(ctx, taskID)
}
