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
func toExecutionRoundConfigAPI(rc schema.ExecutionRoundConfig) ExecutionRoundConfig {
	apiRC := ExecutionRoundConfig{
		Mode:      ExecutionRoundConfigMode(rc.Mode),
		BackendId: rc.BackendID,
	}
	if rc.Mode == "translate" && rc.Translate != nil {
		t := rc.Translate
		apiRC.Concurrency = t.Concurrency
		translateCfg := TranslateRoundConfig{}
		translateCfg.PromptTemplateId = &t.PromptTemplateID
		translateCfg.ProfileId = &t.ProfileID
		translateCfg.BatchSize = &t.BatchSize
		translateCfg.MaxWordsPerBatch = &t.MaxWordsPerBatch
		if t.FallbackShrink > 0 {
			fs := float32(t.FallbackShrink)
			translateCfg.FallbackShrink = &fs
		}
		if t.Retry.MaxAttempts > 0 || t.Retry.BackoffMs > 0 || t.Retry.Jitter {
			retry := toRetryConfigAPI(t.Retry)
			translateCfg.Retry = &retry
		}
		apiRC.Translate = &translateCfg
	}
	if rc.Mode == "extract" && rc.Extract != nil {
		e := rc.Extract
		apiRC.Concurrency = e.Concurrency
		extractCfg := ExtractRoundConfig{}
		extractCfg.TemplateId = &e.BootstrapTemplateID
		extractCfg.BatchSize = &e.BatchSize
		if e.MaxWordsPerBatch > 0 {
			mwpb := e.MaxWordsPerBatch
			extractCfg.MaxWordsPerBatch = &mwpb
		}
		if e.MaxTermsPer1000Chars > 0 {
			mtpc := float32(e.MaxTermsPer1000Chars)
			extractCfg.MaxTermsPer1000Chars = &mtpc
		}
		if e.MinSourceLen > 0 {
			msl := e.MinSourceLen
			extractCfg.MinSourceLen = &msl
		}
		if e.Retry.MaxAttempts > 0 || e.Retry.BackoffMs > 0 || e.Retry.Jitter {
			retry := toRetryConfigAPI(e.Retry)
			extractCfg.Retry = &retry
		}
		apiRC.Extract = &extractCfg
	}
	if rc.Mode == "adjudicate" && rc.Adjudicate != nil {
		a := rc.Adjudicate
		apiRC.Concurrency = a.Concurrency
		adjudicateCfg := AdjudicateRoundConfig{}
		adjudicateCfg.BatchSize = &a.BatchSize
		if a.MaxWordsPerBatch > 0 {
			mwpb := a.MaxWordsPerBatch
			adjudicateCfg.MaxWordsPerBatch = &mwpb
		}
		if len(a.AdjudicateCodes) > 0 {
			codes := make([]AdjudicateRoundConfigAdjudicateCodes, 0, len(a.AdjudicateCodes))
			for _, c := range a.AdjudicateCodes {
				codes = append(codes, AdjudicateRoundConfigAdjudicateCodes(c))
			}
			adjudicateCfg.AdjudicateCodes = &codes
		}
		if a.Retry.MaxAttempts > 0 || a.Retry.BackoffMs > 0 || a.Retry.Jitter {
			retry := toRetryConfigAPI(a.Retry)
			adjudicateCfg.Retry = &retry
		}
		apiRC.Adjudicate = &adjudicateCfg
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
func toExecutionPlanRoundsAPI(apiRounds []ExecutionRoundConfig) []schema.ExecutionRoundConfig {
	rounds := make([]schema.ExecutionRoundConfig, 0, len(apiRounds))
	for _, ar := range apiRounds {
		rc := schema.ExecutionRoundConfig{
			Mode:      string(ar.Mode),
			BackendID: ar.BackendId,
		}
		if ar.Mode == Translate && ar.Translate != nil {
			t := ar.Translate
			translateCfg := &schema.TranslateRoundConfig{
				Concurrency: ar.Concurrency,
			}
			if t.PromptTemplateId != nil {
				translateCfg.PromptTemplateID = *t.PromptTemplateId
			}
			if t.ProfileId != nil {
				translateCfg.ProfileID = *t.ProfileId
			}
			if t.BatchSize != nil {
				translateCfg.BatchSize = *t.BatchSize
			}
			if t.MaxWordsPerBatch != nil {
				translateCfg.MaxWordsPerBatch = *t.MaxWordsPerBatch
			}
			if t.FallbackShrink != nil {
				translateCfg.FallbackShrink = float64(*t.FallbackShrink)
			}
			if t.Retry != nil {
				if t.Retry.MaxAttempts != nil {
					translateCfg.Retry.MaxAttempts = *t.Retry.MaxAttempts
				}
				if t.Retry.BackoffMs != nil {
					translateCfg.Retry.BackoffMs = *t.Retry.BackoffMs
				}
				if t.Retry.Jitter != nil {
					translateCfg.Retry.Jitter = *t.Retry.Jitter
				}
			}
			rc.Translate = translateCfg
		}
		if ar.Mode == Extract && ar.Extract != nil {
			e := ar.Extract
			extractCfg := &schema.ExtractRoundConfig{
				Concurrency: ar.Concurrency,
			}
			if e.TemplateId != nil {
				extractCfg.BootstrapTemplateID = *e.TemplateId
			}
			if e.BatchSize != nil {
				extractCfg.BatchSize = *e.BatchSize
			}
			if e.MaxWordsPerBatch != nil {
				extractCfg.MaxWordsPerBatch = *e.MaxWordsPerBatch
			}
			if e.MaxTermsPer1000Chars != nil {
				extractCfg.MaxTermsPer1000Chars = float64(*e.MaxTermsPer1000Chars)
			}
			if e.MinSourceLen != nil {
				extractCfg.MinSourceLen = *e.MinSourceLen
			}
			if e.Retry != nil {
				if e.Retry.MaxAttempts != nil {
					extractCfg.Retry.MaxAttempts = *e.Retry.MaxAttempts
				}
				if e.Retry.BackoffMs != nil {
					extractCfg.Retry.BackoffMs = *e.Retry.BackoffMs
				}
				if e.Retry.Jitter != nil {
					extractCfg.Retry.Jitter = *e.Retry.Jitter
				}
			}
			rc.Extract = extractCfg
		}
		if ar.Mode == Adjudicate && ar.Adjudicate != nil {
			a := ar.Adjudicate
			adjudicateCfg := &schema.AdjudicateRoundConfig{
				Concurrency: ar.Concurrency,
			}
			if a.BatchSize != nil {
				adjudicateCfg.BatchSize = *a.BatchSize
			}
			if a.MaxWordsPerBatch != nil {
				adjudicateCfg.MaxWordsPerBatch = *a.MaxWordsPerBatch
			}
			if a.AdjudicateCodes != nil {
				codes := make([]string, 0, len(*a.AdjudicateCodes))
				for _, c := range *a.AdjudicateCodes {
					codes = append(codes, string(c))
				}
				adjudicateCfg.AdjudicateCodes = codes
			}
			if a.Retry != nil {
				if a.Retry.MaxAttempts != nil {
					adjudicateCfg.Retry.MaxAttempts = *a.Retry.MaxAttempts
				}
				if a.Retry.BackoffMs != nil {
					adjudicateCfg.Retry.BackoffMs = *a.Retry.BackoffMs
				}
				if a.Retry.Jitter != nil {
					adjudicateCfg.Retry.Jitter = *a.Retry.Jitter
				}
			}
			rc.Adjudicate = adjudicateCfg
		}
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
