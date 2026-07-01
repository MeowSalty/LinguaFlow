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
	ID                 int    `json:"id"`
	Path               string `json:"path"`
	Name               string `json:"name"`
	Directory          string `json:"directory"`
	Format             string `json:"format"`
	TotalSegments      int    `json:"total_segments"`
	TranslatedSegments int    `json:"translated_segments"`
	ApprovedSegments   int    `json:"approved_segments"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
}

func toResourceResponse(r *ent.Resource, translated, approved int) resourceResponse {
	pathValue := resourceResponsePath(r)
	return resourceResponse{
		ID:                 r.ID,
		Path:               pathValue,
		Name:               resourceResponseName(pathValue),
		Directory:          resourceResponseDirectory(pathValue),
		Format:             r.Format,
		TotalSegments:      r.TotalSegments,
		TranslatedSegments: translated,
		ApprovedSegments:   approved,
		CreatedAt:          r.CreatedAt.Format(timeRFC3339),
		UpdatedAt:          r.UpdatedAt.Format(timeRFC3339),
	}
}

// toGeneratedResource 转换 ent.Resource 为 OpenAPI 生成的 Resource 类型。
func toGeneratedResource(r *ent.Resource, translated, approved int) Resource {
	pathValue := resourceResponsePath(r)
	return Resource{
		Id:                 r.ID,
		Path:               pathValue,
		Name:               resourceResponseName(pathValue),
		Directory:          resourceResponseDirectory(pathValue),
		Format:             r.Format,
		TotalSegments:      r.TotalSegments,
		TranslatedSegments: translated,
		ApprovedSegments:   approved,
		CreatedAt:          r.CreatedAt,
		UpdatedAt:          r.UpdatedAt,
	}
}

func resourceResponsePath(r *ent.Resource) string {
	return strings.TrimSpace(r.Path)
}

func resourceResponseName(resourcePath string) string {
	name := filepath.Base(strings.ReplaceAll(resourcePath, "\\", "/"))
	if name == "." || name == string(filepath.Separator) || strings.TrimSpace(name) == "" {
		return "resource"
	}
	return name
}

func resourceResponseDirectory(resourcePath string) string {
	dir := filepath.ToSlash(filepath.Dir(strings.ReplaceAll(resourcePath, "\\", "/")))
	if dir == "." {
		return ""
	}
	return dir
}

func toResourceListResponse(resources []service.ResourceWithProgress) map[string]any {
	items := make([]resourceResponse, 0, len(resources))
	for _, r := range resources {
		items = append(items, toResourceResponse(r.Resource, r.TranslatedSegments, r.ApprovedSegments))
	}
	return map[string]any{"items": items}
}

// handleUploadProjectResources 处理上传资源文件到项目。
// 允许部分成功：冲突或失败的文件不会阻断其他文件的上传。
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

	paths := r.MultipartForm.Value["paths"]
	if len(paths) > 0 && len(paths) != len(files) {
		writeProblem(w, http.StatusBadRequest, "invalid_input", "paths 数量必须与 files 数量一致")
		return
	}

	uploaded := make([]service.UploadedFile, 0, len(files))
	for i, header := range files {
		candidatePath := header.Filename
		if len(paths) > 0 {
			candidatePath = paths[i]
		}
		opened, err := header.Open()
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "invalid_upload", "无法读取上传文件")
			return
		}
		uploaded = append(uploaded, service.UploadedFile{
			Filename: header.Filename,
			Path:     candidatePath,
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

	resourceIDs := make([]int, 0)
	for _, result := range results {
		switch result.Action {
		case "created":
			if result.Resource != nil {
				resourceIDs = append(resourceIDs, result.Resource.ID)
			}
		case "conflict":
			if result.ExistingResource != nil {
				resourceIDs = append(resourceIDs, result.ExistingResource.ID)
			}
		}
	}
	progressMap, _ := s.resourceSvc.ListResourcesProgress(r.Context(), resourceIDs)

	respItems := make([]ResourceUploadFileResult, 0, len(results))
	for _, result := range results {
		item := ResourceUploadFileResult{
			Path: result.Path,
		}
		switch result.Action {
		case "created":
			item.Action = ResourceUploadFileResultActionCreated
			if result.Resource != nil {
				p := progressMap[result.Resource.ID]
				translated, approved := 0, 0
				if p != nil {
					translated, approved = p.Translated, p.Approved
				}
				gr := toGeneratedResource(result.Resource, translated, approved)
				item.Resource = &gr
			}
		case "conflict":
			item.Action = ResourceUploadFileResultActionConflict
			if result.ExistingResource != nil {
				p := progressMap[result.ExistingResource.ID]
				translated, approved := 0, 0
				if p != nil {
					translated, approved = p.Translated, p.Approved
				}
				gr := toGeneratedResource(result.ExistingResource, translated, approved)
				item.ExistingResource = &gr
			}
		case "failed":
			item.Action = ResourceUploadFileResultActionFailed
			if result.Error != "" {
				item.Error = &result.Error
			}
		}
		respItems = append(respItems, item)
	}
	writeJSON(w, http.StatusOK, ResourceUploadBatchResponse{Items: respItems})
}

// handlePrecheckProjectResources 处理资源上传预检。
// 检查批量路径中的冲突情况，不执行任何写入操作。
func (s *Server) handlePrecheckProjectResources(w http.ResponseWriter, r *http.Request) {
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
		writeProblem(w, http.StatusBadRequest, "invalid_multipart", "表单解析失败")
		return
	}

	paths := r.MultipartForm.Value["paths"]
	if len(paths) == 0 {
		writeProblem(w, http.StatusBadRequest, "invalid_input", "至少提供一个路径")
		return
	}
	if len(paths) > maxResourceUploadFiles {
		writeProblem(w, http.StatusBadRequest, "invalid_input", fmt.Sprintf("路径数量超出限制（最多 %d 个）", maxResourceUploadFiles))
		return
	}

	results, err := s.resourceSvc.PrecheckResources(r.Context(), authUser.User.ID, projectID, paths)
	if err != nil {
		s.writeResourceServiceError(w, r, err)
		return
	}

	resourceIDs := make([]int, 0)
	for _, result := range results {
		if result.Action == "conflict" && result.ExistingResource != nil {
			resourceIDs = append(resourceIDs, result.ExistingResource.ID)
		}
	}
	progressMap, _ := s.resourceSvc.ListResourcesProgress(r.Context(), resourceIDs)

	respItems := make([]ResourcePrecheckFileResult, 0, len(results))
	for _, result := range results {
		item := ResourcePrecheckFileResult{
			Path: result.Path,
		}
		switch result.Action {
		case "create":
			item.Action = Create
		case "conflict":
			item.Action = Conflict
			if result.ExistingResource != nil {
				p := progressMap[result.ExistingResource.ID]
				translated, approved := 0, 0
				if p != nil {
					translated, approved = p.Translated, p.Approved
				}
				gr := toGeneratedResource(result.ExistingResource, translated, approved)
				item.ExistingResource = &gr
			}
		case "duplicate":
			item.Action = Duplicate
		}
		respItems = append(respItems, item)
	}
	writeJSON(w, http.StatusOK, ResourcePrecheckBatchResponse{Items: respItems})
}

type resourceTreeNodeResponse struct {
	Type     string                      `json:"type"`
	Name     string                      `json:"name"`
	Path     string                      `json:"path"`
	Resource *resourceResponse           `json:"resource,omitempty"`
	Children []*resourceTreeNodeResponse `json:"children,omitempty"`
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

// handleGetProjectResourceTree 处理获取项目资源目录树。
func (s *Server) handleGetProjectResourceTree(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	projectID, ok := parseIntParam(w, chi.URLParam(r, "projectId"), "projectId")
	if !ok {
		return
	}

	resources, err := s.resourceSvc.ListResources(r.Context(), authUser.User.ID, projectID, service.ResourceListOptions{})
	if err != nil {
		s.writeResourceServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"root": buildResourceTreeResponse(resources)})
}

func buildResourceTreeResponse(resources []service.ResourceWithProgress) *resourceTreeNodeResponse {
	root := &resourceTreeNodeResponse{Type: string(ResourceTreeNodeTypeDirectory), Name: "", Path: ""}
	for _, row := range resources {
		resourceResp := toResourceResponse(row.Resource, row.TranslatedSegments, row.ApprovedSegments)
		parts := strings.Split(resourceResp.Path, "/")
		current := root
		for i, part := range parts {
			currentPath := strings.Join(parts[:i+1], "/")
			if i == len(parts)-1 {
				resp := resourceResp
				current.Children = append(current.Children, &resourceTreeNodeResponse{
					Type:     string(ResourceTreeNodeTypeResource),
					Name:     resourceResp.Name,
					Path:     resourceResp.Path,
					Resource: &resp,
				})
				continue
			}
			child := findResourceTreeDirectory(current.Children, currentPath)
			if child == nil {
				child = &resourceTreeNodeResponse{Type: string(ResourceTreeNodeTypeDirectory), Name: part, Path: currentPath}
				current.Children = append(current.Children, child)
			}
			current = child
		}
	}
	sortResourceTreeChildren(root)
	return root
}

func findResourceTreeDirectory(children []*resourceTreeNodeResponse, resourcePath string) *resourceTreeNodeResponse {
	for _, child := range children {
		if child.Type == string(ResourceTreeNodeTypeDirectory) && child.Path == resourcePath {
			return child
		}
	}
	return nil
}

func sortResourceTreeChildren(node *resourceTreeNodeResponse) {
	sort.SliceStable(node.Children, func(i, j int) bool {
		left, right := node.Children[i], node.Children[j]
		if left.Type != right.Type {
			return left.Type == string(ResourceTreeNodeTypeDirectory)
		}
		return left.Name < right.Name
	})
	for _, child := range node.Children {
		sortResourceTreeChildren(child)
	}
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

	writeJSON(w, http.StatusOK, toResourceResponse(res.Resource, res.TranslatedSegments, res.ApprovedSegments))
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

	progress, err := s.resourceSvc.GetResourceProgress(r.Context(), projectID, resourceID)
	if err != nil {
		s.writeResourceServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, toResourceResponse(res, progress.Translated, progress.Approved))
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

	if res.Resource.StoragePath == "" {
		writeProblem(w, http.StatusNotFound, "file_not_found", "资源文件不存在")
		return
	}

	absolutePath, absErr := s.resourceSvc.Absolute(res.Resource.StoragePath)
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

	progress, err := s.resourceSvc.GetResourceProgress(r.Context(), projectID, resourceID)
	if err != nil {
		s.writeResourceServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"resource": toResourceResponse(res, progress.Translated, progress.Approved),
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
		writeProblem(w, http.StatusConflict, "conflict", "项目中已存在同路径资源")
	case errors.Is(err, service.ErrResourcePathInvalid):
		writeProblem(w, http.StatusBadRequest, "invalid_resource_path", "资源路径不合法")
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

// safeZipResourceEntryName 根据资源路径生成安全的 ZIP 条目名称。
// 过滤路径遍历、绝对路径及文件系统特殊字符。
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

// handleDownloadTranslatedResourceFile 处理下载资源翻译结果文件。
func (s *Server) handleDownloadTranslatedResourceFile(w http.ResponseWriter, r *http.Request) {
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

	filename := safeZipResourceEntryName(res.Resource)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	// 注意：Content-Type 和 Content-Disposition 在渲染前设置。
	// 如果 Render 已向 w 写入部分数据，后续 header 修改可能无效（headers 已 flush）。
	// 这与现有 downloadSingleTranslationOutput 的行为一致。

	if err := s.resourceSvc.RenderTranslatedResource(r.Context(), authUser.User.ID, res.Resource, w); err != nil {
		w.Header().Del("Content-Disposition")
		w.Header().Del("Content-Type")
		switch {
		case errors.Is(err, service.ErrNoTranslatedSegments):
			writeProblem(w, http.StatusConflict, "no_translated_segments", "资源没有已翻译的段落")
		case errors.Is(err, service.ErrResourceNotFound):
			writeProblem(w, http.StatusNotFound, "not_found", "资源不存在")
		default:
			s.writeResourceServiceError(w, r, err)
		}
	}
}
