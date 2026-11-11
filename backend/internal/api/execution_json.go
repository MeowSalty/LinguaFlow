package api

import (
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"io"
	"net/http"
	"reflect"
)

// Execution JSON has a fixed byte/depth/node bound before any request DTO or
// glossary array is allocated. Input preparation is shared across the Server.
func (s *Server) decodeExecutionJSON(w http.ResponseWriter, r *http.Request, actor int, dst any) (*http.Request, func(), bool) {
	const limit int64 = 1 << 20
	ctx := service.WithExecutionActor(r.Context(), actor)
	r = r.WithContext(ctx)
	scope := service.ExecutionScope(ctx)
	id := "http-input:" + scope.CallID
	credit, err := service.ExecutionInputAllocationBound(limit, 0)
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return r, func() {}, false
	}
	usage := resourcebudget.Vector{PayloadBytes: credit, PrepareOwners: 1, QueuedDescriptors: 1}
	close := func() {}
	if s.executionBudget != nil {
		if err := s.prepareExecutionActor(ctx, actor); err != nil {
			s.writeProblem(w, r, http.StatusServiceUnavailable, "execution_busy", err.Error())
			return r, close, false
		}
		if err := s.executionBudget.Claim(scope, id, usage); err != nil {
			service.ReleaseExecutionActor(ctx)
			s.writeProblem(w, r, http.StatusServiceUnavailable, "execution_busy", err.Error())
			return r, close, false
		}
		close = func() {
			value := reflect.ValueOf(dst)
			if value.Kind() == reflect.Pointer && !value.IsNil() {
				value.Elem().SetZero()
			}
			_ = s.executionBudget.Transfer(scope, id, resourcebudget.Vector{})
			service.ReleaseExecutionActor(ctx)
			_ = s.executionBudget.ForgetActor(actor)
		}
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		clear(raw)
		close()
		s.writeProblem(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "执行输入超过字节上限")
		return r, func() {}, false
	}
	usage.BuildWorkers = 1
	if s.executionBudget != nil {
		if err = s.executionBudget.Transfer(scope, id, usage); err != nil {
			clear(raw)
			close()
			s.writeProblem(w, r, http.StatusServiceUnavailable, "execution_busy", err.Error())
			return r, func() {}, false
		}
	}
	bounds, e := backend.NewBuildBounds(0, limit)
	if e == nil {
		e = bounds.DecodeJSONLimited(raw, dst, limit)
	}
	clear(raw)
	raw = nil
	if e != nil {
		close()
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("执行输入无效: %v", e))
		return r, func() {}, false
	}
	usage.PrepareOwners, usage.BuildWorkers, usage.QueuedDescriptors = 0, 0, 0
	if s.executionBudget != nil {
		if err = s.executionBudget.Transfer(scope, id, usage); err != nil {
			close()
			s.writeProblem(w, r, http.StatusServiceUnavailable, "execution_busy", err.Error())
			return r, func() {}, false
		}
	}
	r = r.WithContext(service.WithExecutionBodyOwnership(ctx))
	return r, close, true
}
