package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type createGlossaryEntryRequest struct {
	Source        string `json:"source"`
	Target        string `json:"target"`
	CaseSensitive bool   `json:"case_sensitive"`
	Notes         string `json:"notes"`
}

type updateGlossaryEntryRequest struct {
	Source        string `json:"source"`
	Target        string `json:"target"`
	CaseSensitive bool   `json:"case_sensitive"`
	Notes         string `json:"notes"`
}

type glossaryEntryResponse struct {
	ID            int    `json:"id"`
	Source        string `json:"source"`
	Target        string `json:"target"`
	CaseSensitive bool   `json:"case_sensitive"`
	Notes         string `json:"notes,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type updateGlossaryEntryResponse struct {
	glossaryEntryResponse
	TargetChanged bool `json:"target_changed"`
}

func toGlossaryEntryResponse(e *ent.GlossaryEntry) glossaryEntryResponse {
	return glossaryEntryResponse{
		ID:            e.ID,
		Source:        e.Source,
		Target:        e.Target,
		CaseSensitive: e.CaseSensitive,
		Notes:         e.Notes,
		CreatedAt:     e.CreatedAt.Format(timeRFC3339),
		UpdatedAt:     e.UpdatedAt.Format(timeRFC3339),
	}
}

func toGlossaryListResponse(entries []*ent.GlossaryEntry) map[string]any {
	items := make([]glossaryEntryResponse, 0, len(entries))
	for _, e := range entries {
		items = append(items, toGlossaryEntryResponse(e))
	}
	return map[string]any{"items": items}
}

func (s *Server) handleListGlossaryEntries(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := s.parseIntParam(w, r, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	entries, err := s.glossarySvc.ListEntries(r.Context(), authUser.User.ID, projectID)
	if err != nil {
		s.writeGlossaryServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toGlossaryListResponse(entries))
}

func (s *Server) handleCreateGlossaryEntry(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := s.parseIntParam(w, r, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	var req createGlossaryEntryRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	entry, err := s.glossarySvc.CreateEntry(r.Context(), authUser.User.ID, projectID, service.GlossaryEntryInput{
		Source:        req.Source,
		Target:        req.Target,
		CaseSensitive: req.CaseSensitive,
		Notes:         req.Notes,
	})
	if err != nil {
		s.writeGlossaryServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toGlossaryEntryResponse(entry))
}

func (s *Server) handleUpdateGlossaryEntry(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := s.parseIntParam(w, r, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	entryID, ok := s.parseIntParam(w, r, chi.URLParam(r, "entryId"), "entryId")
	if !ok {
		return
	}
	var req updateGlossaryEntryRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	result, err := s.glossarySvc.UpdateEntry(r.Context(), authUser.User.ID, projectID, entryID, service.GlossaryEntryInput{
		Source:        req.Source,
		Target:        req.Target,
		CaseSensitive: req.CaseSensitive,
		Notes:         req.Notes,
	})
	if err != nil {
		s.writeGlossaryServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updateGlossaryEntryResponse{
		glossaryEntryResponse: toGlossaryEntryResponse(result.Entry),
		TargetChanged:         result.TargetChanged,
	})
}

func (s *Server) handleDeleteGlossaryEntry(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := s.parseIntParam(w, r, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	entryID, ok := s.parseIntParam(w, r, chi.URLParam(r, "entryId"), "entryId")
	if !ok {
		return
	}
	if err := s.glossarySvc.DeleteEntry(r.Context(), authUser.User.ID, projectID, entryID); err != nil {
		s.writeGlossaryServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleImportGlossaryCSV(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := s.parseIntParam(w, r, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_multipart", "上传表单解析失败")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "缺少文件")
		return
	}
	defer file.Close()
	result, err := s.glossarySvc.ImportCSV(r.Context(), authUser.User.ID, projectID, file)
	if err != nil {
		s.writeGlossaryServiceError(w, r, err)
		return
	}
	type skippedItem struct {
		Line   int    `json:"line"`
		Source string `json:"source,omitempty"`
		Reason string `json:"reason"`
	}
	resp := map[string]any{
		"added": result.Added,
	}
	if len(result.Skipped) > 0 {
		skipped := make([]skippedItem, 0, len(result.Skipped))
		for _, s := range result.Skipped {
			skipped = append(skipped, skippedItem{Line: s.Line, Source: s.Source, Reason: s.Reason})
		}
		resp["skipped"] = skipped
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleExportGlossaryCSV(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := s.parseIntParam(w, r, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"glossary.csv\"")
	if err := s.glossarySvc.ExportCSV(r.Context(), authUser.User.ID, projectID, w); err != nil {
		// Headers already sent, log error only
		s.logger.Error("glossary export failed", "err", err)
	}
}

func (s *Server) writeGlossaryServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrProjectNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "项目不存在")
	case errors.Is(err, service.ErrGlossaryEntryNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "术语条目不存在")
	case errors.Is(err, service.ErrGlossaryEntryExists):
		s.writeProblem(w, r, http.StatusConflict, "conflict", "术语条目已存在")
	case errors.Is(err, service.ErrInvalidInput):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求参数不合法")
	default:
		s.writeServiceError(w, r, err)
	}
}
