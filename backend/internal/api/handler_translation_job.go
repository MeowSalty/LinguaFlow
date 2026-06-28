package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type createTranslationJobRequest struct {
	ExecutionPlanID  int      `json:"execution_plan_id"`
	ResourceIDs      []int    `json:"resource_ids"`
	SegmentIDs       []int    `json:"segment_ids"`
	SegmentGroupKeys []string `json:"segment_group_keys"`
	AutoApprove      bool     `json:"auto_approve"`
}

// QueueInfo 携带翻译队列的位置信息。
type QueueInfo struct {
	Position int
	Size     int
}

type translationJobResourceResponse struct {
	ID                int               `json:"id"`
	ResourceID        int               `json:"resource_id"`
	Status            string            `json:"status"`
	SegmentIDs        []int             `json:"segment_ids,omitempty"`
	SegmentCount      int               `json:"segment_count"`
	CompletedSegments int               `json:"completed_segments"`
	OutputPath        string            `json:"output_path,omitempty"`
	ErrorMessage      *string           `json:"error_message,omitempty"`
	Resource          *resourceResponse `json:"resource,omitempty"`
	CurrentStage      string            `json:"current_stage,omitempty"`
	StageTotal        int               `json:"stage_total,omitempty"`
	StageCompleted    int               `json:"stage_completed,omitempty"`
	StartedAt         *string           `json:"started_at,omitempty"`
	CreatedAt         string            `json:"created_at"`
	UpdatedAt         string            `json:"updated_at"`
}

type translationJobResponse struct {
	ID                 int                              `json:"id"`
	ProjectID          int                              `json:"project_id"`
	Status             string                           `json:"status"`
	TriggerType        string                           `json:"trigger_type"`
	ExecutionPlanID    int                              `json:"execution_plan_id"`
	TranslationConfig  map[string]any                   `json:"translation_config,omitempty"`
	ResourceCount      int                              `json:"resource_count"`
	CompletedResources int                              `json:"completed_resources"`
	FailedResources    int                              `json:"failed_resources"`
	TotalSegments      int                              `json:"total_segments"`
	CompletedSegments  int                              `json:"completed_segments"`
	ErrorMessage       *string                          `json:"error_message,omitempty"`
	StartedAt          *string                          `json:"started_at,omitempty"`
	CurrentStage       string                           `json:"current_stage,omitempty"`
	ProgressPercentage float64                          `json:"progress_percentage,omitempty"`
	QueuePosition      *int                             `json:"queue_position,omitempty"`
	QueueSize          *int                             `json:"queue_size,omitempty"`
	CreatedAt          string                           `json:"created_at"`
	UpdatedAt          string                           `json:"updated_at"`
	JobResources       []translationJobResourceResponse `json:"job_resources,omitempty"`
}

// queueInfoForJob returns queue position info for a job, or nil if queue is
// unavailable or the job is not currently queued.
func (s *Server) queueInfoForJob(jobID int) *QueueInfo {
	if s.translationJobQueue == nil {
		return nil
	}
	info := s.translationJobQueue.Position(jobID)
	if info.Position < 0 {
		return nil
	}
	return &QueueInfo{
		Position: info.Position,
		Size:     info.Size,
	}
}

func (s *Server) handleCreateTranslationJob(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	var req createTranslationJobRequest
	if r.Body != nil && strings.TrimSpace(r.Header.Get("Content-Length")) != "0" {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	created, err := s.translationJobSvc.CreateManualJob(r.Context(), authUser.User.ID, projectID, service.CreateTranslationJobInput{
		ResourceIDs:      req.ResourceIDs,
		SegmentIDs:       req.SegmentIDs,
		SegmentGroupKeys: req.SegmentGroupKeys,
		ExecutionPlanID:  req.ExecutionPlanID,
		AutoApprove:      req.AutoApprove,
	})
	if err != nil {
		writeTranslationJobServiceError(w, err)
		return
	}
	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "translation_job.create", ResourceType: "translation_job", ResourceID: created.ID, Message: "创建翻译任务"})
	if s.translationJobQueue != nil {
		if err := s.translationJobQueue.Enqueue(r.Context(), created.ID); err != nil {
			writeServiceError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, toTranslationJobResponse(created, s.queueInfoForJob(created.ID)))
}

