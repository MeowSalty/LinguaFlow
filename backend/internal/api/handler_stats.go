package api

import (
	"net/http"
	"strconv"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
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
	ID             int            `json:"id"`
	OrganizationID *int           `json:"organization_id,omitempty"`
	ProjectID      *int           `json:"project_id,omitempty"`
	Action         string         `json:"action"`
	ResourceType   string         `json:"resource_type"`
	ResourceID     *int           `json:"resource_id,omitempty"`
	Message        string         `json:"message,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Actor          *userResponse  `json:"actor,omitempty"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
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
	if !s.validateJobQueryParameters(w, r, "cursor", "limit", "org_id") {
		return
	}
	var orgIDs []int
	if raw, present := r.URL.Query()["org_id"]; present {
		id, err := strconv.Atoi(raw[0])
		if err != nil || id <= 0 {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "org_id 必须是正整数")
			return
		}
		orgIDs = []int{id}
	}
	pageReq, ok := s.parseCursorPagination(w, r, 50, 100)
	if !ok {
		return
	}
	page, err := s.auditSvc.ListActivity(r.Context(), authUser.User.ID, pageReq.AfterID, pageReq.Limit, orgIDs...)
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
		Metadata:     service.SanitizeActivityMetadata(row.Action, row.Metadata),
		CreatedAt:    timeutil.Format(row.CreatedAt),
		UpdatedAt:    timeutil.Format(row.UpdatedAt),
	}
	if row.Edges.Project != nil {
		id := row.Edges.Project.ID
		resp.ProjectID = &id
		resp.OrganizationID = service.EffectiveProjectOrgID(row.Edges.Project)
	} else if row.Edges.Organization != nil {
		id := row.Edges.Organization.ID
		resp.OrganizationID = &id
	}
	if row.Edges.Actor != nil {
		actor := toUserResponse(row.Edges.Actor)
		resp.Actor = &actor
	}
	return resp
}
