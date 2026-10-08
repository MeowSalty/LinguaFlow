package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
	"github.com/MeowSalty/LinguaFlow/backend/internal/worker"
)

type createJobRequest struct {
	ExecutionPlanID  int      `json:"execution_plan_id"`
	ResourceIDs      []int    `json:"resource_ids"`
	SegmentIDs       []int    `json:"segment_ids"`
	SegmentGroupKeys []string `json:"segment_group_keys"`
	SegmentFilter    string   `json:"segment_filter"`
	AutoApprove      bool     `json:"auto_approve"`
}

// jobRoundResponse 单轮次进度（资源×轮次矩阵的一行）。
type jobRoundResponse struct {
	RoundIndex       int     `json:"round_index"`
	Mode             string  `json:"mode"`
	Status           string  `json:"status"`
	SegmentTotal     int     `json:"segment_total"`
	SegmentCompleted int     `json:"segment_completed"`
	ErrorMessage     *string `json:"error_message,omitempty"`
	StartedAt        *string `json:"started_at,omitempty"`
	FinishedAt       *string `json:"finished_at,omitempty"`
}

type jobResourceResponse struct {
	ID                int                `json:"id"`
	ResourceID        int                `json:"resource_id"`
	Status            string             `json:"status"`
	SegmentCount      int                `json:"segment_count"`
	CompletedSegments int                `json:"completed_segments"`
	SkippedSegments   int                `json:"skipped_segments"`
	WorkWeight        int64              `json:"work_weight"`
	Rounds            []jobRoundResponse `json:"rounds"`
	OutputPath        string             `json:"output_path,omitempty"`
	ErrorMessage      *string            `json:"error_message,omitempty"`
	WarningMessage    *string            `json:"warning_message,omitempty"`
	Resource          *resourceResponse  `json:"resource,omitempty"`
	StartedAt         *string            `json:"started_at,omitempty"`
	CreatedAt         string             `json:"created_at"`
	UpdatedAt         string             `json:"updated_at"`
}

type userBriefResponse struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
}

type jobProgressResponse struct {
	TotalResources     int `json:"total_resources"`
	CompletedResources int `json:"completed_resources"`
	FailedResources    int `json:"failed_resources"`
	// ProgressTotal/ProgressCompleted：工作量单位 = 段落×轮的「已知工作量」
	// 进度对（矩阵派生缓存）。分母随轮次启动动态增长。
	ProgressTotal     *int64 `json:"progress_total"`
	ProgressCompleted *int64 `json:"progress_completed"`
	QueuePosition     *int   `json:"queue_position,omitempty"`
	QueueSize         *int   `json:"queue_size,omitempty"`
}

type jobResponse struct {
	ID              int                   `json:"id"`
	ProjectID       int                   `json:"project_id"`
	CreatedBy       *userBriefResponse    `json:"created_by,omitempty"`
	Status          string                `json:"status"`
	TriggerType     string                `json:"trigger_type"`
	ExecutionPlanID int                   `json:"execution_plan_id"`
	ExecutionConfig map[string]any        `json:"execution_config,omitempty"`
	ErrorMessage    *string               `json:"error_message,omitempty"`
	Progress        jobProgressResponse   `json:"progress"`
	StartedAt       *string               `json:"started_at,omitempty"`
	FinishedAt      *string               `json:"finished_at"`
	CanDelete       bool                  `json:"can_delete"`
	CreatedAt       string                `json:"created_at"`
	UpdatedAt       string                `json:"updated_at"`
	JobResources    []jobResourceResponse `json:"job_resources,omitempty"`
}

