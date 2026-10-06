package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

func (s *Server) handleListAccessibleJobs(w http.ResponseWriter, r *http.Request, params ListAccessibleJobsParams) {
	actor, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	if !s.validateJobQueryParameters(w, r, "state", "status", "project_id", "trigger_type", "updated_from", "updated_before", "cursor", "limit") {
		return
	}
	limit, ok := s.parseLimitParam(w, r, 50, 100)
	if !ok {
		return
	}
	opts := service.AccessibleJobListOptions{
		Limit:         limit,
		UpdatedFrom:   params.UpdatedFrom,
		UpdatedBefore: params.UpdatedBefore,
	}
	if params.ProjectId != nil {
		if *params.ProjectId <= 0 {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "project_id 必须是正整数")
			return
		}
		opts.ProjectID = *params.ProjectId
	}
	if params.State != nil {
		opts.State = string(*params.State)
	}
	if params.Status != nil {
		opts.Status = string(*params.Status)
	}
	if params.TriggerType != nil {
		opts.TriggerType = string(*params.TriggerType)
	}
	if params.Cursor != nil {
		opts.Cursor = *params.Cursor
	}
	page, err := s.jobSvc.ListAccessibleJobs(r.Context(), actor.User.ID, opts)
	if err != nil {
		s.writeJobQueryError(w, r, err)
		return
	}
	response := JobSummaryListResponse{Items: make([]JobSummary, 0, len(page.Items))}
	for _, row := range page.Items {
		item, err := toJobSummary(row)
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
		item.CanDelete = s.canDeleteHistory(r.Context(), actor.User.ID, "translation", row.ID, row.ProjectID, row.Status)
		response.Items = append(response.Items, item)
	}
	if page.NextCursor != "" {
		response.NextCursor = &page.NextCursor
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleJobsSummary(w http.ResponseWriter, r *http.Request, params GetJobsSummaryParams) {
	actor, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	if !s.validateJobQueryParameters(w, r, "project_id", "trigger_type") {
		return
	}
	opts := service.JobSummaryOptions{}
	if params.ProjectId != nil {
		if *params.ProjectId <= 0 {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "project_id 必须是正整数")
			return
		}
		opts.ProjectID = *params.ProjectId
	}
	if params.TriggerType != nil {
		opts.TriggerType = string(*params.TriggerType)
	}
	summary, err := s.jobSvc.GetJobsSummary(r.Context(), actor.User.ID, opts)
	if err != nil {
		s.writeJobQueryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, JobsSummaryResponse{
		Pending:           summary.Pending,
		Running:           summary.Running,
		Paused:            summary.Paused,
		RecentFailed:      summary.RecentFailed,
		RecentFailedSince: timeutil.Normalize(summary.RecentFailedSince),
		AsOf:              timeutil.Normalize(summary.AsOf),
	})
}

// These new endpoints reject ambiguous or unsupported query parameters instead
// of silently ignoring filters (especially status/time filters on the summary).
func (s *Server) validateJobQueryParameters(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "查询参数编码无效")
		return false
	}
	for name, values := range query {
		if !slices.Contains(allowed, name) || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", fmt.Sprintf("不支持、重复或为空的查询参数：%s", name))
			return false
		}
		// The generated binder also accepts date-only values. These filters
		// require an explicit RFC3339 instant, including its UTC offset.
		if name == "updated_from" || name == "updated_before" {
			if _, err := time.Parse(time.RFC3339Nano, values[0]); err != nil {
				s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", name+" 必须是 RFC3339 时间")
				return false
			}
		}
	}
	return true
}

func (s *Server) writeJobQueryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, service.ErrInvalidInput) {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", err.Error())
		return
	}
	s.writeJobServiceError(w, r, err)
}

func toJobSummary(row *ent.Job) (JobSummary, error) {
	project, err := row.Edges.ProjectOrErr()
	if err != nil {
		return JobSummary{}, fmt.Errorf("load summary project for job %d: %w", row.ID, err)
	}
	return JobSummary{
		Id:          row.ID,
		ProjectId:   row.ProjectID,
		ProjectName: project.Name,
		Status:      JobSummaryStatus(row.Status),
		TriggerType: JobSummaryTriggerType(row.TriggerType),
		CreatedAt:   timeutil.Normalize(row.CreatedAt),
		UpdatedAt:   timeutil.Normalize(row.UpdatedAt),
		StartedAt:   timeutil.NormalizePtr(row.StartedAt),
		FinishedAt:  timeutil.NormalizePtr(row.FinishedAt),
		Progress: JobSummaryProgress{
			TotalResources:     row.ResourceCount,
			CompletedResources: row.CompletedResources,
			FailedResources:    row.FailedResources,
			ProgressTotal:      row.ProgressTotal,
			ProgressCompleted:  row.ProgressCompleted,
			// Deliberately leave queue fields nil: the process-wide queue is
			// neither a reliable waiting count nor scoped to project access.
		},
	}, nil
}
