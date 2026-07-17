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

// parsePromptTemplateID 从路径参数解析 promptTemplateId。
func (s *Server) parsePromptTemplateID(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := chi.URLParam(r, "promptTemplateId")
	id, err := strconv.Atoi(raw)
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_id", "提示词模板 ID 必须为整数")
		return 0, false
	}
	return id, true
}

// entTranslationPromptTemplateToResponse 将翻译提示词模板转换为 API 响应。
func entTranslationPromptTemplateToResponse(t *ent.TranslationPromptTemplate) PromptTemplate {
	resp := PromptTemplate{
		Id:          t.ID,
		Name:        t.Name,
		Description: t.Description,
		Scope:       PromptTemplateScope(t.Scope),
	}
	if t.SystemPromptContent != "" {
		resp.SystemPromptContent = &t.SystemPromptContent
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

// handleListPromptTemplates 列出当前用户的提示词模板。
func (s *Server) handleListPromptTemplates(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	templates, err := s.translationPromptTemplateSvc.ListByUser(r.Context(), authUser.User.ID)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}

	items := make([]PromptTemplate, 0, len(templates))
	for _, t := range templates {
		items = append(items, entTranslationPromptTemplateToResponse(t))
	}

	writeJSON(w, http.StatusOK, PromptTemplateListResponse{Items: items})
}

// handleCreatePromptTemplate 创建提示词模板。
func (s *Server) handleCreatePromptTemplate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	var req CreatePromptTemplateRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		s.writeProblem(w, r, http.StatusBadRequest, "validation_error", "提示词模板名称不能为空")
		return
	}

	input := service.CreateTranslationPromptTemplateInput{
		Name:        req.Name,
		Scope:       "user",
		OwnerUserID: &authUser.User.ID,
	}
	if req.Description != nil {
		input.Description = *req.Description
	}
	if req.SystemPromptContent != nil {
		input.SystemPromptContent = *req.SystemPromptContent
	}

	pt, err := s.translationPromptTemplateSvc.Create(r.Context(), input)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, entTranslationPromptTemplateToResponse(pt))
}

// handleGetPromptTemplate 获取提示词模板详情。
func (s *Server) handleGetPromptTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parsePromptTemplateID(w, r)
	if !ok {
		return
	}

	pt, err := s.translationPromptTemplateSvc.GetByID(r.Context(), id)
	if err != nil {
		if err == service.ErrTranslationPromptTemplateNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "提示词模板不存在")
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entTranslationPromptTemplateToResponse(pt))
}

// handleUpdatePromptTemplate 更新提示词模板。
func (s *Server) handleUpdatePromptTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parsePromptTemplateID(w, r)
	if !ok {
		return
	}

	var req UpdatePromptTemplateRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}

	input := service.UpdateTranslationPromptTemplateInput{
		Name:                req.Name,
		Description:         req.Description,
		SystemPromptContent: req.SystemPromptContent,
	}

	pt, err := s.translationPromptTemplateSvc.Update(r.Context(), id, input)
	if err != nil {
		if err == service.ErrTranslationPromptTemplateNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "提示词模板不存在")
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entTranslationPromptTemplateToResponse(pt))
}

// handleDeletePromptTemplate 删除提示词模板。
func (s *Server) handleDeletePromptTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parsePromptTemplateID(w, r)
	if !ok {
		return
	}

	err := s.translationPromptTemplateSvc.Delete(r.Context(), id)
	if err != nil {
		if err == service.ErrTranslationPromptTemplateNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "提示词模板不存在")
			return
		}
		if errors.Is(err, service.ErrTranslationPromptTemplateInUse) {
			s.writeProblem(w, r, http.StatusConflict, "conflict", err.Error())
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
