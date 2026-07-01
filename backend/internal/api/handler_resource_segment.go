package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type resourceSegmentUpdateRequest struct {
	SourceText *string `json:"source_text"`
	TargetText *string `json:"target_text"`
	Comment    *string `json:"comment"`
}

func (s *Server) handleListResourceSegments(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	resourceID, ok := parseIntParam(w, chi.URLParam(r, "resourceId"), "resourceId")
	if !ok {
		return
	}
	pageReq, ok := parseCursorPagination(w, r, 50, 200)
	if !ok {
		return
	}

	page, err := s.segmentSvc.ListResourceSegments(r.Context(), authUser.User.ID, projectID, resourceID, service.ResourceSegmentListOptions{
		AfterID:  pageReq.AfterID,
		Limit:    pageReq.Limit,
		Status:   strings.TrimSpace(r.URL.Query().Get("status")),
		Search:   strings.TrimSpace(r.URL.Query().Get("search")),
		GroupKey: strings.TrimSpace(r.URL.Query().Get("group_key")),
	})
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}

	items := make([]segmentResponse, 0, len(page.Items))
	for _, row := range page.Items {
		items = append(items, toSegmentResponse(row))
	}
	writeJSON(w, http.StatusOK, segmentListResponse{Items: items, NextCursor: formatCursor(page.NextCursor)})
}

func (s *Server) handleUpdateResourceSegment(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	resourceID, ok := parseIntParam(w, chi.URLParam(r, "resourceId"), "resourceId")
	if !ok {
		return
	}
	segmentID, ok := parseIntParam(w, chi.URLParam(r, "segmentId"), "segmentId")
	if !ok {
		return
	}

	var req resourceSegmentUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	updated, err := s.segmentSvc.UpdateResourceSegment(r.Context(), authUser.User.ID, projectID, resourceID, segmentID, service.ResourceSegmentUpdateInput{
		SourceText: req.SourceText,
		TargetText: req.TargetText,
		Comment:    req.Comment,
	})
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}

	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "resource.segment.update", ResourceType: "segment", ResourceID: segmentID, Message: "编辑资源段落"})
	writeJSON(w, http.StatusOK, toSegmentResponse(updated))
}

func (s *Server) handleListResourceSegmentGroups(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	resourceID, ok := parseIntParam(w, chi.URLParam(r, "resourceId"), "resourceId")
	if !ok {
		return
	}

	groups, err := s.segmentSvc.ListResourceSegmentGroups(r.Context(), authUser.User.ID, projectID, resourceID)
	if err != nil {
		writeReviewServiceError(w, err)
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
