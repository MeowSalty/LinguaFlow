package api

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/subjob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

const maxUploadedFiles = 32

type jobCreateResponse struct {
	JobID     int                 `json:"job_id"`
	Status    string              `json:"status"`
	SubJobs   []subJobSummaryItem `json:"sub_jobs"`
	CreatedAt string              `json:"created_at"`
}

type jobResponse struct {
	ID               int          `json:"id"`
	ProjectID        int          `json:"project_id"`
	Status           string       `json:"status"`
	SubJobCount      int          `json:"sub_job_count"`
	CompletedSubJobs int          `json:"completed_sub_jobs"`
	FailedSubJobs    int          `json:"failed_sub_jobs"`
	SourceLang       string       `json:"source_lang"`
	TargetLang       string       `json:"target_lang"`
	InputPath        string       `json:"input_path,omitempty"`
	OutputPath       string       `json:"output_path,omitempty"`
	ErrorMessage     *string      `json:"error_message,omitempty"`
	CreatedAt        string       `json:"created_at"`
	UpdatedAt        string       `json:"updated_at"`
	SubJobs          []subJobItem `json:"sub_jobs"`
}

type subJobSummaryItem struct {
	ID       int    `json:"id"`
	Filename string `json:"filename"`
	Status   string `json:"status"`
}

