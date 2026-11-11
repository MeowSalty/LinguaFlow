package api

import (
	"net/http"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// Frame allocation and CPU finish before network I/O. The frame's payload
// lease remains live through the final Write, including a disconnected client.
func (s *Server) writeExecutionJSON(w http.ResponseWriter, r *http.Request, status int, payload any) {
	ctx := service.WithExecutionInputBudget(r.Context(), s.executionBudget, service.ExecutionScope(r.Context()), service.DefaultExecutionInputBytes)
	raw, lease, err := service.MarshalExecutionOutput(ctx, payload)
	if err != nil {
		s.writeProblem(w, r, http.StatusServiceUnavailable, "execution_busy", err.Error())
		return
	}
	defer func() { clear(raw); raw = nil; lease.Close() }()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
