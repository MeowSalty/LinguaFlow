package api

import (
	"net/http"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func (s *Server) GetStorageCapabilities(w http.ResponseWriter, r *http.Request, params GetStorageCapabilitiesParams) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		if !s.validateJobQueryParameters(w, r, "scope", "organization_id") {
			return
		}
		if params.Scope != "user" && params.Scope != "org" {
			s.writeStorageError(w, r, service.ErrInvalidInput)
			return
		}
		out, err := s.storageConnections.Capabilities(r.Context(), actor, string(params.Scope), params.OrganizationId)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}
