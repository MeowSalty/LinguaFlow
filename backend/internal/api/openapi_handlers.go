package api

import "net/http"

func (s *Server) Ping(w http.ResponseWriter, r *http.Request) {
	s.handlePing(w, r)
}

func (s *Server) GetMode(w http.ResponseWriter, r *http.Request) {
	s.handleMode(w, r)
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

func (s *Server) ListOrgProjects(w http.ResponseWriter, r *http.Request, _ OrgId) {
	s.requireAuth(http.HandlerFunc(s.handleListOrgProjects)).ServeHTTP(w, r)
}

func (s *Server) CreateOrgProject(w http.ResponseWriter, r *http.Request, _ OrgId) {
	s.requireAuth(http.HandlerFunc(s.handleCreateOrgProject)).ServeHTTP(w, r)
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

func (s *Server) AnalyzeGlossarySyncImpact(w http.ResponseWriter, r *http.Request, projectId ProjectId, entryId EntryId) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleAnalyzeGlossarySyncImpact(w, r, projectId, entryId)
	})).ServeHTTP(w, r)
}

func (s *Server) ExecuteGlossarySyncUpdate(w http.ResponseWriter, r *http.Request, projectId ProjectId, entryId EntryId) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleExecuteGlossarySyncUpdate(w, r, projectId, entryId)
	})).ServeHTTP(w, r)
}

func (s *Server) GetGlossarySyncTaskStatus(w http.ResponseWriter, r *http.Request, projectId ProjectId, taskId string) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleGetGlossarySyncTaskStatus(w, r, projectId, taskId)
	})).ServeHTTP(w, r)
}

func (s *Server) CancelGlossarySyncTask(w http.ResponseWriter, r *http.Request, projectId ProjectId, taskId string) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleCancelGlossarySyncTask(w, r, projectId, taskId)
	})).ServeHTTP(w, r)
}

func (s *Server) ListProjectResources(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ListProjectResourcesParams) {
	s.requireAuth(http.HandlerFunc(s.handleListProjectResources)).ServeHTTP(w, r)
}

func (s *Server) UploadProjectResources(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleUploadProjectResources)).ServeHTTP(w, r)
}

func (s *Server) PrecheckProjectResources(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handlePrecheckProjectResources)).ServeHTTP(w, r)
}

func (s *Server) GetProjectResourceTree(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleGetProjectResourceTree)).ServeHTTP(w, r)
}

func (s *Server) GetResource(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId) {
	s.requireAuth(http.HandlerFunc(s.handleGetResource)).ServeHTTP(w, r)
}

func (s *Server) UpdateResource(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdateResource)).ServeHTTP(w, r)
}

func (s *Server) IncrementalUpdateResource(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId) {
	s.requireAuth(http.HandlerFunc(s.handleIncrementalUpdateResource)).ServeHTTP(w, r)
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

func (s *Server) ListJobs(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ListJobsParams) {
	s.requireAuth(http.HandlerFunc(s.handleListJobs)).ServeHTTP(w, r)
}

func (s *Server) CreateJob(w http.ResponseWriter, r *http.Request, _ ProjectId) {
	s.requireAuth(http.HandlerFunc(s.handleCreateJob)).ServeHTTP(w, r)
}

func (s *Server) GetJob(w http.ResponseWriter, r *http.Request, _ JobId) {
	s.requireAuth(http.HandlerFunc(s.handleGetJob)).ServeHTTP(w, r)
}

func (s *Server) CancelJob(w http.ResponseWriter, r *http.Request, _ JobId) {
	s.requireAuth(http.HandlerFunc(s.handleCancelJob)).ServeHTTP(w, r)
}

func (s *Server) RetryJob(w http.ResponseWriter, r *http.Request, _ JobId) {
	s.requireAuth(http.HandlerFunc(s.handleRetryJob)).ServeHTTP(w, r)
}

func (s *Server) DownloadTranslatedResourceFile(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId) {
	s.requireAuth(http.HandlerFunc(s.handleDownloadTranslatedResourceFile)).ServeHTTP(w, r)
}

func (s *Server) ListActivity(w http.ResponseWriter, r *http.Request, _ ListActivityParams) {
	s.requireAuth(http.HandlerFunc(s.handleListActivity)).ServeHTTP(w, r)
}

func (s *Server) GetStatsSummary(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleStatsSummary)).ServeHTTP(w, r)
}

func (s *Server) ReviewResourceSegment(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId, _ SegmentId) {
	s.requireAuth(http.HandlerFunc(s.handleReviewSegment)).ServeHTTP(w, r)
}

func (s *Server) BatchReviewResourceSegments(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId) {
	s.requireAuth(http.HandlerFunc(s.handleBatchReviewSegments)).ServeHTTP(w, r)
}

