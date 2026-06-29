package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/worker"
)

const readinessPingTimeout = 2 * time.Second

type Server struct {
	config                *config.Config
	logger                *slog.Logger
	db                    *sql.DB
	entClient             *ent.Client
	authService           *service.AuthService
	userService           *service.UserService
	backendSvc            *service.BackendService
	projectSvc            *service.ProjectService
	glossarySvc           *service.GlossaryService
	glossarySyncSvc       *service.GlossarySyncService
	promptTemplateSvc     *service.PromptTemplateService
	translationProfileSvc *service.TranslationProfileService
	translationJobSvc     *service.TranslationJobService
	executionPlanSvc      *service.ExecutionPlanService
	reviewSvc             *service.ReviewService
	segmentSvc            *service.SegmentService
	statsSvc              *service.StatsService
	auditSvc              *service.AuditService
	resourceSvc           *service.ResourceService
	jobStore              *filestore.LocalStore
	translationJobQueue   *worker.Queue
	translationJobRunner  *worker.TranslationRunner
	syncTaskQueue         *worker.Queue
	syncTaskRunner        *worker.SyncTaskRunner
	httpServer            *http.Server
	executionPlanHandler  *HandlerExecutionPlan
	eventBroker           *event.Broker
	ready                 atomic.Bool
}

func NewServer(cfg *config.Config, logger *slog.Logger, db *sql.DB, client *ent.Client) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		config:      cfg,
		logger:      logger,
		db:          db,
		entClient:   client,
		authService: service.NewAuthService(client, service.AuthConfigFromServer(cfg.Server)),
		eventBroker: event.NewBroker(),
	}
	limiterPool := backend.NewLimiterPool()
	s.userService = service.NewUserService(client, s.authService)
	s.backendSvc = service.NewBackendService(client, s.userService, limiterPool)
	s.projectSvc = service.NewProjectService(client, s.userService)
	s.executionPlanSvc = service.NewExecutionPlanService(client, s.userService)
	s.glossarySvc = service.NewGlossaryService(client, s.projectSvc)
	s.promptTemplateSvc = service.NewPromptTemplateService(client)
	s.translationProfileSvc = service.NewTranslationProfileService(client)
	jobStore, err := filestore.NewLocal(filepath.Join(cfg.Server.DataDir, "jobs"))
	if err != nil {
		return nil, err
	}
	s.jobStore = jobStore
	s.translationJobSvc = service.NewTranslationJobService(client, s.projectSvc, s.executionPlanSvc, s.backendSvc, s.promptTemplateSvc, s.translationProfileSvc, jobStore, s.eventBroker)
	s.executionPlanHandler = NewHandlerExecutionPlan(s.executionPlanSvc)
	s.reviewSvc = service.NewReviewService(client, s.projectSvc)
	s.segmentSvc = service.NewSegmentService(client, s.projectSvc)
	s.statsSvc = service.NewStatsService(client, s.projectSvc)
	s.auditSvc = service.NewAuditService(client, s.userService, s.projectSvc)
	s.glossarySyncSvc = service.NewGlossarySyncService(client, s.glossarySvc, s.projectSvc, s.auditSvc, logger)
	s.resourceSvc = service.NewResourceService(client, s.projectSvc, jobStore)
	queueSize := cfg.Pipeline.Translate.Concurrency * 8
	if queueSize < 16 {
		queueSize = 16
	}
	s.translationJobQueue = worker.NewQueue(queueSize)
	s.translationJobRunner = worker.NewTranslationRunner(cfg, logger, client, s.translationJobSvc, jobStore, s.translationJobQueue, s.eventBroker, limiterPool)
	s.syncTaskQueue = worker.NewQueue(100)
	s.syncTaskRunner = worker.NewSyncTaskRunner(cfg, logger, client, s.glossarySyncSvc, s.syncTaskQueue)
	s.httpServer = &http.Server{
		Addr:              cfg.Server.Address(),
		Handler:           s.newRouter(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s, nil
}

func (s *Server) Run(ctx context.Context) error {
	serveErr := make(chan error, 1)
	if s.translationJobRunner != nil {
		if err := s.translationJobRunner.Recover(ctx); err != nil {
			return err
		}
		go func() {
			if err := s.translationJobRunner.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Error("translation job runner stopped with error", "err", err)
			}
		}()
	}

	// 恢复并启动同步任务执行器
	if s.syncTaskRunner != nil {
		if err := s.syncTaskRunner.Recover(ctx); err != nil {
			s.logger.Warn("failed to recover sync tasks", "error", err)
		}
		go func() {
			if err := s.syncTaskRunner.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Error("sync task runner stopped with error", "err", err)
			}
		}()
	}

	// 启动时执行一次过期任务清理
	if err := s.glossarySyncSvc.CleanupExpiredTasks(ctx); err != nil {
		s.logger.Warn("failed to cleanup expired sync tasks on startup", "error", err)
	}

	// 启动过期任务清理定时器（每小时执行一次）
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.glossarySyncSvc.CleanupExpiredTasks(ctx); err != nil {
					s.logger.Warn("failed to cleanup expired sync tasks", "error", err)
				}
			}
		}
	}()

	s.ready.Store(true)

	go func() {
		s.logger.Info("http server listening", "addr", s.httpServer.Addr)
		serveErr <- s.httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		s.ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.config.Server.ShutdownTimeout)
		defer cancel()
		if err := s.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-serveErr
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case err := <-serveErr:
		s.ready.Store(false)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("http server shutting down")
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) checkReadiness(ctx context.Context) error {
	if !s.ready.Load() {
		return errors.New("server is not accepting requests")
	}
	if s.db == nil {
		return errors.New("database is not configured")
	}

	pingCtx, cancel := context.WithTimeout(ctx, readinessPingTimeout)
	defer cancel()
	if err := s.db.PingContext(pingCtx); err != nil {
		return err
	}
	return nil
}
