package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func (s *Server) handleQuickTranslate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	var req QuickTranslateRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	input := service.QuickTranslateInput{
		ActorUserID:     authUser.User.ID,
		SourceText:      req.SourceText,
		ExecutionPlanID: req.ExecutionPlanId,
	}
	if req.SourceLang != nil {
		input.SourceLang = *req.SourceLang
	}
	if req.TargetLang != nil {
		input.TargetLang = *req.TargetLang
	}
	if req.ProjectId != nil {
		p := *req.ProjectId
		input.ProjectID = &p
	}
	if req.Glossary != nil {
		entries := make([]service.QuickGlossaryEntryInput, 0, len(*req.Glossary))
		for _, g := range *req.Glossary {
			entry := service.QuickGlossaryEntryInput{
				Source: g.Source,
				Target: g.Target,
			}
			if g.CaseSensitive != nil {
				entry.CaseSensitive = *g.CaseSensitive
			}
			if g.Forbidden != nil {
				entry.Forbidden = *g.Forbidden
			}
			if g.Mandatory != nil {
				entry.Mandatory = *g.Mandatory
			}
			if g.Notes != nil {
				entry.Notes = *g.Notes
			}
			entries = append(entries, entry)
		}
		input.Glossary = entries
	}

	result, err := s.quickTranslateSvc.Translate(r.Context(), input)
	if err != nil {
		s.writeQuickTranslateServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toQuickTranslateResponse(result))
}

func (s *Server) writeQuickTranslateServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrQuickTranslateBusy):
		w.Header().Set("Retry-After", "1")
		s.writeProblem(w, r, http.StatusServiceUnavailable, "quick_translate_busy", "即时翻译并发已满,请稍后重试")
	case errors.Is(err, service.ErrInvalidInput):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "输入文本不能为空")
	case errors.Is(err, service.ErrQuickTranslateNoTranslate):
		s.writeProblem(w, r, http.StatusBadRequest, "no_translate_round", "执行计划不含翻译轮次")
	case errors.Is(err, service.ErrForbidden):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrBackendNotFound), errors.Is(err, service.ErrExecutionPlanNotFound), errors.Is(err, service.ErrProjectNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, context.Canceled):
		s.writeProblem(w, r, 499, "client_closed_request", "客户端已取消请求")
	case errors.Is(err, context.DeadlineExceeded):
		s.writeProblem(w, r, http.StatusGatewayTimeout, "quick_translate_timeout", "即时翻译执行超时")
	default:
		s.writeProblem(w, r, http.StatusInternalServerError, "internal", "即时翻译执行失败")
	}
}

func toQuickTranslateResponse(result *service.QuickTranslateOutput) QuickTranslateResponse {
	response := QuickTranslateResponse{
		Status:     QuickTranslateResponseStatus(result.Status),
		SourceText: result.SourceText,
		TargetText: result.TargetText,
	}
	if result.SourceLang != "" {
		response.SourceLang = stringPtr(result.SourceLang)
	}
	if result.TargetLang != "" {
		response.TargetLang = stringPtr(result.TargetLang)
	}
	if len(result.QualityIssues) > 0 {
		issues := make([]QualityIssue, 0, len(result.QualityIssues))
		for _, issue := range result.QualityIssues {
			issues = append(issues, toOpenAPIQualityIssue(issue))
		}
		response.QualityIssues = &issues
	}
	if len(result.RoundSummary) > 0 {
		rounds := make([]QuickRoundSummary, 0, len(result.RoundSummary))
		for _, rs := range result.RoundSummary {
			entry := QuickRoundSummary{
				Index:      rs.Index,
				Mode:       rs.Mode,
				DurationMs: int(rs.Duration / time.Millisecond),
				Status:     QuickRoundSummaryStatus(rs.Status),
			}
			if rs.Backend != "" {
				entry.Backend = stringPtr(rs.Backend)
			}
			rounds = append(rounds, entry)
		}
		response.RoundSummary = &rounds
	}
	if len(result.BatchEvents) > 0 {
		batches := make([]TranslationBatchDiagnostic, 0, len(result.BatchEvents))
		for _, event := range result.BatchEvents {
			batches = append(batches, toTranslationBatchDiagnostic(event))
		}
		response.Batches = &batches
	} else {
		response.Batches = &[]TranslationBatchDiagnostic{}
	}
	response.Usage = &QuickUsage{
		ApiCalls:     int(result.Usage.APICalls),
		InputTokens:  int(result.Usage.InputTokens),
		OutputTokens: int(result.Usage.OutputTokens),
	}
	if len(result.Warnings) > 0 {
		warnings := append([]string(nil), result.Warnings...)
		response.Warnings = &warnings
	}
	return response
}
