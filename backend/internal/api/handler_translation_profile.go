package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// ---- 辅助函数 ----

// parseTranslationProfileID 从路径参数解析 translationProfileId。
func parseTranslationProfileID(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := chi.URLParam(r, "translationProfileId")
	id, err := strconv.Atoi(raw)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_id", "翻译配置 ID 必须为整数")
		return 0, false
	}
	return id, true
}

// entTranslationProfileToResponse 将数据库翻译配置转换为 API 响应。
func entTranslationProfileToResponse(t *ent.TranslationProfile) TranslationProfile {
	resp := TranslationProfile{
		Id:          t.ID,
		Name:        t.Name,
		Description: t.Description,
		Scope:       TranslationProfileScope(t.Scope),
		Config:      profileConfigToResponse(&t.Config),
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

// profileConfigToResponse 将 schema 配置转换为 API 响应。
func profileConfigToResponse(c *schema.TranslationProfileConfigData) TranslationProfileConfig {
	rules := make([]ProfileProtectConfigRules, len(c.Protect.Rules))
	for i, r := range c.Protect.Rules {
		rules[i] = ProfileProtectConfigRules(r)
	}

	outputFormat := ProfileRubyConfigOutputFormat(c.Protect.Ruby.OutputFormat)

	return TranslationProfileConfig{
		Split: ProfileSplitConfig{
			Enabled:  c.Split.Enabled,
			Strategy: c.Split.Strategy,
			MaxChars: c.Split.MaxChars,
		},
		Protect: ProfileProtectConfig{
			Enabled: c.Protect.Enabled,
			Rules:   &rules,
			Ruby: &ProfileRubyConfig{
				Enabled:      c.Protect.Ruby.Enabled,
				OutputFormat: &outputFormat,
			},
		},
		Postprocess: ProfilePostprocessConfig{
			Enabled:    c.Postprocess.Enabled,
			TrimSpaces: c.Postprocess.TrimSpaces,
		},
		Repair: ProfileRepairConfig{
			Enabled:              c.Repair.Enabled,
			JsonStructural:       c.Repair.JSONStructural,
			SchemaAliases:        c.Repair.SchemaAliases,
			Partial:              c.Repair.Partial,
			PartialThreshold:     c.Repair.PartialThreshold,
			PlaceholderNormalize: c.Repair.PlaceholderNormalize,
			PromptUpgrade:        c.Repair.PromptUpgrade,
		},
		Glossary: ProfileGlossaryConfig{
			Bootstrap: ProfileBootstrapConfig{
				Enabled:                c.Glossary.Bootstrap.Enabled,
				MaxTermsPerBatch:       c.Glossary.Bootstrap.MaxTermsPerBatch,
				MinSourceLen:           c.Glossary.Bootstrap.MinSourceLen,
				InlineConflictStrategy: ProfileBootstrapConfigInlineConflictStrategy(c.Glossary.Bootstrap.InlineConflictStrategy),
			},
		},
	}
}

// parseProfileConfig 从 API 请求解析配置。
func parseProfileConfig(c *TranslationProfileConfig) *schema.TranslationProfileConfigData {
	if c == nil {
		return nil
	}

	var rules []string
	if c.Protect.Rules != nil {
		rules = make([]string, len(*c.Protect.Rules))
		for i, r := range *c.Protect.Rules {
			rules[i] = string(r)
		}
	}

	ruby := schema.ProfileRubyConfig{}
	if c.Protect.Ruby != nil {
		ruby.Enabled = c.Protect.Ruby.Enabled
		if c.Protect.Ruby.OutputFormat != nil {
			ruby.OutputFormat = string(*c.Protect.Ruby.OutputFormat)
		}
	}

	return &schema.TranslationProfileConfigData{
		Split: schema.ProfileSplitConfig{
			Enabled:  c.Split.Enabled,
			Strategy: c.Split.Strategy,
			MaxChars: c.Split.MaxChars,
		},
		Protect: schema.ProfileProtectConfig{
			Enabled: c.Protect.Enabled,
			Rules:   rules,
			Ruby:    ruby,
		},
		Postprocess: schema.ProfilePostprocessConfig{
			Enabled:    c.Postprocess.Enabled,
			TrimSpaces: c.Postprocess.TrimSpaces,
		},
		Repair: schema.ProfileRepairConfig{
			Enabled:              c.Repair.Enabled,
			JSONStructural:       c.Repair.JsonStructural,
			SchemaAliases:        c.Repair.SchemaAliases,
			Partial:              c.Repair.Partial,
			PartialThreshold:     c.Repair.PartialThreshold,
			PlaceholderNormalize: c.Repair.PlaceholderNormalize,
			PromptUpgrade:        c.Repair.PromptUpgrade,
		},
		Glossary: schema.ProfileGlossaryConfig{
			Bootstrap: schema.ProfileBootstrapConfig{
				Enabled:                c.Glossary.Bootstrap.Enabled,
				MaxTermsPerBatch:       c.Glossary.Bootstrap.MaxTermsPerBatch,
				MinSourceLen:           c.Glossary.Bootstrap.MinSourceLen,
				InlineConflictStrategy: string(c.Glossary.Bootstrap.InlineConflictStrategy),
			},
		},
	}
}

// ---- Handler 方法 ----

// handleListTranslationProfiles 列出当前用户的翻译配置。
func (s *Server) handleListTranslationProfiles(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	profiles, err := s.translationProfileSvc.ListByUser(r.Context(), authUser.User.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal_error", "查询翻译配置失败")
		return
	}

	items := make([]TranslationProfile, 0, len(profiles))
	for _, t := range profiles {
		items = append(items, entTranslationProfileToResponse(t))
	}

	writeJSON(w, http.StatusOK, TranslationProfileListResponse{Items: items})
}

// handleCreateTranslationProfile 创建翻译配置。
func (s *Server) handleCreateTranslationProfile(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	var req CreateTranslationProfileRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeProblem(w, http.StatusBadRequest, "validation_error", "翻译配置名称不能为空")
		return
	}

	input := service.CreateTranslationProfileInput{
		Name:        req.Name,
		Scope:       "user",
		OwnerUserID: &authUser.User.ID,
	}
	if req.Description != nil {
		input.Description = *req.Description
	}
	if req.Config != nil {
		input.Config = parseProfileConfig(req.Config)
	}

	tp, err := s.translationProfileSvc.Create(r.Context(), input)
	if err != nil {
		if err == service.ErrTranslationProfileConfigInvalid {
			writeProblem(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		writeProblem(w, http.StatusInternalServerError, "internal_error", "创建翻译配置失败")
		return
	}
	writeJSON(w, http.StatusCreated, entTranslationProfileToResponse(tp))
}

// handleGetTranslationProfile 获取翻译配置详情。
func (s *Server) handleGetTranslationProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTranslationProfileID(w, r)
	if !ok {
		return
	}

	tp, err := s.translationProfileSvc.GetByID(r.Context(), id)
	if err != nil {
		if err == service.ErrTranslationProfileNotFound {
			writeProblem(w, http.StatusNotFound, "not_found", "翻译配置不存在")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "internal_error", "查询翻译配置失败")
		return
	}
	writeJSON(w, http.StatusOK, entTranslationProfileToResponse(tp))
}

// handleUpdateTranslationProfile 更新翻译配置。
func (s *Server) handleUpdateTranslationProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTranslationProfileID(w, r)
	if !ok {
		return
	}

	var req UpdateTranslationProfileRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	input := service.UpdateTranslationProfileInput{
		Name:        req.Name,
		Description: req.Description,
	}
	if req.Config != nil {
		input.Config = parseProfileConfig(req.Config)
	}

	tp, err := s.translationProfileSvc.Update(r.Context(), id, input)
	if err != nil {
		if err == service.ErrTranslationProfileNotFound {
			writeProblem(w, http.StatusNotFound, "not_found", "翻译配置不存在")
			return
		}
		if err == service.ErrTranslationProfileConfigInvalid {
			writeProblem(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		writeProblem(w, http.StatusInternalServerError, "internal_error", "更新翻译配置失败")
		return
	}
	writeJSON(w, http.StatusOK, entTranslationProfileToResponse(tp))
}

// handleDeleteTranslationProfile 删除翻译配置。
func (s *Server) handleDeleteTranslationProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTranslationProfileID(w, r)
	if !ok {
		return
	}

	err := s.translationProfileSvc.Delete(r.Context(), id)
	if err != nil {
		if err == service.ErrTranslationProfileNotFound {
			writeProblem(w, http.StatusNotFound, "not_found", "翻译配置不存在")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "internal_error", "删除翻译配置失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
