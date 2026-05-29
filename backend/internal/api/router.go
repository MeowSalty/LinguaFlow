package api

import (
	"encoding/json"
	"net/http"

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

	// Resource 管理 API（手动注册，待后续纳入 OpenAPI spec）
	// 每个 handler 内部已调用 authUserFromContext 进行认证检查
	apiV1.Route("/projects/{projectId}/resources", func(r chi.Router) {
		r.Get("/", s.authHandleFunc(s.handleListProjectResources))
		r.Post("/", s.authHandleFunc(s.handleUploadProjectResources))
		r.Get("/{resourceId}", s.authHandleFunc(s.handleGetResource))
		r.Put("/{resourceId}", s.authHandleFunc(s.handleUpdateResource))
		r.Delete("/{resourceId}", s.authHandleFunc(s.handleDeleteResource))
		r.Get("/{resourceId}/download", s.authHandleFunc(s.handleDownloadResourceFile))
		r.Get("/{resourceId}/segments", s.authHandleFunc(s.handleListResourceSegments))
		r.Patch("/{resourceId}/segments/{segmentId}", s.authHandleFunc(s.handleUpdateResourceSegment))
	})

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
