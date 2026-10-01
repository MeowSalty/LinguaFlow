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
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	backendentity "github.com/MeowSalty/LinguaFlow/backend/internal/ent/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/telemetry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/worker"
)

const readinessPingTimeout = 2 * time.Second

type Server struct {
	serverCfg                    *config.ServerConfig
	logger                       *slog.Logger
	db                           *sql.DB
	entClient                    *ent.Client
	mode                         string    // "server" | "local"
	localUser                    *ent.User // 本地模式下非 nil
	authService                  *service.AuthService
	adminService                 *service.AdminService
	settingsService              *service.SettingsService
	runtimeAddress               config.RuntimeAddress
	userService                  *service.UserService
	backendSvc                   *service.BackendService
	projectSvc                   *service.ProjectService
	glossarySvc                  *service.GlossaryService
	glossarySyncSvc              *service.GlossarySyncService
	translationPromptTemplateSvc *service.TranslationPromptTemplateService
	bootstrapPromptTemplateSvc   *service.BootstrapPromptTemplateService
	prunePromptTemplateSvc       *service.PrunePromptTemplateService
	glossaryPruneSvc             *service.GlossaryPruneService
	executionProfileSvc          *service.ExecutionProfileService
	qaRecheck                    *service.QARecheckService
	jobSvc                       *service.JobService
	previewSvc                   *service.PreviewService
	revisionPreviewSvc           *service.RevisionPreviewService
	quickTranslateSvc            *service.QuickTranslateService
	executionPlanSvc             *service.ExecutionPlanService
	reviewSvc                    *service.ReviewService
	segmentSvc                   *service.SegmentService
	statsSvc                     *service.StatsService
	auditSvc                     *service.AuditService
	resourceSvc                  *service.ResourceService
	jobStore                     *filestore.LocalStore
	dispatcher                   *worker.Dispatcher
	resMutex                     *worker.ResourceMutex
	httpServer                   *http.Server
	executionPlanHandler         *HandlerExecutionPlan
	eventBroker                  *event.Broker
	sseReplayBatch               int
	sseMaxReplay                 int
	ready                        atomic.Bool
	collector                    *telemetry.Collector
	limiterPool                  *backend.LimiterPool
	httpClients                  *telemetry.HTTPClients
	runMu                        sync.Mutex
	runCancel                    context.CancelFunc
	runStarted                   bool
	shuttingDown                 bool
	shutdownOnce                 sync.Once
	shutdownDone                 chan struct{}
	shutdownErr                  error
}

func (s *Server) isLocal() bool {
	return s.mode == config.ModeLocal
}

func (s *Server) shouldServeUI() bool {
	return s.serverCfg.ServeUI
}

func (s *Server) localAuthUser() (authenticatedUser, bool) {
	if s.isLocal() && s.localUser != nil {
		return authenticatedUser{User: s.localUser}, true
	}
	return authenticatedUser{}, false
}

