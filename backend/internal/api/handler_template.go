package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// ---- 请求/响应辅助结构体 ----

type createTemplateRequest struct {
	Name                string                  `json:"name"`
	Description         *string                 `json:"description,omitempty"`
	SystemPromptContent *string                 `json:"system_prompt_content,omitempty"`
	Pipeline            *TemplatePipelineConfig `json:"pipeline,omitempty"`
	Glossary            *TemplateGlossaryConfig `json:"glossary,omitempty"`
}

type updateTemplateRequest struct {
	Name                *string                 `json:"name,omitempty"`
	Description         *string                 `json:"description,omitempty"`
	SystemPromptContent *string                 `json:"system_prompt_content,omitempty"`
	Pipeline            *TemplatePipelineConfig `json:"pipeline,omitempty"`
	Glossary            *TemplateGlossaryConfig `json:"glossary,omitempty"`
}

type copyTemplateRequest struct {
	Name *string `json:"name,omitempty"`
}

// ---- 辅助函数 ----

// parseTemplateID 从路径参数解析 templateId（可正可负）。
func parseTemplateID(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := chi.URLParam(r, "templateId")
	id, err := strconv.Atoi(raw)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_template_id", "模板 ID 必须为整数")
		return 0, false
	}
	return id, true
}

// builtinTemplateToResponse 将内置模板转换为 API 响应。
func builtinTemplateToResponse(b *service.BuiltinTemplate) Template {
	return Template{
		Id:                  b.ID,
		Name:                b.Name,
		Description:         b.Description,
		Scope:               TemplateScopeSystem,
		SystemPromptContent: &b.Prompt.SystemPromptContent,
		Pipeline:            pipelineConfigToResponse(&b.Pipeline),
		Glossary:            glossaryConfigToResponse(&b.Glossary),
	}
}

