package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/go-chi/chi/v5"
)

type segmentResponse struct {
	ID            int               `json:"id"`
	SubJobID      int               `json:"sub_job_id,omitempty"`
	ResourceID    int               `json:"resource_id,omitempty"`
	SegmentIndex  int               `json:"segment_index"`
	SourceText    string            `json:"source_text"`
	TargetText    string            `json:"target_text,omitempty"`
	Status        string            `json:"status"`
	ReviewComment *string           `json:"review_comment,omitempty"`
	ReviewedBy    *userResponse     `json:"reviewed_by,omitempty"`
	QualityIssues []qa.QualityIssue `json:"quality_issues,omitempty"`
	Meta          map[string]any    `json:"meta,omitempty"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
}

type segmentListResponse struct {
	Items      []segmentResponse `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type segmentReviewRequest struct {
	Action     string  `json:"action"`
	TargetText *string `json:"target_text"`
	Comment    *string `json:"comment"`
}

type batchReviewRequest struct {
	SegmentIDs []int   `json:"segment_ids"`
	Action     string  `json:"action"`
	Comment    *string `json:"comment"`
}

type batchReviewResponse struct {
	Items []segmentResponse `json:"items"`
}

type approveAllResponse struct {
	ApprovedCount int `json:"approved_count"`
}

type retranslateResponse struct {
	ResetCount int `json:"reset_count"`
}

func writeReviewServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrSegmentNotFound), errors.Is(err, service.ErrResourceNotFound), errors.Is(err, service.ErrTranslationJobNotFound):
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
		Status:        string(row.Status),
		ReviewComment: row.ReviewComment,
		CreatedAt:     row.CreatedAt.Format(timeRFC3339),
		UpdatedAt:     row.UpdatedAt.Format(timeRFC3339),
	}
	if row.TargetText != nil {
		resp.TargetText = *row.TargetText
	}
	if row.Edges.Resource != nil {
		resp.ResourceID = row.Edges.Resource.ID
	}
	if row.ResourceID != nil {
		resp.ResourceID = *row.ResourceID
	}
	if row.Edges.ReviewedBy != nil {
		reviewer := toUserResponse(row.Edges.ReviewedBy)
		resp.ReviewedBy = &reviewer
	}
	if len(row.QualityIssues) > 0 {
		resp.QualityIssues = row.QualityIssues
	}
	if row.Meta != nil {
		var meta map[string]any
		if err := json.Unmarshal([]byte(*row.Meta), &meta); err == nil {
			resp.Meta = meta
		}
	}
	return resp
}

// handleReviewSegment 审核单个段落（通过/拒绝/编辑）。
func (s *Server) handleReviewSegment(w http.ResponseWriter, r *http.Request) {
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

	var req segmentReviewRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	switch req.Action {
	case "approve":
		comment := ""
		if req.Comment != nil {
			comment = *req.Comment
		}
		updated, err := s.reviewSvc.ApproveSegment(r.Context(), authUser.User.ID, projectID, resourceID, segmentID, service.SegmentDecisionInput{Comment: comment})
		if err != nil {
			writeReviewServiceError(w, err)
			return
		}
		_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "segment.approve", ResourceType: "segment", ResourceID: segmentID, Message: "审批通过段落"})
		writeJSON(w, http.StatusOK, toSegmentResponse(updated))

	case "reject":
		comment := ""
		if req.Comment != nil {
			comment = *req.Comment
		}
		updated, err := s.reviewSvc.RejectSegment(r.Context(), authUser.User.ID, projectID, resourceID, segmentID, service.SegmentDecisionInput{Comment: comment})
		if err != nil {
			writeReviewServiceError(w, err)
			return
		}
		_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "segment.reject", ResourceType: "segment", ResourceID: segmentID, Message: "审批拒绝段落"})
		writeJSON(w, http.StatusOK, toSegmentResponse(updated))

	case "edit":
		if req.TargetText == nil {
			writeProblem(w, http.StatusBadRequest, "invalid_input", "编辑操作需要 target_text")
			return
		}
		comment := ""
		if req.Comment != nil {
			comment = *req.Comment
		}
		updated, err := s.reviewSvc.EditSegment(r.Context(), authUser.User.ID, projectID, resourceID, segmentID, service.SegmentEditInput{TargetText: *req.TargetText, Comment: comment})
		if err != nil {
			writeReviewServiceError(w, err)
			return
		}
		_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "segment.edit", ResourceType: "segment", ResourceID: segmentID, Message: "编辑段落译文"})
		writeJSON(w, http.StatusOK, toSegmentResponse(updated))

	default:
		writeProblem(w, http.StatusBadRequest, "invalid_input", "action 必须是 approve, reject 或 edit")
	}
}

// handleBatchReviewSegments 批量审核段落。
func (s *Server) handleBatchReviewSegments(w http.ResponseWriter, r *http.Request) {
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

	var req batchReviewRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	comment := ""
	if req.Comment != nil {
		comment = *req.Comment
	}

	updated, err := s.reviewSvc.BatchReview(r.Context(), authUser.User.ID, projectID, resourceID, service.BatchReviewInput{
		SegmentIDs: req.SegmentIDs,
		Action:     req.Action,
		Comment:    comment,
	})
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}

	items := make([]segmentResponse, 0, len(updated))
	for _, row := range updated {
		items = append(items, toSegmentResponse(row))
	}
	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "segment.batch_review", ResourceType: "resource", ResourceID: resourceID, Message: "批量审核段落"})
	writeJSON(w, http.StatusOK, batchReviewResponse{Items: items})
}

// handleApproveAllResourceSegments 批准资源中所有已翻译/已编辑的段落。
func (s *Server) handleApproveAllResourceSegments(w http.ResponseWriter, r *http.Request) {
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

	count, err := s.reviewSvc.ApproveAllResource(r.Context(), authUser.User.ID, projectID, resourceID)
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}

	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "segment.approve_all", ResourceType: "resource", ResourceID: resourceID, Message: "批准所有段落"})
	writeJSON(w, http.StatusOK, approveAllResponse{ApprovedCount: count})
}

// handleRetranslateRejected 将资源中被拒绝的段落重置为待翻译。
func (s *Server) handleRetranslateRejected(w http.ResponseWriter, r *http.Request) {
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

	count, err := s.reviewSvc.RetranslateRejected(r.Context(), authUser.User.ID, projectID, resourceID)
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}

	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "segment.retranslate_rejected", ResourceType: "resource", ResourceID: resourceID, Message: "重置被拒绝段落"})
	writeJSON(w, http.StatusOK, retranslateResponse{ResetCount: count})
}
