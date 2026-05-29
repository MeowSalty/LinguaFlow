package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type createProjectRequest struct {
	Name                     string         `json:"name"`
	OwnerOrgID               *int           `json:"owner_org_id"`
	ResourceScope            string         `json:"resource_scope"`
	Config                   map[string]any `json:"config"`
	DefaultTranslationConfig map[string]any `json:"default_translation_config"`
	SourceLang               string         `json:"source_lang"`
	TargetLang               string         `json:"target_lang"`
}

type updateProjectRequest struct {
	Name                     string         `json:"name"`
	ResourceScope            string         `json:"resource_scope"`
	Config                   map[string]any `json:"config"`
	DefaultTranslationConfig map[string]any `json:"default_translation_config"`
	SourceLang               string         `json:"source_lang"`
	TargetLang               string         `json:"target_lang"`
}

type projectResponse struct {
	ID                       int            `json:"id"`
	Name                     string         `json:"name"`
	ResourceScope            string         `json:"resource_scope"`
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
		ResourceScope:            p.ResourceScope,
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

func toBackendBindingResponse(bindings []service.ProjectBackendBinding) []map[string]any {
	out := make([]map[string]any, 0, len(bindings))
	for _, b := range bindings {
		item := map[string]any{
			"order_index": b.OrderIndex,
			"source":      b.Source,
			"backend_id":  b.BackendID,
			"name":        b.Name,
			"type":        b.Type,
			"priority":    b.Priority,
		}
		if b.Options != nil {
			item["options"] = b.Options
		}
		out = append(out, item)
	}
	return out
}

func toStageOverrideResponse(v *service.StageBackendOverrideView) map[string]any {
	resp := map[string]any{
		"stage":        v.Stage,
		"backend_mode": v.BackendMode,
	}
	if len(v.BackendOrder) > 0 {
		resp["backend_order"] = v.BackendOrder
	}
	return resp
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
		ResourceScope:            req.ResourceScope,
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
		ResourceScope:            req.ResourceScope,
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
	if err := s.projectSvc.DeleteProject(r.Context(), authUser.User.ID, projectID); err != nil {
		writeProjectServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetProjectBackends(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	settings, err := s.projectSvc.GetBackendSettings(r.Context(), authUser.User.ID, projectID)
	if err != nil {
		writeProjectServiceError(w, err)
		return
	}
	resp := map[string]any{
		"backends": toBackendBindingResponse(settings.Backends),
	}
	if len(settings.StageOverrides) > 0 {
		overrides := make(map[string]any, len(settings.StageOverrides))
		for k, v := range settings.StageOverrides {
			overrides[k] = toStageOverrideResponse(&v)
		}
		resp["stage_overrides"] = overrides
	}
	writeJSON(w, http.StatusOK, resp)
}

type setBackendOrderRequest struct {
	Bindings []struct {
		Source    string `json:"source"`
		BackendID int    `json:"backend_id"`
	} `json:"bindings"`
}

func (s *Server) handleSetProjectBackendOrder(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	var req setBackendOrderRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	bindings := make([]service.ProjectBackendBindingInput, 0, len(req.Bindings))
	for _, b := range req.Bindings {
		bindings = append(bindings, service.ProjectBackendBindingInput{
			Source:    b.Source,
			BackendID: b.BackendID,
		})
	}
	result, err := s.projectSvc.SetBackendOrder(r.Context(), authUser.User.ID, projectID, bindings)
	if err != nil {
		writeProjectServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackendBindingResponse(result))
}

type setStageOverrideRequest struct {
	Stage        string   `json:"stage"`
	BackendMode  string   `json:"backend_mode"`
	BackendOrder []string `json:"backend_order"`
}

func (s *Server) handleSetStageBackendOverride(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	var req setStageOverrideRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	view, err := s.projectSvc.SetStageBackendOverride(r.Context(), authUser.User.ID, projectID, service.StageBackendOverrideInput{
		Stage:        req.Stage,
		BackendMode:  req.BackendMode,
		BackendOrder: req.BackendOrder,
	})
	if err != nil {
		writeProjectServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toStageOverrideResponse(view))
}

func (s *Server) handleGetStagePlan(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	stage := chi.URLParam(r, "stage")
	bindings, err := s.projectSvc.ResolveStagePlan(r.Context(), authUser.User.ID, projectID, stage)
	if err != nil {
		writeProjectServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackendBindingResponse(bindings))
}
