package api

import "net/http"

func (s *Server) Ping(w http.ResponseWriter, r *http.Request) {
	s.handlePing(w, r)
}

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

func (s *Server) GetOrganization(w http.ResponseWriter, r *http.Request, _ OrgId) {
	s.requireAuth(http.HandlerFunc(s.handleGetOrg)).ServeHTTP(w, r)
}

func (s *Server) UpdateOrganization(w http.ResponseWriter, r *http.Request, _ OrgId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdateOrg)).ServeHTTP(w, r)
}

func (s *Server) ListOrganizationMembers(w http.ResponseWriter, r *http.Request, _ OrgId) {
	s.requireAuth(http.HandlerFunc(s.handleListOrgMembers)).ServeHTTP(w, r)
}

func (s *Server) AddOrganizationMember(w http.ResponseWriter, r *http.Request, _ OrgId) {
	s.requireAuth(http.HandlerFunc(s.handleAddOrgMember)).ServeHTTP(w, r)
}

func (s *Server) UpdateOrganizationMember(w http.ResponseWriter, r *http.Request, _ OrgId, _ UserId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdateOrgMember)).ServeHTTP(w, r)
}

func (s *Server) DeleteOrganizationMember(w http.ResponseWriter, r *http.Request, _ OrgId, _ UserId) {
	s.requireAuth(http.HandlerFunc(s.handleDeleteOrgMember)).ServeHTTP(w, r)
}

func (s *Server) ListUserBackends(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleListUserBackends)).ServeHTTP(w, r)
}

func (s *Server) CreateUserBackend(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleCreateUserBackend)).ServeHTTP(w, r)
}

func (s *Server) DeleteUserBackend(w http.ResponseWriter, r *http.Request, _ BackendId) {
	s.requireAuth(http.HandlerFunc(s.handleDeleteUserBackend)).ServeHTTP(w, r)
}

func (s *Server) UpdateUserBackend(w http.ResponseWriter, r *http.Request, _ BackendId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdateUserBackend)).ServeHTTP(w, r)
}

func (s *Server) ListOrgBackends(w http.ResponseWriter, r *http.Request, _ OrgId) {
	s.requireAuth(http.HandlerFunc(s.handleListOrgBackends)).ServeHTTP(w, r)
}

func (s *Server) CreateOrgBackend(w http.ResponseWriter, r *http.Request, _ OrgId) {
	s.requireAuth(http.HandlerFunc(s.handleCreateOrgBackend)).ServeHTTP(w, r)
}

func (s *Server) DeleteOrgBackend(w http.ResponseWriter, r *http.Request, _ OrgId, _ BackendId) {
	s.requireAuth(http.HandlerFunc(s.handleDeleteOrgBackend)).ServeHTTP(w, r)
}

func (s *Server) UpdateOrgBackend(w http.ResponseWriter, r *http.Request, _ OrgId, _ BackendId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdateOrgBackend)).ServeHTTP(w, r)
}

func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleListProjects)).ServeHTTP(w, r)
}

func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleCreateProject)).ServeHTTP(w, r)
}

func (s *Server) DeleteProject(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleDeleteProject)).ServeHTTP(w, r)
}

func (s *Server) GetProject(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleGetProject)).ServeHTTP(w, r)
}

func (s *Server) UpdateProject(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdateProject)).ServeHTTP(w, r)
}

func (s *Server) GetProjectBackends(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleGetProjectBackends)).ServeHTTP(w, r)
}

func (s *Server) SetProjectBackendOrder(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleSetProjectBackendOrder)).ServeHTTP(w, r)
}

func (s *Server) ListGlossaryEntries(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleListGlossaryEntries)).ServeHTTP(w, r)
}

func (s *Server) CreateGlossaryEntry(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleCreateGlossaryEntry)).ServeHTTP(w, r)
}

func (s *Server) ExportGlossaryCSV(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleExportGlossaryCSV)).ServeHTTP(w, r)
}

