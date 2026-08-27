package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service/segmatch"
)

// parseBoolQuery 解析布尔查询参数。未提供返回 nil（调用方据此使用默认值）；
// "true"/"1" 返回指向 true，"false"/"0" 返回指向 false，其它值视为 nil。
func parseBoolQuery(r *http.Request, key string) *bool {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil
	}
	b := v == "true" || v == "1"
	if !(b || v == "false" || v == "0") {
		return nil
	}
	return &b
}

type searchReplacePreviewItem struct {
	SegmentID    int    `json:"segment_id"`
	SegmentIndex int    `json:"segment_index"`
	SourceText   string `json:"source_text"`
	Before       string `json:"before"`
	After        string `json:"after"`
	MatchCount   int    `json:"match_count"`
}

type searchReplacePreviewResponse struct {
	MatchedSegmentCount int                        `json:"matched_segment_count"`
	TotalReplacements   int                        `json:"total_replacements"`
	Items               []searchReplacePreviewItem `json:"items"`
}

type searchReplaceSkipItem struct {
	SegmentID int    `json:"segment_id"`
	Reason    string `json:"reason"`
}

type searchReplaceApplyResponse struct {
	OperationID  string                  `json:"operation_id"`
	AppliedCount int                     `json:"applied_count"`
	SkippedCount int                     `json:"skipped_count"`
	Items        []segmentResponse       `json:"items"`
	Skipped      []searchReplaceSkipItem `json:"skipped,omitempty"`
}

type searchReplaceUndoResponse struct {
	UndoOperationID string                  `json:"undo_operation_id"`
	UndoneCount     int                     `json:"undone_count"`
	SkippedCount    int                     `json:"skipped_count"`
	Items           []segmentResponse       `json:"items"`
	Skipped         []searchReplaceSkipItem `json:"skipped,omitempty"`
}

func (s *Server) writeSearchReplaceServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrRevisionNotFound),
		errors.Is(err, service.ErrSegmentNotFound),
		errors.Is(err, service.ErrResourceNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, service.ErrNoReversibleSegments):
		s.writeProblem(w, r, http.StatusConflict, "no_reversible_segments", err.Error())
	case errors.Is(err, service.ErrForbidden):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrInvalidInput), errors.Is(err, segmatch.ErrInvalidPattern):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", err.Error())
	default:
		s.writeProjectServiceError(w, r, err)
	}
}

// matchOptsFromPreview 把预览请求映射为 service 层选项。
func matchOptsFromPreview(req SearchReplacePreviewRequest) service.SearchReplaceOptions {
	var matchMode string
	if req.MatchMode != nil {
		matchMode = string(*req.MatchMode)
	}
	var status, qualityIssues, qualitySeverity, qualityCode, groupKey string
	if req.Status != nil {
		status = string(*req.Status)
	}
	if req.QualityIssues != nil {
		qualityIssues = string(*req.QualityIssues)
	}
	if req.QualitySeverity != nil {
		qualitySeverity = string(*req.QualitySeverity)
	}
	if req.QualityCode != nil {
		qualityCode = *req.QualityCode
	}
	if req.GroupKey != nil {
		groupKey = *req.GroupKey
	}
	var segmentIDs []int
	if req.SegmentIds != nil {
		segmentIDs = *req.SegmentIds
	}
	return service.SearchReplaceOptions{
		Find:            req.Find,
		ReplaceWith:     req.ReplaceWith,
		MatchMode:       matchMode,
		CaseSensitive:   req.CaseSensitive,
		WholeWord:       req.WholeWord,
		MaxResults:      req.MaxResults,
		Status:          status,
		QualityIssues:   qualityIssues,
		QualitySeverity: qualitySeverity,
		QualityCode:     qualityCode,
		GroupKey:        groupKey,
		SegmentIDs:      segmentIDs,
	}
}

func matchOptsFromApply(req SearchReplaceApplyRequest) service.SearchReplaceOptions {
	var matchMode string
	if req.MatchMode != nil {
		matchMode = string(*req.MatchMode)
	}
	var segmentIDs []int
	if req.SegmentIds != nil {
		segmentIDs = *req.SegmentIds
	}
	return service.SearchReplaceOptions{
		Find:          req.Find,
		ReplaceWith:   req.ReplaceWith,
		MatchMode:     matchMode,
		CaseSensitive: req.CaseSensitive,
		WholeWord:     req.WholeWord,
		SegmentIDs:    segmentIDs,
	}
}