// queueInfoForJob returns queue position info for a job, or nil if queue is
// unavailable or the job is not currently queued.
func (s *Server) queueInfoForJob(jobID int) *worker.QueueInfo {
	// Instance queue data belongs only to the protected runtime summary.
	return nil
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := s.parseIntParam(w, r, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	var req createJobRequest
	if r.Body != nil && strings.TrimSpace(r.Header.Get("Content-Length")) != "0" {
		if !s.decodeJSON(w, r, &req) {
			return
		}
	}
	created, err := s.jobSvc.CreateManualJob(r.Context(), authUser.User.ID, projectID, service.CreateJobInput{
		ResourceIDs:      req.ResourceIDs,
		SegmentIDs:       req.SegmentIDs,
		SegmentGroupKeys: req.SegmentGroupKeys,
		SegmentFilter:    req.SegmentFilter,
		ExecutionPlanID:  req.ExecutionPlanID,
		AutoApprove:      req.AutoApprove,
	})
	if err != nil {
		s.writeJobServiceError(w, r, err)
		return
	}
	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, ProjectID: &created.ProjectID, Action: "job.create", ResourceType: "job", ResourceID: created.ID, Message: "创建任务"})
	if s.dispatcher != nil {
		s.dispatcher.Notify("translation")
	}
	writeJSON(w, http.StatusAccepted, s.toJobDetailResponse(r.Context(), authUser.User.ID, created, s.queueInfoForJob(created.ID)))
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := s.parseIntParam(w, r, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	pageReq, ok := s.parseCursorPagination(w, r, 50, 100)
	if !ok {
		return
	}
	jobs, err := s.jobSvc.ListJobs(r.Context(), authUser.User.ID, projectID, service.JobListOptions{
		Status:      strings.TrimSpace(r.URL.Query().Get("status")),
		TriggerType: strings.TrimSpace(r.URL.Query().Get("trigger_type")),
		AfterID:     pageReq.AfterID,
		Limit:       pageReq.Limit,
	})
	if err != nil {
		s.writeJobServiceError(w, r, err)
		return
	}
	items := make([]jobResponse, 0, len(jobs))
	for _, job := range jobs {
		items = append(items, s.toJobListResponse(r.Context(), authUser.User.ID, job, s.queueInfoForJob(job.ID)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	jobID, ok := s.parseIntParam(w, r, chi.URLParam(r, "jobId"), "jobId")
	if !ok {
		return
	}
	job, err := s.jobSvc.GetJob(r.Context(), authUser.User.ID, jobID)
	if err != nil {
		s.writeJobServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toJobDetailResponse(r.Context(), authUser.User.ID, job, s.queueInfoForJob(jobID)))
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	jobID, ok := s.parseIntParam(w, r, chi.URLParam(r, "jobId"), "jobId")
	if !ok {
		return
	}
	job, err := s.jobSvc.CancelJob(r.Context(), authUser.User.ID, jobID)
	if err != nil {
		s.writeJobServiceError(w, r, err)
		return
	}
	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, ProjectID: &job.ProjectID, Action: "job.cancel", ResourceType: "job", ResourceID: job.ID, Message: "取消任务"})
	writeJSON(w, http.StatusOK, s.toJobDetailResponse(r.Context(), authUser.User.ID, job, s.queueInfoForJob(job.ID)))
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	jobID, ok := s.parseIntParam(w, r, chi.URLParam(r, "jobId"), "jobId")
	if !ok {
		return
	}
	job, err := s.jobSvc.RetryJob(r.Context(), authUser.User.ID, jobID)
	if err != nil {
		s.writeJobServiceError(w, r, err)
		return
	}
	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, ProjectID: &job.ProjectID, Action: "job.retry", ResourceType: "job", ResourceID: job.ID, Message: "重试任务"})
	if s.dispatcher != nil {
		s.dispatcher.Notify("translation")
	}
	writeJSON(w, http.StatusOK, s.toJobDetailResponse(r.Context(), authUser.User.ID, job, s.queueInfoForJob(job.ID)))
}