func (s *Server) ImportGlossaryCSV(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleImportGlossaryCSV)).ServeHTTP(w, r)
}

func (s *Server) DeleteGlossaryEntry(w http.ResponseWriter, r *http.Request, _ ProjectId, _ EntryId) {
	s.requireAuth(http.HandlerFunc(s.handleDeleteGlossaryEntry)).ServeHTTP(w, r)
}

func (s *Server) UpdateGlossaryEntry(w http.ResponseWriter, r *http.Request, _ ProjectId, _ EntryId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdateGlossaryEntry)).ServeHTTP(w, r)
}

func (s *Server) SetStageBackendOverride(w http.ResponseWriter, r *http.Request, _ ProjectId, _ Stage) {
	s.requireAuth(http.HandlerFunc(s.handleSetStageBackendOverride)).ServeHTTP(w, r)
}

func (s *Server) GetStagePlan(w http.ResponseWriter, r *http.Request, _ ProjectId, _ Stage) {
	s.requireAuth(http.HandlerFunc(s.handleGetStagePlan)).ServeHTTP(w, r)
}

func (s *Server) ListProjectResources(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ListProjectResourcesParams) {
	s.requireAuth(http.HandlerFunc(s.handleListProjectResources)).ServeHTTP(w, r)
}

func (s *Server) UploadProjectResources(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleUploadProjectResources)).ServeHTTP(w, r)
}

func (s *Server) GetResource(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId) {
	s.requireAuth(http.HandlerFunc(s.handleGetResource)).ServeHTTP(w, r)
}

func (s *Server) UpdateResource(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdateResource)).ServeHTTP(w, r)
}

func (s *Server) DeleteResource(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId) {
	s.requireAuth(http.HandlerFunc(s.handleDeleteResource)).ServeHTTP(w, r)
}

func (s *Server) DownloadResourceFile(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId) {
	s.requireAuth(http.HandlerFunc(s.handleDownloadResourceFile)).ServeHTTP(w, r)
}

func (s *Server) ListResourceSegments(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId, _ ListResourceSegmentsParams) {
	s.requireAuth(http.HandlerFunc(s.handleListResourceSegments)).ServeHTTP(w, r)
}

func (s *Server) UpdateResourceSegment(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId, _ SegmentId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdateResourceSegment)).ServeHTTP(w, r)
}

func (s *Server) ListTranslationJobs(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ListTranslationJobsParams) {
	s.requireAuth(http.HandlerFunc(s.handleListTranslationJobs)).ServeHTTP(w, r)
}

func (s *Server) CreateTranslationJob(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleCreateTranslationJob)).ServeHTTP(w, r)
}

func (s *Server) GetTranslationJob(w http.ResponseWriter, r *http.Request, _ TranslationJobId) {
	s.requireAuth(http.HandlerFunc(s.handleGetTranslationJob)).ServeHTTP(w, r)
}

func (s *Server) CancelTranslationJob(w http.ResponseWriter, r *http.Request, _ TranslationJobId) {
	s.requireAuth(http.HandlerFunc(s.handleCancelTranslationJob)).ServeHTTP(w, r)
}

func (s *Server) RetryTranslationJob(w http.ResponseWriter, r *http.Request, _ TranslationJobId) {
	s.requireAuth(http.HandlerFunc(s.handleRetryTranslationJob)).ServeHTTP(w, r)
}

func (s *Server) DownloadTranslationJobResult(w http.ResponseWriter, r *http.Request, _ TranslationJobId, _ DownloadTranslationJobResultParams) {
	s.requireAuth(http.HandlerFunc(s.handleDownloadTranslationJobResult)).ServeHTTP(w, r)
}

func (s *Server) ListActivity(w http.ResponseWriter, r *http.Request, _ ListActivityParams) {
	s.requireAuth(http.HandlerFunc(s.handleListActivity)).ServeHTTP(w, r)
}

func (s *Server) GetStatsSummary(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleStatsSummary)).ServeHTTP(w, r)
}
