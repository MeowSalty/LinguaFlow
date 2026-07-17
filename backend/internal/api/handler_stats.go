package api

import (
	"net/http"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type usageStatsResponse struct {
	APICalls      int `json:"api_calls"`
	InputTokens   int `json:"input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	SegmentCount  int `json:"segment_count"`
	UsageRecords  int `json:"usage_records"`
	CompletedJobs int `json:"completed_jobs"`
	FailedJobs    int `json:"failed_jobs"`
}

type activityResponse struct {
	ID           int            `json:"id"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   *int           `json:"resource_id,omitempty"`
	Message      string         `json:"message,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	Actor        *userResponse  `json:"actor,omitempty"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
}

type activityListResponse struct {
	Items      []activityResponse `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

func (s *Server) handleStatsSummary(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	stats, err := s.statsSvc.Summary(r.Context(), authUser.User.ID)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUsageStatsResponse(stats))
}

func (s *Server) handleListActivity(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	pageReq, ok := s.parseCursorPagination(w, r, 50, 100)
	if !ok {
		return
	}
	page, err := s.auditSvc.ListActivity(r.Context(), authUser.User.ID, pageReq.AfterID, pageReq.Limit)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	items := make([]activityResponse, 0, len(page.Items))
	for _, row := range page.Items {
		items = append(items, toActivityResponse(row))
	}
	writeJSON(w, http.StatusOK, activityListResponse{Items: items, NextCursor: formatCursor(page.NextCursor)})
}

func toUsageStatsResponse(stats *service.UsageStats) usageStatsResponse {
	return usageStatsResponse{
		APICalls:      stats.APICalls,
		InputTokens:   stats.InputTokens,
		OutputTokens:  stats.OutputTokens,
		SegmentCount:  stats.SegmentCount,
		UsageRecords:  stats.UsageRecords,
		CompletedJobs: stats.CompletedJobs,
		FailedJobs:    stats.FailedJobs,
	}
}

func toActivityResponse(row *ent.ActivityLog) activityResponse {
	resp := activityResponse{
		ID:           row.ID,
		Action:       row.Action,
		ResourceType: row.ResourceType,
		ResourceID:   row.ResourceID,
		Message:      row.Message,
		Metadata:     row.Metadata,
		CreatedAt:    row.CreatedAt.Format(timeRFC3339),
		UpdatedAt:    row.UpdatedAt.Format(timeRFC3339),
	}
	if row.Edges.Actor != nil {
		actor := toUserResponse(row.Edges.Actor)
		resp.Actor = &actor
	}
	return resp
}
