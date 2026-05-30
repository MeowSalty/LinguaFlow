package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
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

	fileHeader, fileHeaderErr := r.MultipartForm.File["file"]
	if fileHeaderErr || len(fileHeader) == 0 {
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

// writeResourceServiceError 写入资源服务的错误响应。
func (s *Server) writeResourceServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, service.ErrProjectNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "项目不存在")
	case errors.Is(err, service.ErrResourceNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "资源不存在")
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
