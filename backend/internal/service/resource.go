package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
)

const (
	ResourceStatusReady      = "ready"
	ResourceStatusProcessing = "processing"
	ResourceStatusError      = "error"
)

var (
	ErrResourceNotFound   = errors.New("resource not found")
	ErrResourceProcessing = errors.New("resource is still processing")
)

type ResourceService struct {
	client    *ent.Client
	projects  *ProjectService
	fileStore *filestore.LocalStore
}

type ResourceUploadResult struct {
	Resource      *ent.Resource
	TotalSegments int
}

func NewResourceService(client *ent.Client, projects *ProjectService, fileStore *filestore.LocalStore) *ResourceService {
	return &ResourceService{
		client:    client,
		projects:  projects,
		fileStore: fileStore,
	}
}

// UploadResources 上传资源文件到项目。
// 保存文件、解析段落、创建 Resource 和 Segment 记录。
func (s *ResourceService) UploadResources(ctx context.Context, actorUserID, projectID int, files []UploadedFile) ([]ResourceUploadResult, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return nil, err
	}

	results := make([]ResourceUploadResult, 0, len(files))
	for _, f := range files {
		result, err := s.uploadSingleResource(ctx, projectID, f)
		if err != nil {
			return nil, err
		}
		results = append(results, *result)
	}
	return results, nil
}

// UploadedFile 上传文件的抽象。
type UploadedFile struct {
	Filename string
	Size     int64
	Reader   io.Reader
}

func (s *ResourceService) uploadSingleResource(ctx context.Context, projectID int, file UploadedFile) (*ResourceUploadResult, error) {
	cleanName := sanitizeFilename(file.Filename)
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(cleanName)), ".")

	// 先创建 Resource 记录（status = processing）
	res, err := s.client.Resource.Create().
		SetFilename(cleanName).
		SetFormat(format).
		SetStoragePath(""). // 稍后更新
		SetStatus(ResourceStatusProcessing).
		SetNillableProjectID(&projectID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("resource: create record: %w", err)
	}

	// 保存文件到存储
	relPath := s.buildResourcePath(projectID, res.ID, cleanName)
	if err := s.fileStore.Write(ctx, relPath, file.Reader); err != nil {
		// 更新状态为 error
		_, _ = s.client.Resource.UpdateOneID(res.ID).
			SetStatus(ResourceStatusError).
			SetErrorMessage(fmt.Sprintf("file save failed: %v", err)).
			Save(ctx)
		return nil, fmt.Errorf("resource: save file: %w", err)
	}

	// 解析文件并创建 Resource 级段落
	parsedSegments, parseErr := s.parseResourceSegments(relPath)
	segmentCount := len(parsedSegments)
	if parseErr == nil {
		if err := s.replaceResourceSegments(ctx, res.ID, parsedSegments); err != nil {
			return nil, fmt.Errorf("resource: create segments: %w", err)
		}
	}

	// 更新 Resource 记录
	update := s.client.Resource.UpdateOneID(res.ID).
		SetStoragePath(relPath).
		SetTotalSegments(segmentCount)
	if parseErr != nil {
		update = update.
			SetStatus(ResourceStatusError).
			SetErrorMessage(fmt.Sprintf("parse failed: %v", parseErr))
	} else {
		update = update.
			SetStatus(ResourceStatusReady).
			ClearErrorMessage()
	}
	updated, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("resource: update record: %w", err)
	}

	return &ResourceUploadResult{
		Resource:      updated,
		TotalSegments: segmentCount,
	}, parseErr
}

// ListResources 列出项目中的资源文件。
func (s *ResourceService) ListResources(ctx context.Context, actorUserID, projectID int, opts ResourceListOptions) ([]*ent.Resource, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, false); err != nil {
		return nil, err
	}

	q := s.client.Resource.Query().
		Where(resource.ProjectID(projectID))

	if opts.Status != "" {
		q = q.Where(resource.StatusEQ(opts.Status))
	}
	if opts.Format != "" {
		q = q.Where(resource.FormatEQ(opts.Format))
	}
	if opts.Search != "" {
		q = q.Where(resource.FilenameContains(opts.Search))
	}

	q = q.Order(ent.Asc(resource.FieldID))

	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}

	return q.All(ctx)
}

// GetResource 获取单个资源详情。
func (s *ResourceService) GetResource(ctx context.Context, actorUserID, projectID, resourceID int) (*ent.Resource, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, false); err != nil {
		return nil, err
	}

	res, err := s.client.Resource.Query().
		Where(resource.ID(resourceID), resource.ProjectID(projectID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrResourceNotFound
		}
		return nil, err
	}
	return res, nil
}

// DeleteResource 删除资源文件及其存储。
func (s *ResourceService) DeleteResource(ctx context.Context, actorUserID, projectID, resourceID int) error {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return err
	}

	res, err := s.client.Resource.Query().
		Where(resource.ID(resourceID), resource.ProjectID(projectID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrResourceNotFound
		}
		return err
	}

	// 删除存储文件
	if res.StoragePath != "" {
		_ = s.fileStore.Delete(res.StoragePath)
	}

	// 删除数据库记录
	return s.client.Resource.DeleteOneID(res.ID).Exec(ctx)
}

