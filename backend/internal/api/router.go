package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/web"
	"github.com/go-chi/chi/v5"
)

type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) newRouter() http.Handler {
	r := chi.NewRouter()
	s.applyMiddleware(r)

	r.Get("/health", s.handleHealth)
	r.Get("/health/ready", s.handleReady)
	r.Get("/metrics", s.handleMetrics)
	r.Get("/api/docs", s.handleDocs)
	r.Get("/api/openapi.json", s.handleOpenAPISpec)

	apiV1 := chi.NewRouter()
	r.Mount("/api/v1", HandlerFromMux(s, apiV1))

	// SSE 流式端点（不在 OpenAPI 规范中）
	apiV1.Get("/translation-jobs/{translationJobId}/stream", s.handleTranslationJobStream)

	// 本地模式下挂载嵌入的前端静态资源
	if s.isLocal() {
		distSub, err := fs.Sub(web.DistFS, "dist")
		if err == nil {
			fileServer := http.FileServer(http.FS(distSub))
			r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
				path := r.URL.Path
				// 尝试直接提供静态文件
				f, err := distSub.Open(strings.TrimPrefix(path, "/"))
				if err == nil {
					f.Close()
					fileServer.ServeHTTP(w, r)
					return
				}
				// SPA 回退：非文件路径返回 index.html
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/"
				fileServer.ServeHTTP(w, r2)
			})
		}
	}

	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

func (s *Server) handlePing(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Service: s.config.Server.ServiceName})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.checkReadiness(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "not_ready", Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ready"})
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}