func (s *Server) handleListTranslationJobs(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	pageReq, ok := parseCursorPagination(w, r, 50, 100)
	if !ok {
		return
	}
	jobs, err := s.translationJobSvc.ListJobs(r.Context(), authUser.User.ID, projectID, service.TranslationJobListOptions{
		Status:      strings.TrimSpace(r.URL.Query().Get("status")),
		TriggerType: strings.TrimSpace(r.URL.Query().Get("trigger_type")),
		AfterID:     pageReq.AfterID,
		Limit:       pageReq.Limit,
	})
	if err != nil {
		writeTranslationJobServiceError(w, err)
		return
	}
	items := make([]translationJobResponse, 0, len(jobs))
	for _, job := range jobs {
		items = append(items, toTranslationJobResponse(job, s.queueInfoForJob(job.ID)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGetTranslationJob(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	jobID, ok := parseIntParam(w, chi.URLParam(r, "translationJobId"), "translationJobId")
	if !ok {
		return
	}
	job, err := s.translationJobSvc.GetJob(r.Context(), authUser.User.ID, jobID)
	if err != nil {
		writeTranslationJobServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTranslationJobResponse(job, s.queueInfoForJob(jobID)))
}

func (s *Server) handleCancelTranslationJob(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	jobID, ok := parseIntParam(w, chi.URLParam(r, "translationJobId"), "translationJobId")
	if !ok {
		return
	}
	job, err := s.translationJobSvc.CancelJob(r.Context(), authUser.User.ID, jobID)
	if err != nil {
		writeTranslationJobServiceError(w, err)
		return
	}
	// 通知正在运行的 worker 立即停止
	if s.translationJobRunner != nil {
		s.translationJobRunner.CancelRunningJob(jobID)
	}
	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "translation_job.cancel", ResourceType: "translation_job", ResourceID: job.ID, Message: "取消翻译任务"})
	writeJSON(w, http.StatusOK, toTranslationJobResponse(job, s.queueInfoForJob(job.ID)))
}

func (s *Server) handleRetryTranslationJob(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	jobID, ok := parseIntParam(w, chi.URLParam(r, "translationJobId"), "translationJobId")
	if !ok {
		return
	}
	job, err := s.translationJobSvc.RetryJob(r.Context(), authUser.User.ID, jobID)
	if err != nil {
		writeTranslationJobServiceError(w, err)
		return
	}
	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "translation_job.retry", ResourceType: "translation_job", ResourceID: job.ID, Message: "重试翻译任务"})
	if s.translationJobQueue != nil {
		if err := s.translationJobQueue.Enqueue(r.Context(), job.ID); err != nil {
			writeServiceError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, toTranslationJobResponse(job, s.queueInfoForJob(job.ID)))
}

func toTranslationJobResponse(row *ent.TranslationJob, queueInfo *QueueInfo) translationJobResponse {
	resp := translationJobResponse{
		ID:                 row.ID,
		Status:             row.Status,
		TriggerType:        row.TriggerType,
		ExecutionPlanID:    row.ExecutionPlanID,
		TranslationConfig:  row.TranslationConfig,
		ResourceCount:      row.ResourceCount,
		CompletedResources: row.CompletedResources,
		FailedResources:    row.FailedResources,
		TotalSegments:      row.TotalSegments,
		CompletedSegments:  row.CompletedSegments,
		ErrorMessage:       row.ErrorMessage,
		StartedAt:          timePtrToString(row.StartedAt),
		CreatedAt:          row.CreatedAt.Format(timeRFC3339),
		UpdatedAt:          row.UpdatedAt.Format(timeRFC3339),
	}

	// 计算进度百分比
	if row.TotalSegments > 0 {
		resp.ProgressPercentage = float64(row.CompletedSegments) / float64(row.TotalSegments) * 100
	}

	if row.Edges.Project != nil {
		resp.ProjectID = row.Edges.Project.ID
	}

	// 聚合当前阶段（取第一个 running 资源的 stage）
	if len(row.Edges.JobResources) > 0 {
		resp.JobResources = make([]translationJobResourceResponse, 0, len(row.Edges.JobResources))
		for _, item := range row.Edges.JobResources {
			rr := toTranslationJobResourceResponse(item)
			resp.JobResources = append(resp.JobResources, rr)
			if item.Status == "running" && item.CurrentStage != "" && resp.CurrentStage == "" {
				resp.CurrentStage = item.CurrentStage
			}
		}
	}

	// 队列信息
	if queueInfo != nil {
		resp.QueuePosition = &queueInfo.Position
		resp.QueueSize = &queueInfo.Size
	}

	return resp
}

func toTranslationJobResourceResponse(row *ent.JobResource) translationJobResourceResponse {
	resp := translationJobResourceResponse{
		ID:                row.ID,
		Status:            row.Status,
		SegmentIDs:        row.SegmentIds,
		SegmentCount:      row.SegmentCount,
		CompletedSegments: row.CompletedSegments,
		OutputPath:        row.OutputPath,
		ErrorMessage:      row.ErrorMessage,
		CurrentStage:      row.CurrentStage,
		StageTotal:        row.StageTotal,
		StageCompleted:    row.StageCompleted,
		StartedAt:         timePtrToString(row.StartedAt),
		CreatedAt:         row.CreatedAt.Format(timeRFC3339),
		UpdatedAt:         row.UpdatedAt.Format(timeRFC3339),
	}
	if row.Edges.Resource != nil {
		resp.ResourceID = row.Edges.Resource.ID
		resourceResp := toResourceResponse(row.Edges.Resource, 0, 0)
		resp.Resource = &resourceResp
	}
	return resp
}

func writeTranslationJobServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrTranslationJobNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "翻译任务不存在")
	case errors.Is(err, service.ErrTranslationJobEmpty):
		writeProblem(w, http.StatusBadRequest, "invalid_input", "没有可翻译的待处理段落")
	case errors.Is(err, service.ErrResourceNotFound), errors.Is(err, service.ErrSegmentNotFound), errors.Is(err, service.ErrProjectNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, service.ErrForbidden):
		writeProblem(w, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrInvalidInput):
		writeProblem(w, http.StatusBadRequest, "invalid_input", err.Error())
	default:
		writeProjectServiceError(w, err)
	}
}
