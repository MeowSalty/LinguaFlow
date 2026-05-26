package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

const readinessPingTimeout = 2 * time.Second

type Server struct {
	config     *config.Config
	logger     *slog.Logger
	db         *sql.DB
	entClient  *ent.Client
	httpServer *http.Server
	ready      atomic.Bool
}

func NewServer(cfg *config.Config, logger *slog.Logger, db *sql.DB, client *ent.Client) *Server {
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		config:    cfg,
		logger:    logger,
		db:        db,
		entClient: client,
	}
	s.httpServer = &http.Server{
		Addr:              cfg.Server.Address(),
		Handler:           s.newRouter(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s
}

func (s *Server) Run(ctx context.Context) error {
	serveErr := make(chan error, 1)
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