type subJobItem struct {
	ID            int     `json:"id"`
	Status        string  `json:"status"`
	InputFilename string  `json:"input_filename"`
	InputFormat   string  `json:"input_format"`
	InputPath     string  `json:"input_path"`
	OutputPath    string  `json:"output_path,omitempty"`
	SegmentCount  int     `json:"segment_count"`
	ErrorMessage  *string `json:"error_message,omitempty"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

func (s *Server) handleCreateProjectJob(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}
	projectRow, err := s.projectSvc.GetProject(r.Context(), authUser.User.ID, projectID)
	if err != nil {
		writeProjectServiceError(w, err)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_multipart", "上传表单解析失败")
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		writeProblem(w, http.StatusBadRequest, "invalid_input", "至少上传一个文件")
		return
	}
	if len(files) > maxUploadedFiles {
		writeProblem(w, http.StatusBadRequest, "invalid_input", "上传文件数量超出限制")
		return
	}
	sourceLang := strings.TrimSpace(r.FormValue("source_lang"))
	targetLang := strings.TrimSpace(r.FormValue("target_lang"))
	if sourceLang == "" {
		sourceLang = projectRow.SourceLang
	}
	if targetLang == "" {
		targetLang = projectRow.TargetLang
	}
	tx, err := s.entClient.Tx(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	createdJob, err := tx.Job.Create().
		SetProjectID(projectID).
		SetCreatedByID(authUser.User.ID).
		SetStatus(service.JobStatusPending).
		SetSourceLang(firstNonEmptyString(sourceLang, "auto")).
		SetTargetLang(firstNonEmptyString(targetLang, firstNonEmptyString(projectRow.TargetLang, "zh"))).
		SetSubJobCount(len(files)).
		Save(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	summaries := make([]subJobSummaryItem, 0, len(files))
	var firstInputPath string
	for _, header := range files {
		opened, openErr := header.Open()
		if openErr != nil {
			writeProblem(w, http.StatusBadRequest, "invalid_upload", "无法读取上传文件")
			return
		}
		sub, createErr := tx.SubJob.Create().
			SetJobID(createdJob.ID).
			SetStatus(service.SubJobStatusPending).
			SetInputFilename(header.Filename).
			SetInputFormat(strings.TrimPrefix(strings.ToLower(filepath.Ext(header.Filename)), ".")).
			Save(r.Context())
		if createErr != nil {
			_ = opened.Close()
			writeServiceError(w, createErr)
			return
		}
		ref, saveErr := s.jobStore.SaveUpload(r.Context(), createdJob.ID, sub.ID, header.Filename, opened)
		_ = opened.Close()
		if saveErr != nil {
			writeServiceError(w, saveErr)
			return
		}
		outputRef, outputErr := s.jobStore.PrepareOutput(createdJob.ID, sub.ID, header.Filename)
		if outputErr != nil {
			writeServiceError(w, outputErr)
			return
		}
		if err := tx.SubJob.UpdateOneID(sub.ID).
			SetInputPath(ref.RelativePath).
			SetOutputPath(outputRef.RelativePath).
			Exec(r.Context()); err != nil {
			writeServiceError(w, err)
			return
		}
		if firstInputPath == "" {
			firstInputPath = ref.RelativePath
		}
		summaries = append(summaries, subJobSummaryItem{ID: sub.ID, Filename: header.Filename, Status: service.SubJobStatusPending})
	}
	if err := tx.Job.UpdateOneID(createdJob.ID).SetInputPath(firstInputPath).Exec(r.Context()); err != nil {
		writeServiceError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeServiceError(w, err)
		return
	}
	committed = true
	if err := s.jobQueue.Enqueue(r.Context(), createdJob.ID); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, jobCreateResponse{JobID: createdJob.ID, Status: service.JobStatusPending, SubJobs: summaries, CreatedAt: createdJob.CreatedAt.Format(timeRFC3339)})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobRow, ok := s.loadAuthorizedJob(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toJobResponse(jobRow))
}

func (s *Server) handleListJobSubJobs(w http.ResponseWriter, r *http.Request) {
	jobRow, ok := s.loadAuthorizedJob(w, r)
	if !ok {
		return
	}
	items := make([]subJobItem, 0, len(jobRow.Edges.SubJobs))
	for _, item := range jobRow.Edges.SubJobs {
		items = append(items, toSubJobItem(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGetJobSubJob(w http.ResponseWriter, r *http.Request) {
	jobRow, ok := s.loadAuthorizedJob(w, r)
	if !ok {
		return
	}
	subJobID, ok := parseIntParam(w, chi.URLParam(r, "subJobId"), "subJobId")
	if !ok {
		return
	}
	for _, item := range jobRow.Edges.SubJobs {
		if item.ID == subJobID {
			writeJSON(w, http.StatusOK, toSubJobItem(item))
			return
		}
	}
	writeProblem(w, http.StatusNotFound, "not_found", "子任务不存在")
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	jobRow, ok := s.loadAuthorizedJob(w, r)
	if !ok {
		return
	}
	if err := s.entClient.SubJob.Update().
		Where(subjob.HasJobWith(job.IDEQ(jobRow.ID)), subjob.StatusIn(service.SubJobStatusPending, service.SubJobStatusRunning)).
		SetStatus(service.SubJobStatusCancelled).
		Exec(r.Context()); err != nil {
		writeServiceError(w, err)
		return
	}
	if err := s.entClient.Job.UpdateOneID(jobRow.ID).SetStatus(service.JobStatusCancelled).Exec(r.Context()); err != nil {
		writeServiceError(w, err)
		return
	}
	reloaded, err := s.queryJobForResponse(r.Context(), jobRow.ID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toJobResponse(reloaded))
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	jobRow, ok := s.loadAuthorizedJob(w, r)
	if !ok {
		return
	}
	if err := s.entClient.SubJob.Update().
		Where(subjob.HasJobWith(job.IDEQ(jobRow.ID)), subjob.StatusEQ(service.SubJobStatusFailed)).
		SetStatus(service.SubJobStatusPending).
		ClearErrorMessage().
		Exec(r.Context()); err != nil {
		writeServiceError(w, err)
		return
	}
	if err := s.entClient.Job.UpdateOneID(jobRow.ID).
		SetStatus(service.JobStatusPending).
		SetFailedSubJobs(0).
		ClearErrorMessage().
		Exec(r.Context()); err != nil {
		writeServiceError(w, err)
		return
	}
	if err := s.jobQueue.Enqueue(r.Context(), jobRow.ID); err != nil {
		writeServiceError(w, err)
		return
	}
	reloaded, err := s.queryJobForResponse(r.Context(), jobRow.ID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toJobResponse(reloaded))
}

func (s *Server) handleDownloadJobResult(w http.ResponseWriter, r *http.Request) {
	jobRow, ok := s.loadAuthorizedJob(w, r)
	if !ok {
		return
	}
	ready := make([]*ent.SubJob, 0, len(jobRow.Edges.SubJobs))
	for _, item := range jobRow.Edges.SubJobs {
		if item.Status == service.SubJobStatusCompleted && strings.TrimSpace(item.OutputPath) != "" {
			ready = append(ready, item)
		}
	}
	if len(ready) == 0 {
		writeProblem(w, http.StatusConflict, "result_not_ready", "当前没有可下载输出")
		return
	}
	if len(ready) == 1 {
		s.downloadSingleOutput(w, r, ready[0])
		return
	}
	s.downloadZipOutputs(w, r, jobRow.ID, ready)
}

func (s *Server) loadAuthorizedJob(w http.ResponseWriter, r *http.Request) (*ent.Job, bool) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return nil, false
	}
	jobID, ok := parseIntParam(w, chi.URLParam(r, "jobId"), "jobId")
	if !ok {
		return nil, false
	}
	jobRow, err := s.queryJobForResponse(r.Context(), jobID)
	if err != nil {
		if ent.IsNotFound(err) {
			writeProblem(w, http.StatusNotFound, "not_found", "任务不存在")
			return nil, false
		}
		writeServiceError(w, err)
		return nil, false
	}
	projectRow, err := jobRow.Edges.ProjectOrErr()
	if err != nil {
		writeServiceError(w, err)
		return nil, false
	}
	if _, err := s.projectSvc.GetProject(r.Context(), authUser.User.ID, projectRow.ID); err != nil {
		writeProjectServiceError(w, err)
		return nil, false
	}
	return jobRow, true
}

func (s *Server) queryJobForResponse(ctx context.Context, jobID int) (*ent.Job, error) {
	return s.entClient.Job.Query().
		Where(job.IDEQ(jobID)).
		WithProject().
		WithSubJobs(func(q *ent.SubJobQuery) {
			q.Order(ent.Asc(subjob.FieldID))
		}).
		Only(ctx)
}

func (s *Server) downloadSingleOutput(w http.ResponseWriter, _ *http.Request, item *ent.SubJob) {
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

func (s *Server) downloadZipOutputs(w http.ResponseWriter, _ *http.Request, jobID int, items []*ent.SubJob) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fmt.Sprintf("job-%d-results.zip", jobID)))
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
		entry, err := zw.Create(filepath.Base(absolutePath))
		if err != nil {
			_ = file.Close()
			continue
		}
		_, _ = io.Copy(entry, file)
		_ = file.Close()
	}
}

func toJobResponse(entity *ent.Job) jobResponse {
	items := make([]subJobItem, 0, len(entity.Edges.SubJobs))
	for _, item := range entity.Edges.SubJobs {
		items = append(items, toSubJobItem(item))
	}
	projectID := 0
	if entity.Edges.Project != nil {
		projectID = entity.Edges.Project.ID
	}
	return jobResponse{
		ID:               entity.ID,
		ProjectID:        projectID,
		Status:           entity.Status,
		SubJobCount:      entity.SubJobCount,
		CompletedSubJobs: entity.CompletedSubJobs,
		FailedSubJobs:    entity.FailedSubJobs,
		SourceLang:       entity.SourceLang,
		TargetLang:       entity.TargetLang,
		InputPath:        entity.InputPath,
		OutputPath:       entity.OutputPath,
		ErrorMessage:     entity.ErrorMessage,
		CreatedAt:        entity.CreatedAt.Format(timeRFC3339),
		UpdatedAt:        entity.UpdatedAt.Format(timeRFC3339),
		SubJobs:          items,
	}
}

func toSubJobItem(entity *ent.SubJob) subJobItem {
	return subJobItem{
		ID:            entity.ID,
		Status:        entity.Status,
		InputFilename: entity.InputFilename,
		InputFormat:   entity.InputFormat,
		InputPath:     entity.InputPath,
		OutputPath:    entity.OutputPath,
		SegmentCount:  entity.SegmentCount,
		ErrorMessage:  entity.ErrorMessage,
		CreatedAt:     entity.CreatedAt.Format(timeRFC3339),
		UpdatedAt:     entity.UpdatedAt.Format(timeRFC3339),
	}
}

const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
