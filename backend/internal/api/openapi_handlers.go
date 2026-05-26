package api

import "net/http"

func (s *Server) RegisterAuth(w http.ResponseWriter, r *http.Request) {
	s.handleRegister(w, r)
}

func (s *Server) LoginAuth(w http.ResponseWriter, r *http.Request) {
	s.handleLogin(w, r)
}

func (s *Server) RefreshAuth(w http.ResponseWriter, r *http.Request) {
	s.handleRefresh(w, r)
}

func (s *Server) LogoutAuth(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleLogout)).ServeHTTP(w, r)
}

func (s *Server) GetCurrentUser(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleGetMe)).ServeHTTP(w, r)
}

func (s *Server) UpdateCurrentUser(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleUpdateMe)).ServeHTTP(w, r)
}

func (s *Server) ChangeCurrentUserPassword(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleChangePassword)).ServeHTTP(w, r)
}

func (s *Server) ListOrganizations(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleListOrgs)).ServeHTTP(w, r)
}

func (s *Server) CreateOrganization(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleCreateOrg)).ServeHTTP(w, r)
}

func (s *Server) GetOrganization(w http.ResponseWriter, r *http.Request, _ ComponentsParametersOrgId) {
	s.requireAuth(http.HandlerFunc(s.handleGetOrg)).ServeHTTP(w, r)
}

func (s *Server) UpdateOrganization(w http.ResponseWriter, r *http.Request, _ ComponentsParametersOrgId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdateOrg)).ServeHTTP(w, r)
}

func (s *Server) ListOrganizationMembers(w http.ResponseWriter, r *http.Request, _ ComponentsParametersOrgId) {
	s.requireAuth(http.HandlerFunc(s.handleListOrgMembers)).ServeHTTP(w, r)
}

func (s *Server) AddOrganizationMember(w http.ResponseWriter, r *http.Request, _ ComponentsParametersOrgId) {
	s.requireAuth(http.HandlerFunc(s.handleAddOrgMember)).ServeHTTP(w, r)
}

func (s *Server) UpdateOrganizationMember(w http.ResponseWriter, r *http.Request, _ ComponentsParametersOrgId, _ ComponentsParametersUserId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdateOrgMember)).ServeHTTP(w, r)
}

func (s *Server) DeleteOrganizationMember(w http.ResponseWriter, r *http.Request, _ ComponentsParametersOrgId, _ ComponentsParametersUserId) {
	s.requireAuth(http.HandlerFunc(s.handleDeleteOrgMember)).ServeHTTP(w, r)
}
