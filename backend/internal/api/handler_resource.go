package api

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

const maxResourceUploadFiles = 50

type resourceResponse struct {
	ID            int     `json:"id"`
	Filename      string  `json:"filename"`
	Format        string  `json:"format"`
	TotalSegments int     `json:"total_segments"`
	Status        string  `json:"status"`
	ErrorMessage  *string `json:"error_message,omitempty"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

func toResourceResponse(r *ent.Resource) resourceResponse {
	return resourceResponse{
		ID:            r.ID,
		Filename:      r.Filename,
		Format:        r.Format,
		TotalSegments: r.TotalSegments,
		Status:        r.Status,
		ErrorMessage:  r.ErrorMessage,
		CreatedAt:     r.CreatedAt.Format(timeRFC3339),
		UpdatedAt:     r.UpdatedAt.Format(timeRFC3339),
	}
}

func toResourceListResponse(resources []*ent.Resource) map[string]any {
	items := make([]resourceResponse, 0, len(resources))
	for _, r := range resources {
		items = append(items, toResourceResponse(r))
	}
	return map[string]any{"items": items}
}

// handleUploadProjectResources 处理上传资源文件到项目。
func (s *Server) handleUploadProjectResources(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
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
	if len(files) > maxResourceUploadFiles {
		writeProblem(w, http.StatusBadRequest, "invalid_input", fmt.Sprintf("上传文件数量超出限制（最多 %d 个）", maxResourceUploadFiles))
		return
	}

	// 检测同名文件冲突
	for _, header := range files {
		existing, err := s.resourceSvc.FindResourceByFilename(r.Context(), projectID, header.Filename)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "internal_error", "检查文件冲突失败")
			return
		}
		if existing != nil {
			writeJSON(w, http.StatusConflict, map[string]any{
				"existing_resource": toResourceResponse(existing),
			})
			return
		}
	}

	uploaded := make([]service.UploadedFile, 0, len(files))
	for _, header := range files {
		opened, err := header.Open()
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "invalid_upload", "无法读取上传文件")
			return
		}
		uploaded = append(uploaded, service.UploadedFile{
			Filename: header.Filename,
			Size:     header.Size,
			Reader:   opened,
		})
	}

	// 确保关闭所有打开的文件
	defer func() {
		for _, f := range uploaded {
			if closer, ok := f.Reader.(io.Closer); ok {
				_ = closer.Close()
			}
		}
	}()

	results, err := s.resourceSvc.UploadResources(r.Context(), authUser.User.ID, projectID, uploaded)
	if err != nil {
		s.writeResourceServiceError(w, r, err)
		return
	}

	items := make([]resourceResponse, 0, len(results))
	for _, result := range results {
		items = append(items, toResourceResponse(result.Resource))
	}
	writeJSON(w, http.StatusCreated, map[string]any{"items": items})
}

// handleListProjectResources 处理列出项目资源。
func (s *Server) handleListProjectResources(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}

	opts := service.ResourceListOptions{
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Format: strings.TrimSpace(r.URL.Query().Get("format")),
		Search: strings.TrimSpace(r.URL.Query().Get("search")),
		Limit:  100,
	}

	resources, err := s.resourceSvc.ListResources(r.Context(), authUser.User.ID, projectID, opts)
	if err != nil {
		s.writeResourceServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, toResourceListResponse(resources))
}

// handleGetResource 处理获取资源详情。
func (s *Server) handleGetResource(w http.ResponseWriter, r *http.Request) {
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

	res, err := s.resourceSvc.GetResource(r.Context(), authUser.User.ID, projectID, resourceID)
	if err != nil {
		s.writeResourceServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, toResourceResponse(res))
}

// handleUpdateResource 处理更新资源文件。
func (s *Server) handleUpdateResource(w http.ResponseWriter, r *http.Request) {
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

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_multipart", "上传表单解析失败")
		return
	}

	fileHeader, fileFieldOK := r.MultipartForm.File["file"]
	if !fileFieldOK || len(fileHeader) == 0 {
		writeProblem(w, http.StatusBadRequest, "invalid_input", "请上传一个文件")
		return
	}

	header := fileHeader[0]
	opened, openErr := header.Open()
	if openErr != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_upload", "无法读取上传文件")
		return
	}
	defer opened.Close()

	res, err := s.resourceSvc.UpdateResource(r.Context(), authUser.User.ID, projectID, resourceID, service.UploadedFile{
		Filename: header.Filename,
		Size:     header.Size,
		Reader:   opened,
	})
	if err != nil {
		s.writeResourceServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, toResourceResponse(res))
}

// handleDeleteResource 处理删除资源文件。
func (s *Server) handleDeleteResource(w http.ResponseWriter, r *http.Request) {
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

	if err := s.resourceSvc.DeleteResource(r.Context(), authUser.User.ID, projectID, resourceID); err != nil {
		s.writeResourceServiceError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleDownloadResourceFile 处理下载资源原始文件。
func (s *Server) handleDownloadResourceFile(w http.ResponseWriter, r *http.Request) {
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

	res, err := s.resourceSvc.GetResource(r.Context(), authUser.User.ID, projectID, resourceID)
	if err != nil {
		s.writeResourceServiceError(w, r, err)
		return
	}

	if res.StoragePath == "" {
		writeProblem(w, http.StatusNotFound, "file_not_found", "资源文件不存在")
		return
	}

	absolutePath, absErr := s.resourceSvc.Absolute(res.StoragePath)
	if absErr != nil {
		writeServiceError(w, absErr)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(absolutePath)))
	http.ServeFile(w, r, absolutePath)
}

// handleIncrementalUpdateResource 处理增量更新资源文件。
func (s *Server) handleIncrementalUpdateResource(w http.ResponseWriter, r *http.Request) {
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

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.logResourceMultipartDebug(r, "parse incremental update multipart form failed", "err", err)
		writeProblem(w, http.StatusBadRequest, "invalid_multipart", "上传表单解析失败")
		return
	}

	s.logResourceMultipartDebug(r, "parsed incremental update multipart form")

	fileHeader, fileFieldOK := r.MultipartForm.File["file"]
	if !fileFieldOK || len(fileHeader) == 0 {
		s.logResourceMultipartDebug(r, "incremental update multipart form missing file field", "file_field_exists", fileFieldOK, "file_field_count", len(fileHeader))
		writeProblem(w, http.StatusBadRequest, "invalid_input", "请上传一个文件")
		return
	}

	header := fileHeader[0]
	s.logResourceMultipartDebug(r, "incremental update selected uploaded file", "filename", header.Filename, "size", header.Size, "content_type", header.Header.Get("Content-Type"))
	opened, openErr := header.Open()
	if openErr != nil {
		s.logResourceMultipartDebug(r, "open incremental update uploaded file failed", "filename", header.Filename, "size", header.Size, "err", openErr)
		writeProblem(w, http.StatusBadRequest, "invalid_upload", "无法读取上传文件")
		return
	}
	defer opened.Close()

	res, stats, err := s.resourceSvc.IncrementalUpdateResource(r.Context(), authUser.User.ID, projectID, resourceID, service.UploadedFile{
		Filename: header.Filename,
		Size:     header.Size,
		Reader:   opened,
	})
	if err != nil {
		s.writeResourceServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"resource": toResourceResponse(res),
		"changes": map[string]int{
			"added":     stats.Added,
			"updated":   stats.Updated,
			"unchanged": stats.Unchanged,
			"deleted":   stats.Deleted,
		},
	})
}

func (s *Server) logResourceMultipartDebug(r *http.Request, message string, attrs ...any) {
	fields := []any{
		"request_id", chimiddleware.GetReqID(r.Context()),
		"method", r.Method,
		"path", r.URL.Path,
		"query", r.URL.RawQuery,
		"content_type", r.Header.Get("Content-Type"),
		"content_length", r.ContentLength,
		"transfer_encoding", strings.Join(r.TransferEncoding, ","),
	}

	if r.MultipartForm == nil {
		fields = append(fields, "multipart_form_present", false)
	} else {
		fields = append(fields,
			"multipart_form_present", true,
			"multipart_value_fields", summarizeMultipartValueFields(r.MultipartForm.Value),
			"multipart_file_fields", summarizeMultipartFileFields(r.MultipartForm.File),
		)
	}

	fields = append(fields, attrs...)
	s.logger.Debug("resource multipart debug: "+message, fields...)
}

func summarizeMultipartValueFields(values map[string][]string) []string {
	if len(values) == 0 {
		return nil
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	summaries := make([]string, 0, len(keys))
	for _, key := range keys {
		summaries = append(summaries, fmt.Sprintf("%s[count=%d]", key, len(values[key])))
	}
	return summaries
}

func summarizeMultipartFileFields(files map[string][]*multipart.FileHeader) []string {
	if len(files) == 0 {
		return nil
	}

	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	summaries := make([]string, 0, len(keys))
	for _, key := range keys {
		parts := make([]string, 0, len(files[key]))
		for _, header := range files[key] {
			parts = append(parts, fmt.Sprintf("filename=%q size=%d content_type=%q", header.Filename, header.Size, header.Header.Get("Content-Type")))
		}
		summaries = append(summaries, fmt.Sprintf("%s[count=%d files=[%s]]", key, len(files[key]), strings.Join(parts, "; ")))
	}
	return summaries
}

// writeResourceServiceError 写入资源服务的错误响应。
func (s *Server) writeResourceServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, service.ErrProjectNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "项目不存在")
	case errors.Is(err, service.ErrResourceNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, service.ErrResourceAlreadyExists):
		writeProblem(w, http.StatusConflict, "conflict", "项目中已存在同名文件")
	case errors.Is(err, service.ErrForbidden):
		writeProblem(w, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrUnsupportedFormat):
		writeProblem(w, http.StatusBadRequest, "unsupported_format", err.Error())
	case errors.Is(err, service.ErrParseFailed):
		writeProblem(w, http.StatusBadRequest, "parse_failed", err.Error())
	default:
		s.logger.Error("resource service error",
			"request_id", chimiddleware.GetReqID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"err", err,
		)
		writeProblem(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
	}
}