// handlePauseJob 优雅暂停：running 任务先通知 worker 排空（在途 LLM 请求返回后
// 冻结，最终由 worker 落 paused 终态）；pending 任务直接翻转状态不派发。
func (s *Server) handlePauseJob(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	jobID, ok := s.parseIntParam(w, r, chi.URLParam(r, "jobId"), "jobId")
	if !ok {
		return
	}
	result, err := s.jobSvc.PauseJob(r.Context(), authUser.User.ID, jobID)
	if err != nil {
		s.writeJobServiceError(w, r, err)
		return
	}
	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, ProjectID: &result.Job.ProjectID, Action: "job.pause", ResourceType: "job", ResourceID: result.Job.ID, Message: "暂停任务"})
	writeJSON(w, http.StatusOK, s.toJobDetailResponse(r.Context(), authUser.User.ID, result.Job, s.queueInfoForJob(result.Job.ID)))
}

// handleResumeJob 从轮次断点恢复已暂停的任务：重置 running 资源与轮次为 pending
// （保留 resolved 断点与已完成计数），重新入队。
func (s *Server) handleResumeJob(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	jobID, ok := s.parseIntParam(w, r, chi.URLParam(r, "jobId"), "jobId")
	if !ok {
		return
	}
	job, err := s.jobSvc.ResumeJob(r.Context(), authUser.User.ID, jobID)
	if err != nil {
		s.writeJobServiceError(w, r, err)
		return
	}
	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, ProjectID: &job.ProjectID, Action: "job.resume", ResourceType: "job", ResourceID: job.ID, Message: "恢复任务"})
	if s.dispatcher != nil {
		s.dispatcher.Notify("translation")
	}
	writeJSON(w, http.StatusOK, s.toJobDetailResponse(r.Context(), authUser.User.ID, job, s.queueInfoForJob(job.ID)))
}

func sanitizeExecutionConfig(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return nil
	}
	var sanitized map[string]any
	if err := json.Unmarshal(raw, &sanitized); err != nil {
		return nil
	}
	if rounds, ok := sanitized["rounds"].([]any); ok {
		for _, r := range rounds {
			if round, ok := r.(map[string]any); ok {
				maskBackendOptions(round)
			}
		}
	}
	if bootstrap, ok := sanitized["bootstrap"].(map[string]any); ok {
		maskBackendOptions(bootstrap)
	}
	if rubyRetry, ok := sanitized["ruby_retry"].(map[string]any); ok {
		maskBackendOptions(rubyRetry)
	}
	return sanitized
}

func maskBackendOptions(node map[string]any) {
	if backend, ok := node["backend"].(map[string]any); ok {
		if opts, ok := backend["options"].(map[string]any); ok {
			if _, hasKey := opts["api_key"]; hasKey {
				opts["api_key"] = "***"
			}
		}
	}
}

func (s *Server) toJobListResponse(ctx context.Context, actorID int, row *ent.Job, queueInfo *worker.QueueInfo) jobResponse {
	resp := jobResponse{
		ID:              row.ID,
		ProjectID:       row.ProjectID,
		Status:          row.Status,
		TriggerType:     row.TriggerType,
		ExecutionPlanID: row.ExecutionPlanID,
		ErrorMessage:    row.ErrorMessage,
		StartedAt:       timePtrToString(row.StartedAt),
		FinishedAt:      timePtrToString(row.FinishedAt),
		CanDelete:       s.canDeleteHistory(ctx, actorID, "translation", row.ID, row.ProjectID, row.Status),
		CreatedAt:       timeutil.Format(row.CreatedAt),
		UpdatedAt:       timeutil.Format(row.UpdatedAt),
	}
	if row.Edges.CreatedBy != nil {
		resp.CreatedBy = &userBriefResponse{ID: row.Edges.CreatedBy.ID, Username: row.Edges.CreatedBy.Username}
	}
	resp.Progress = buildProgressResponse(row, queueInfo)
	return resp
}

func (s *Server) toJobDetailResponse(ctx context.Context, actorID int, row *ent.Job, queueInfo *worker.QueueInfo) jobResponse {
	resp := s.toJobListResponse(ctx, actorID, row, queueInfo)
	resp.ExecutionConfig = sanitizeExecutionConfig(row.ExecutionConfig)
	if len(row.Edges.JobResources) > 0 {
		resp.JobResources = make([]jobResourceResponse, 0, len(row.Edges.JobResources))
		for _, item := range row.Edges.JobResources {
			resp.JobResources = append(resp.JobResources, toJobResourceResponse(item))
		}
	}
	return resp
}