// UpdateResource 替换资源文件内容。
func (s *ResourceService) UpdateResource(ctx context.Context, actorUserID, projectID, resourceID int, file UploadedFile) (*ent.Resource, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return nil, err
	}

	res, err := s.client.Resource.Query().
		Where(resource.ID(resourceID), resource.ProjectID(projectID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrResourceNotFound
		}
		return nil, err
	}

	cleanName := sanitizeFilename(file.Filename)
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(cleanName)), ".")

	// 删除旧文件
	if res.StoragePath != "" {
		_ = s.fileStore.Delete(res.StoragePath)
	}

	// 保存新文件
	relPath := s.buildResourcePath(projectID, res.ID, cleanName)
	if err := s.fileStore.Write(ctx, relPath, file.Reader); err != nil {
		_, _ = s.client.Resource.UpdateOneID(res.ID).
			SetStatus(ResourceStatusError).
			SetErrorMessage(fmt.Sprintf("file save failed: %v", err)).
			Save(ctx)
		return nil, fmt.Errorf("resource: save file: %w", err)
	}

	// 解析新文件并替换 Resource 级段落
	parsedSegments, parseErr := s.parseResourceSegments(relPath)
	segmentCount := len(parsedSegments)
	if parseErr == nil {
		if err := s.replaceResourceSegments(ctx, res.ID, parsedSegments); err != nil {
			return nil, fmt.Errorf("resource: replace segments: %w", err)
		}
	}

	update := s.client.Resource.UpdateOneID(res.ID).
		SetFilename(cleanName).
		SetFormat(format).
		SetStoragePath(relPath).
		SetTotalSegments(segmentCount)
	if parseErr != nil {
		update = update.
			SetStatus(ResourceStatusError).
			SetErrorMessage(fmt.Sprintf("parse failed: %v", parseErr))
	} else {
		update = update.
			SetStatus(ResourceStatusReady).
			ClearErrorMessage()
	}
	updated, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("resource: update record: %w", err)
	}

	return updated, parseErr
}

// Absolute 获取文件的绝对路径。
func (s *ResourceService) Absolute(relativePath string) (string, error) {
	return s.fileStore.Absolute(relativePath)
}

// buildResourcePath 构建资源文件的存储路径：resources/project-{id}/resource-{id}/{filename}
func (s *ResourceService) buildResourcePath(projectID, resourceID int, filename string) string {
	return filepath.Join("resources",
		fmt.Sprintf("project-%d", projectID),
		fmt.Sprintf("resource-%d", resourceID),
		filename,
	)
}

type parsedResourceSegment struct {
	Index      int
	SourceText string
	TargetText string
}

// parseResourceSegments 解析文件并返回 Resource 级段落。
func (s *ResourceService) parseResourceSegments(relPath string) ([]parsedResourceSegment, error) {
	absPath, err := s.fileStore.Absolute(relPath)
	if err != nil {
		return nil, err
	}

	p, err := parser.DetectByExt(absPath)
	if err != nil {
		return nil, fmt.Errorf("unsupported format: %w", err)
	}

	f, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	doc, err := p.Parse(context.Background(), f)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	items := make([]parsedResourceSegment, 0, len(doc.Segments))
	for i, item := range doc.Segments {
		sourceText := strings.TrimSpace(item.OriginalSource)
		if sourceText == "" {
			sourceText = strings.TrimSpace(item.Source)
		}
		if sourceText == "" {
			sourceText = " "
		}
		items = append(items, parsedResourceSegment{Index: i, SourceText: sourceText, TargetText: item.Target})
	}
	return items, nil
}

func (s *ResourceService) replaceResourceSegments(ctx context.Context, resourceID int, items []parsedResourceSegment) error {
	if _, err := s.client.Segment.Delete().Where(segment.ResourceIDEQ(resourceID)).Exec(ctx); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	builders := make([]*ent.SegmentCreate, 0, len(items))
	for _, item := range items {
		create := s.client.Segment.Create().
			SetResourceID(resourceID).
			SetSegmentIndex(item.Index).
			SetSourceText(item.SourceText).
			SetStatus(SegmentStatusPending)
		if strings.TrimSpace(item.TargetText) != "" {
			create.SetTargetText(item.TargetText).SetStatus(SegmentStatusTranslated)
		}
		builders = append(builders, create)
	}
	_, err := s.client.Segment.CreateBulk(builders...).Save(ctx)
	return err
}

// ResourceListOptions 资源列表查询选项。
type ResourceListOptions struct {
	Status string
	Format string
	Search string
	Limit  int
}

func sanitizeFilename(name string) string {
	base := strings.TrimSpace(filepath.Base(name))
	if base == "" || base == "." || base == ".." {
		base = "file"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	base = replacer.Replace(base)
	if strings.TrimSpace(base) == "" {
		return "file"
	}
	return base
}
