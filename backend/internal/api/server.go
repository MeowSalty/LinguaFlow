package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
	serverCfg             *config.ServerConfig
	logger                *slog.Logger
	db                    *sql.DB
	entClient             *ent.Client
	mode                  string    // "server" | "local"
	localUser             *ent.User // 本地模式下非 nil
	authService           *service.AuthService
	adminService          *service.AdminService
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
	dispatcher            *worker.Dispatcher
	resMutex              *worker.ResourceMutex
	httpServer            *http.Server
	executionPlanHandler  *HandlerExecutionPlan
	eventBroker           *event.Broker
	ready                 atomic.Bool
}

func (s *Server) isLocal() bool {
	return s.mode == config.ModeLocal
}

func (s *Server) localAuthUser() (authenticatedUser, bool) {
	if s.isLocal() && s.localUser != nil {
		return authenticatedUser{User: s.localUser}, true
	}
	return authenticatedUser{}, false
}

func NewServer(cfg *config.ServerConfig, logger *slog.Logger, db *sql.DB, client *ent.Client, mode string, localUser *ent.User) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}

	hybridStore, err := event.NewHybridStore(
		event.NewRingBufferStore(event.DefaultRingBufferConfig()),
		event.NewEntEventStore(client),
	)
	if err != nil {
		return nil, fmt.Errorf("init event store: %w", err)
	}

	s := &Server{
		serverCfg:   cfg,
		logger:      logger,
		db:          db,
		entClient:   client,
		mode:        mode,
		localUser:   localUser,
		eventBroker: event.NewBroker(hybridStore),
	}
	limiterPool := backend.NewLimiterPool()
	s.adminService = service.NewAdminService(client)
	s.authService = service.NewAuthService(client, service.AuthConfigFromServer(*cfg), s.adminService)
	s.userService = service.NewUserService(client, s.authService)

	// Seed default system settings from YAML config (only writes if table is empty for each key).
	regEnabled := "true"
	if !cfg.Registration.Enabled {
		regEnabled = "false"
	}
	autoAdmin := "true"
	if !cfg.Registration.AutoAdmin {
		autoAdmin = "false"
	}
	if err := s.adminService.InitializeSettings(context.Background(), map[string]string{
		service.SettingRegistrationEnabled: regEnabled,
		service.SettingDefaultUserRole:     "user",
		service.SettingAutoAdmin:           autoAdmin,
	}); err != nil {
		logger.Warn("failed to initialize system settings", "error", err)
	}
	s.backendSvc = service.NewBackendService(client, s.userService, limiterPool)
	s.projectSvc = service.NewProjectService(client, s.userService)
	s.executionPlanSvc = service.NewExecutionPlanService(client, s.userService)
	s.glossarySvc = service.NewGlossaryService(client, s.projectSvc)
	s.promptTemplateSvc = service.NewPromptTemplateService(client)
	s.translationProfileSvc = service.NewTranslationProfileService(client)
	jobStore, err := filestore.NewLocal(filepath.Join(cfg.DataDir, "jobs"))
	if err != nil {
		return nil, err
	}
	s.jobStore = jobStore
	s.translationJobSvc = service.NewTranslationJobService(client, s.projectSvc, s.executionPlanSvc, s.backendSvc, s.promptTemplateSvc, s.translationProfileSvc, jobStore, s.eventBroker)
	s.executionPlanHandler = NewHandlerExecutionPlan(s.executionPlanSvc, s)
	s.reviewSvc = service.NewReviewService(client, s.projectSvc)
	s.segmentSvc = service.NewSegmentService(client, s.projectSvc)
	s.statsSvc = service.NewStatsService(client, s.projectSvc)
	s.auditSvc = service.NewAuditService(client, s.userService, s.projectSvc)
	s.glossarySyncSvc = service.NewGlossarySyncService(client, s.glossarySvc, s.projectSvc, s.auditSvc, logger)
	s.resourceSvc = service.NewResourceService(client, s.projectSvc, jobStore)

	// 创建 ResourceMutex
	s.resMutex = worker.NewResourceMutex()

	translationQueue := worker.NewQueue(cfg.Workers.Translation.QueueCapacity)
	syncQueue := worker.NewQueue(cfg.Workers.Sync.QueueCapacity)

	// 创建 Runner
	translationRunner := worker.NewTranslationRunner(
		logger, client, s.translationJobSvc, jobStore,
		translationQueue, s.eventBroker, limiterPool, s.resMutex,
	)
	syncTaskRunner := worker.NewSyncTaskRunner(
		logger, client, s.glossarySyncSvc, syncQueue, s.resMutex,
	)

	// 创建 Dispatcher
	s.dispatcher = worker.NewDispatcher(logger, s.resMutex, cfg.Workers, translationRunner, syncTaskRunner)

	s.httpServer = &http.Server{
		Addr:              cfg.Address(),
		Handler:           s.newRouter(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s, nil
}

func (s *Server) Run(ctx context.Context, ln net.Listener) error {
	serveErr := make(chan error, 1)

	// 启动 Dispatcher（内部执行 Recover + WorkerPool）
	if s.dispatcher != nil {
		go func() {
			if err := s.dispatcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Error("dispatcher stopped with error", "err", err)
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
		s.logger.Info("http server listening", "addr", ln.Addr().String())
		serveErr <- s.httpServer.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		s.ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.serverCfg.ShutdownTimeout)
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
