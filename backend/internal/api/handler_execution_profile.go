package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
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
		resp.CreatedAt = timeutil.NormalizePtr(&t.CreatedAt)
	}
	if !t.UpdatedAt.IsZero() {
		resp.UpdatedAt = timeutil.NormalizePtr(&t.UpdatedAt)
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

	qaConfig := &ProfileQAConfig{
		Enabled:        c.QA.Enabled,
		AutoReject:     &c.QA.AutoReject,
		LengthMethod:   (*ProfileQAConfigLengthMethod)(&c.QA.LengthMethod),
		LengthRatioMin: &c.QA.LengthRatioMin,
		LengthRatioMax: &c.QA.LengthRatioMax,
	}
	if c.QA.Checks != nil {
		checks := append([]string{}, c.QA.Checks...)
		qaConfig.Checks = &checks
	}

	return ExecutionProfileConfig{
		SchemaVersion: ExecutionProfileConfigSchemaVersion(c.SchemaVersion),
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
			PlaceholderNormalize: c.Repair.PlaceholderNormalize,
			PromptUpgrade:        c.Repair.PromptUpgrade,
		},
		Context: ProfileContextConfig{
			Enabled:  c.Context.Enabled,
			Before:   c.Context.Before,
			After:    c.Context.After,
			MaxChars: c.Context.MaxChars,
		},
		Qa: qaConfig,
	}
}

// ---- Handler 方法 ----

// handleListExecutionProfiles 列出当前用户的执行策略配置。
func (s *Server) handleListExecutionProfiles(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	orgID, ok := s.parseSharedOrgQuery(w, r)
	if !ok {
		return
	}
	var profiles []*ent.ExecutionProfile
	var err error
	if orgID == nil {
		profiles, err = s.executionProfileSvc.ListByUser(r.Context(), authUser.User.ID)
	} else {
		profiles, err = s.executionProfileSvc.ListByOrg(r.Context(), authUser.User.ID, *orgID)
	}
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

	var req struct {
		Name        string          `json:"name"`
		Description *string         `json:"description"`
		OrgId       *int            `json:"org_id"`
		Config      json.RawMessage `json:"config"`
	}
	if !s.decodeSharedJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		s.writeProblem(w, r, http.StatusBadRequest, "validation_error", "执行策略配置名称不能为空")
		return
	}

	input := service.CreateExecutionProfileInput{
		Name:  req.Name,
		OrgID: req.OrgId,
	}
	if req.Description != nil {
		input.Description = *req.Description
	}
	if req.Config != nil {
		parsed, err := execution.DecodeProfileJSON(req.Config, execution.DefaultProfile())
		if err != nil {
			s.writeProblem(w, r, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		input.Config = &parsed
	}

	tp, err := s.executionProfileSvc.Create(r.Context(), authUser.User.ID, input)
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
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	id, ok := s.parseExecutionProfileID(w, r)
	if !ok {
		return
	}

	tp, err := s.executionProfileSvc.GetByID(r.Context(), authUser.User.ID, id)
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
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	id, ok := s.parseExecutionProfileID(w, r)
	if !ok {
		return
	}

	var req struct {
		Name        *string         `json:"name"`
		Description *string         `json:"description"`
		Config      json.RawMessage `json:"config"`
	}
	if !s.decodeSharedJSON(w, r, &req) {
		return
	}

	input := service.UpdateExecutionProfileInput{
		Name:        req.Name,
		Description: req.Description,
	}
	if req.Config != nil {
		// 获取现有配置，将请求中的字段合并上去，避免未指定字段被零值覆盖。
		existing, err := s.executionProfileSvc.GetByID(r.Context(), authUser.User.ID, id)
		if err != nil {
			if err == service.ErrExecutionProfileNotFound {
				s.writeProblem(w, r, http.StatusNotFound, "not_found", "执行策略配置不存在")
				return
			}
			s.writeServiceError(w, r, err)
			return
		}
		parsed, err := execution.DecodeProfileJSON(req.Config, existing.Config)
		if err != nil {
			s.writeProblem(w, r, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		input.Config = &parsed
	}

	tp, err := s.executionProfileSvc.Update(r.Context(), authUser.User.ID, id, input)
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
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	id, ok := s.parseExecutionProfileID(w, r)
	if !ok {
		return
	}

	err := s.executionProfileSvc.Delete(r.Context(), authUser.User.ID, id)
	if err != nil {
		if err == service.ErrExecutionProfileNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "执行策略配置不存在")
			return
		}
		if errors.Is(err, service.ErrExecutionProfileInUse) {
			// 固定文案：不回传 service 层错误详情，避免泄露引用计划的信息。
			s.writeProblem(w, r, http.StatusConflict, "conflict", "该执行策略正被执行计划引用，无法删除")
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
