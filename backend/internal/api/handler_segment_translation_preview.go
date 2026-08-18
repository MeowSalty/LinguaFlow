package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/previewtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func (s *Server) handlePreviewResourceSegmentTranslation(w http.ResponseWriter, r *http.Request) {
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

	var req SegmentTranslationPreviewRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	input := service.PreviewInput{
		ActorUserID:     authUser.User.ID,
		ProjectID:       projectID,
		ResourceID:      resourceID,
		SegmentID:       segmentID,
		ExecutionPlanID: req.ExecutionPlanId,
	}
	if req.SourceText != nil {
		input.SourceTextSet = true
		input.SourceText = *req.SourceText
	}

	result, err := s.previewSvc.RunPreview(r.Context(), input)
	if err != nil {
		s.writePreviewServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSegmentTranslationPreviewResponse(result))
}

func (s *Server) handleApplyResourceSegmentTranslationPreview(w http.ResponseWriter, r *http.Request) {
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

	var req ApplySegmentTranslationPreviewRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	row, err := s.previewSvc.ApplyPreview(
		r.Context(), authUser.User.ID, projectID, resourceID, segmentID,
		req.ApplyToken, req.TargetText,
	)
	if err != nil {
		s.writePreviewServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOpenAPISegment(row))
}

func (s *Server) writePreviewServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrPreviewBusy):
		w.Header().Set("Retry-After", "1")
		s.writeProblem(w, r, http.StatusTooManyRequests, "preview_busy", "预览并发已满，请稍后重试")
	case errors.Is(err, service.ErrPreviewConflict):
		s.writeProblem(w, r, http.StatusConflict, "preview_conflict", "段落基线已变化，请重新预览")
	case errors.Is(err, service.ErrPreviewTokenExpired):
		s.writeProblem(w, r, http.StatusGone, "preview_token_expired", "预览应用令牌已过期")
	case errors.Is(err, service.ErrPreviewTokenInvalid),
		errors.Is(err, previewtoken.ErrTokenInvalid),
		errors.Is(err, previewtoken.ErrTokenIssuer),
		errors.Is(err, previewtoken.ErrTokenType):
		s.writeProblem(w, r, http.StatusGone, "preview_token_invalid", "预览应用令牌无效")
	case errors.Is(err, service.ErrPreviewTargetBlank), errors.Is(err, service.ErrPreviewNoTranslate), errors.Is(err, service.ErrInvalidInput):
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

func toSegmentTranslationPreviewResponse(result *service.PreviewOutput) SegmentTranslationPreviewResponse {
	response := SegmentTranslationPreviewResponse{
		Status:     SegmentTranslationPreviewResponseStatus(result.Status),
		SegmentId:  result.SegmentID,
		SourceText: result.SourceText,
		Batches:    make([]TranslationBatchDiagnostic, 0, len(result.BatchEvents)),
	}
	if result.TargetText != "" {
		value := result.TargetText
		response.TargetText = &value
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
		response.Execution.Rounds = make([]struct {
			BackendName        *string                                              `json:"backend_name,omitempty"`
			Index              int                                                  `json:"index"`
			Mode               SegmentTranslationPreviewResponseExecutionRoundsMode `json:"mode"`
			ProfileName        *string                                              `json:"profile_name,omitempty"`
			PromptTemplateName *string                                              `json:"prompt_template_name,omitempty"`
		}, 0, len(result.Snapshot.Rounds))
		for index, snapshotRound := range result.Snapshot.Rounds {
			mode := SegmentTranslationPreviewResponseExecutionRoundsMode(snapshotRound.Mode)
			backendName := snapshotRound.Backend.Name
			var profileName, promptName *string
			if snapshotRound.Translate != nil {
				profileName = stringPtr(snapshotRound.Translate.Strategy.ProfileName)
				promptName = stringPtr(snapshotRound.Translate.Prompt.TemplateName)
			}
			response.Execution.Rounds = append(response.Execution.Rounds, struct {
				BackendName        *string                                              `json:"backend_name,omitempty"`
				Index              int                                                  `json:"index"`
				Mode               SegmentTranslationPreviewResponseExecutionRoundsMode `json:"mode"`
				ProfileName        *string                                              `json:"profile_name,omitempty"`
				PromptTemplateName *string                                              `json:"prompt_template_name,omitempty"`
			}{BackendName: stringPtr(backendName), Index: index, Mode: mode, ProfileName: profileName, PromptTemplateName: promptName})
		}
	}
	response.Usage.ApiCalls = int(result.Usage.APICalls)
	response.Usage.InputTokens = int(result.Usage.InputTokens)
	response.Usage.OutputTokens = int(result.Usage.OutputTokens)
	for _, event := range result.BatchEvents {
		response.Batches = append(response.Batches, toTranslationBatchDiagnostic(event))
	}
	return response
}