// handlePreviewResourceSegmentsSearchReplace 搜索替换预览（只读 dry-run）。
func (s *Server) handlePreviewResourceSegmentsSearchReplace(w http.ResponseWriter, r *http.Request) {
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

	var req SearchReplacePreviewRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}

	result, err := s.segmentSvc.PreviewSearchReplace(r.Context(), authUser.User.ID, projectID, resourceID, matchOptsFromPreview(req))
	if err != nil {
		s.writeSearchReplaceServiceError(w, r, err)
		return
	}

	items := make([]searchReplacePreviewItem, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, searchReplacePreviewItem{
			SegmentID:    item.SegmentID,
			SegmentIndex: item.SegmentIndex,
			SourceText:   item.SourceText,
			Before:       item.Before,
			After:        item.After,
			MatchCount:   item.MatchCount,
		})
	}
	writeJSON(w, http.StatusOK, searchReplacePreviewResponse{
		MatchedSegmentCount: result.MatchedSegmentCount,
		TotalReplacements:   result.TotalReplacements,
		Items:               items,
	})
}

// handleApplyResourceSegmentsSearchReplace 应用搜索替换。
func (s *Server) handleApplyResourceSegmentsSearchReplace(w http.ResponseWriter, r *http.Request) {
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

	var req SearchReplaceApplyRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}

	opts := matchOptsFromApply(req)
	result, err := s.segmentSvc.ApplySearchReplace(r.Context(), authUser.User.ID, projectID, resourceID, opts)
	if err != nil {
		s.writeSearchReplaceServiceError(w, r, err)
		return
	}

	items := make([]segmentResponse, 0, len(result.Items))
	for _, row := range result.Items {
		items = append(items, toSegmentResponse(row))
	}
	skipped := make([]searchReplaceSkipItem, 0, len(result.Skipped))
	for _, sk := range result.Skipped {
		skipped = append(skipped, searchReplaceSkipItem{SegmentID: sk.SegmentID, Reason: sk.Reason})
	}

	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{
		ActorUserID:  authUser.User.ID,
		Action:       "segment.search_replace",
		ResourceType: "resource",
		ResourceID:   resourceID,
		Message:      "搜索替换段落译文",
		Metadata: map[string]any{
			"find":           opts.Find,
			"replace_with":   opts.ReplaceWith,
			"match_mode":     opts.MatchMode,
			"case_sensitive": opts.CaseSensitive,
			"whole_word":     opts.WholeWord,
			"operation_id":   result.OperationID,
			"applied_count":  result.AppliedCount,
			"skipped_count":  result.SkippedCount,
		},
	})
	writeJSON(w, http.StatusOK, searchReplaceApplyResponse{
		OperationID:  result.OperationID,
		AppliedCount: result.AppliedCount,
		SkippedCount: result.SkippedCount,
		Items:        items,
		Skipped:      skipped,
	})
}

// handleUndoResourceSegmentsSearchReplace 撤销搜索替换。
func (s *Server) handleUndoResourceSegmentsSearchReplace(w http.ResponseWriter, r *http.Request) {
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
	operationID := strings.TrimSpace(chi.URLParam(r, "operationId"))
	if operationID == "" {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "operationId 为空")
		return
	}

	result, err := s.segmentSvc.UndoSearchReplace(r.Context(), authUser.User.ID, projectID, resourceID, operationID)
	if err != nil {
		s.writeSearchReplaceServiceError(w, r, err)
		return
	}

	items := make([]segmentResponse, 0, len(result.Items))
	for _, row := range result.Items {
		items = append(items, toSegmentResponse(row))
	}
	skipped := make([]searchReplaceSkipItem, 0, len(result.Skipped))
	for _, sk := range result.Skipped {
		skipped = append(skipped, searchReplaceSkipItem{SegmentID: sk.SegmentID, Reason: sk.Reason})
	}

	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{
		ActorUserID:  authUser.User.ID,
		Action:       "segment.search_replace_undo",
		ResourceType: "resource",
		ResourceID:   resourceID,
		Message:      "撤销搜索替换",
		Metadata: map[string]any{
			"operation_id":      operationID,
			"undo_operation_id": result.UndoOperationID,
			"undone_count":      result.UndoneCount,
			"skipped_count":     result.SkippedCount,
		},
	})
	writeJSON(w, http.StatusOK, searchReplaceUndoResponse{
		UndoOperationID: result.UndoOperationID,
		UndoneCount:     result.UndoneCount,
		SkippedCount:    result.SkippedCount,
		Items:           items,
		Skipped:         skipped,
	})
}
