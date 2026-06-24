package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type createProjectRequest struct {
	Name                     string         `json:"name"`
	OwnerOrgID               *int           `json:"owner_org_id"`
	Config                   map[string]any `json:"config"`
	DefaultTranslationConfig map[string]any `json:"default_translation_config"`
	SourceLang               string         `json:"source_lang"`
	TargetLang               string         `json:"target_lang"`
}

type updateProjectRequest struct {
	Name                     string         `json:"name"`
	Config                   map[string]any `json:"config"`
	DefaultTranslationConfig map[string]any `json:"default_translation_config"`
	SourceLang               string         `json:"source_lang"`
	TargetLang               string         `json:"target_lang"`
}

type projectResponse struct {
	ID                       int            `json:"id"`
	Name                     string         `json:"name"`
	OwnerUserID              *int           `json:"owner_user_id,omitempty"`
	OwnerOrgID               *int           `json:"owner_org_id,omitempty"`
	Config                   map[string]any `json:"config,omitempty"`
	DefaultTranslationConfig map[string]any `json:"default_translation_config,omitempty"`
	SourceLang               string         `json:"source_lang"`
	TargetLang               string         `json:"target_lang"`
}

func toProjectResponse(p *ent.Project) projectResponse {
	return projectResponse{
		ID:                       p.ID,
		Name:                     p.Name,
		OwnerUserID:              p.OwnerUserID,
		OwnerOrgID:               p.OwnerOrgID,
		Config:                   p.Config,
		DefaultTranslationConfig: p.DefaultTranslationConfig,
		SourceLang:               p.SourceLang,
		TargetLang:               p.TargetLang,
	}
}

func toProjectListResponse(projects []*ent.Project) map[string]any {
	items := make([]projectResponse, 0, len(projects))
	for _, p := range projects {
		items = append(items, toProjectResponse(p))
	}
	return map[string]any{"items": items}
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	var req createProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.projectSvc.CreateProject(r.Context(), authUser.User.ID, service.CreateProjectInput{
		Name:                     req.Name,
		OwnerOrgID:               req.OwnerOrgID,
		Config:                   req.Config,
		DefaultTranslationConfig: req.DefaultTranslationConfig,
		SourceLang:               req.SourceLang,
		TargetLang:               req.TargetLang,
	})
	if err != nil {
		writeProjectServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toProjectResponse(p))
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projects, err := s.projectSvc.ListProjectsForUser(r.Context(), authUser.User.ID)
	if err != nil {
		writeProjectServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProjectListResponse(projects))
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	p, err := s.projectSvc.GetProject(r.Context(), authUser.User.ID, projectID)
	if err != nil {
		writeProjectServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProjectResponse(p))
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	var req updateProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.projectSvc.UpdateProject(r.Context(), authUser.User.ID, projectID, service.UpdateProjectInput{
		Name:                     req.Name,
		Config:                   req.Config,
		DefaultTranslationConfig: req.DefaultTranslationConfig,
		SourceLang:               req.SourceLang,
		TargetLang:               req.TargetLang,
	})
	if err != nil {
		writeProjectServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProjectResponse(p))
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	storagePaths, err := s.projectSvc.DeleteProject(r.Context(), authUser.User.ID, projectID)
	if err != nil {
		writeProjectServiceError(w, err)
		return
	}
	// 事务成功后清理物理文件
	for _, p := range storagePaths {
		if fileErr := s.jobStore.Delete(p); fileErr != nil {
			// 文件删除失败仅记录警告，不影响响应
			slog.Warn("failed to delete resource file after project deletion",
				"path", p, "error", fileErr)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
