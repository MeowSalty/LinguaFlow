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

// HandlerExecutionPlan 执行计划模板 handler。
type HandlerExecutionPlan struct {
	executionPlans *service.ExecutionPlanService
	server         *Server
}

// NewHandlerExecutionPlan 创建执行计划模板 handler。
func NewHandlerExecutionPlan(executionPlans *service.ExecutionPlanService, server *Server) *HandlerExecutionPlan {
	return &HandlerExecutionPlan{executionPlans: executionPlans, server: server}
}

// ---- 辅助函数 ----

// parseExecutionPlanTemplateID 从路径参数解析 executionPlanTemplateId。
func (s *Server) parseExecutionPlanTemplateID(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := chi.URLParam(r, "executionPlanTemplateId")
	id, err := strconv.Atoi(raw)
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_id", "执行计划模板 ID 必须为整数")
		return 0, false
	}
	return id, true
}

// toExecutionRoundConfigAPI 将 schema 层的轮次配置转换为 API 响应类型。
// 仅支持 translate 轮次；extract 轮次暂不映射到旧 API 类型。
func toExecutionRoundConfigAPI(rc schema.ExecutionRoundConfig) ExecutionRoundConfig {
	apiRC := ExecutionRoundConfig{
		BackendId: rc.BackendID,
	}
	if rc.Name != "" {
		name := rc.Name
		apiRC.Name = &name
	}
	if rc.Mode == "translate" && rc.Translate != nil {
		t := rc.Translate
		apiRC.Concurrency = t.Concurrency
		apiRC.ProfileId = t.ProfileID
		apiRC.PromptTemplateId = t.PromptTemplateID
		if t.BatchSize > 0 {
			bs := t.BatchSize
			apiRC.BatchSize = &bs
		}
		if t.MaxWordsPerBatch > 0 {
			mwpb := t.MaxWordsPerBatch
			apiRC.MaxWordsPerBatch = &mwpb
		}
		if t.FallbackShrink > 0 {
			fs := float32(t.FallbackShrink)
			apiRC.FallbackShrink = &fs
		}
		retry := toRetryConfigAPI(t.Retry)
		apiRC.Retry = &retry
	}
	return apiRC
}

// toRetryConfigAPI 将 schema 层的重试配置转换为 API 响应类型。
func toRetryConfigAPI(rc schema.RetryConfig) RetryConfig {
	return RetryConfig{
		MaxAttempts: intPtr(rc.MaxAttempts),
		BackoffMs:   intPtr(rc.BackoffMs),
		Jitter:      boolPtr(rc.Jitter),
	}
}

func intPtr(v int) *int             { return &v }
func boolPtr(v bool) *bool          { return &v }
func float32Ptr(v float32) *float32 { return &v }

