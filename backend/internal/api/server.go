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

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/worker"
)

const readinessPingTimeout = 2 * time.Second

type Server struct {
	config      *config.Config
	logger      *slog.Logger
	db          *sql.DB
	entClient   *ent.Client
	authService *service.AuthService
	userService *service.UserService
	backendSvc  *service.BackendService
	projectSvc  *service.ProjectService
	glossarySvc *service.GlossaryService
	jobService  *service.JobService
	reviewSvc   *service.ReviewService
	statsSvc    *service.StatsService
	auditSvc    *service.AuditService
	jobStore    *filestore.LocalStore
	jobQueue    *worker.Queue
	jobRunner   *worker.Runner
	httpServer  *http.Server
	ready       atomic.Bool
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
	}
	s.userService = service.NewUserService(client, s.authService)
	s.backendSvc = service.NewBackendService(client, s.userService)
	s.projectSvc = service.NewProjectService(client, s.userService, s.backendSvc)
	s.glossarySvc = service.NewGlossaryService(client, s.projectSvc)
	s.jobService = service.NewJobService(client, s.projectSvc)
	s.reviewSvc = service.NewReviewService(client, s.projectSvc)
	s.statsSvc = service.NewStatsService(client, s.projectSvc)
	s.auditSvc = service.NewAuditService(client, s.userService, s.projectSvc)
	jobStore, err := filestore.NewLocal(filepath.Join(cfg.Server.DataDir, "jobs"))
	if err != nil {
		return nil, err
	}
	s.jobStore = jobStore
	queueSize := cfg.Pipeline.Translate.Concurrency * 8
	if queueSize < 16 {
		queueSize = 16
	}
	s.jobQueue = worker.NewQueue(queueSize)
	s.jobRunner = worker.NewRunner(cfg, logger, client, s.projectSvc, s.jobService, jobStore, s.jobQueue)
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
	if s.jobRunner != nil {
		if err := s.jobRunner.Recover(ctx); err != nil {
			return err
		}
		go func() {
			if err := s.jobRunner.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Error("job runner stopped with error", "err", err)
			}
		}()
	}
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
