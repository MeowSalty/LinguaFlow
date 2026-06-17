package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	ResourceStatusReady = "ready"
	ResourceStatusError = "error"
)

// segmentBatchSize 每批插入的最大 Segment 数量。
// SQLite 默认绑定变量上限为 999，每条 Segment 记录约 4 个字段，
// 100 条 × 4 字段 = 400，安全低于限制。
const segmentBatchSize = 100

var (
	ErrResourceNotFound      = errors.New("resource not found")
	ErrResourceAlreadyExists = errors.New("resource already exists with same path")
	ErrResourcePathInvalid   = errors.New("resource path invalid")
	ErrUnsupportedFormat     = errors.New("unsupported file format")
	ErrParseFailed           = errors.New("file parse failed")
)

// SegmentChangeType 段落变更类型。
type SegmentChangeType string

const (
	SegmentChangeAdded     SegmentChangeType = "added"
	SegmentChangeUpdated   SegmentChangeType = "updated"
	SegmentChangeUnchanged SegmentChangeType = "unchanged"
	SegmentChangeDeleted   SegmentChangeType = "deleted"
)

// SegmentChange 段落变更详情。
type SegmentChange struct {
	ChangeType SegmentChangeType
	OldSegment *ent.Segment   // 旧段落（updated/unchanged/deleted 时有值）
	NewIndex   int            // 新文件中的索引
	NewSource  string         // 新源文本
	NewMeta    map[string]any // 新增：格式元数据
}

