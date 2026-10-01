package api

import (
	"net/http"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/go-chi/chi/v5"
)

type credentialWriteRequest struct {
	Provider string `json:"provider"`
	Endpoint string `json:"endpoint"`
	Secret   string `json:"secret"`
}

func (s *Server) CreateUserCredential(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleCreateCredential)).ServeHTTP(w, r)
}
func (s *Server) ListUserCredentials(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleListCredentials)).ServeHTTP(w, r)
}
func (s *Server) CreateOrgCredential(w http.ResponseWriter, r *http.Request, _ OrgId) {
	s.requireAuth(http.HandlerFunc(s.handleCreateCredential)).ServeHTTP(w, r)
}
func (s *Server) ListOrgCredentials(w http.ResponseWriter, r *http.Request, _ OrgId) {
	s.requireAuth(http.HandlerFunc(s.handleListCredentials)).ServeHTTP(w, r)
}
func (s *Server) ListCredentialVersions(w http.ResponseWriter, r *http.Request, _ int) {
	s.requireAuth(http.HandlerFunc(s.handleCredentialVersions)).ServeHTTP(w, r)
}
func (s *Server) RotateCredential(w http.ResponseWriter, r *http.Request, _ int) {
	s.requireAuth(http.HandlerFunc(s.handleRotateCredential)).ServeHTTP(w, r)
}
func (s *Server) RevokeCredentialVersion(w http.ResponseWriter, r *http.Request, _ int, _ int) {
	s.requireAuth(http.HandlerFunc(s.handleRevokeCredential)).ServeHTTP(w, r)
}
func (s *Server) CollectCredentialVersions(w http.ResponseWriter, r *http.Request, _ int) {
	s.requireAuth(http.HandlerFunc(s.handleCollectCredentials)).ServeHTTP(w, r)
}

func (s *Server) credentialActor(w http.ResponseWriter, r *http.Request) (int, bool) {
	auth, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return 0, false
	}
	if s.backendSvc == nil || s.backendSvc.Credentials() == nil {
		s.writeProblem(w, r, http.StatusServiceUnavailable, "unavailable", "凭据存储尚未就绪")
		return 0, false
	}
	return auth.User.ID, true
}
func (s *Server) credentialScope(w http.ResponseWriter, r *http.Request, actorID int) (string, int, bool) {
	if raw := chi.URLParam(r, "orgId"); raw != "" {
		id, ok := s.parseIntParam(w, r, raw, "orgId")
		return service.ScopeOrg, id, ok
	}
	return service.ScopeUser, actorID, true
}
func (s *Server) handleCreateCredential(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.credentialActor(w, r)
	if !ok {
		return
	}
	scope, owner, ok := s.credentialScope(w, r, actor)
	if !ok {
		return
	}
	var req credentialWriteRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	row, err := s.backendSvc.Credentials().Create(r.Context(), actor, service.CreateCredentialInput{Scope: scope, OwnerID: owner, Provider: req.Provider, Endpoint: req.Endpoint, Secret: req.Secret})
	if err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}
func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.credentialActor(w, r)
	if !ok {
		return
	}
	scope, owner, ok := s.credentialScope(w, r, actor)
	if !ok {
		return
	}
	rows, err := s.backendSvc.Credentials().List(r.Context(), actor, scope, owner)
	if err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}
func (s *Server) credentialID(w http.ResponseWriter, r *http.Request) (int, bool) {
	return s.parseIntParam(w, r, chi.URLParam(r, "credentialId"), "credentialId")
}
func (s *Server) handleCredentialVersions(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.credentialActor(w, r)
	if !ok {
		return
	}
	id, ok := s.credentialID(w, r)
	if !ok {
		return
	}
	versions, err := s.backendSvc.Credentials().Versions(r.Context(), actor, id)
	if err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": versions})
}
func (s *Server) handleRotateCredential(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.credentialActor(w, r)
	if !ok {
		return
	}
	id, ok := s.credentialID(w, r)
	if !ok {
		return
	}
	var req struct {
		Secret string `json:"secret"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	b, err := s.backendSvc.Credentials().Rotate(r.Context(), actor, id, req.Secret)
	if err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}
func (s *Server) handleRevokeCredential(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.credentialActor(w, r)
	if !ok {
		return
	}
	id, ok := s.credentialID(w, r)
	if !ok {
		return
	}
	version, ok := s.parseIntParam(w, r, chi.URLParam(r, "version"), "version")
	if !ok {
		return
	}
	if err := s.backendSvc.Credentials().Revoke(r.Context(), actor, credential.Binding{ID: id, Version: version}); err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleCollectCredentials(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.credentialActor(w, r)
	if !ok {
		return
	}
	id, ok := s.credentialID(w, r)
	if !ok {
		return
	}
	n, err := s.backendSvc.Credentials().Collect(r.Context(), actor, id)
	if err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted_versions": n})
}
