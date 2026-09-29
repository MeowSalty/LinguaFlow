package api

import (
	"net/http"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/telemetry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/worker"
)

func (s *Server) AdminGetRuntimeSummary(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.requireAdmin(http.HandlerFunc(s.handleRuntimeSummary)).ServeHTTP(w, r)
}
func (s *Server) handleRuntimeSummary(w http.ResponseWriter, r *http.Request) {
	if !s.validateJobQueryParameters(w, r) {
		return
	}
	if s.collector == nil {
		s.writeProblem(w, r, http.StatusServiceUnavailable, "runtime_unavailable", "运行指标尚未初始化")
		return
	}
	response := struct {
		telemetry.Snapshot
		Runners  []worker.RunnerSnapshot  `json:"runners"`
		Limiters *backend.LimiterSnapshot `json:"limiters"`
	}{Snapshot: s.collector.Snapshot(), Runners: []worker.RunnerSnapshot{}}
	if s.dispatcher != nil {
		response.Runners = s.dispatcher.Snapshot()
	}
	if s.limiterPool != nil {
		metrics := s.limiterPool.Snapshot()
		if metrics.State != "uninitialized" {
			response.Limiters = &metrics
		}
	}
	writeJSON(w, http.StatusOK, response)
}