// entTemplateToResponse 将数据库模板转换为 API 响应。
func entTemplateToResponse(t *ent.TranslationTemplate) Template {
	resp := Template{
		Id:          t.ID,
		Name:        t.Name,
		Description: t.Description,
		Scope:       TemplateScope(t.Scope),
		Pipeline:    pipelineConfigToResponse(&t.PipelineConfig),
		Glossary:    glossaryConfigToResponse(&t.GlossaryConfig),
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

// pipelineConfigToResponse 将 schema 管线配置转换为 API 响应。
func pipelineConfigToResponse(p *schema.TemplatePipelineConfigData) TemplatePipelineConfig {
	rules := make([]string, len(p.Protect.Rules))
	copy(rules, p.Protect.Rules)

	return TemplatePipelineConfig{
		Split: TemplateSplitConfig{
			Enabled:  p.Split.Enabled,
			Strategy: p.Split.Strategy,
			MaxChars: p.Split.MaxChars,
		},
		Protect: TemplateProtectConfig{
			Enabled: p.Protect.Enabled,
			Rules:   &rules,
		},
		Retry: TemplateRetryConfig{
			MaxAttempts: p.Retry.MaxAttempts,
			BackoffMs:   p.Retry.BackoffMs,
			Jitter:      p.Retry.Jitter,
		},
		Repair: TemplateRepairConfig{
			Enabled:              p.Repair.Enabled,
			JsonStructural:       p.Repair.JSONStructural,
			SchemaAliases:        p.Repair.SchemaAliases,
			Partial:              p.Repair.Partial,
			PartialThreshold:     p.Repair.PartialThreshold,
			PlaceholderNormalize: p.Repair.PlaceholderNormalize,
			PromptUpgrade:        p.Repair.PromptUpgrade,
		},
		Postprocess: TemplatePostprocessConfig{
			Enabled:    p.Postprocess.Enabled,
			TrimSpaces: p.Postprocess.TrimSpaces,
		},
	}
}

// glossaryConfigToResponse 将 schema 术语表配置转换为 API 响应。
func glossaryConfigToResponse(g *schema.TemplateGlossaryConfigData) TemplateGlossaryConfig {
	return TemplateGlossaryConfig{
		Enabled: g.Enabled,
		Bootstrap: TemplateBootstrapConfig{
			Mode:                   TemplateBootstrapConfigMode(g.Bootstrap.Mode),
			Save:                   g.Bootstrap.Save,
			MaxTermsPerBatch:       g.Bootstrap.MaxTermsPerBatch,
			MinSourceLen:           g.Bootstrap.MinSourceLen,
			InlineConflictStrategy: TemplateBootstrapConfigInlineConflictStrategy(g.Bootstrap.InlineConflictStrategy),
		},
	}
}

// parsePipelineConfig 从 API 请求解析管线配置。
func parsePipelineConfig(p *TemplatePipelineConfig) *schema.TemplatePipelineConfigData {
	if p == nil {
		return nil
	}
	var rules []string
	if p.Protect.Rules != nil {
		rules = make([]string, len(*p.Protect.Rules))
		copy(rules, *p.Protect.Rules)
	}

	return &schema.TemplatePipelineConfigData{
		Split: schema.TemplateSplitConfig{
			Enabled:  p.Split.Enabled,
			Strategy: p.Split.Strategy,
			MaxChars: p.Split.MaxChars,
		},
		Protect: schema.TemplateProtectConfig{
			Enabled: p.Protect.Enabled,
			Rules:   rules,
		},
		Retry: schema.TemplateRetryConfig{
			MaxAttempts: p.Retry.MaxAttempts,
			BackoffMs:   p.Retry.BackoffMs,
			Jitter:      p.Retry.Jitter,
		},
		Repair: schema.TemplateRepairConfig{
			Enabled:              p.Repair.Enabled,
			JSONStructural:       p.Repair.JsonStructural,
			SchemaAliases:        p.Repair.SchemaAliases,
			Partial:              p.Repair.Partial,
			PartialThreshold:     p.Repair.PartialThreshold,
			PlaceholderNormalize: p.Repair.PlaceholderNormalize,
			PromptUpgrade:        p.Repair.PromptUpgrade,
		},
		Postprocess: schema.TemplatePostprocessConfig{
			Enabled:    p.Postprocess.Enabled,
			TrimSpaces: p.Postprocess.TrimSpaces,
		},
	}
}

// parseGlossaryConfig 从 API 请求解析术语表配置。
func parseGlossaryConfig(g *TemplateGlossaryConfig) *schema.TemplateGlossaryConfigData {
	if g == nil {
		return nil
	}
	return &schema.TemplateGlossaryConfigData{
		Enabled: g.Enabled,
		Bootstrap: schema.TemplateBootstrapConfig{
			Mode:                   string(g.Bootstrap.Mode),
			Save:                   g.Bootstrap.Save,
			MaxTermsPerBatch:       g.Bootstrap.MaxTermsPerBatch,
			MinSourceLen:           g.Bootstrap.MinSourceLen,
			InlineConflictStrategy: string(g.Bootstrap.InlineConflictStrategy),
		},
	}
}

// ---- Handler 方法 ----

// handleListTemplates 列出所有可用模板（内置 + 用户）。
func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	// 内置模板
	builtins := service.BuiltinTemplates
	items := make([]Template, 0, len(builtins)+8)
	for i := range builtins {
		items = append(items, builtinTemplateToResponse(&builtins[i]))
	}

	// 用户模板
	userTemplates, err := s.templateSvc.ListByUser(r.Context(), authUser.User.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal_error", "查询用户模板失败")
		return
	}
	for _, t := range userTemplates {
		items = append(items, entTemplateToResponse(t))
	}

	writeJSON(w, http.StatusOK, TemplateListResponse{Items: items})
}

// handleCreateTemplate 创建用户模板。
func (s *Server) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	var req createTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeProblem(w, http.StatusBadRequest, "validation_error", "模板名称不能为空")
		return
	}

	input := service.CreateTemplateInput{
		Name:           req.Name,
		Scope:          "user",
		OwnerUserID:    &authUser.User.ID,
		PipelineConfig: parsePipelineConfig(req.Pipeline),
		GlossaryConfig: parseGlossaryConfig(req.Glossary),
	}
	if req.Description != nil {
		input.Description = *req.Description
	}
	if req.SystemPromptContent != nil {
		input.SystemPromptContent = *req.SystemPromptContent
	}

	tmpl, err := s.templateSvc.Create(r.Context(), input)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal_error", "创建模板失败")
		return
	}
	writeJSON(w, http.StatusCreated, entTemplateToResponse(tmpl))
}

// handleGetTemplate 获取模板详情（内置或用户）。
func (s *Server) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	templateID, ok := parseTemplateID(w, r)
	if !ok {
		return
	}

	if templateID < 0 {
		builtin := service.FindBuiltinTemplate(templateID)
		if builtin == nil {
			writeProblem(w, http.StatusNotFound, "not_found", "内置模板不存在")
			return
		}
		writeJSON(w, http.StatusOK, builtinTemplateToResponse(builtin))
		return
	}

	tmpl, err := s.templateSvc.GetByID(r.Context(), templateID)
	if err != nil {
		if err == service.ErrTemplateNotFound {
			writeProblem(w, http.StatusNotFound, "not_found", "模板不存在")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "internal_error", "查询模板失败")
		return
	}
	writeJSON(w, http.StatusOK, entTemplateToResponse(tmpl))
}

// handleUpdateTemplate 更新用户模板。
func (s *Server) handleUpdateTemplate(w http.ResponseWriter, r *http.Request) {
	templateID, ok := parseTemplateID(w, r)
	if !ok {
		return
	}

	// 内置模板不可修改
	if templateID < 0 {
		writeProblem(w, http.StatusForbidden, "forbidden", "内置模板不可修改")
		return
	}

	var req updateTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	input := service.UpdateTemplateInput{
		Name:                req.Name,
		Description:         req.Description,
		SystemPromptContent: req.SystemPromptContent,
		PipelineConfig:      parsePipelineConfig(req.Pipeline),
		GlossaryConfig:      parseGlossaryConfig(req.Glossary),
	}

	tmpl, err := s.templateSvc.Update(r.Context(), templateID, input)
	if err != nil {
		if err == service.ErrTemplateNotFound {
			writeProblem(w, http.StatusNotFound, "not_found", "模板不存在")
			return
		}
		if err == service.ErrTemplateBuiltinReadonly {
			writeProblem(w, http.StatusForbidden, "forbidden", "内置模板不可修改")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "internal_error", "更新模板失败")
		return
	}
	writeJSON(w, http.StatusOK, entTemplateToResponse(tmpl))
}

