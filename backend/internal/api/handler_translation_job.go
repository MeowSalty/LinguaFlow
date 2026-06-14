package api

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type createTranslationJobRequest struct {
	ExecutionPlanID int   `json:"execution_plan_id"`
	ResourceIDs     []int `json:"resource_ids"`
	SegmentIDs      []int `json:"segment_ids"`
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
	CreatedAt          string                           `json:"created_at"`
	UpdatedAt          string                           `json:"updated_at"`
	JobResources       []translationJobResourceResponse `json:"job_resources,omitempty"`
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
		ResourceIDs:     req.ResourceIDs,
		SegmentIDs:      req.SegmentIDs,
		ExecutionPlanID: req.ExecutionPlanID,
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
	writeJSON(w, http.StatusAccepted, toTranslationJobResponse(created))
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
		items = append(items, toTranslationJobResponse(job))
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
	writeJSON(w, http.StatusOK, toTranslationJobResponse(job))
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
	_ = s.auditSvc.Record(r.Context(), service.AuditEvent{ActorUserID: authUser.User.ID, Action: "translation_job.cancel", ResourceType: "translation_job", ResourceID: job.ID, Message: "取消翻译任务"})
	writeJSON(w, http.StatusOK, toTranslationJobResponse(job))
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
	writeJSON(w, http.StatusOK, toTranslationJobResponse(job))
}

func (s *Server) handleDownloadTranslationJobResult(w http.ResponseWriter, r *http.Request) {
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
	resourceID := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("resource_id")); raw != "" {
		parsed, parsedOK := parseIntParam(w, raw, "resource_id")
		if !parsedOK {
			return
		}
		resourceID = parsed
	}
	ready := make([]*ent.JobResource, 0, len(job.Edges.JobResources))
	for _, item := range job.Edges.JobResources {
		if resourceID > 0 {
			res, err := item.Edges.ResourceOrErr()
			if err != nil || res.ID != resourceID {
				continue
			}
		}
		if item.Status == service.JobResourceStatusCompleted && strings.TrimSpace(item.OutputPath) != "" {
			ready = append(ready, item)
		}
	}
	if len(ready) == 0 {
		writeProblem(w, http.StatusConflict, "result_not_ready", "当前没有可下载输出")
		return
	}
	if len(ready) == 1 {
		s.downloadSingleTranslationOutput(w, ready[0])
		return
	}
	s.downloadZipTranslationOutputs(w, job.ID, ready)
}

func (s *Server) downloadSingleTranslationOutput(w http.ResponseWriter, item *ent.JobResource) {
	absolutePath, err := s.jobStore.Absolute(item.OutputPath)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	file, err := os.Open(absolutePath)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	defer file.Close()
	filename := filepath.Base(absolutePath)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	_, _ = io.Copy(w, file)
}

func (s *Server) downloadZipTranslationOutputs(w http.ResponseWriter, jobID int, items []*ent.JobResource) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fmt.Sprintf("translation-job-%d-results.zip", jobID)))
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, item := range items {
		absolutePath, err := s.jobStore.Absolute(item.OutputPath)
		if err != nil {
			continue
		}
		file, err := os.Open(absolutePath)
		if err != nil {
			continue
		}
		entryName := filepath.Base(absolutePath)
		if item.Edges.Resource != nil {
			entryName = safeZipResourceEntryName(item.Edges.Resource)
		}
		entry, err := zw.Create(entryName)
		if err != nil {
			_ = file.Close()
			continue
		}
		_, _ = io.Copy(entry, file)
		_ = file.Close()
	}
}

func safeZipResourceEntryName(res *ent.Resource) string {
	candidate := ""
	if res != nil {
		candidate = strings.TrimSpace(res.Path)
	}
	candidate = filepath.ToSlash(filepath.Clean(strings.ReplaceAll(candidate, "\\", "/")))
	if candidate == "" || candidate == "." || candidate == ".." || strings.HasPrefix(candidate, "../") || strings.HasPrefix(candidate, "/") {
		return "resource"
	}
	parts := strings.Split(candidate, "/")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." || part == ".." {
			parts[i] = "resource"
			continue
		}
		parts[i] = strings.NewReplacer(":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_").Replace(part)
	}
	return strings.Join(parts, "/")
}

func toTranslationJobResponse(row *ent.TranslationJob) translationJobResponse {
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
		CreatedAt:          row.CreatedAt.Format(timeRFC3339),
		UpdatedAt:          row.UpdatedAt.Format(timeRFC3339),
	}
	if row.Edges.Project != nil {
		resp.ProjectID = row.Edges.Project.ID
	}
	if len(row.Edges.JobResources) > 0 {
		resp.JobResources = make([]translationJobResourceResponse, 0, len(row.Edges.JobResources))
		for _, item := range row.Edges.JobResources {
			resp.JobResources = append(resp.JobResources, toTranslationJobResourceResponse(item))
		}
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
		CreatedAt:         row.CreatedAt.Format(timeRFC3339),
		UpdatedAt:         row.UpdatedAt.Format(timeRFC3339),
	}
	if row.Edges.Resource != nil {
		resp.ResourceID = row.Edges.Resource.ID
		resourceResp := toResourceResponse(row.Edges.Resource)
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
