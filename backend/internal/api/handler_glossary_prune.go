package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func (s *Server) handlePreviewGlossaryPrune(w http.ResponseWriter, r *http.Request, projectId int) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	var req GlossaryPruneRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}

	templateID := 0
	if req.TemplateId != nil {
		templateID = *req.TemplateId
	}

	preview, err := s.glossaryPruneSvc.Preview(r.Context(), authUser.User.ID, projectId, req.BackendId, templateID)
	if err != nil {
		// LLM 调用失败 / 解析失败 → 200 + 空建议 + diagnostics
		var pruneErr *service.PruneError
		if errors.As(err, &pruneErr) {
			s.logger.Warn("prune preview completed with error",
				slog.String("error_type", pruneErr.Diagnostics.ErrorType),
				slog.String("error_message", pruneErr.Diagnostics.ErrorMessage))
			writeJSON(w, http.StatusOK, GlossaryPrunePreview{
				Suggestions: []GlossaryPruneSuggestion{},
				Total:       0,
				ToDelete:    0,
				ToUpdate:    0,
				ToKeep:      0,
				Diagnostics: convertPruneDiagnostics(pruneErr.Diagnostics),
			})
			return
		}
		// 其他错误（认证、权限、项目不存在等）→ 标准 HTTP 错误
		s.writeGlossaryPruneServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, convertPrunePreview(preview))
}

func (s *Server) handleApplyGlossaryPrune(w http.ResponseWriter, r *http.Request, projectId int) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	var req GlossaryPruneApplyRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}

	changes := make([]service.PruneChange, 0, len(req.Changes))
	for _, c := range req.Changes {
		ch := service.PruneChange{
			EntryID: c.EntryId,
			Action:  string(c.Action),
		}
		if c.Target != nil {
			ch.Target = *c.Target
		}
		if c.Notes != nil {
			ch.Notes = *c.Notes
		}
		changes = append(changes, ch)
	}

	result, err := s.glossaryPruneSvc.Apply(r.Context(), authUser.User.ID, projectId, changes)
	if err != nil {
		s.writeGlossaryPruneServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, GlossaryPruneApplyResult{
		Deleted: result.Deleted,
		Updated: result.Updated,
		Failed:  result.Failed,
	})
}

// convertPrunePreview 将 service.PrunePreview 转换为 OpenAPI 生成的响应类型。
func convertPrunePreview(p *service.PrunePreview) GlossaryPrunePreview {
	suggestions := make([]GlossaryPruneSuggestion, 0, len(p.Suggestions))
	for _, s := range p.Suggestions {
		sug := GlossaryPruneSuggestion{
			Action:        GlossaryPruneSuggestionAction(s.Action),
			EntryId:       s.EntryID,
			Source:        s.Source,
			OldTarget:     s.OldTarget,
			OldNotes:      s.OldNotes,
			NewTarget:     s.NewTarget,
			NewNotes:      s.NewNotes,
			TargetChanged: s.TargetChanged,
			NotesChanged:  s.NotesChanged,
		}
		suggestions = append(suggestions, sug)
	}
	return GlossaryPrunePreview{
		Suggestions: suggestions,
		Total:       p.Total,
		ToDelete:    p.ToDelete,
		ToUpdate:    p.ToUpdate,
		ToKeep:      p.ToKeep,
		Diagnostics: convertPruneDiagnostics(p.Diagnostics),
	}
}

// convertPruneDiagnostics 将 service.PruneDiagnostics 转换为 OpenAPI 生成的响应类型。
func convertPruneDiagnostics(d service.PruneDiagnostics) GlossaryPruneDiagnostics {
	diag := GlossaryPruneDiagnostics{}
	if d.BackendName != "" {
		diag.BackendName = &d.BackendName
	}
	if d.TemplateName != "" {
		diag.TemplateName = &d.TemplateName
	}
	if d.DurationMs > 0 {
		diag.DurationMs = &d.DurationMs
	}
	if d.PromptTokens > 0 {
		diag.PromptTokens = &d.PromptTokens
	}
	if d.CompletionTokens > 0 {
		diag.CompletionTokens = &d.CompletionTokens
	}
	if d.SystemPrompt != "" {
		diag.SystemPrompt = &d.SystemPrompt
	}
	if d.UserMessage != "" {
		diag.UserMessage = &d.UserMessage
	}
	if d.SystemTruncated {
		diag.SystemTruncated = &d.SystemTruncated
	}
	if d.UserTruncated {
		diag.UserTruncated = &d.UserTruncated
	}
	if d.SystemLength > 0 {
		diag.SystemLength = &d.SystemLength
	}
	if d.UserLength > 0 {
		diag.UserLength = &d.UserLength
	}
	if d.ReceivedContent != "" {
		diag.ReceivedContent = &d.ReceivedContent
	}
	if d.RecvTruncated {
		diag.ReceivedTruncated = &d.RecvTruncated
	}
	if d.RecvLength > 0 {
		diag.ReceivedLength = &d.RecvLength
	}
	if d.EntryCount > 0 {
		diag.EntryCount = &d.EntryCount
	}
	if d.ParsedCount > 0 {
		diag.ParsedCount = &d.ParsedCount
	}
	if len(d.RepairedOps) > 0 {
		diag.RepairedOps = &d.RepairedOps
	}
	if d.ErrorType != "" {
		diag.ErrorType = &d.ErrorType
	}
	if d.ErrorMessage != "" {
		diag.ErrorMessage = &d.ErrorMessage
	}
	if d.HTTPStatus > 0 {
		diag.HttpStatus = &d.HTTPStatus
	}
	return diag
}

// writeGlossaryPruneServiceError 将 GlossaryPruneService 非 LLM 错误映射为 HTTP 状态码。
// LLM 调用失败 / 解析失败已在 handlePreviewGlossaryPrune 中以 200 + diagnostics 返回。
func (s *Server) writeGlossaryPruneServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrProjectNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "项目不存在")
	case errors.Is(err, service.ErrBackendNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "后端不存在")
	case errors.Is(err, service.ErrPrunePromptTemplateNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "术语精简提示词模板不存在")
	case errors.Is(err, service.ErrGlossaryEntryNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "术语条目不存在")
	case errors.Is(err, service.ErrInvalidInput):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求参数不合法")
	case errors.Is(err, context.Canceled):
		s.writeProblem(w, r, 499, "client_closed", "客户端已断开连接")
	case errors.Is(err, context.DeadlineExceeded):
		s.writeProblem(w, r, http.StatusGatewayTimeout, "gateway_timeout", "请求超时")
	default:
		s.writeProblem(w, r, http.StatusInternalServerError, "internal_error", "服务器内部错误",
			slog.String("error", err.Error()))
	}
}