// toExecutionPlanTemplateResponse 将 ent 实体转换为 API 响应。
func toExecutionPlanTemplateResponse(t *ent.ExecutionPlanTemplate) ExecutionPlanTemplate {
	resp := ExecutionPlanTemplate{
		Id:    t.ID,
		Name:  t.Name,
		Scope: ExecutionPlanTemplateScope(t.Scope),
	}
	if t.Description != "" {
		resp.Description = &t.Description
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
	// 注音对齐重试配置
	if t.RubyRetry.Enabled {
		rr := toRubyRetryConfigAPI(t.RubyRetry)
		resp.RubyRetry = &rr
	}
	rounds := make([]ExecutionRoundConfig, 0, len(t.Rounds))
	for _, rc := range t.Rounds {
		rounds = append(rounds, toExecutionRoundConfigAPI(rc))
	}
	resp.Rounds = rounds
	return resp
}

// toRubyRetryConfigAPI 将 schema 层的注音对齐重试配置转换为 API 响应类型。
func toRubyRetryConfigAPI(rr schema.ExecutionPlanRubyRetryConfig) ExecutionPlanRubyRetryConfig {
	result := ExecutionPlanRubyRetryConfig{
		Enabled: rr.Enabled,
	}
	if rr.BackendID > 0 {
		result.BackendId = &rr.BackendID
	}
	return result
}

// parseRubyRetryConfig 将 API 请求中的注音对齐重试配置转换为 schema 层。
func parseRubyRetryConfig(api *ExecutionPlanRubyRetryConfig) schema.ExecutionPlanRubyRetryConfig {
	if api == nil {
		return schema.ExecutionPlanRubyRetryConfig{}
	}
	result := schema.ExecutionPlanRubyRetryConfig{
		Enabled: api.Enabled,
	}
	if api.BackendId != nil {
		result.BackendID = *api.BackendId
	}
	return result
}

// toExecutionPlanRoundsAPI 将 API 请求中的轮次配置转换为 schema 层。
// 旧 API 类型为扁平结构，默认映射为 translate 轮次。
func toExecutionPlanRoundsAPI(apiRounds []ExecutionRoundConfig) []schema.ExecutionRoundConfig {
	rounds := make([]schema.ExecutionRoundConfig, 0, len(apiRounds))
	for _, ar := range apiRounds {
		rc := schema.ExecutionRoundConfig{
			Mode:      "translate",
			BackendID: ar.BackendId,
		}
		if ar.Name != nil {
			rc.Name = *ar.Name
		}
		t := &schema.TranslateRoundConfig{
			PromptTemplateID: ar.PromptTemplateId,
			ProfileID:        ar.ProfileId,
			Concurrency:      ar.Concurrency,
		}
		if ar.BatchSize != nil {
			t.BatchSize = *ar.BatchSize
		}
		if ar.MaxWordsPerBatch != nil {
			t.MaxWordsPerBatch = *ar.MaxWordsPerBatch
		}
		if ar.FallbackShrink != nil {
			t.FallbackShrink = float64(*ar.FallbackShrink)
		}
		if ar.Retry != nil {
			if ar.Retry.MaxAttempts != nil {
				t.Retry.MaxAttempts = *ar.Retry.MaxAttempts
			}
			if ar.Retry.BackoffMs != nil {
				t.Retry.BackoffMs = *ar.Retry.BackoffMs
			}
			if ar.Retry.Jitter != nil {
				t.Retry.Jitter = *ar.Retry.Jitter
			}
		}
		rc.Translate = t
		rounds = append(rounds, rc)
	}
	return rounds
}

// ---- Handler 方法 ----

// handleListExecutionPlanTemplates 列出当前用户可访问的执行计划模板。
func (h *HandlerExecutionPlan) handleList(w http.ResponseWriter, r *http.Request, userID int) {
	templates, err := h.executionPlans.ListByUser(r.Context(), userID)
	if err != nil {
		h.server.writeProblem(w, r, http.StatusInternalServerError, "internal_error", "查询执行计划模板失败")
		return
	}
	items := make([]ExecutionPlanTemplate, 0, len(templates))
	for _, t := range templates {
		items = append(items, toExecutionPlanTemplateResponse(t))
	}
	writeJSON(w, http.StatusOK, ExecutionPlanTemplateListResponse{Items: items})
}

// handleCreate 创建执行计划模板。
func (h *HandlerExecutionPlan) handleCreate(w http.ResponseWriter, r *http.Request, userID int) {
	var req CreateExecutionPlanTemplateRequest
	if !h.server.decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		h.server.writeProblem(w, r, http.StatusBadRequest, "validation_error", "执行计划模板名称不能为空")
		return
	}

	input := service.CreateExecutionPlanTemplateInput{
		Name:        req.Name,
		Scope:       "user",
		OwnerUserID: &userID,
		RubyRetry:   parseRubyRetryConfig(req.RubyRetry),
		Rounds:      toExecutionPlanRoundsAPI(req.Rounds),
	}
	if req.Description != nil {
		input.Description = *req.Description
	}

	pt, err := h.executionPlans.Create(r.Context(), input)
	if err != nil {
		h.server.writeExecutionPlanServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toExecutionPlanTemplateResponse(pt))
}

// handleGet 获取执行计划模板详情。
func (h *HandlerExecutionPlan) handleGet(w http.ResponseWriter, r *http.Request, userID, planID int) {
	pt, err := h.executionPlans.GetByID(r.Context(), userID, planID)
	if err != nil {
		h.server.writeExecutionPlanServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toExecutionPlanTemplateResponse(pt))
}

// handleUpdate 更新执行计划模板。
func (h *HandlerExecutionPlan) handleUpdate(w http.ResponseWriter, r *http.Request, userID, planID int) {
	var req UpdateExecutionPlanTemplateRequest
	if !h.server.decodeJSON(w, r, &req) {
		return
	}

	input := service.UpdateExecutionPlanTemplateInput{
		Name:        req.Name,
		Description: req.Description,
	}
	if req.RubyRetry != nil {
		rr := parseRubyRetryConfig(req.RubyRetry)
		input.RubyRetry = &rr
	}
	if req.Rounds != nil {
		rounds := toExecutionPlanRoundsAPI(*req.Rounds)
		input.Rounds = rounds
	}

	pt, err := h.executionPlans.Update(r.Context(), userID, planID, input)
	if err != nil {
		h.server.writeExecutionPlanServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toExecutionPlanTemplateResponse(pt))
}

// handleDelete 删除执行计划模板。
func (h *HandlerExecutionPlan) handleDelete(w http.ResponseWriter, r *http.Request, userID, planID int) {
	err := h.executionPlans.Delete(r.Context(), userID, planID)
	if err != nil {
		h.server.writeExecutionPlanServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeExecutionPlanServiceError 将 service 层错误转换为 HTTP 响应。
func (s *Server) writeExecutionPlanServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrExecutionPlanNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "执行计划模板不存在")
	case errors.Is(err, service.ErrExecutionPlanScopeInvalid):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_scope", "无效的 scope")
	case errors.Is(err, service.ErrExecutionPlanConfigInvalid):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_config", err.Error())
	case errors.Is(err, service.ErrExecutionPlanInUse):
		s.writeProblem(w, r, http.StatusConflict, "in_use", "该模板正在被翻译任务引用，无法删除")
	case errors.Is(err, service.ErrForbidden):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrInvalidInput):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", err.Error())
	default:
		s.writeServiceError(w, r, err)
	}
}
