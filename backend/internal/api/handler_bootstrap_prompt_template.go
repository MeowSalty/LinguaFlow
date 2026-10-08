package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// ---- 辅助函数 ----

// parseBootstrapPromptTemplateID 从路径参数解析 bootstrapPromptTemplateId。
func (s *Server) parseBootstrapPromptTemplateID(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := chi.URLParam(r, "bootstrapPromptTemplateId")
	id, err := strconv.Atoi(raw)
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_id", "术语抽取提示词模板 ID 必须为整数")
		return 0, false
	}
	return id, true
}

// entBootstrapPromptTemplateToResponse 将术语抽取提示词模板转换为 API 响应。
func entBootstrapPromptTemplateToResponse(t *ent.BootstrapPromptTemplate) BootstrapPromptTemplate {
	resp := BootstrapPromptTemplate{
		Id:          t.ID,
		Name:        t.Name,
		Description: t.Description,
		Scope:       BootstrapPromptTemplateScope(t.Scope),
	}
	if t.Content != "" {
		resp.Content = &t.Content
	}
	if t.OwnerUserID != nil {
		resp.OwnerUserId = t.OwnerUserID
	}
	if t.OwnerOrgID != nil {
		resp.OwnerOrgId = t.OwnerOrgID
	}
	if !t.CreatedAt.IsZero() {
		resp.CreatedAt = timeutil.NormalizePtr(&t.CreatedAt)
	}
	if !t.UpdatedAt.IsZero() {
		resp.UpdatedAt = timeutil.NormalizePtr(&t.UpdatedAt)
	}
	return resp
}

// ---- Handler 方法 ----

// handleListBootstrapPromptTemplates 列出当前用户的术语抽取提示词模板。
func (s *Server) handleListBootstrapPromptTemplates(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	orgID, ok := s.parseSharedOrgQuery(w, r)
	if !ok {
		return
	}
	var templates []*ent.BootstrapPromptTemplate
	var err error
	if orgID == nil {
		templates, err = s.bootstrapPromptTemplateSvc.ListByUser(r.Context(), authUser.User.ID)
	} else {
		templates, err = s.bootstrapPromptTemplateSvc.ListByOrg(r.Context(), authUser.User.ID, *orgID)
	}
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}

	items := make([]BootstrapPromptTemplate, 0, len(templates))
	for _, t := range templates {
		items = append(items, entBootstrapPromptTemplateToResponse(t))
	}

	writeJSON(w, http.StatusOK, BootstrapPromptTemplateListResponse{Items: items})
}

// handleCreateBootstrapPromptTemplate 创建术语抽取提示词模板。
func (s *Server) handleCreateBootstrapPromptTemplate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	var req CreateBootstrapPromptTemplateRequest
	if !s.decodeSharedJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		s.writeProblem(w, r, http.StatusBadRequest, "validation_error", "术语抽取提示词模板名称不能为空")
		return
	}

	input := service.CreateBootstrapPromptTemplateInput{
		Name:  req.Name,
		OrgID: req.OrgId,
	}
	if req.Description != nil {
		input.Description = *req.Description
	}
	if req.Content != nil {
		input.Content = *req.Content
	}

	pt, err := s.bootstrapPromptTemplateSvc.Create(r.Context(), authUser.User.ID, input)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, entBootstrapPromptTemplateToResponse(pt))
}

// handleGetBootstrapPromptTemplate 获取术语抽取提示词模板详情。
func (s *Server) handleGetBootstrapPromptTemplate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	id, ok := s.parseBootstrapPromptTemplateID(w, r)
	if !ok {
		return
	}

	pt, err := s.bootstrapPromptTemplateSvc.GetByID(r.Context(), authUser.User.ID, id)
	if err != nil {
		if err == service.ErrBootstrapPromptTemplateNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "术语抽取提示词模板不存在")
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entBootstrapPromptTemplateToResponse(pt))
}

// handleUpdateBootstrapPromptTemplate 更新术语抽取提示词模板。
func (s *Server) handleUpdateBootstrapPromptTemplate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	id, ok := s.parseBootstrapPromptTemplateID(w, r)
	if !ok {
		return
	}

	var req UpdateBootstrapPromptTemplateRequest
	if !s.decodeSharedJSON(w, r, &req) {
		return
	}

	input := service.UpdateBootstrapPromptTemplateInput{
		Name:        req.Name,
		Description: req.Description,
		Content:     req.Content,
	}

	pt, err := s.bootstrapPromptTemplateSvc.Update(r.Context(), authUser.User.ID, id, input)
	if err != nil {
		if err == service.ErrBootstrapPromptTemplateNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "术语抽取提示词模板不存在")
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entBootstrapPromptTemplateToResponse(pt))
}

// handleDeleteBootstrapPromptTemplate 删除术语抽取提示词模板。
func (s *Server) handleDeleteBootstrapPromptTemplate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	id, ok := s.parseBootstrapPromptTemplateID(w, r)
	if !ok {
		return
	}

	err := s.bootstrapPromptTemplateSvc.Delete(r.Context(), authUser.User.ID, id)
	if err != nil {
		if err == service.ErrBootstrapPromptTemplateNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "术语抽取提示词模板不存在")
			return
		}
		if errors.Is(err, service.ErrBootstrapPromptTemplateInUse) {
			s.writeProblem(w, r, http.StatusConflict, "conflict", "该模板正被执行计划引用，无法删除")
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
