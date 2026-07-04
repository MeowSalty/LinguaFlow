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

// toAPIPreserveKinds 将 []string 转换为 API 类型 []ProfileRubyConfigPreserveKinds。
func toAPIPreserveKinds(kinds []string) []ProfileRubyConfigPreserveKinds {
	result := make([]ProfileRubyConfigPreserveKinds, len(kinds))
	for i, k := range kinds {
		result[i] = ProfileRubyConfigPreserveKinds(k)
	}
	return result
}

// fromAPIPreserveKinds 将 API 类型 []ProfileRubyConfigPreserveKinds 转换为 []string。
func fromAPIPreserveKinds(kinds []ProfileRubyConfigPreserveKinds) []string {
	result := make([]string, len(kinds))
	for i, k := range kinds {
		result[i] = string(k)
	}
	return result
}

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

	rubyConfig := &ProfileRubyConfig{
		Enabled: c.Protect.Ruby.Enabled,
	}
	if c.Protect.Ruby.PreserveKinds != nil {
		pk := toAPIPreserveKinds(c.Protect.Ruby.PreserveKinds)
		rubyConfig.PreserveKinds = &pk
	}

	return TranslationProfileConfig{
		Split: ProfileSplitConfig{
			Enabled:  c.Split.Enabled,
			Strategy: c.Split.Strategy,
			MaxChars: c.Split.MaxChars,
		},
		Protect: ProfileProtectConfig{
			Enabled: c.Protect.Enabled,
			Rules:   &rules,
			Ruby:    rubyConfig,
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
				MaxTermsPer1000Chars:   c.Glossary.Bootstrap.MaxTermsPer1000Chars,
				MinSourceLen:           c.Glossary.Bootstrap.MinSourceLen,
				InlineConflictStrategy: ProfileBootstrapConfigInlineConflictStrategy(c.Glossary.Bootstrap.InlineConflictStrategy),
			},
		},
		Context: ProfileContextConfig{
			Enabled:  c.Context.Enabled,
			Before:   c.Context.Before,
			After:    c.Context.After,
			MaxChars: c.Context.MaxChars,
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
		if c.Protect.Ruby.PreserveKinds != nil {
			ruby.PreserveKinds = fromAPIPreserveKinds(*c.Protect.Ruby.PreserveKinds)
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
				MaxTermsPer1000Chars:   c.Glossary.Bootstrap.MaxTermsPer1000Chars,
				MinSourceLen:           c.Glossary.Bootstrap.MinSourceLen,
				InlineConflictStrategy: string(c.Glossary.Bootstrap.InlineConflictStrategy),
			},
		},
		Context: schema.ProfileContextConfig{
			Enabled:  c.Context.Enabled,
			Before:   c.Context.Before,
			After:    c.Context.After,
			MaxChars: c.Context.MaxChars,
		},
	}
}

// mergeProfileConfig 将请求中的部分配置合并到现有配置上。
// 仅覆盖请求中显式提供的字段，未指定的字段保留现有值。
func mergeProfileConfig(existing *schema.TranslationProfileConfigData, incoming *TranslationProfileConfig) *schema.TranslationProfileConfigData {
	merged := *existing

	if incoming.Split.Strategy != "" || incoming.Split.MaxChars > 0 {
		merged.Split.Enabled = incoming.Split.Enabled
		merged.Split.Strategy = incoming.Split.Strategy
		merged.Split.MaxChars = incoming.Split.MaxChars
	}

	if incoming.Protect.Rules != nil {
		rules := make([]string, len(*incoming.Protect.Rules))
		for i, r := range *incoming.Protect.Rules {
			rules[i] = string(r)
		}
		merged.Protect.Rules = rules
	}
	if incoming.Protect.Ruby != nil {
		merged.Protect.Ruby.Enabled = incoming.Protect.Ruby.Enabled
		if incoming.Protect.Ruby.PreserveKinds != nil {
			merged.Protect.Ruby.PreserveKinds = fromAPIPreserveKinds(*incoming.Protect.Ruby.PreserveKinds)
		}
	}

	merged.Postprocess.Enabled = incoming.Postprocess.Enabled
	merged.Postprocess.TrimSpaces = incoming.Postprocess.TrimSpaces

	merged.Repair.Enabled = incoming.Repair.Enabled
	merged.Repair.JSONStructural = incoming.Repair.JsonStructural
	merged.Repair.SchemaAliases = incoming.Repair.SchemaAliases
	merged.Repair.Partial = incoming.Repair.Partial
	merged.Repair.PartialThreshold = incoming.Repair.PartialThreshold
	merged.Repair.PlaceholderNormalize = incoming.Repair.PlaceholderNormalize
	merged.Repair.PromptUpgrade = incoming.Repair.PromptUpgrade

	merged.Glossary.Bootstrap.Enabled = incoming.Glossary.Bootstrap.Enabled
	merged.Glossary.Bootstrap.MaxTermsPer1000Chars = incoming.Glossary.Bootstrap.MaxTermsPer1000Chars
	merged.Glossary.Bootstrap.MinSourceLen = incoming.Glossary.Bootstrap.MinSourceLen
	merged.Glossary.Bootstrap.InlineConflictStrategy = string(incoming.Glossary.Bootstrap.InlineConflictStrategy)

	merged.Context.Enabled = incoming.Context.Enabled
	merged.Context.Before = incoming.Context.Before
	merged.Context.After = incoming.Context.After
	merged.Context.MaxChars = incoming.Context.MaxChars

	return &merged
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
		// 获取现有配置，将请求中的字段合并上去，避免未指定字段被零值覆盖。
		existing, err := s.translationProfileSvc.GetByID(r.Context(), id)
		if err != nil {
			if err == service.ErrTranslationProfileNotFound {
				writeProblem(w, http.StatusNotFound, "not_found", "翻译配置不存在")
				return
			}
			writeProblem(w, http.StatusInternalServerError, "internal_error", "查询翻译配置失败")
			return
		}
		input.Config = mergeProfileConfig(&existing.Config, req.Config)
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
