package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type segmentResponse struct {
	ID            int           `json:"id"`
	SubJobID      int           `json:"sub_job_id,omitempty"`
	ResourceID    int           `json:"resource_id,omitempty"`
	SegmentIndex  int           `json:"segment_index"`
	SourceText    string        `json:"source_text"`
	TargetText    string        `json:"target_text,omitempty"`
	Status        string        `json:"status"`
	ReviewComment *string       `json:"review_comment,omitempty"`
	ReviewedBy    *userResponse `json:"reviewed_by,omitempty"`
	CreatedAt     string        `json:"created_at"`
	UpdatedAt     string        `json:"updated_at"`
}

type segmentListResponse struct {
	Items      []segmentResponse `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type segmentEditRequest struct {
	TargetText string `json:"target_text"`
	Comment    string `json:"comment"`
}

type segmentDecisionRequest struct {
	Comment string `json:"comment"`
}

func (s *Server) handleListJobSegments(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	jobID, ok := parseIntParam(w, chi.URLParam(r, "jobId"), "jobId")
	if !ok {
		return
	}
	pageReq, ok := parseCursorPagination(w, r, 50, 100)
	if !ok {
		return
	}
	page, err := s.reviewSvc.ListJobSegments(r.Context(), authUser.User.ID, jobID, pageReq.AfterID, pageReq.Limit)
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

func (s *Server) handleEditSegment(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	segmentID, ok := parseIntParam(w, chi.URLParam(r, "segmentId"), "segmentId")
	if !ok {
		return
	}
	var req segmentEditRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	updated, err := s.reviewSvc.EditSegment(r.Context(), authUser.User.ID, segmentID, service.SegmentEditInput{
		TargetText: req.TargetText,
		Comment:    req.Comment,
	})
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "segment.edit", ResourceType: "segment", ResourceID: segmentID, Message: "编辑段译文"})
	writeJSON(w, http.StatusOK, toSegmentResponse(updated))
}

func (s *Server) handleApproveSegment(w http.ResponseWriter, r *http.Request) {
	s.handleSegmentDecision(w, r, "approve")
}

func (s *Server) handleRejectSegment(w http.ResponseWriter, r *http.Request) {
	s.handleSegmentDecision(w, r, "reject")
}

func (s *Server) handleSegmentDecision(w http.ResponseWriter, r *http.Request, decision string) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	segmentID, ok := parseIntParam(w, chi.URLParam(r, "segmentId"), "segmentId")
	if !ok {
		return
	}
	var req segmentDecisionRequest
	if r.Body != nil && strings.TrimSpace(r.Header.Get("Content-Length")) != "0" {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	var (
		updated *ent.Segment
		err     error
		action  string
		message string
	)
	input := service.SegmentDecisionInput{Comment: req.Comment}
	if decision == "approve" {
		updated, err = s.reviewSvc.ApproveSegment(r.Context(), authUser.User.ID, segmentID, input)
		action = "segment.approve"
		message = "通过段译文"
	} else {
		updated, err = s.reviewSvc.RejectSegment(r.Context(), authUser.User.ID, segmentID, input)
		action = "segment.reject"
		message = "驳回段译文"
	}
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: action, ResourceType: "segment", ResourceID: segmentID, Message: message})
	writeJSON(w, http.StatusOK, toSegmentResponse(updated))
}

func (s *Server) handleApproveJob(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	jobID, ok := parseIntParam(w, chi.URLParam(r, "jobId"), "jobId")
	if !ok {
		return
	}
	updated, err := s.reviewSvc.ApproveJob(r.Context(), authUser.User.ID, jobID)
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "job.review.approve", ResourceType: "job", ResourceID: jobID, Message: "批量通过任务审校"})
	writeJSON(w, http.StatusOK, toJobResponse(updated))
}

func (s *Server) handleRetranslateRejected(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	jobID, ok := parseIntParam(w, chi.URLParam(r, "jobId"), "jobId")
	if !ok {
		return
	}
	if err := s.reviewSvc.RetranslateRejected(r.Context(), authUser.User.ID, jobID); err != nil {
		writeReviewServiceError(w, err)
		return
	}
	if err := s.jobQueue.Enqueue(r.Context(), jobID); err != nil {
		writeServiceError(w, err)
		return
	}
	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "job.review.retranslate", ResourceType: "job", ResourceID: jobID, Message: "重译被驳回段"})
	reloaded, err := s.queryJobForResponse(r.Context(), jobID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toJobResponse(reloaded))
}

func writeReviewServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrSegmentNotFound), errors.Is(err, service.ErrJobNotFound), errors.Is(err, service.ErrSubJobNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, service.ErrForbidden):
		writeProblem(w, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrInvalidInput), errors.Is(err, service.ErrInvalidReviewState), errors.Is(err, service.ErrRetranslateNoReject):
		writeProblem(w, http.StatusBadRequest, "invalid_input", err.Error())
	default:
		writeProjectServiceError(w, err)
	}
}

func toSegmentResponse(row *ent.Segment) segmentResponse {
	resp := segmentResponse{
		ID:            row.ID,
		SegmentIndex:  row.SegmentIndex,
		SourceText:    row.SourceText,
		Status:        row.Status,
		ReviewComment: row.ReviewComment,
		CreatedAt:     row.CreatedAt.Format(timeRFC3339),
		UpdatedAt:     row.UpdatedAt.Format(timeRFC3339),
	}
	if row.TargetText != nil {
		resp.TargetText = *row.TargetText
	}
	if row.Edges.SubJob != nil {
		resp.SubJobID = row.Edges.SubJob.ID
	}
	if row.Edges.Resource != nil {
		resp.ResourceID = row.Edges.Resource.ID
	}
	if row.Edges.ReviewedBy != nil {
		reviewer := toUserResponse(row.Edges.ReviewedBy)
		resp.ReviewedBy = &reviewer
	}
	return resp
}