func (s *Server) ApproveAllResourceSegments(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId) {
	s.requireAuth(http.HandlerFunc(s.handleApproveAllResourceSegments)).ServeHTTP(w, r)
}

func (s *Server) RetranslateRejectedSegments(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId) {
	s.requireAuth(http.HandlerFunc(s.handleRetranslateRejected)).ServeHTTP(w, r)
}

func (s *Server) ListResourceSegmentGroups(w http.ResponseWriter, r *http.Request, _ ProjectId, _ ResourceId) {
	s.requireAuth(http.HandlerFunc(s.handleListResourceSegmentGroups)).ServeHTTP(w, r)
}

// ---- 提示词模板适配器 ----

func (s *Server) ListPromptTemplates(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleListPromptTemplates)).ServeHTTP(w, r)
}

func (s *Server) CreatePromptTemplate(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleCreatePromptTemplate)).ServeHTTP(w, r)
}

func (s *Server) GetPromptTemplate(w http.ResponseWriter, r *http.Request, _ TranslationPromptTemplateId) {
	s.requireAuth(http.HandlerFunc(s.handleGetPromptTemplate)).ServeHTTP(w, r)
}

func (s *Server) UpdatePromptTemplate(w http.ResponseWriter, r *http.Request, _ TranslationPromptTemplateId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdatePromptTemplate)).ServeHTTP(w, r)
}

func (s *Server) DeletePromptTemplate(w http.ResponseWriter, r *http.Request, _ TranslationPromptTemplateId) {
	s.requireAuth(http.HandlerFunc(s.handleDeletePromptTemplate)).ServeHTTP(w, r)
}

// ---- 术语抽取提示词模板适配器 ----

func (s *Server) ListBootstrapPromptTemplates(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleListBootstrapPromptTemplates)).ServeHTTP(w, r)
}

func (s *Server) CreateBootstrapPromptTemplate(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleCreateBootstrapPromptTemplate)).ServeHTTP(w, r)
}

func (s *Server) GetBootstrapPromptTemplate(w http.ResponseWriter, r *http.Request, _ BootstrapPromptTemplateId) {
	s.requireAuth(http.HandlerFunc(s.handleGetBootstrapPromptTemplate)).ServeHTTP(w, r)
}

func (s *Server) UpdateBootstrapPromptTemplate(w http.ResponseWriter, r *http.Request, _ BootstrapPromptTemplateId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdateBootstrapPromptTemplate)).ServeHTTP(w, r)
}

func (s *Server) DeleteBootstrapPromptTemplate(w http.ResponseWriter, r *http.Request, _ BootstrapPromptTemplateId) {
	s.requireAuth(http.HandlerFunc(s.handleDeleteBootstrapPromptTemplate)).ServeHTTP(w, r)
}

// ---- 术语精简提示词模板适配器 ----

func (s *Server) ListPrunePromptTemplates(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleListPrunePromptTemplates)).ServeHTTP(w, r)
}

func (s *Server) CreatePrunePromptTemplate(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleCreatePrunePromptTemplate)).ServeHTTP(w, r)
}

func (s *Server) GetPrunePromptTemplate(w http.ResponseWriter, r *http.Request, _ PrunePromptTemplateId) {
	s.requireAuth(http.HandlerFunc(s.handleGetPrunePromptTemplate)).ServeHTTP(w, r)
}

func (s *Server) UpdatePrunePromptTemplate(w http.ResponseWriter, r *http.Request, _ PrunePromptTemplateId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdatePrunePromptTemplate)).ServeHTTP(w, r)
}

func (s *Server) DeletePrunePromptTemplate(w http.ResponseWriter, r *http.Request, _ PrunePromptTemplateId) {
	s.requireAuth(http.HandlerFunc(s.handleDeletePrunePromptTemplate)).ServeHTTP(w, r)
}

// ---- 术语精简适配器 ----

func (s *Server) PreviewGlossaryPrune(w http.ResponseWriter, r *http.Request, projectId ProjectId) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handlePreviewGlossaryPrune(w, r, projectId)
	})).ServeHTTP(w, r)
}

func (s *Server) ApplyGlossaryPrune(w http.ResponseWriter, r *http.Request, projectId ProjectId) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleApplyGlossaryPrune(w, r, projectId)
	})).ServeHTTP(w, r)
}

// ---- 执行策略配置适配器 ----

func (s *Server) ListExecutionProfiles(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleListExecutionProfiles)).ServeHTTP(w, r)
}

// ---- 执行计划模板适配器 ----

func (s *Server) ListExecutionPlanTemplates(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authUser, ok := authUserFromContext(r.Context())
		if !ok {
			s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
			return
		}
		s.executionPlanHandler.handleList(w, r, authUser.User.ID)
	})).ServeHTTP(w, r)
}

