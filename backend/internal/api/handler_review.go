package api

import (
	"errors"
	"net/http"

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