func buildProgressResponse(row *ent.Job, queueInfo *worker.QueueInfo) jobProgressResponse {
	progressTotal := row.ProgressTotal
	progressCompleted := row.ProgressCompleted
	progress := jobProgressResponse{
		TotalResources:     row.ResourceCount,
		CompletedResources: row.CompletedResources,
		FailedResources:    row.FailedResources,
		ProgressTotal:      &progressTotal,
		ProgressCompleted:  &progressCompleted,
	}
	// These deprecated fields stay unavailable even for callers holding an
	// instance queue snapshot; ordinary job responses have project scope.
	return progress
}

func toJobResourceResponse(row *ent.JobResource) jobResourceResponse {
	resp := jobResourceResponse{
		ID:                row.ID,
		Status:            row.Status,
		SegmentCount:      row.SegmentCount,
		CompletedSegments: row.CompletedSegments,
		SkippedSegments:   row.SkippedSegments,
		WorkWeight:        row.WorkWeight,
		OutputPath:        row.OutputPath,
		ErrorMessage:      row.ErrorMessage,
		WarningMessage:    row.WarningMessage,
		StartedAt:         timePtrToString(row.StartedAt),
		CreatedAt:         timeutil.Format(row.CreatedAt),
		UpdatedAt:         timeutil.Format(row.UpdatedAt),
	}
	// rounds 数组：未 eager-load 时为空（列表视图）；详情视图由
	// service 层 WithRounds 预载。
	if roundRows := row.Edges.Rounds; len(roundRows) > 0 {
		resp.Rounds = make([]jobRoundResponse, 0, len(roundRows))
		for _, rd := range roundRows {
			resp.Rounds = append(resp.Rounds, jobRoundResponse{
				RoundIndex:       rd.RoundIndex,
				Mode:             rd.Mode,
				Status:           rd.Status,
				SegmentTotal:     rd.SegmentTotal,
				SegmentCompleted: rd.SegmentCompleted,
				ErrorMessage:     rd.ErrorMessage,
				StartedAt:        timePtrToString(rd.StartedAt),
				FinishedAt:       timePtrToString(rd.FinishedAt),
			})
		}
	} else {
		resp.Rounds = []jobRoundResponse{}
	}
	if row.Edges.Resource != nil {
		resp.ResourceID = row.Edges.Resource.ID
		resourceResp := toResourceResponse(row.Edges.Resource, 0, 0)
		resp.Resource = &resourceResp
	}
	return resp
}

func (s *Server) writeJobServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrJobNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "任务不存在")
	case errors.Is(err, service.ErrJobEmpty):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "没有可处理的待处理段落")
	case errors.Is(err, service.ErrJobNotCancellable):
		s.writeProblem(w, r, http.StatusConflict, "conflict", "任务当前状态不可取消")
	case errors.Is(err, service.ErrJobNotRetryable):
		s.writeProblem(w, r, http.StatusConflict, "conflict", "任务未失败或未取消，无法重试")
	case errors.Is(err, service.ErrJobNoFailedResource):
		s.writeProblem(w, r, http.StatusConflict, "conflict", "没有可重试的失败资源")
	case errors.Is(err, service.ErrJobNotPausable):
		s.writeProblem(w, r, http.StatusConflict, "conflict", "任务当前状态不可暂停")
	case errors.Is(err, service.ErrJobNotResumable):
		s.writeProblem(w, r, http.StatusConflict, "conflict", "任务未处于暂停状态，无法恢复")
	case errors.Is(err, service.ErrProjectNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "项目不存在")
	case errors.Is(err, service.ErrResourceNotFound), errors.Is(err, service.ErrSegmentNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, service.ErrForbidden):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrInvalidInput):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", err.Error())
	default:
		s.writeProjectServiceError(w, r, err)
	}
}