func NewServer(cfg *config.ServerConfig, keys *credential.Keyring, logger *slog.Logger, db *sql.DB, client *ent.Client, mode string, localUser *ent.User, addresses ...config.RuntimeAddress) (*Server, error) {
	if keys == nil || !keys.HasKey(keys.ActiveKeyID()) {
		return nil, fmt.Errorf("resolved provider credential keys are required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	entEventStore := event.NewEntEventStore(client, cfg.Database.Driver)
	hybridStore, err := event.NewHybridStore(
		event.NewRingBufferStore(event.RingBufferConfig{Capacity: cfg.SSE.RingBufferCapacity}),
		entEventStore,
	)
	if err != nil {
		return nil, fmt.Errorf("init event store: %w", err)
	}

	s := &Server{
		serverCfg:      cfg,
		logger:         logger,
		db:             db,
		entClient:      client,
		mode:           mode,
		localUser:      localUser,
		eventBroker:    event.NewBroker(hybridStore).WithHistorian(entEventStore),
		sseReplayBatch: cfg.SSE.ReplayBatchSize,
		sseMaxReplay:   cfg.SSE.MaxReplayEvents,
	}
	s.runtimeAddress = config.RuntimeAddress{Host: cfg.Host, Port: cfg.Port}
	if len(addresses) > 0 {
		s.runtimeAddress = addresses[0]
	}
	limiterPool := backend.NewLimiterPool()
	s.collector = telemetry.NewCollector()
	s.httpClients = telemetry.NewHTTPClients(s.collector)
	s.limiterPool = limiterPool
	s.shutdownDone = make(chan struct{})
	initialized := false
	defer func() {
		if !initialized {
			limiterPool.Shutdown()
			s.httpClients.Shutdown()
		}
	}()
	policies, err := client.Backend.Query().Select(backendentity.FieldID, backendentity.FieldRateLimitPerMinute).All(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load current backend capacity policies: %w", err)
	}
	rates := make(map[int]int, len(policies))
	for _, policy := range policies {
		rates[policy.ID] = policy.RateLimitPerMinute
	}
	limiterPool.Initialize(rates)
	s.adminService = service.NewAdminService(client)
	s.settingsService = service.NewSettingsService(client)
	s.authService = service.NewAuthService(client, service.AuthConfigFromServer(*cfg), s.settingsService)
	s.userService = service.NewUserService(client, s.authService)

	s.backendSvc = service.NewBackendService(client, s.userService, limiterPool, s.httpClients)
	credentials := service.NewCredentialService(client, keys, s.userService)
	if err := credentials.ValidateKeys(context.Background()); err != nil {
		return nil, fmt.Errorf("validate provider credential keys: %w", err)
	}
	s.backendSvc.SetCredentials(credentials)
	s.projectSvc = service.NewProjectService(client, s.userService)
	s.executionProfileSvc = service.NewExecutionProfileService(client, s.userService)
	s.qaRecheck = service.NewQARecheckService(client, s.projectSvc, s.executionProfileSvc, logger)
	s.executionPlanSvc = service.NewExecutionPlanService(client, s.userService, s.executionProfileSvc)
	s.glossarySvc = service.NewGlossaryService(client, s.projectSvc)
	s.translationPromptTemplateSvc = service.NewTranslationPromptTemplateService(client)
	s.bootstrapPromptTemplateSvc = service.NewBootstrapPromptTemplateService(client)
	s.prunePromptTemplateSvc = service.NewPrunePromptTemplateService(client)
	jobStore, err := filestore.NewLocal(filepath.Join(cfg.DataDir, "jobs"))
	if err != nil {
		return nil, err
	}
	s.jobStore = jobStore
	s.jobSvc = service.NewJobService(client, s.projectSvc, s.executionPlanSvc, s.backendSvc, s.translationPromptTemplateSvc, s.bootstrapPromptTemplateSvc, s.executionProfileSvc, jobStore, s.eventBroker)
	s.executionPlanHandler = NewHandlerExecutionPlan(s.executionPlanSvc, s)
	s.reviewSvc = service.NewReviewService(client, s.projectSvc)
	s.segmentSvc = service.NewSegmentService(client, s.projectSvc, database.DialectFor(cfg.Database.Driver), cfg.RevisionRetention, logger)
	s.statsSvc = service.NewStatsService(client, s.projectSvc)
	s.auditSvc = service.NewAuditService(client, s.userService, s.projectSvc)
	s.glossarySyncSvc = service.NewGlossarySyncService(client, s.glossarySvc, s.projectSvc, s.auditSvc, logger)
	s.glossaryPruneSvc = service.NewGlossaryPruneService(client, s.projectSvc, s.backendSvc, s.glossarySvc, s.prunePromptTemplateSvc, limiterPool, logger, s.httpClients)
	s.resourceSvc = service.NewResourceService(client, s.projectSvc, jobStore)
	previewRunner := worker.NewPreviewRunner(logger, client, limiterPool, s.httpClients)
	previewRunner.SetCredentials(credentials, credentials)
	revisionRunner := worker.NewRevisionPreviewRunner(logger, client, limiterPool, s.httpClients)
	revisionRunner.SetCredentials(credentials, credentials)
	revisionSemaphore := service.NewPreviewSemaphore(cfg.Preview.MaxConcurrency)
	s.previewSvc = service.NewPreviewServiceWithSemaphore(
		logger,
		client,
		s.projectSvc,
		s.jobSvc,
		s.auditSvc,
		previewRunner,
		cfg.JWTSecret,
		cfg.Preview.ApplyTokenTTL,
		cfg.Preview.MaxConcurrency,
		cfg.Preview.Timeout,
		revisionSemaphore,
	)
	s.revisionPreviewSvc = service.NewRevisionPreviewService(
		logger,
		client,
		s.projectSvc,
		s.jobSvc,
		revisionRunner,
		cfg.JWTSecret,
		cfg.Preview.ApplyTokenTTL,
		cfg.Preview.MaxConcurrency,
		cfg.Preview.Timeout,
		revisionSemaphore,
	)
	quickTranslateRunner := worker.NewQuickTranslateRunner(logger, client, limiterPool, s.httpClients)
	quickTranslateRunner.SetCredentials(credentials, credentials)
	s.quickTranslateSvc = service.NewQuickTranslateService(
		logger,
		client,
		s.projectSvc,
		s.jobSvc,
		s.backendSvc,
		s.executionPlanSvc,
		s.auditSvc,
		quickTranslateRunner,
		cfg.QuickTranslate.MaxConcurrency,
		cfg.QuickTranslate.Timeout,
	)

	// 创建 ResourceMutex
	s.resMutex = worker.NewResourceMutex()

	translationQueue := worker.NewQueue(cfg.Workers.Translation.QueueCapacity)
	syncQueue := worker.NewQueue(cfg.Workers.Sync.QueueCapacity)

	// RSS 保险丝：进程级双水位准入闸门（0 = 关闭）。仅是准入控制，
	// 不改变任务状态；触发时资源排队、在途请求继续。
	pipelineConfig, rssFuse := worker.PipelineRuntime(cfg.Pipeline, logger)

	// 创建 Runner
	translationRunner := worker.NewJobRunner(
		logger, client, s.jobSvc, jobStore,
		translationQueue, s.eventBroker, limiterPool, s.resMutex, cfg.Database.Driver,
		pipelineConfig,
		rssFuse, s.httpClients,
	)
	translationRunner.SetCredentials(credentials, credentials)
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
	initialized = true
	return s, nil
}

func (s *Server) Run(ctx context.Context, ln net.Listener) error {
	s.runMu.Lock()
	if s.runStarted || s.shuttingDown {
		s.runMu.Unlock()
		return errors.New("server already started or shutting down")
	}
	s.runStarted = true
	runCtx, cancel := context.WithCancel(ctx)
	s.runCancel = cancel
	s.httpServer.BaseContext = func(net.Listener) context.Context { return runCtx }
	s.runMu.Unlock()
	defer cancel()
	serveErr := make(chan error, 1)

	// 启动 Dispatcher（内部执行 Recover + WorkerPool）
	if s.dispatcher != nil {
		go func() {
			if err := s.dispatcher.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Error("dispatcher stopped with error", "err", err)
			}
		}()
	}

	s.ready.Store(true)

	go func() {
		s.logger.Info("http server listening", "addr", ln.Addr().String())
		serveErr <- s.httpServer.Serve(ln)
	}()

	var servingError error
	select {
	case <-runCtx.Done():
	case servingError = <-serveErr:
	}
	s.ready.Store(false)
	shutdownCtx, stop := context.WithTimeout(context.Background(), s.serverCfg.ShutdownTimeout)
	defer stop()
	closeErr := s.Shutdown(shutdownCtx)
	if errors.Is(servingError, http.ErrServerClosed) {
		servingError = nil
	}
	return errors.Join(servingError, closeErr)
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.runMu.Lock()
	s.shuttingDown = true
	if s.shutdownDone == nil {
		s.shutdownDone = make(chan struct{})
	}
	done := s.shutdownDone
	s.runMu.Unlock()
	s.shutdownOnce.Do(func() {
		budget := 10 * time.Second
		if s.serverCfg != nil && s.serverCfg.ShutdownTimeout > 0 {
			budget = s.serverCfg.ShutdownTimeout
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(ctx, budget)
		go func() {
			defer cancelCleanup()
			s.ready.Store(false)
			s.runMu.Lock()
			if s.runCancel != nil {
				s.runCancel()
			}
			s.runMu.Unlock()
			if s.limiterPool != nil {
				s.limiterPool.Shutdown()
			}
			if s.httpClients != nil {
				s.httpClients.Shutdown()
			}
			results := make(chan error, 3)
			go func() {
				if s.dispatcher != nil {
					results <- s.dispatcher.Shutdown(cleanupCtx)
				} else {
					results <- nil
				}
			}()
			go func() {
				if s.httpServer != nil {
					err := s.httpServer.Shutdown(cleanupCtx)
					if err != nil {
						_ = s.httpServer.Close()
					}
					results <- err
				} else {
					results <- nil
				}
			}()
			go func() {
				if s.httpClients != nil {
					results <- s.httpClients.Wait(cleanupCtx)
				} else {
					results <- nil
				}
			}()
			for range 3 {
				select {
				case err := <-results:
					s.shutdownErr = errors.Join(s.shutdownErr, err)
				case <-cleanupCtx.Done():
					s.shutdownErr = errors.Join(s.shutdownErr, cleanupCtx.Err())
					close(done)
					return
				}
			}
			close(done)
		}()
	})
	select {
	case <-done:
		return s.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
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
	_, err := service.NewInitializationService(s.entClient).Validate(pingCtx, s.mode)
	return err
}
