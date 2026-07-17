package api

import (
	"errors"
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

// parseExecutionProfileID 从路径参数解析 executionProfileId。
func (s *Server) parseExecutionProfileID(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := chi.URLParam(r, "executionProfileId")
	id, err := strconv.Atoi(raw)
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_id", "执行策略配置 ID 必须为整数")
		return 0, false
	}
	return id, true
}

// entExecutionProfileToResponse 将数据库执行策略配置转换为 API 响应。
func entExecutionProfileToResponse(t *ent.ExecutionProfile) ExecutionProfile {
	resp := ExecutionProfile{
		Id:          t.ID,
		Name:        t.Name,
		Description: t.Description,
		Scope:       ExecutionProfileScope(t.Scope),
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
func profileConfigToResponse(c *schema.ExecutionProfileConfigData) ExecutionProfileConfig {
	rules := make([]ProfileProtectConfigRules, len(c.Protect.Rules))
	for i, r := range c.Protect.Rules {
		rules[i] = ProfileProtectConfigRules(r)
	}

	rubyConfig := &ProfileRubyConfig{
		Enabled: c.Ruby.Enabled,
	}
	if c.Ruby.PreserveKinds != nil {
		pk := toAPIPreserveKinds(c.Ruby.PreserveKinds)
		rubyConfig.PreserveKinds = &pk
	}

	return ExecutionProfileConfig{
		Protect: ProfileProtectConfig{
			Enabled: c.Protect.Enabled,
			Rules:   &rules,
		},
		Ruby: rubyConfig,
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
		Qa: &ProfileQAConfig{
			Enabled:        c.QA.Enabled,
			AutoReject:     &c.QA.AutoReject,
			LengthMethod:   (*ProfileQAConfigLengthMethod)(&c.QA.LengthMethod),
			LengthRatioMin: &c.QA.LengthRatioMin,
			LengthRatioMax: &c.QA.LengthRatioMax,
		},
	}
}

// parseProfileConfig 从 API 请求解析配置。
func parseProfileConfig(c *ExecutionProfileConfig) *schema.ExecutionProfileConfigData {
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
	if c.Ruby != nil {
		ruby.Enabled = c.Ruby.Enabled
		if c.Ruby.PreserveKinds != nil {
			ruby.PreserveKinds = fromAPIPreserveKinds(*c.Ruby.PreserveKinds)
		}
	}

	qa := schema.ProfileQAConfig{}
	if c.Qa != nil {
		qa.Enabled = c.Qa.Enabled
		if c.Qa.AutoReject != nil {
			qa.AutoReject = *c.Qa.AutoReject
		}
		if c.Qa.LengthMethod != nil {
			qa.LengthMethod = string(*c.Qa.LengthMethod)
		}
		if c.Qa.LengthRatioMin != nil {
			qa.LengthRatioMin = *c.Qa.LengthRatioMin
		}
		if c.Qa.LengthRatioMax != nil {
			qa.LengthRatioMax = *c.Qa.LengthRatioMax
		}
	}

	return &schema.ExecutionProfileConfigData{
		Protect: schema.ProfileProtectConfig{
			Enabled: c.Protect.Enabled,
			Rules:   rules,
		},
		Ruby: ruby,
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
		QA: qa,
	}
}

// mergeProfileConfig 将请求中的部分配置合并到现有配置上。
// 仅覆盖请求中显式提供的字段，未指定的字段保留现有值。
func mergeProfileConfig(existing *schema.ExecutionProfileConfigData, incoming *ExecutionProfileConfig) *schema.ExecutionProfileConfigData {
	merged := *existing

	if incoming.Protect.Rules != nil {
		rules := make([]string, len(*incoming.Protect.Rules))
		for i, r := range *incoming.Protect.Rules {
			rules[i] = string(r)
		}
		merged.Protect.Rules = rules
	}
	if incoming.Ruby != nil {
		merged.Ruby.Enabled = incoming.Ruby.Enabled
		if incoming.Ruby.PreserveKinds != nil {
			merged.Ruby.PreserveKinds = fromAPIPreserveKinds(*incoming.Ruby.PreserveKinds)
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

	if incoming.Qa != nil {
		merged.QA.Enabled = incoming.Qa.Enabled
		if incoming.Qa.AutoReject != nil {
			merged.QA.AutoReject = *incoming.Qa.AutoReject
		}
		if incoming.Qa.LengthMethod != nil {
			merged.QA.LengthMethod = string(*incoming.Qa.LengthMethod)
		}
		if incoming.Qa.LengthRatioMin != nil {
			merged.QA.LengthRatioMin = *incoming.Qa.LengthRatioMin
		}
		if incoming.Qa.LengthRatioMax != nil {
			merged.QA.LengthRatioMax = *incoming.Qa.LengthRatioMax
		}
	}

	return &merged
}

// ---- Handler 方法 ----

// handleListExecutionProfiles 列出当前用户的执行策略配置。
func (s *Server) handleListExecutionProfiles(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	profiles, err := s.executionProfileSvc.ListByUser(r.Context(), authUser.User.ID)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}

	items := make([]ExecutionProfile, 0, len(profiles))
	for _, t := range profiles {
		items = append(items, entExecutionProfileToResponse(t))
	}

	writeJSON(w, http.StatusOK, ExecutionProfileListResponse{Items: items})
}

// handleCreateExecutionProfile 创建执行策略配置。
func (s *Server) handleCreateExecutionProfile(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	var req CreateExecutionProfileRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		s.writeProblem(w, r, http.StatusBadRequest, "validation_error", "执行策略配置名称不能为空")
		return
	}

	input := service.CreateExecutionProfileInput{
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

	tp, err := s.executionProfileSvc.Create(r.Context(), input)
	if err != nil {
		if errors.Is(err, service.ErrExecutionProfileConfigInvalid) {
			s.writeProblem(w, r, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, entExecutionProfileToResponse(tp))
}

// handleGetExecutionProfile 获取执行策略配置详情。
func (s *Server) handleGetExecutionProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parseExecutionProfileID(w, r)
	if !ok {
		return
	}

	tp, err := s.executionProfileSvc.GetByID(r.Context(), id)
	if err != nil {
		if err == service.ErrExecutionProfileNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "执行策略配置不存在")
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entExecutionProfileToResponse(tp))
}

// handleUpdateExecutionProfile 更新执行策略配置。
func (s *Server) handleUpdateExecutionProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parseExecutionProfileID(w, r)
	if !ok {
		return
	}

	var req UpdateExecutionProfileRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}

	input := service.UpdateExecutionProfileInput{
		Name:        req.Name,
		Description: req.Description,
	}
	if req.Config != nil {
		// 获取现有配置，将请求中的字段合并上去，避免未指定字段被零值覆盖。
		existing, err := s.executionProfileSvc.GetByID(r.Context(), id)
		if err != nil {
			if err == service.ErrExecutionProfileNotFound {
				s.writeProblem(w, r, http.StatusNotFound, "not_found", "执行策略配置不存在")
				return
			}
			s.writeServiceError(w, r, err)
			return
		}
		input.Config = mergeProfileConfig(&existing.Config, req.Config)
	}

	tp, err := s.executionProfileSvc.Update(r.Context(), id, input)
	if err != nil {
		if err == service.ErrExecutionProfileNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "执行策略配置不存在")
			return
		}
		if errors.Is(err, service.ErrExecutionProfileConfigInvalid) {
			s.writeProblem(w, r, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entExecutionProfileToResponse(tp))
}

// handleDeleteExecutionProfile 删除执行策略配置。
func (s *Server) handleDeleteExecutionProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parseExecutionProfileID(w, r)
	if !ok {
		return
	}

	err := s.executionProfileSvc.Delete(r.Context(), id)
	if err != nil {
		if err == service.ErrExecutionProfileNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "执行策略配置不存在")
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
