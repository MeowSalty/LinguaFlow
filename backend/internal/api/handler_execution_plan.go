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
		Mode: ExecutionRoundConfigMode(rc.Mode),
	}
	if rc.BackendID > 0 {
		bid := rc.BackendID
		apiRC.BackendId = &bid
	}
	if rc.Mode == "translate" && rc.Translate != nil {
		t := rc.Translate
		apiRC.Concurrency = t.Concurrency
		translateCfg := TranslateRoundConfig{}
		translateCfg.PromptTemplateId = &t.PromptTemplateID
		translateCfg.BatchSize = &t.BatchSize
		translateCfg.MaxWordsPerBatch = &t.MaxWordsPerBatch
		if t.FallbackShrink > 0 {
			translateCfg.FallbackShrink = float32(t.FallbackShrink)
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
		// NOTE: extract 不接缩批（仅 translate 暴露 fallback_shrink）。
		// 若未来需要，在此加 if e.FallbackShrink > 0 { fs := float32(e.FallbackShrink); extractCfg.FallbackShrink = &fs }。
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
		// NOTE: adjudicate 不接缩批（仅 translate 暴露 fallback_shrink）。
		// 若未来需要，在此加 if a.FallbackShrink > 0 { fs := float32(a.FallbackShrink); adjudicateCfg.FallbackShrink = &fs }。
		if a.Retry.MaxAttempts > 0 || a.Retry.BackoffMs > 0 || a.Retry.Jitter {
			retry := toRetryConfigAPI(a.Retry)
			adjudicateCfg.Retry = &retry
		}
		apiRC.Adjudicate = &adjudicateCfg
	}
	if rc.Mode == "semantic_qa" && rc.SemanticQA != nil {
		s := rc.SemanticQA
		apiRC.Concurrency = s.Concurrency
		semanticQACfg := SemanticQARoundConfig{}
		semanticQACfg.BatchSize = &s.BatchSize
		if s.MaxWordsPerBatch > 0 {
			mwpb := s.MaxWordsPerBatch
			semanticQACfg.MaxWordsPerBatch = &mwpb
		}
		if s.SegmentScope != "" {
			ss := SemanticQARoundConfigSegmentScope(s.SegmentScope)
			semanticQACfg.SegmentScope = &ss
		}
		if len(s.IssueCodes) > 0 {
			codes := make([]SemanticQARoundConfigIssueCodes, 0, len(s.IssueCodes))
			for _, c := range s.IssueCodes {
				codes = append(codes, SemanticQARoundConfigIssueCodes(c))
			}
			semanticQACfg.IssueCodes = &codes
		}
		// NOTE: semantic_qa 不接缩批（仅 translate 暴露 fallback_shrink）。
		// 若未来需要，在此加 if s.FallbackShrink > 0 { fs := float32(s.FallbackShrink); semanticQACfg.FallbackShrink = &fs }。
		if s.Retry.MaxAttempts > 0 || s.Retry.BackoffMs > 0 || s.Retry.Jitter {
			retry := toRetryConfigAPI(s.Retry)
			semanticQACfg.Retry = &retry
		}
		apiRC.SemanticQa = &semanticQACfg
	}
	if rc.Mode == "revise" && rc.Revise != nil {
		r := rc.Revise
		apiRC.Concurrency = r.Concurrency
		reviseCfg := ReviseRoundConfig{}
		reviseCfg.BatchSize = &r.BatchSize
		if r.MaxWordsPerBatch > 0 {
			mwpb := r.MaxWordsPerBatch
			reviseCfg.MaxWordsPerBatch = &mwpb
		}
		if r.SegmentScope != "" {
			ss := ReviseRoundConfigSegmentScope(r.SegmentScope)
			reviseCfg.SegmentScope = &ss
		}
		if len(r.IssueCodes) > 0 {
			codes := make([]ReviseRoundConfigIssueCodes, 0, len(r.IssueCodes))
			for _, c := range r.IssueCodes {
				codes = append(codes, ReviseRoundConfigIssueCodes(c))
			}
			reviseCfg.IssueCodes = &codes
		}
		// NOTE: revise 不接缩批（仅 translate 暴露 fallback_shrink）。
		// 若未来需要，在此加 fallback_shrink 字段并补 OpenAPI/校验/snapshot/engine_factory 映射。
		if r.Retry.MaxAttempts > 0 || r.Retry.BackoffMs > 0 || r.Retry.Jitter {
			retry := toRetryConfigAPI(r.Retry)
			reviseCfg.Retry = &retry
		}
		apiRC.Revise = &reviseCfg
	}
	if rc.Mode == "correct" && rc.Correct != nil {
		c := rc.Correct
		apiRC.Concurrency = c.Concurrency
		rules := make([]CorrectRuleConfig, 0, len(c.Rules))
		for _, r := range c.Rules {
			rr := CorrectRuleConfig{Name: CorrectRuleConfigName(r.Name)}
			if !r.Enabled {
				f := false
				rr.Enabled = &f
			}
			rules = append(rules, rr)
		}
		apiRC.Correct = &CorrectRoundConfig{Rules: rules}
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
		Id:        t.ID,
		Name:      t.Name,
		Scope:     ExecutionPlanTemplateScope(t.Scope),
		ProfileId: t.ProfileID,
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
	if rr.MaxAttempts > 0 {
		v := rr.MaxAttempts
		result.MaxAttempts = &v
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
	if api.MaxAttempts != nil {
		result.MaxAttempts = *api.MaxAttempts
	}
	return result
}

// toExecutionPlanRoundsAPI 将 API 请求中的轮次配置转换为 schema 层。
func toExecutionPlanRoundsAPI(apiRounds []ExecutionRoundConfig) []schema.ExecutionRoundConfig {
	rounds := make([]schema.ExecutionRoundConfig, 0, len(apiRounds))
	for _, ar := range apiRounds {
		rc := schema.ExecutionRoundConfig{
			Mode: string(ar.Mode),
		}
		if ar.BackendId != nil {
			rc.BackendID = *ar.BackendId
		}
		if ar.Mode == ExecutionRoundConfigModeTranslate && ar.Translate != nil {
			t := ar.Translate
			translateCfg := &schema.TranslateRoundConfig{
				Concurrency: ar.Concurrency,
			}
			if t.PromptTemplateId != nil {
				translateCfg.PromptTemplateID = *t.PromptTemplateId
			}
			if t.BatchSize != nil {
				translateCfg.BatchSize = *t.BatchSize
			}
			if t.MaxWordsPerBatch != nil {
				translateCfg.MaxWordsPerBatch = *t.MaxWordsPerBatch
			}
			translateCfg.FallbackShrink = float64(t.FallbackShrink)
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
		if ar.Mode == ExecutionRoundConfigModeExtract && ar.Extract != nil {
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
			// NOTE: extract 不接缩批（仅 translate 暴露 fallback_shrink）。
			// 若未来需要，在此加 if e.FallbackShrink != nil { extractCfg.FallbackShrink = float64(*e.FallbackShrink) }。
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
		if ar.Mode == ExecutionRoundConfigModeAdjudicate && ar.Adjudicate != nil {
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
			// NOTE: adjudicate 不接缩批（仅 translate 暴露 fallback_shrink）。
			// 若未来需要，在此加 if a.FallbackShrink != nil { adjudicateCfg.FallbackShrink = float64(*a.FallbackShrink) }。
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
		if ar.Mode == ExecutionRoundConfigModeSemanticQa && ar.SemanticQa != nil {
			s := ar.SemanticQa
			semanticQACfg := &schema.SemanticQARoundConfig{
				Concurrency: ar.Concurrency,
			}
			if s.BatchSize != nil {
				semanticQACfg.BatchSize = *s.BatchSize
			}
			if s.MaxWordsPerBatch != nil {
				semanticQACfg.MaxWordsPerBatch = *s.MaxWordsPerBatch
			}
			if s.SegmentScope != nil {
				semanticQACfg.SegmentScope = string(*s.SegmentScope)
			}
			if s.IssueCodes != nil {
				codes := make([]string, 0, len(*s.IssueCodes))
				for _, c := range *s.IssueCodes {
					codes = append(codes, string(c))
				}
				semanticQACfg.IssueCodes = codes
			}
			// NOTE: semantic_qa 不接缩批（仅 translate 暴露 fallback_shrink）。
			// 若未来需要，在此加 if s.FallbackShrink != nil { semanticQACfg.FallbackShrink = float64(*s.FallbackShrink) }。
			if s.Retry != nil {
				if s.Retry.MaxAttempts != nil {
					semanticQACfg.Retry.MaxAttempts = *s.Retry.MaxAttempts
				}
				if s.Retry.BackoffMs != nil {
					semanticQACfg.Retry.BackoffMs = *s.Retry.BackoffMs
				}
				if s.Retry.Jitter != nil {
					semanticQACfg.Retry.Jitter = *s.Retry.Jitter
				}
			}
			rc.SemanticQA = semanticQACfg
		}
		if ar.Mode == ExecutionRoundConfigModeRevise && ar.Revise != nil {
			r := ar.Revise
			reviseCfg := &schema.ReviseRoundConfig{
				Concurrency: ar.Concurrency,
			}
			if r.BatchSize != nil {
				reviseCfg.BatchSize = *r.BatchSize
			}
			if r.MaxWordsPerBatch != nil {
				reviseCfg.MaxWordsPerBatch = *r.MaxWordsPerBatch
			}
			if r.SegmentScope != nil {
				reviseCfg.SegmentScope = string(*r.SegmentScope)
			}
			if r.IssueCodes != nil {
				codes := make([]string, 0, len(*r.IssueCodes))
				for _, c := range *r.IssueCodes {
					codes = append(codes, string(c))
				}
				reviseCfg.IssueCodes = codes
			}
			// NOTE: revise 不接缩批（仅 translate 暴露 fallback_shrink）。
			// 若未来需要，在此加 fallback_shrink 字段并补 OpenAPI/校验/snapshot/engine_factory 映射。
			if r.Retry != nil {
				if r.Retry.MaxAttempts != nil {
					reviseCfg.Retry.MaxAttempts = *r.Retry.MaxAttempts
				}
				if r.Retry.BackoffMs != nil {
					reviseCfg.Retry.BackoffMs = *r.Retry.BackoffMs
				}
				if r.Retry.Jitter != nil {
					reviseCfg.Retry.Jitter = *r.Retry.Jitter
				}
			}
			rc.Revise = reviseCfg
		}
		if ar.Mode == ExecutionRoundConfigModeCorrect && ar.Correct != nil {
			c := ar.Correct
			correctCfg := &schema.CorrectRoundConfig{
				Concurrency: ar.Concurrency,
			}
			if len(c.Rules) > 0 {
				rules := make([]schema.CorrectRuleConfig, 0, len(c.Rules))
				for _, r := range c.Rules {
					rr := schema.CorrectRuleConfig{Name: string(r.Name)}
					rr.Enabled = true
					if r.Enabled != nil {
						rr.Enabled = *r.Enabled
					}
					rules = append(rules, rr)
				}
				correctCfg.Rules = rules
			}
			rc.Correct = correctCfg
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
		ProfileID:   req.ProfileId,
		RubyRetry:   parseRubyRetryConfig(req.RubyRetry),
		Rounds:      toExecutionPlanRoundsAPI(req.Rounds),
	}
	if req.Description != nil {
		input.Description = *req.Description
	}

	pt, err := h.executionPlans.Create(r.Context(), userID, input)
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
	if req.ProfileId != nil {
		pid := *req.ProfileId
		input.ProfileID = &pid
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