// IncrementalUpdateStats 增量更新统计信息。
type IncrementalUpdateStats struct {
	Added     int `json:"added"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Deleted   int `json:"deleted"`
}

type ResourceService struct {
	client    *ent.Client
	projects  *ProjectService
	fileStore *filestore.LocalStore
}

type ResourceUploadResult struct {
	Resource      *ent.Resource
	TotalSegments int
}

// UploadFileResult 单个文件的上传结果。
type UploadFileResult struct {
	Path             string
	Action           string // created, conflict, failed
	Resource         *ent.Resource
	ExistingResource *ent.Resource
	Error            string
}

// PrecheckFileResult 单个文件的预检结果。
type PrecheckFileResult struct {
	Path             string
	Action           string // create, conflict, duplicate
	ExistingResource *ent.Resource
}

func NewResourceService(client *ent.Client, projects *ProjectService, fileStore *filestore.LocalStore) *ResourceService {
	return &ResourceService{
		client:    client,
		projects:  projects,
		fileStore: fileStore,
	}
}

// UploadResources 上传资源文件到项目。
// 允许部分成功：冲突或失败的文件不会阻断其他文件的上传。
func (s *ResourceService) UploadResources(ctx context.Context, actorUserID, projectID int, files []UploadedFile) ([]UploadFileResult, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return nil, err
	}

	results := make([]UploadFileResult, 0, len(files))
	for _, f := range files {
		resourcePath, err := NormalizeResourcePath(firstNonEmpty(f.Path, f.Filename))
		if err != nil {
			results = append(results, UploadFileResult{
				Path:   firstNonEmpty(f.Path, f.Filename),
				Action: "failed",
				Error:  fmt.Sprintf("资源路径不合法: %v", err),
			})
			continue
		}

		// 检查路径冲突
		existing, err := s.FindResourceByPath(ctx, projectID, resourcePath)
		if err != nil {
			results = append(results, UploadFileResult{
				Path:   resourcePath,
				Action: "failed",
				Error:  fmt.Sprintf("检查资源路径冲突失败: %v", err),
			})
			continue
		}
		if existing != nil {
			results = append(results, UploadFileResult{
				Path:             resourcePath,
				Action:           "conflict",
				ExistingResource: existing,
			})
			continue
		}

		// 上传单个资源
		result, err := s.uploadSingleResource(ctx, projectID, f)
		if err != nil {
			results = append(results, UploadFileResult{
				Path:   resourcePath,
				Action: "failed",
				Error:  err.Error(),
			})
			continue
		}
		results = append(results, UploadFileResult{
			Path:     resourcePath,
			Action:   "created",
			Resource: result.Resource,
		})
	}
	return results, nil
}

// PrecheckResources 预检批量资源路径，检查冲突情况，不执行任何写入操作。
func (s *ResourceService) PrecheckResources(ctx context.Context, actorUserID, projectID int, paths []string) ([]PrecheckFileResult, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, false); err != nil {
		return nil, err
	}

	results := make([]PrecheckFileResult, 0, len(paths))
	seenPaths := make(map[string]struct{}, len(paths))

	for _, rawPath := range paths {
		resourcePath, err := NormalizeResourcePath(rawPath)
		if err != nil {
			results = append(results, PrecheckFileResult{
				Path:   rawPath,
				Action: "duplicate", // 路径不合法也标记为不可创建
			})
			continue
		}

		// 检查批次内重复
		if _, exists := seenPaths[resourcePath]; exists {
			results = append(results, PrecheckFileResult{
				Path:   resourcePath,
				Action: "duplicate",
			})
			continue
		}
		seenPaths[resourcePath] = struct{}{}

		// 检查与已有资源冲突
		existing, err := s.FindResourceByPath(ctx, projectID, resourcePath)
		if err != nil {
			return nil, fmt.Errorf("检查资源路径冲突失败: %w", err)
		}
		if existing != nil {
			results = append(results, PrecheckFileResult{
				Path:             resourcePath,
				Action:           "conflict",
				ExistingResource: existing,
			})
			continue
		}

		results = append(results, PrecheckFileResult{
			Path:   resourcePath,
			Action: "create",
		})
	}
	return results, nil
}

// UploadedFile 上传文件的抽象。
type UploadedFile struct {
	Filename string
	Path     string
	Size     int64
	Reader   io.Reader
}

func (s *ResourceService) uploadSingleResource(ctx context.Context, projectID int, file UploadedFile) (*ResourceUploadResult, error) {
	resourcePath, err := NormalizeResourcePath(firstNonEmpty(file.Path, file.Filename))
	if err != nil {
		return nil, err
	}
	cleanName := sanitizeFilename(pathBase(resourcePath))
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(cleanName)), ".")

	// 生成随机唯一 ID，用于构建存储路径
	uniqueID := generateUniqueID()
	relPath := s.buildResourcePath(projectID, uniqueID, resourcePath)

	// 保存文件到存储
	if err := s.fileStore.Write(ctx, relPath, file.Reader); err != nil {
		return nil, fmt.Errorf("resource: save file: %w", err)
	}

	// 解析文件段落
	parsedSegments, parseErr := s.parseResourceSegments(relPath)
	segmentCount := len(parsedSegments)

	// 事务包裹：Resource 创建 + Segment 插入，保证原子性
	status := ResourceStatusReady
	var errMsg *string
	if parseErr != nil {
		status = ResourceStatusError
		msg := fmt.Sprintf("parse failed: %v", parseErr)
		errMsg = &msg
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		_ = s.fileStore.Delete(relPath)
		return nil, fmt.Errorf("resource: begin transaction: %w", err)
	}

	res, err := tx.Resource.Create().
		SetPath(resourcePath).
		SetFormat(format).
		SetStoragePath(relPath).
		SetStatus(status).
		SetNillableProjectID(&projectID).
		SetTotalSegments(segmentCount).
		SetNillableErrorMessage(errMsg).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		_ = s.fileStore.Delete(relPath)
		if ent.IsConstraintError(err) {
			return nil, ErrResourceAlreadyExists
		}
		return nil, fmt.Errorf("resource: create record: %w", err)
	}

	// 在事务中创建段落记录（需要 res.ID）
	if parseErr == nil {
		if err := replaceResourceSegmentsBatch(ctx, tx.Segment, res.ID, parsedSegments); err != nil {
			_ = tx.Rollback()
			_ = s.fileStore.Delete(relPath)
			return nil, fmt.Errorf("resource: create segments: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		_ = s.fileStore.Delete(relPath)
		return nil, fmt.Errorf("resource: commit transaction: %w", err)
	}

	if parseErr != nil {
		if errors.Is(parseErr, parser.ErrNoParser) {
			return &ResourceUploadResult{
				Resource:      res,
				TotalSegments: segmentCount,
			}, fmt.Errorf("%w: %s", ErrUnsupportedFormat, format)
		}
		return &ResourceUploadResult{
			Resource:      res,
			TotalSegments: segmentCount,
		}, fmt.Errorf("%w: %v", ErrParseFailed, parseErr)
	}

	return &ResourceUploadResult{
		Resource:      res,
		TotalSegments: segmentCount,
	}, nil
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
		q = q.Where(resource.PathContains(opts.Search))
	}

	q = q.Order(ent.Asc(resource.FieldPath), ent.Asc(resource.FieldID))

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
// 段落替换和 Resource 更新在同一事务中执行，保证原子性。
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

	cleanName := sanitizeFilename(pathBase(res.Path))
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(cleanName)), ".")

	// 删除旧文件
	if res.StoragePath != "" {
		_ = s.fileStore.Delete(res.StoragePath)
	}

	// 保存新文件
	relPath := s.buildResourcePath(projectID, fmt.Sprintf("resource-%d", res.ID), resourcePathForStorage(res.Path, cleanName))
	if err := s.fileStore.Write(ctx, relPath, file.Reader); err != nil {
		_, _ = s.client.Resource.UpdateOneID(res.ID).
			SetStatus(ResourceStatusError).
			SetErrorMessage(fmt.Sprintf("file save failed: %v", err)).
			Save(ctx)
		return nil, fmt.Errorf("resource: save file: %w", err)
	}

	// 解析新文件
	parsedSegments, parseErr := s.parseResourceSegments(relPath)
	segmentCount := len(parsedSegments)

	// 事务包裹：段落替换 + Resource 更新，保证原子性
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("resource: begin transaction: %w", err)
	}

	if parseErr == nil {
		if err := replaceResourceSegmentsBatch(ctx, tx.Segment, res.ID, parsedSegments); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("resource: replace segments: %w", err)
		}
	}

	update := tx.Resource.UpdateOneID(res.ID).
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
		_ = tx.Rollback()
		return nil, fmt.Errorf("resource: update record: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("resource: commit transaction: %w", err)
	}

	if parseErr != nil {
		if errors.Is(parseErr, parser.ErrNoParser) {
			return updated, fmt.Errorf("%w: %s", ErrUnsupportedFormat, format)
		}
		return updated, fmt.Errorf("%w: %v", ErrParseFailed, parseErr)
	}

	return updated, nil
}

// Absolute 获取文件的绝对路径。
func (s *ResourceService) Absolute(relativePath string) (string, error) {
	return s.fileStore.Absolute(relativePath)
}

// buildResourcePath 构建资源文件的存储路径：resources/project-{id}/{uniqueID}/{resourcePath}。
func (s *ResourceService) buildResourcePath(projectID int, uniqueID, resourcePath string) string {
	cleanPath, err := NormalizeResourcePath(resourcePath)
	if err != nil {
		cleanPath = sanitizeFilename(resourcePath)
	}
	return filepath.ToSlash(filepath.Join("resources",
		fmt.Sprintf("project-%d", projectID),
		uniqueID,
		filepath.FromSlash(cleanPath),
	))
}

type parsedResourceSegment struct {
	Index      int
	SourceText string
	TargetText string
	Meta       map[string]any // 新增：格式元数据
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
		items = append(items, parsedResourceSegment{Index: i, SourceText: sourceText, TargetText: item.Target, Meta: item.Meta})
	}
	return items, nil
}

// segmentClientAccessor 抽象 Segment 客户端操作，兼容 *ent.Client 和 *ent.Tx。
type segmentClientAccessor interface {
	Create() *ent.SegmentCreate
	Delete() *ent.SegmentDelete
	CreateBulk(builders ...*ent.SegmentCreate) *ent.SegmentCreateBulk
}

// replaceResourceSegmentsBatch 删除旧段落并分批插入新段落。
// 分批避免超过 SQLite 999 绑定变量限制（segmentBatchSize × 4 字段 < 999）。
func replaceResourceSegmentsBatch(ctx context.Context, accessor segmentClientAccessor, resourceID int, items []parsedResourceSegment) error {
	if _, err := accessor.Delete().Where(segment.ResourceIDEQ(resourceID)).Exec(ctx); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	for i := 0; i < len(items); i += segmentBatchSize {
		end := i + segmentBatchSize
		if end > len(items) {
			end = len(items)
		}
		batch := items[i:end]
		builders := make([]*ent.SegmentCreate, 0, len(batch))
		for _, item := range batch {
			create := accessor.Create().
				SetResourceID(resourceID).
				SetSegmentIndex(item.Index).
				SetSourceText(item.SourceText).
				SetStatus(SegmentStatusPending)
			if strings.TrimSpace(item.TargetText) != "" {
				create.SetTargetText(item.TargetText).SetStatus(SegmentStatusTranslated)
			}
			if item.Meta != nil {
				metaJSON, _ := json.Marshal(item.Meta)
				create = create.SetMeta(string(metaJSON))
			}
			builders = append(builders, create)
		}
		if _, err := accessor.CreateBulk(builders...).Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *ResourceService) replaceResourceSegments(ctx context.Context, resourceID int, items []parsedResourceSegment) error {
	return replaceResourceSegmentsBatch(ctx, s.client.Segment, resourceID, items)
}

// ResourceListOptions 资源列表查询选项。
type ResourceListOptions struct {
	Status string
	Format string
	Search string
	Limit  int
}

func sanitizeFilename(name string) string {
	base := strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
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

// NormalizeResourcePath 将用户提供的资源路径规范化为项目内相对路径。
func NormalizeResourcePath(value string) (string, error) {
	raw := strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if raw == "" || strings.HasPrefix(raw, "/") {
		return "", ErrResourcePathInvalid
	}
	rawParts := strings.Split(raw, "/")
	for _, part := range rawParts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." || part == ".." {
			return "", ErrResourcePathInvalid
		}
	}
	clean := filepath.ToSlash(filepath.Clean(raw))
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || clean == ".." || strings.HasPrefix(clean, "/") {
		return "", ErrResourcePathInvalid
	}
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." || part == ".." {
			return "", ErrResourcePathInvalid
		}
		parts[i] = sanitizePathSegment(part)
	}
	if len(parts) == 0 || strings.TrimSpace(parts[len(parts)-1]) == "" {
		return "", ErrResourcePathInvalid
	}
	return strings.Join(parts, "/"), nil
}

func sanitizePathSegment(part string) string {
	replacer := strings.NewReplacer(":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	part = replacer.Replace(part)
	if strings.TrimSpace(part) == "" {
		return "_"
	}
	return part
}

func pathBase(value string) string {
	return filepath.Base(strings.ReplaceAll(value, "\\", "/"))
}

func resourcePathForStorage(resourcePath, fallbackFilename string) string {
	if normalized, err := NormalizeResourcePath(resourcePath); err == nil {
		return normalized
	}
	return sanitizeFilename(fallbackFilename)
}

func resourceDirectory(resourcePath string) string {
	resourcePath = strings.TrimSpace(strings.ReplaceAll(resourcePath, "\\", "/"))
	dir := filepath.ToSlash(filepath.Dir(resourcePath))
	if dir == "." {
		return ""
	}
	return dir
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// generateUniqueID 生成 16 位随机十六进制字符串，用于构建存储路径。
func generateUniqueID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// FindResourceByPath 在项目中查找同路径资源文件。
func (s *ResourceService) FindResourceByPath(ctx context.Context, projectID int, resourcePath string) (*ent.Resource, error) {
	cleanPath, err := NormalizeResourcePath(resourcePath)
	if err != nil {
		return nil, err
	}
	res, err := s.client.Resource.Query().
		Where(resource.ProjectID(projectID), resource.Path(cleanPath)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return res, nil
}

// diffSegments 对比新旧段落，返回变更列表。
// 按 source_text 内容匹配，处理段落重排。
func diffSegments(oldSegments []*ent.Segment, newSegments []parsedResourceSegment) []SegmentChange {
	// 构建旧段落索引：source_text → 未消耗的旧段落队列
	oldQueue := make(map[string][]*ent.Segment)
	for _, seg := range oldSegments {
		key := strings.TrimSpace(seg.SourceText)
		oldQueue[key] = append(oldQueue[key], seg)
	}

	changes := make([]SegmentChange, 0, len(newSegments))
	matchedOldIDs := make(map[int]bool)

	// 遍历新段落，尝试匹配旧段落
	for _, newSeg := range newSegments {
		key := strings.TrimSpace(newSeg.SourceText)
		queue := oldQueue[key]

		if len(queue) > 0 {
			// 匹配到旧段落
			old := queue[0]
			oldQueue[key] = queue[1:]
			matchedOldIDs[old.ID] = true

			if strings.TrimSpace(old.SourceText) == strings.TrimSpace(newSeg.SourceText) {
				// 源文本完全相同 → unchanged
				changes = append(changes, SegmentChange{
					ChangeType: SegmentChangeUnchanged,
					OldSegment: old,
					NewIndex:   newSeg.Index,
					NewSource:  newSeg.SourceText,
				})
			} else {
				// 源文本有细微差异 → updated
				changes = append(changes, SegmentChange{
					ChangeType: SegmentChangeUpdated,
					OldSegment: old,
					NewIndex:   newSeg.Index,
					NewSource:  newSeg.SourceText,
				})
			}
		} else {
			// 未匹配到旧段落 → added
			changes = append(changes, SegmentChange{
				ChangeType: SegmentChangeAdded,
				NewIndex:   newSeg.Index,
				NewSource:  newSeg.SourceText,
				NewMeta:    newSeg.Meta,
			})
		}
	}

	// 检查未匹配的旧段落 → deleted
	for _, seg := range oldSegments {
		if !matchedOldIDs[seg.ID] {
			changes = append(changes, SegmentChange{
				ChangeType: SegmentChangeDeleted,
				OldSegment: seg,
			})
		}
	}

	return changes
}

// IncrementalUpdateResource 增量更新资源文件。
// 对比新旧文件段落变化，保留已有译文。
func (s *ResourceService) IncrementalUpdateResource(
	ctx context.Context,
	actorUserID, projectID, resourceID int,
	file UploadedFile,
) (*ent.Resource, *IncrementalUpdateStats, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return nil, nil, err
	}

	// 查询旧资源
	res, err := s.client.Resource.Query().
		Where(resource.ID(resourceID), resource.ProjectID(projectID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil, ErrResourceNotFound
		}
		return nil, nil, err
	}

	// 删除旧文件
	if res.StoragePath != "" {
		_ = s.fileStore.Delete(res.StoragePath)
	}

	// 保存新文件
	cleanName := sanitizeFilename(pathBase(res.Path))
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(cleanName)), ".")
	relPath := s.buildResourcePath(projectID, fmt.Sprintf("resource-%d", res.ID), resourcePathForStorage(res.Path, cleanName))
	if err := s.fileStore.Write(ctx, relPath, file.Reader); err != nil {
		_, _ = s.client.Resource.UpdateOneID(res.ID).
			SetStatus(ResourceStatusError).
			SetErrorMessage(fmt.Sprintf("file save failed: %v", err)).
			Save(ctx)
		return nil, nil, fmt.Errorf("resource: save file: %w", err)
	}

	// 解析新文件段落
	newSegments, parseErr := s.parseResourceSegments(relPath)
	if parseErr != nil {
		// 解析失败，更新资源状态
		update := s.client.Resource.UpdateOneID(res.ID).
			SetFormat(format).
			SetStoragePath(relPath).
			SetStatus(ResourceStatusError).
			SetErrorMessage(fmt.Sprintf("parse failed: %v", parseErr))
		updated, _ := update.Save(ctx)

		if errors.Is(parseErr, parser.ErrNoParser) {
			return updated, nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, format)
		}
		return updated, nil, fmt.Errorf("%w: %v", ErrParseFailed, parseErr)
	}

	// 查询旧段落
	oldSegments, err := s.client.Segment.Query().
		Where(segment.ResourceIDEQ(res.ID)).
		Order(ent.Asc(segment.FieldSegmentIndex)).
		All(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("resource: query old segments: %w", err)
	}

	// 执行段落对比
	changes := diffSegments(oldSegments, newSegments)

	// 统计变更
	stats := &IncrementalUpdateStats{}
	for _, c := range changes {
		switch c.ChangeType {
		case SegmentChangeAdded:
			stats.Added++
		case SegmentChangeUpdated:
			stats.Updated++
		case SegmentChangeUnchanged:
			stats.Unchanged++
		case SegmentChangeDeleted:
			stats.Deleted++
		}
	}

	// 应用变更
	if err := s.applySegmentChanges(ctx, res.ID, changes); err != nil {
		return nil, nil, fmt.Errorf("resource: apply changes: %w", err)
	}

	// 更新资源元数据
	updated, err := s.client.Resource.UpdateOneID(res.ID).
		SetFormat(format).
		SetStoragePath(relPath).
		SetTotalSegments(len(newSegments)).
		SetStatus(ResourceStatusReady).
		ClearErrorMessage().
		Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("resource: update record: %w", err)
	}

	return updated, stats, nil
}

// applySegmentChanges 应用段落变更到数据库。
func (s *ResourceService) applySegmentChanges(ctx context.Context, resourceID int, changes []SegmentChange) error {
	// 收集需要删除的段落 ID
	deleteIDs := make([]int, 0)
	for _, c := range changes {
		if c.ChangeType == SegmentChangeDeleted && c.OldSegment != nil {
			deleteIDs = append(deleteIDs, c.OldSegment.ID)
		}
	}

	// 批量删除
	if len(deleteIDs) > 0 {
		if _, err := s.client.Segment.Delete().
			Where(segment.IDIn(deleteIDs...)).
			Exec(ctx); err != nil {
			return err
		}
	}

	// 处理更新和新增
	for _, c := range changes {
		switch c.ChangeType {
		case SegmentChangeUnchanged:
			// 更新索引位置，保留译文
			if _, err := s.client.Segment.UpdateOneID(c.OldSegment.ID).
				SetSegmentIndex(c.NewIndex).
				Save(ctx); err != nil {
				return err
			}

		case SegmentChangeUpdated:
			// 更新索引和源文本，清空译文，重置状态
			if _, err := s.client.Segment.UpdateOneID(c.OldSegment.ID).
				SetSegmentIndex(c.NewIndex).
				SetSourceText(c.NewSource).
				ClearTargetText().
				SetStatus(SegmentStatusPending).
				Save(ctx); err != nil {
				return err
			}

		case SegmentChangeAdded:
			// 创建新段落
			create := s.client.Segment.Create().
				SetResourceID(resourceID).
				SetSegmentIndex(c.NewIndex).
				SetSourceText(c.NewSource).
				SetStatus(SegmentStatusPending)
			if c.NewMeta != nil {
				metaJSON, _ := json.Marshal(c.NewMeta)
				create = create.SetMeta(string(metaJSON))
			}
			if _, err := create.Save(ctx); err != nil {
				return err
			}
		}
	}

	return nil
}
