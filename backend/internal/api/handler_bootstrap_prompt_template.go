package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
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
		resp.CreatedAt = &t.CreatedAt
	}
	if !t.UpdatedAt.IsZero() {
		resp.UpdatedAt = &t.UpdatedAt
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

	templates, err := s.bootstrapPromptTemplateSvc.ListByUser(r.Context(), authUser.User.ID)
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
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		s.writeProblem(w, r, http.StatusBadRequest, "validation_error", "术语抽取提示词模板名称不能为空")
		return
	}

	input := service.CreateBootstrapPromptTemplateInput{
		Name:        req.Name,
		Scope:       "user",
		OwnerUserID: &authUser.User.ID,
	}
	if req.Description != nil {
		input.Description = *req.Description
	}
	if req.Content != nil {
		input.Content = *req.Content
	}

	pt, err := s.bootstrapPromptTemplateSvc.Create(r.Context(), input)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, entBootstrapPromptTemplateToResponse(pt))
}

// handleGetBootstrapPromptTemplate 获取术语抽取提示词模板详情。
func (s *Server) handleGetBootstrapPromptTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parseBootstrapPromptTemplateID(w, r)
	if !ok {
		return
	}

	pt, err := s.bootstrapPromptTemplateSvc.GetByID(r.Context(), id)
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
	id, ok := s.parseBootstrapPromptTemplateID(w, r)
	if !ok {
		return
	}

	var req UpdateBootstrapPromptTemplateRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}

	input := service.UpdateBootstrapPromptTemplateInput{
		Name:        req.Name,
		Description: req.Description,
		Content:     req.Content,
	}

	pt, err := s.bootstrapPromptTemplateSvc.Update(r.Context(), id, input)
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
	id, ok := s.parseBootstrapPromptTemplateID(w, r)
	if !ok {
		return
	}

	err := s.bootstrapPromptTemplateSvc.Delete(r.Context(), id)
	if err != nil {
		if err == service.ErrBootstrapPromptTemplateNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "术语抽取提示词模板不存在")
			return
		}
		if errors.Is(err, service.ErrBootstrapPromptTemplateInUse) {
			s.writeProblem(w, r, http.StatusConflict, "conflict", err.Error())
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
