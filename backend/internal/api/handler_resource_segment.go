package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service/segmatch"
)

type resourceSegmentUpdateRequest struct {
	SourceText *string `json:"source_text"`
	TargetText *string `json:"target_text"`
	Comment    *string `json:"comment"`
}

func (s *Server) handleListResourceSegments(w http.ResponseWriter, r *http.Request) {
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
	query := r.URL.Query()
	pageReq, ok := s.parseCursorPagination(w, r, 50, 200)
	if !ok {
		return
	}

	direction := strings.TrimSpace(query.Get("direction"))
	if direction == "" {
		direction = "asc"
	}
	if direction != "asc" && direction != "desc" {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "direction 只支持 asc 或 desc")
		return
	}

	matchMode := strings.TrimSpace(query.Get("match_mode"))
	if matchMode != "" && matchMode != "substring" && matchMode != "regex" {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "match_mode 只支持 substring 或 regex")
		return
	}

	_, cursorProvided := query["cursor"]

	var anchorSegmentID *int
	if anchorValues, present := query["anchor_segment_id"]; present {
		if len(anchorValues) != 1 {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "anchor_segment_id 只能出现一次")
			return
		}
		anchorID, err := strconv.Atoi(strings.TrimSpace(anchorValues[0]))
		if err != nil || anchorID <= 0 {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "anchor_segment_id 必须是有效正整数")
			return
		}
		anchorSegmentID = &anchorID
		if cursorProvided {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "anchor_segment_id 不能与 cursor 同时使用")
			return
		}
		if direction == "desc" {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "anchor_segment_id 不能与 direction=desc 同时使用")
			return
		}
	}

	page, err := s.segmentSvc.ListResourceSegments(r.Context(), authUser.User.ID, projectID, resourceID, service.ResourceSegmentListOptions{
		AfterID:         pageReq.AfterID,
		HasCursor:       cursorProvided,
		AnchorSegmentID: anchorSegmentID,
		Direction:       direction,
		Limit:           pageReq.Limit,
		Status:          strings.TrimSpace(query.Get("status")),
		// search 保留原值：前后空白对 substring/regex 都有语义（" foo" ≠ "foo"），
		// 仅空字符串表示未搜索；其余参数仍统一 trim。
		Search:          query.Get("search"),
		SearchField:     strings.TrimSpace(query.Get("search_field")),
		MatchMode:       matchMode,
		CaseSensitive:   parseBoolQuery(r, "case_sensitive"),
		WholeWord:       parseBoolQuery(r, "whole_word"),
		IncludeTotal:    query.Get("include_total") == "true",
		GroupKey:        strings.TrimSpace(query.Get("group_key")),
		QualityIssues:   strings.TrimSpace(query.Get("quality_issues")),
		QualitySeverity: strings.TrimSpace(query.Get("quality_severity")),
		QualityCode:     strings.TrimSpace(query.Get("quality_code")),
	})
	if err != nil {
		if errors.Is(err, segmatch.ErrInvalidPattern) || errors.Is(err, segmatch.ErrUnsupportedMatchMode) {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", err.Error())
			return
		}
		if errors.Is(err, service.ErrSegmentGroupMismatch) {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "锚点段落不属于指定的 group_key 章节")
			return
		}
		if errors.Is(err, service.ErrDuplicateSegmentIndex) {
			s.writeProblem(w, r, http.StatusConflict, "duplicate_segment_index",
				"资源段落索引损坏：segment_index 存在重复，分页游标无法跨越该边界表达遍历位置，其后的数据不可达。请重新导入或修复该资源的段落后再试")
			return
		}
		s.writeReviewServiceError(w, r, err)
		return
	}

	items := make([]segmentResponse, 0, len(page.Items))
	for _, row := range page.Items {
		items = append(items, toSegmentResponse(row))
	}
	writeJSON(w, http.StatusOK, segmentListResponse{
		Items:      items,
		NextCursor: formatOptionalCursor(page.NextCursor, page.HasNextCursor),
		PrevCursor: formatOptionalCursor(page.PrevCursor, page.HasPrevCursor),
		Total:      page.Total,
	})
}

func formatOptionalCursor(cursor int, present bool) string {
	if !present {
		return ""
	}
	return strconv.Itoa(cursor)
}

func (s *Server) handleUpdateResourceSegment(w http.ResponseWriter, r *http.Request) {
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

	var req resourceSegmentUpdateRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}

	updated, err := s.segmentSvc.UpdateResourceSegment(r.Context(), authUser.User.ID, projectID, resourceID, segmentID, service.ResourceSegmentUpdateInput{
		SourceText: req.SourceText,
		TargetText: req.TargetText,
		Comment:    req.Comment,
	})
	if err != nil {
		s.writeReviewServiceError(w, r, err)
		return
	}

	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "resource.segment.update", ResourceType: "segment", ResourceID: segmentID, Message: "编辑资源段落"})
	writeJSON(w, http.StatusOK, toSegmentResponse(updated))
}

func (s *Server) handleListResourceSegmentGroups(w http.ResponseWriter, r *http.Request) {
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

	groups, err := s.segmentSvc.ListResourceSegmentGroups(r.Context(), authUser.User.ID, projectID, resourceID)
	if err != nil {
		s.writeReviewServiceError(w, r, err)
		return
	}

	items := make([]ResourceSegmentGroup, 0, len(groups))
	for _, g := range groups {
		items = append(items, ResourceSegmentGroup{
			GroupKey:        g.GroupKey,
			GroupTitle:      g.GroupTitle,
			SegmentCount:    g.SegmentCount,
			TranslatedCount: g.TranslatedCount,
			ApprovedCount:   g.ApprovedCount,
		})
	}
	writeJSON(w, http.StatusOK, ResourceSegmentGroupListResponse{Items: items})
}