func toTranslationBatchDiagnostic(event progress.BatchEvent) TranslationBatchDiagnostic {
	diagnostic := TranslationBatchDiagnostic{
		Stage:           event.Stage,
		Status:          TranslationBatchDiagnosticStatus(event.Status),
		RoundIndex:      intPtr(event.RoundIndex),
		Attempt:         intPtr(event.Attempt),
		BackendName:     stringPtr(event.BackendName),
		DurationMs:      intPtr64(event.DurationMs),
		InputTokens:     intPtr64(event.InputTokens),
		OutputTokens:    intPtr64(event.OutputTokens),
		SegmentCount:    intPtr(event.SegmentCount),
		ErrorType:       stringPtr(event.ErrorType),
		ErrorMessage:    stringPtr(event.ErrorMessage),
		HttpStatus:      intPtr(event.HTTPStatus),
		ShrinkAttempted: boolPtr(event.ShrinkAttempted),
	}
	if len(event.SegmentIDs) > 0 {
		ids := append([]string(nil), event.SegmentIDs...)
		diagnostic.SegmentIds = &ids
	}
	if len(event.TriedBackends) > 0 {
		tried := append([]string(nil), event.TriedBackends...)
		diagnostic.TriedBackends = &tried
	}
	if len(event.UsedGlossary) > 0 {
		entries := make([]GlossaryEntry, 0, len(event.UsedGlossary))
		for _, entry := range event.UsedGlossary {
			entries = append(entries, GlossaryEntry{Source: entry.Source, Target: entry.Target, Notes: stringPtr(entry.Notes)})
		}
		diagnostic.UsedGlossary = &entries
	}
	if len(event.AddedGlossary) > 0 {
		entries := make([]GlossaryEntry, 0, len(event.AddedGlossary))
		for _, entry := range event.AddedGlossary {
			entries = append(entries, GlossaryEntry{Source: entry.Source, Target: entry.Target, Notes: stringPtr(entry.Notes)})
		}
		diagnostic.AddedGlossary = &entries
	}
	system, systemTruncated, systemLength := progress.TruncateSSEContent(event.SystemPrompt)
	user, userTruncated, userLength := progress.TruncateSSEContent(firstNonEmptyString(event.UserMessage, event.SentContent))
	content, contentTruncated, contentLength := progress.TruncateSSEContent(firstNonEmptyString(event.ResponseContent, event.ReceivedContent))
	var jsonSchema *map[string]interface{}
	if event.JSONSchema != nil {
		converted := map[string]interface{}(event.JSONSchema)
		jsonSchema = &converted
	}
	diagnostic.Request = &struct {
		JsonSchema            *map[string]interface{} `json:"json_schema,omitempty"`
		ResponseFormat        *string                 `json:"response_format,omitempty"`
		SystemPrompt          *string                 `json:"system_prompt,omitempty"`
		SystemPromptLength    *int                    `json:"system_prompt_length,omitempty"`
		SystemPromptTruncated *bool                   `json:"system_prompt_truncated,omitempty"`
		UserMessage           *string                 `json:"user_message,omitempty"`
		UserMessageLength     *int                    `json:"user_message_length,omitempty"`
		UserMessageTruncated  *bool                   `json:"user_message_truncated,omitempty"`
	}{JsonSchema: jsonSchema, ResponseFormat: stringPtr(event.ResponseFormat), SystemPrompt: stringPtr(system), SystemPromptLength: intPtr(systemLength), SystemPromptTruncated: boolPtr(systemTruncated), UserMessage: stringPtr(user), UserMessageLength: intPtr(userLength), UserMessageTruncated: boolPtr(userTruncated)}
	diagnostic.Response = &struct {
		Content          *string `json:"content,omitempty"`
		ContentLength    *int    `json:"content_length,omitempty"`
		ContentTruncated *bool   `json:"content_truncated,omitempty"`
	}{Content: stringPtr(content), ContentLength: intPtr(contentLength), ContentTruncated: boolPtr(contentTruncated)}
	return diagnostic
}

func toOpenAPIQualityIssue(issue qa.QualityIssue) QualityIssue {
	result := QualityIssue{SegmentIndex: issue.SegmentIndex, Code: issue.Code, Message: issue.Message, Severity: QualityIssueSeverity(issue.Severity)}
	if issue.Span != nil {
		result.Span = &QualityIssueSpan{MatchedText: issue.Span.MatchedText, TargetStart: issue.Span.TargetStart, TargetEnd: issue.Span.TargetEnd}
	}
	if !issue.IsPending() {
		d := QualityIssueDisposition(issue.Disposition)
		result.Disposition = &d
	}
	result.DecidedBy = issue.DecidedBy
	result.DecidedAt = issue.DecidedAt
	result.Note = stringPtr(issue.Note)
	return result
}

func toOpenAPISegment(row *ent.Segment) Segment {
	result := Segment{Id: row.ID, SegmentIndex: row.SegmentIndex, SourceText: row.SourceText, Status: SegmentStatus(row.Status), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.TargetText != nil {
		value := *row.TargetText
		result.TargetText = &value
	}
	if len(row.QualityIssues) > 0 {
		issues := make([]QualityIssue, 0, len(row.QualityIssues))
		for _, issue := range row.QualityIssues {
			issues = append(issues, toOpenAPIQualityIssue(issue))
		}
		result.QualityIssues = &issues
	}
	return result
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func intPtr64(value int64) *int {
	converted := int(value)
	return &converted
}
