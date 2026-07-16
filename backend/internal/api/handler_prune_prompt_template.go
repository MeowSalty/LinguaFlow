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

// parsePrunePromptTemplateID 从路径参数解析 prunePromptTemplateId。
func (s *Server) parsePrunePromptTemplateID(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := chi.URLParam(r, "prunePromptTemplateId")
	id, err := strconv.Atoi(raw)
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_id", "术语精简提示词模板 ID 必须为整数")
		return 0, false
	}
	return id, true
}

// entPrunePromptTemplateToResponse 将术语精简提示词模板转换为 API 响应。
func entPrunePromptTemplateToResponse(t *ent.PrunePromptTemplate) PrunePromptTemplate {
	resp := PrunePromptTemplate{
		Id:          t.ID,
		Name:        t.Name,
		Description: t.Description,
		Scope:       PrunePromptTemplateScope(t.Scope),
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

// handleListPrunePromptTemplates 列出当前用户的术语精简提示词模板。
func (s *Server) handleListPrunePromptTemplates(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	templates, err := s.prunePromptTemplateSvc.ListByUser(r.Context(), authUser.User.ID)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}

	items := make([]PrunePromptTemplate, 0, len(templates))
	for _, t := range templates {
		items = append(items, entPrunePromptTemplateToResponse(t))
	}

	writeJSON(w, http.StatusOK, PrunePromptTemplateListResponse{Items: items})
}

// handleCreatePrunePromptTemplate 创建术语精简提示词模板。
func (s *Server) handleCreatePrunePromptTemplate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	var req CreatePrunePromptTemplateRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		s.writeProblem(w, r, http.StatusBadRequest, "validation_error", "术语精简提示词模板名称不能为空")
		return
	}

	input := service.CreatePrunePromptTemplateInput{
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

	pt, err := s.prunePromptTemplateSvc.Create(r.Context(), input)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, entPrunePromptTemplateToResponse(pt))
}

// handleGetPrunePromptTemplate 获取术语精简提示词模板详情。
func (s *Server) handleGetPrunePromptTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parsePrunePromptTemplateID(w, r)
	if !ok {
		return
	}

	pt, err := s.prunePromptTemplateSvc.GetByID(r.Context(), id)
	if err != nil {
		if err == service.ErrPrunePromptTemplateNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "术语精简提示词模板不存在")
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entPrunePromptTemplateToResponse(pt))
}

// handleUpdatePrunePromptTemplate 更新术语精简提示词模板。
func (s *Server) handleUpdatePrunePromptTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parsePrunePromptTemplateID(w, r)
	if !ok {
		return
	}

	var req UpdatePrunePromptTemplateRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}

	input := service.UpdatePrunePromptTemplateInput{
		Name:        req.Name,
		Description: req.Description,
		Content:     req.Content,
	}

	pt, err := s.prunePromptTemplateSvc.Update(r.Context(), id, input)
	if err != nil {
		if err == service.ErrPrunePromptTemplateNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "术语精简提示词模板不存在")
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entPrunePromptTemplateToResponse(pt))
}

// handleDeletePrunePromptTemplate 删除术语精简提示词模板。
func (s *Server) handleDeletePrunePromptTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parsePrunePromptTemplateID(w, r)
	if !ok {
		return
	}

	err := s.prunePromptTemplateSvc.Delete(r.Context(), id)
	if err != nil {
		if err == service.ErrPrunePromptTemplateNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "术语精简提示词模板不存在")
			return
		}
		if errors.Is(err, service.ErrPrunePromptTemplateInUse) {
			s.writeProblem(w, r, http.StatusConflict, "conflict", err.Error())
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