func (s *Server) CreateExecutionPlanTemplate(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authUser, ok := authUserFromContext(r.Context())
		if !ok {
			s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
			return
		}
		s.executionPlanHandler.handleCreate(w, r, authUser.User.ID)
	})).ServeHTTP(w, r)
}

func (s *Server) GetExecutionPlanTemplate(w http.ResponseWriter, r *http.Request, executionPlanTemplateId ExecutionPlanTemplateId) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authUser, ok := authUserFromContext(r.Context())
		if !ok {
			s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
			return
		}
		s.executionPlanHandler.handleGet(w, r, authUser.User.ID, executionPlanTemplateId)
	})).ServeHTTP(w, r)
}

func (s *Server) UpdateExecutionPlanTemplate(w http.ResponseWriter, r *http.Request, executionPlanTemplateId ExecutionPlanTemplateId) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authUser, ok := authUserFromContext(r.Context())
		if !ok {
			s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
			return
		}
		s.executionPlanHandler.handleUpdate(w, r, authUser.User.ID, executionPlanTemplateId)
	})).ServeHTTP(w, r)
}

func (s *Server) DeleteExecutionPlanTemplate(w http.ResponseWriter, r *http.Request, executionPlanTemplateId ExecutionPlanTemplateId) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authUser, ok := authUserFromContext(r.Context())
		if !ok {
			s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
			return
		}
		s.executionPlanHandler.handleDelete(w, r, authUser.User.ID, executionPlanTemplateId)
	})).ServeHTTP(w, r)
}

func (s *Server) CreateExecutionProfile(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleCreateExecutionProfile)).ServeHTTP(w, r)
}

func (s *Server) GetExecutionProfile(w http.ResponseWriter, r *http.Request, _ ExecutionProfileId) {
	s.requireAuth(http.HandlerFunc(s.handleGetExecutionProfile)).ServeHTTP(w, r)
}

func (s *Server) UpdateExecutionProfile(w http.ResponseWriter, r *http.Request, _ ExecutionProfileId) {
	s.requireAuth(http.HandlerFunc(s.handleUpdateExecutionProfile)).ServeHTTP(w, r)
}

func (s *Server) DeleteExecutionProfile(w http.ResponseWriter, r *http.Request, _ ExecutionProfileId) {
	s.requireAuth(http.HandlerFunc(s.handleDeleteExecutionProfile)).ServeHTTP(w, r)
}

// ---- 管理员适配器 ----

func (s *Server) AdminListUsers(w http.ResponseWriter, r *http.Request, _ AdminListUsersParams) {
	s.requireAdmin(http.HandlerFunc(s.handleAdminListUsers)).ServeHTTP(w, r)
}

func (s *Server) AdminGetUser(w http.ResponseWriter, r *http.Request, _ UserId) {
	s.requireAdmin(http.HandlerFunc(s.handleAdminGetUser)).ServeHTTP(w, r)
}

func (s *Server) AdminCreateUser(w http.ResponseWriter, r *http.Request) {
	s.requireAdmin(http.HandlerFunc(s.handleAdminCreateUser)).ServeHTTP(w, r)
}

func (s *Server) AdminUpdateUser(w http.ResponseWriter, r *http.Request, _ UserId) {
	s.requireAdmin(http.HandlerFunc(s.handleAdminUpdateUser)).ServeHTTP(w, r)
}

func (s *Server) AdminDisableUser(w http.ResponseWriter, r *http.Request, _ UserId) {
	s.requireAdmin(http.HandlerFunc(s.handleAdminDisableUser)).ServeHTTP(w, r)
}

func (s *Server) AdminResetUserPassword(w http.ResponseWriter, r *http.Request, _ UserId) {
	s.requireAdmin(http.HandlerFunc(s.handleAdminResetPassword)).ServeHTTP(w, r)
}

func (s *Server) AdminGetStats(w http.ResponseWriter, r *http.Request) {
	s.requireAdmin(http.HandlerFunc(s.handleAdminGetStats)).ServeHTTP(w, r)
}

func (s *Server) AdminListAuditLogs(w http.ResponseWriter, r *http.Request, _ AdminListAuditLogsParams) {
	s.requireAdmin(http.HandlerFunc(s.handleAdminListAuditLogs)).ServeHTTP(w, r)
}

func (s *Server) AdminGetSettings(w http.ResponseWriter, r *http.Request) {
	s.requireAdmin(http.HandlerFunc(s.handleAdminGetSettings)).ServeHTTP(w, r)
}

func (s *Server) AdminUpdateSettings(w http.ResponseWriter, r *http.Request) {
	s.requireAdmin(http.HandlerFunc(s.handleAdminUpdateSettings)).ServeHTTP(w, r)
}
