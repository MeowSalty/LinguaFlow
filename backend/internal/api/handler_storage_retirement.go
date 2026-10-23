package api

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) retirementRequest(w http.ResponseWriter, r *http.Request, fn func(int)) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		if s.storageRetirements == nil {
			s.writeStorageError(w, r, storage.ErrUnsupported)
			return
		}
		fn(actor)
	})
}
func (s *Server) CreateStorageRetirementPreview(w http.ResponseWriter, r *http.Request) {
	s.retirementRequest(w, r, func(actor int) {
		var in service.RetirementPreviewInput
		if !s.decodeRetirementJSON(w, r, &in, "StorageRetirementPreviewRequest") {
			return
		}
		out, err := s.storageRetirements.Preview(r.Context(), actor, in)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}
func (s *Server) CreateStorageRetirement(w http.ResponseWriter, r *http.Request, p CreateStorageRetirementParams) {
	s.retirementRequest(w, r, func(actor int) {
		var in service.RetirementCreateInput
		if !s.decodeRetirementJSON(w, r, &in, "StorageRetirementCreateRequest") {
			return
		}
		row, err := s.storageRetirements.Create(r.Context(), actor, p.IdempotencyKey, in)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		out, err := s.storageRetirements.Record(r.Context(), actor, row.ID)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		w.Header().Set("Location", "/api/v1/storage/retirements/"+strconv.Itoa(row.ID))
		writeJSON(w, http.StatusAccepted, out)
	})
}
func retirementQuery(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
func (s *Server) ListStorageRetirements(w http.ResponseWriter, r *http.Request, p ListStorageRetirementsParams) {
	s.retirementRequest(w, r, func(actor int) {
		if !s.validateJobQueryParameters(w, r, "scope", "owner_id", "cursor", "limit") {
			return
		}
		out, err := s.storageRetirements.List(r.Context(), actor, string(p.Scope), p.OwnerId, retirementQuery(p.Cursor), retirementQuery(p.Limit))
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}
func (s *Server) GetStorageRetirement(w http.ResponseWriter, r *http.Request, id int, p GetStorageRetirementParams) {
	s.retirementRequest(w, r, func(actor int) {
		if !s.validateJobQueryParameters(w, r, "cursor", "limit") {
			return
		}
		out, err := s.storageRetirements.Detail(r.Context(), actor, id, retirementQuery(p.Cursor), retirementQuery(p.Limit))
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}
func (s *Server) UpdateStorageRetirementPlan(w http.ResponseWriter, r *http.Request, id int) {
	s.retirementRequest(w, r, func(actor int) {
		var in struct {
			PreviewRevision    string                          `json:"preview_revision"`
			ConfirmedScope     service.RetirementTarget        `json:"confirmed_scope"`
			Plan               []service.RetirementProjectPlan `json:"plan"`
			ExpectedGeneration int64                           `json:"expected_generation"`
		}
		if !s.decodeRetirementJSON(w, r, &in, "StorageRetirementPlanRequest") {
			return
		}
		_, err := s.storageRetirements.UpdatePlan(r.Context(), actor, id, service.RetirementPlanInput{RetirementCreateInput: service.RetirementCreateInput{PreviewRevision: in.PreviewRevision, ConfirmedScope: in.ConfirmedScope, Plan: in.Plan}, ExpectedGeneration: in.ExpectedGeneration})
		s.retirementResponse(w, r, actor, id, err)
	})
}
func (s *Server) ResumeStorageRetirement(w http.ResponseWriter, r *http.Request, id int) {
	s.retirementAction(w, r, id, false)
}
func (s *Server) CancelStorageRetirement(w http.ResponseWriter, r *http.Request, id int) {
	s.retirementAction(w, r, id, true)
}
func (s *Server) retirementAction(w http.ResponseWriter, r *http.Request, id int, cancel bool) {
	s.retirementRequest(w, r, func(actor int) {
		var in struct {
			ExpectedGeneration int64 `json:"expected_generation"`
		}
		if !s.decodeRetirementJSON(w, r, &in, "StorageRetirementActionRequest") {
			return
		}
		var err error
		if cancel {
			_, err = s.storageRetirements.Cancel(r.Context(), actor, id, in.ExpectedGeneration)
		} else {
			_, err = s.storageRetirements.Resume(r.Context(), actor, id, in.ExpectedGeneration)
		}
		s.retirementResponse(w, r, actor, id, err)
	})
}
func (s *Server) RecoverStorageRetirementAuthorization(w http.ResponseWriter, r *http.Request, id int, p RecoverStorageRetirementAuthorizationParams) {
	s.retirementRequest(w, r, func(actor int) {
		var in struct {
			ExpectedGeneration           int64 `json:"expected_generation"`
			ExpectedManagementGeneration int64 `json:"expected_management_generation"`
			Credentials                  struct {
				AccessKeyID     string     `json:"access_key_id"`
				SecretAccessKey string     `json:"secret_access_key"`
				SessionToken    string     `json:"session_token"`
				ExpiresAt       *time.Time `json:"expires_at"`
			} `json:"credentials"`
		}
		if !s.decodeRetirementJSON(w, r, &in, "StorageRetirementRecoverAuthorizationRequest") {
			return
		}
		_, err := s.storageRetirements.RecoverAuthorization(r.Context(), actor, id, p.IdempotencyKey, in.ExpectedGeneration, in.ExpectedManagementGeneration, storageauth.S3Payload{Version: storageauth.PayloadVersion, AccessKeyID: in.Credentials.AccessKeyID, SecretAccessKey: in.Credentials.SecretAccessKey, SessionToken: in.Credentials.SessionToken}, in.Credentials.ExpiresAt)
		s.retirementResponse(w, r, actor, id, err)
	})
}
func (s *Server) retirementResponse(w http.ResponseWriter, r *http.Request, actor, id int, err error) {
	if err != nil {
		s.writeStorageError(w, r, err)
		return
	}
	out, err := s.storageRetirements.Record(r.Context(), actor, id)
	if err != nil {
		s.writeStorageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