// handleDeleteTemplate 删除用户模板。
func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	templateID, ok := parseTemplateID(w, r)
	if !ok {
		return
	}

	// 内置模板不可删除
	if templateID < 0 {
		writeProblem(w, http.StatusForbidden, "forbidden", "内置模板不可删除")
		return
	}

	err := s.templateSvc.Delete(r.Context(), templateID)
	if err != nil {
		if err == service.ErrTemplateNotFound {
			writeProblem(w, http.StatusNotFound, "not_found", "模板不存在")
			return
		}
		if err == service.ErrTemplateBuiltinReadonly {
			writeProblem(w, http.StatusForbidden, "forbidden", "内置模板不可删除")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "internal_error", "删除模板失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCopyTemplate 复制模板为自定义模板。
func (s *Server) handleCopyTemplate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	templateID, ok := parseTemplateID(w, r)
	if !ok {
		return
	}

	var req copyTemplateRequest
	// 请求体可选
	_ = decodeJSON(w, r, &req)

	newName := ""
	if req.Name != nil {
		newName = *req.Name
	}

	tmpl, err := s.templateSvc.CopyToUser(r.Context(), templateID, authUser.User.ID, newName)
	if err != nil {
		if err == service.ErrTemplateNotFound {
			writeProblem(w, http.StatusNotFound, "not_found", "源模板不存在")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "internal_error", "复制模板失败")
		return
	}
	writeJSON(w, http.StatusCreated, entTemplateToResponse(tmpl))
}

// ---- 组织模板 Handler ----

// handleListOrgTemplates 列出组织模板。
func (s *Server) handleListOrgTemplates(w http.ResponseWriter, r *http.Request) {
	orgIDStr := chi.URLParam(r, "orgId")
	orgID, err := strconv.Atoi(orgIDStr)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_org_id", "组织 ID 必须为整数")
		return
	}

	orgTemplates, err := s.templateSvc.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal_error", "查询组织模板失败")
		return
	}

	items := make([]Template, 0, len(orgTemplates))
	for _, t := range orgTemplates {
		items = append(items, entTemplateToResponse(t))
	}

	writeJSON(w, http.StatusOK, TemplateListResponse{Items: items})
}

// handleCreateOrgTemplate 创建组织模板。
func (s *Server) handleCreateOrgTemplate(w http.ResponseWriter, r *http.Request) {
	orgIDStr := chi.URLParam(r, "orgId")
	orgID, err := strconv.Atoi(orgIDStr)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_org_id", "组织 ID 必须为整数")
		return
	}

	var req createTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeProblem(w, http.StatusBadRequest, "validation_error", "模板名称不能为空")
		return
	}

	input := service.CreateTemplateInput{
		Name:           req.Name,
		Scope:          "org",
		OwnerOrgID:     &orgID,
		PipelineConfig: parsePipelineConfig(req.Pipeline),
		GlossaryConfig: parseGlossaryConfig(req.Glossary),
	}
	if req.Description != nil {
		input.Description = *req.Description
	}
	if req.SystemPromptContent != nil {
		input.SystemPromptContent = *req.SystemPromptContent
	}

	tmpl, err := s.templateSvc.Create(r.Context(), input)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal_error", "创建组织模板失败")
		return
	}
	writeJSON(w, http.StatusCreated, entTemplateToResponse(tmpl))
}

// handleUpdateOrgTemplate 更新组织模板。
func (s *Server) handleUpdateOrgTemplate(w http.ResponseWriter, r *http.Request) {
	templateID, ok := parseTemplateID(w, r)
	if !ok {
		return
	}

	var req updateTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	input := service.UpdateTemplateInput{
		Name:                req.Name,
		Description:         req.Description,
		SystemPromptContent: req.SystemPromptContent,
		PipelineConfig:      parsePipelineConfig(req.Pipeline),
		GlossaryConfig:      parseGlossaryConfig(req.Glossary),
	}

	tmpl, err := s.templateSvc.Update(r.Context(), templateID, input)
	if err != nil {
		if err == service.ErrTemplateNotFound {
			writeProblem(w, http.StatusNotFound, "not_found", "模板不存在")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "internal_error", "更新组织模板失败")
		return
	}
	writeJSON(w, http.StatusOK, entTemplateToResponse(tmpl))
}

// handleDeleteOrgTemplate 删除组织模板。
func (s *Server) handleDeleteOrgTemplate(w http.ResponseWriter, r *http.Request) {
	templateID, ok := parseTemplateID(w, r)
	if !ok {
		return
	}

	err := s.templateSvc.Delete(r.Context(), templateID)
	if err != nil {
		if err == service.ErrTemplateNotFound {
			writeProblem(w, http.StatusNotFound, "not_found", "模板不存在")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "internal_error", "删除组织模板失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
