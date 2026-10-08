package api

import (
	"net/http"
	"strconv"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

func (s *Server) ListOperations(w http.ResponseWriter, r *http.Request, p ListOperationsParams) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleListOperations(w, r, p) })).ServeHTTP(w, r)
}
func (s *Server) GetOperationsSummary(w http.ResponseWriter, r *http.Request, p GetOperationsSummaryParams) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleOperationsSummary(w, r, p) })).ServeHTTP(w, r)
}
func (s *Server) handleListOperations(w http.ResponseWriter, r *http.Request, p ListOperationsParams) {
	if !s.validateJobQueryParameters(w, r, "task_type", "project_id", "trigger_type", "state", "status", "updated_from", "updated_before", "cursor", "limit") {
		return
	}
	actor, _ := authUserFromContext(r.Context())
	limit, ok := s.parseLimitParam(w, r, 50, 100)
	if !ok {
		return
	}
	opts := service.OperationListOptions{AccessibleJobListOptions: service.AccessibleJobListOptions{Limit: limit, UpdatedFrom: p.UpdatedFrom, UpdatedBefore: p.UpdatedBefore}}
	if p.ProjectId != nil {
		if *p.ProjectId <= 0 {
			s.writeProblem(w, r, 400, "invalid_query_parameter", "project_id 必须是正整数")
			return
		}
		opts.ProjectID = *p.ProjectId
	}
	if p.TaskType != nil {
		opts.TaskType = string(*p.TaskType)
	}
	if p.TriggerType != nil {
		opts.TriggerType = string(*p.TriggerType)
	}
	if p.State != nil {
		opts.State = string(*p.State)
	}
	if p.Status != nil {
		opts.Status = string(*p.Status)
	}
	if p.Cursor != nil {
		opts.Cursor = *p.Cursor
	}
	page, err := service.NewOperationQueryService(s.entClient).List(r.Context(), actor.User.ID, opts)
	if err != nil {
		s.writeJobQueryError(w, r, err)
		return
	}
	response := OperationListResponse{Items: make([]OperationSummary, 0, len(page.Items))}
	for _, row := range page.Items {
		var item OperationSummary
		if row.Job != nil {
			j := row.Job
			err = item.FromTranslationOperation(TranslationOperation{TaskType: "translation", TaskId: strconv.Itoa(j.ID), ProjectId: j.ProjectID, ProjectName: row.ProjectName, Status: TranslationOperationStatus(j.Status), TriggerType: TranslationOperationTriggerType(j.TriggerType), CreatedAt: timeutil.Normalize(j.CreatedAt), UpdatedAt: timeutil.Normalize(j.UpdatedAt), StartedAt: timeutil.NormalizePtr(j.StartedAt), FinishedAt: timeutil.NormalizePtr(j.FinishedAt), CanDelete: s.canDeleteHistory(r.Context(), actor.User.ID, "translation", j.ID, j.ProjectID, j.Status), SupportedActions: []TranslationOperationSupportedActions{"view", "pause", "resume", "cancel", "retry", "delete"}, Progress: JobSummaryProgress{TotalResources: j.ResourceCount, CompletedResources: j.CompletedResources, FailedResources: j.FailedResources, ProgressTotal: j.ProgressTotal, ProgressCompleted: j.ProgressCompleted}})
		} else if row.StorageTask != nil {
			t := row.StorageTask
			err = item.FromStorageOperation(StorageOperation{
				TaskType: "storage", TaskId: strconv.Itoa(t.ID), ProjectId: t.ProjectID, ProjectName: row.ProjectName,
				StorageKind: t.Kind, Status: StorageOperationStatus(t.Status), Phase: t.Phase,
				CleanupStatus: StorageOperationCleanupStatus(t.CleanupStatus), ErrorCode: t.ErrorCode,
				NextRetryAt: timeutil.NormalizePtr(t.NextRetryAt), CreatedAt: timeutil.Normalize(t.CreatedAt), UpdatedAt: timeutil.Normalize(t.UpdatedAt),
				SupportedActions: []StorageOperationSupportedActions{"view", "cancel", "retry"},
			})
		} else {
			t := row.SyncTask
			sync := GlossarySyncOperation{TaskType: "glossary_sync", TaskId: strconv.Itoa(t.ID), ProjectId: t.ProjectID, ProjectName: row.ProjectName, Status: GlossarySyncOperationStatus(t.Status), CreatedAt: timeutil.Normalize(t.CreatedAt), UpdatedAt: timeutil.Normalize(t.UpdatedAt), StartedAt: timeutil.NormalizePtr(t.StartedAt), FinishedAt: timeutil.NormalizePtr(t.FinishedAt), CanDelete: s.canDeleteHistory(r.Context(), actor.User.ID, "glossary_sync", t.ID, t.ProjectID, t.Status), SupportedActions: []GlossarySyncOperationSupportedActions{"view", "cancel", "delete"}}
			sync.Progress.ProcessedSegments = t.ProcessedSegments
			sync.Progress.TotalSegments = t.TotalSegments
			err = item.FromGlossarySyncOperation(sync)
		}
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
		response.Items = append(response.Items, item)
	}
	if page.NextCursor != "" {
		response.NextCursor = &page.NextCursor
	}
	writeJSON(w, http.StatusOK, response)
}
func (s *Server) handleOperationsSummary(w http.ResponseWriter, r *http.Request, p GetOperationsSummaryParams) {
	if !s.validateJobQueryParameters(w, r, "task_type", "project_id", "trigger_type") {
		return
	}
	actor, _ := authUserFromContext(r.Context())
	var opts service.OperationSummaryOptions
	if p.ProjectId != nil {
		if *p.ProjectId <= 0 {
			s.writeProblem(w, r, 400, "invalid_query_parameter", "project_id 必须是正整数")
			return
		}
		opts.ProjectID = *p.ProjectId
	}
	if p.TaskType != nil {
		opts.TaskType = string(*p.TaskType)
	}
	if p.TriggerType != nil {
		opts.TriggerType = string(*p.TriggerType)
	}
	result, err := service.NewOperationQueryService(s.entClient).Summary(r.Context(), actor.User.ID, opts)
	if err != nil {
		s.writeJobQueryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
