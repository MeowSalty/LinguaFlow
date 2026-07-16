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
			Action:    GlossaryPruneSuggestionAction(s.Action),
			EntryId:   s.EntryID,
			Source:    s.Source,
			OldTarget: s.OldTarget,
			OldNotes:  s.OldNotes,
		}
		if s.NewTarget != "" {
			sug.NewTarget = &s.NewTarget
		}
		if s.NewNotes != "" {
			sug.NewNotes = &s.NewNotes
		}
		suggestions = append(suggestions, sug)
	}
	return GlossaryPrunePreview{
		Suggestions: suggestions,
		Total:       p.Total,
		ToDelete:    p.ToDelete,
		ToUpdate:    p.ToUpdate,
		ToKeep:      p.ToKeep,
	}
}

// writeGlossaryPruneServiceError 将 GlossaryPruneService 错误映射为 HTTP 状态码。
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
	case errors.Is(err, service.ErrPruneLLMCallFailed):
		s.writeProblem(w, r, http.StatusBadGateway, "llm_error", "LLM 调用失败",
			slog.String("error", err.Error()))
	case errors.Is(err, service.ErrPruneParseFailed):
		s.writeProblem(w, r, http.StatusBadGateway, "parse_error", "LLM 响应解析失败",
			slog.String("error", err.Error()))
	default:
		s.writeProblem(w, r, http.StatusInternalServerError, "internal_error", "服务器内部错误",
			slog.String("error", err.Error()))
	}
}
