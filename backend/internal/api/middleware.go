package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

func (s *Server) applyMiddleware(r *chi.Mux) {
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(s.requestLoggingMiddleware)
	r.Use(s.recoverer)

	allowedOrigins := s.serverCfg.CORS.AllowedOrigins
	if s.isLocal() {
		allowedOrigins = []string{
			"http://127.0.0.1:" + fmt.Sprintf("%d", s.serverCfg.Port),
			"http://localhost:" + fmt.Sprintf("%d", s.serverCfg.Port),
			"http://127.0.0.1",
			"http://localhost",
		}
	}

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: false,
		MaxAge:           300,
	}))
}

func (s *Server) requestLoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		statusCode := ww.Status()
		if statusCode == 0 {
			statusCode = http.StatusOK
		}

		s.logger.Info("http request",
			"request_id", chimiddleware.GetReqID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"status", statusCode,
			"bytes", ww.BytesWritten(),
			"duration_ms", time.Since(startedAt).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}

				s.writeProblem(w, r, http.StatusInternalServerError, "internal_error", "服务器内部错误",
					slog.String("error", fmt.Sprintf("%v", rec)),
					slog.String("stack", string(debug.Stack())),
				)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
