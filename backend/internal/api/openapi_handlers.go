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

func (s *Server) CreateProjectJob(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleCreateProjectJob)).ServeHTTP(w, r)
}

func (s *Server) GetJob(w http.ResponseWriter, r *http.Request, _ JobId) {
	s.requireAuth(http.HandlerFunc(s.handleGetJob)).ServeHTTP(w, r)
}

func (s *Server) ListJobSubJobs(w http.ResponseWriter, r *http.Request, _ JobId) {
	s.requireAuth(http.HandlerFunc(s.handleListJobSubJobs)).ServeHTTP(w, r)
}

func (s *Server) GetJobSubJob(w http.ResponseWriter, r *http.Request, _ JobId, _ SubJobId) {
	s.requireAuth(http.HandlerFunc(s.handleGetJobSubJob)).ServeHTTP(w, r)
}

func (s *Server) CancelJob(w http.ResponseWriter, r *http.Request, _ JobId) {
	s.requireAuth(http.HandlerFunc(s.handleCancelJob)).ServeHTTP(w, r)
}

func (s *Server) RetryJob(w http.ResponseWriter, r *http.Request, _ JobId) {
	s.requireAuth(http.HandlerFunc(s.handleRetryJob)).ServeHTTP(w, r)
}

func (s *Server) DownloadJobResult(w http.ResponseWriter, r *http.Request, _ JobId) {
	s.requireAuth(http.HandlerFunc(s.handleDownloadJobResult)).ServeHTTP(w, r)
}

func (s *Server) ListActivity(w http.ResponseWriter, r *http.Request, _ ListActivityParams) {
	s.requireAuth(http.HandlerFunc(s.handleListActivity)).ServeHTTP(w, r)
}

func (s *Server) ApproveJob(w http.ResponseWriter, r *http.Request, _ JobId) {
	s.requireAuth(http.HandlerFunc(s.handleApproveJob)).ServeHTTP(w, r)
}

func (s *Server) RetranslateRejectedSegments(w http.ResponseWriter, r *http.Request, _ JobId) {
	s.requireAuth(http.HandlerFunc(s.handleRetranslateRejected)).ServeHTTP(w, r)
}

func (s *Server) ListJobSegments(w http.ResponseWriter, r *http.Request, _ JobId, _ ListJobSegmentsParams) {
	s.requireAuth(http.HandlerFunc(s.handleListJobSegments)).ServeHTTP(w, r)
}

func (s *Server) EditSegment(w http.ResponseWriter, r *http.Request, _ SegmentId) {
	s.requireAuth(http.HandlerFunc(s.handleEditSegment)).ServeHTTP(w, r)
}

func (s *Server) ApproveSegment(w http.ResponseWriter, r *http.Request, _ SegmentId) {
	s.requireAuth(http.HandlerFunc(s.handleApproveSegment)).ServeHTTP(w, r)
}

func (s *Server) RejectSegment(w http.ResponseWriter, r *http.Request, _ SegmentId) {
	s.requireAuth(http.HandlerFunc(s.handleRejectSegment)).ServeHTTP(w, r)
}

func (s *Server) GetStatsSummary(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleStatsSummary)).ServeHTTP(w, r)
}
