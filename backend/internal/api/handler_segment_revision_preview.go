package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// handlePreviewResourceSegmentRevision 单段修订预览。
func (s *Server) handlePreviewResourceSegmentRevision(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := s.parseIntParam(w, r, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	resourceID, ok := s.parseIntParam(w, r, chi.URLParam(r, "resourceId"), "resourceId")
	if !ok {
		return
	}
	segmentID, ok := s.parseIntParam(w, r, chi.URLParam(r, "segmentId"), "segmentId")
	if !ok {
		return
	}

	var req SegmentRevisionPreviewRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	input := service.RevisionPreviewInput{
		ActorUserID:     authUser.User.ID,
		ProjectID:       projectID,
		ResourceID:      resourceID,
		SegmentID:       segmentID,
		ExecutionPlanID: req.ExecutionPlanId,
	}
	if req.IssueCodes != nil {
		input.IssueCodes = make([]string, 0, len(*req.IssueCodes))
		for _, code := range *req.IssueCodes {
			value := strings.TrimSpace(string(code))
			if value != "" {
				input.IssueCodes = append(input.IssueCodes, value)
			}
		}
	}

	if s.revisionPreviewSvc == nil {
		s.writeProblem(w, r, http.StatusInternalServerError, "service_unavailable", "修订预览服务不可用")
		return
	}
	result, err := s.revisionPreviewSvc.RunRevisionPreview(r.Context(), input)
	if err != nil {
		s.writeRevisionPreviewServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSegmentRevisionPreviewResponse(result))
}

func (s *Server) writeRevisionPreviewServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrRevisionPreviewBusy):
		w.Header().Set("Retry-After", "1")
		s.writeProblem(w, r, http.StatusTooManyRequests, "preview_busy", "预览并发已满，请稍后重试")
	case errors.Is(err, service.ErrRevisionNoTarget), errors.Is(err, service.ErrRevisionNoIssues):
		s.writeProblem(w, r, http.StatusConflict, "revision_preview_conflict", err.Error())
	case errors.Is(err, service.ErrRevisionInvalidIssueCodes), errors.Is(err, service.ErrRevisionNoBackend):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, service.ErrForbidden):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrProjectNotFound), errors.Is(err, service.ErrResourceNotFound), errors.Is(err, service.ErrSegmentNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, context.Canceled):
		s.writeProblem(w, r, 499, "client_closed_request", "客户端已取消请求")
	case errors.Is(err, context.DeadlineExceeded):
		s.writeProblem(w, r, http.StatusGatewayTimeout, "preview_timeout", "预览执行超时")
	default:
		s.writeProjectServiceError(w, r, err)
	}
}

func toSegmentRevisionPreviewResponse(result *service.RevisionPreviewOutput) SegmentRevisionPreviewResponse {
	response := SegmentRevisionPreviewResponse{
		Status:             SegmentRevisionPreviewResponseStatus(result.Status),
		SegmentId:          result.SegmentID,
		SourceText:         result.SourceText,
		OriginalTargetText: result.OriginalTargetText,
		Batches:            make([]TranslationBatchDiagnostic, 0, len(result.BatchEvents)),
	}
	if result.TargetText != "" {
		value := result.TargetText
		response.TargetText = &value
	}
	if len(result.FixIssues) > 0 {
		issues := make([]QualityIssue, 0, len(result.FixIssues))
		for _, issue := range result.FixIssues {
			issues = append(issues, toOpenAPIQualityIssue(issue))
		}
		response.FixIssues = &issues
	}
	if len(result.QualityIssues) > 0 {
		issues := make([]QualityIssue, 0, len(result.QualityIssues))
		for _, issue := range result.QualityIssues {
			issues = append(issues, toOpenAPIQualityIssue(issue))
		}
		response.QualityIssues = &issues
	}
	if len(result.Warnings) > 0 {
		warnings := append([]string(nil), result.Warnings...)
		response.Warnings = &warnings
	}
	if result.ApplyToken != "" {
		response.ApplyToken = &result.ApplyToken
		expires := result.ApplyExpiresAt
		response.ApplyExpiresAt = &expires
	}
	if result.Snapshot != nil {
		response.Execution.ExecutionPlanId = result.Snapshot.ExecutionPlanID
		response.Execution.ExecutionPlanName = result.Snapshot.ExecutionPlanName
	}
	response.Execution.Rounds = make([]struct {
		BackendName *string                                           `json:"backend_name,omitempty"`
		Index       int                                               `json:"index"`
		Mode        SegmentRevisionPreviewResponseExecutionRoundsMode `json:"mode"`
		Synthesized *bool                                             `json:"synthesized,omitempty"`
	}, 0, len(result.RoundSummary))
	for _, round := range result.RoundSummary {
		synthesized := round.Synthesized
		response.Execution.Rounds = append(response.Execution.Rounds, struct {
			BackendName *string                                           `json:"backend_name,omitempty"`
			Index       int                                               `json:"index"`
			Mode        SegmentRevisionPreviewResponseExecutionRoundsMode `json:"mode"`
			Synthesized *bool                                             `json:"synthesized,omitempty"`
		}{
			BackendName: stringPtr(round.Backend),
			Index:       round.Index,
			Mode:        SegmentRevisionPreviewResponseExecutionRoundsMode(round.Mode),
			Synthesized: &synthesized,
		})
	}
	response.Usage.ApiCalls = int(result.Usage.APICalls)
	response.Usage.InputTokens = int(result.Usage.InputTokens)
	response.Usage.OutputTokens = int(result.Usage.OutputTokens)
	for _, event := range result.BatchEvents {
		response.Batches = append(response.Batches, toTranslationBatchDiagnostic(event))
	}
	return response
}
