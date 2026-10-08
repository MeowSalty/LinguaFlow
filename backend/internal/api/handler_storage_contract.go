package api

import (
	"encoding/json"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"net/http"
)

func (s *Server) GetStorageOptions(w http.ResponseWriter, r *http.Request, params GetStorageOptionsParams) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		if (params.Scope == "org" && params.OrganizationId == nil) || (params.Scope == "user" && params.OrganizationId != nil) {
			s.writeStorageError(w, r, service.ErrInvalidInput)
			return
		}
		out, err := s.storageSvc.Options(r.Context(), actor, string(params.Scope), storageValue(params.OrganizationId))
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}
func (s *Server) GetProjectStorageOptions(w http.ResponseWriter, r *http.Request, projectID int, params GetProjectStorageOptionsParams) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		if (params.Purpose == "repair" && params.SourceRevisionId == nil) || (params.Purpose != "repair" && params.SourceRevisionId != nil) {
			s.writeStorageError(w, r, service.ErrInvalidInput)
			return
		}
		out, err := s.storageSvc.ProjectOptions(r.Context(), actor, projectID, string(params.Purpose), storageValue(params.SourceRevisionId))
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}
func (s *Server) ListStorageChecks(w http.ResponseWriter, r *http.Request, connectionID int, params ListStorageChecksParams) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		limit := 50
		if params.Limit != nil {
			limit = *params.Limit
		}
		rows, err := s.storageConnections.ListChecks(r.Context(), actor, connectionID, storageValue(params.Cursor), limit)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": rows})
	})
}
func (s *Server) GetStorageCheck(w http.ResponseWriter, r *http.Request, connectionID, checkID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		result, err := s.storageConnections.GetCheck(r.Context(), actor, connectionID, checkID)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
}
func (s *Server) DownloadLegacySourceSnapshot(w http.ResponseWriter, r *http.Request, projectID, taskID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		data, err := s.resourceSvc.LegacySnapshot(r.Context(), actor, projectID, taskID)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		w.Header().Set("Content-Disposition", contentDisposition("legacy-source-snapshot.json"))
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, json.RawMessage(data))
	})
}
func (s *Server) writeConnectionCheckResult(w http.ResponseWriter, connection *service.StorageConnectionRecord, check *service.StorageCheckResult) {
	type response struct {
		*service.StorageConnectionRecord
		CheckID *int `json:"check_id"`
	}
	result := response{StorageConnectionRecord: connection}
	if check != nil {
		result.CheckID = &check.CheckID
	}
	writeJSON(w, http.StatusOK, result)
}
