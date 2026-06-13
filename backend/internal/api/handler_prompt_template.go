package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// ---- 请求/响应辅助结构体 ----

type createPromptTemplateRequest struct {
	Name                string  `json:"name"`
	Description         *string `json:"description,omitempty"`
	SystemPromptContent *string `json:"system_prompt_content,omitempty"`
}

type updatePromptTemplateRequest struct {
	Name                *string `json:"name,omitempty"`
	Description         *string `json:"description,omitempty"`
	SystemPromptContent *string `json:"system_prompt_content,omitempty"`
}

// ---- 辅助函数 ----

// parsePromptTemplateID 从路径参数解析 promptTemplateId。
func parsePromptTemplateID(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := chi.URLParam(r, "promptTemplateId")
	id, err := strconv.Atoi(raw)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_id", "提示词模板 ID 必须为整数")
		return 0, false
	}
	return id, true
}

// entPromptTemplateToResponse 将数据库提示词模板转换为 API 响应。
func entPromptTemplateToResponse(t *ent.PromptTemplate) PromptTemplate {
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
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	templates, err := s.promptTemplateSvc.ListByUser(r.Context(), authUser.User.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal_error", "查询提示词模板失败")
		return
	}

	items := make([]PromptTemplate, 0, len(templates))
	for _, t := range templates {
		items = append(items, entPromptTemplateToResponse(t))
	}

	writeJSON(w, http.StatusOK, PromptTemplateListResponse{Items: items})
}

// handleCreatePromptTemplate 创建提示词模板。
func (s *Server) handleCreatePromptTemplate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	var req CreatePromptTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeProblem(w, http.StatusBadRequest, "validation_error", "提示词模板名称不能为空")
		return
	}

	input := service.CreatePromptTemplateInput{
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

	pt, err := s.promptTemplateSvc.Create(r.Context(), input)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal_error", "创建提示词模板失败")
		return
	}
	writeJSON(w, http.StatusCreated, entPromptTemplateToResponse(pt))
}

// handleGetPromptTemplate 获取提示词模板详情。
func (s *Server) handleGetPromptTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePromptTemplateID(w, r)
	if !ok {
		return
	}

	pt, err := s.promptTemplateSvc.GetByID(r.Context(), id)
	if err != nil {
		if err == service.ErrPromptTemplateNotFound {
			writeProblem(w, http.StatusNotFound, "not_found", "提示词模板不存在")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "internal_error", "查询提示词模板失败")
		return
	}
	writeJSON(w, http.StatusOK, entPromptTemplateToResponse(pt))
}

// handleUpdatePromptTemplate 更新提示词模板。
func (s *Server) handleUpdatePromptTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePromptTemplateID(w, r)
	if !ok {
		return
	}

	var req UpdatePromptTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	input := service.UpdatePromptTemplateInput{
		Name:                req.Name,
		Description:         req.Description,
		SystemPromptContent: req.SystemPromptContent,
	}

	pt, err := s.promptTemplateSvc.Update(r.Context(), id, input)
	if err != nil {
		if err == service.ErrPromptTemplateNotFound {
			writeProblem(w, http.StatusNotFound, "not_found", "提示词模板不存在")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "internal_error", "更新提示词模板失败")
		return
	}
	writeJSON(w, http.StatusOK, entPromptTemplateToResponse(pt))
}

// handleDeletePromptTemplate 删除提示词模板。
func (s *Server) handleDeletePromptTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePromptTemplateID(w, r)
	if !ok {
		return
	}

	err := s.promptTemplateSvc.Delete(r.Context(), id)
	if err != nil {
		if err == service.ErrPromptTemplateNotFound {
			writeProblem(w, http.StatusNotFound, "not_found", "提示词模板不存在")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "internal_error", "删除提示词模板失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
