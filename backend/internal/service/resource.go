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
	"strconv"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
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
	ErrNoTranslatedSegments  = errors.New("resource has no translated segments")
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
			return nil, fmt.Errorf("检查资源路径冲突失败：%w", err)
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
	if parseErr != nil {
		// 解析失败：删除已写入的文件，不落库
		_ = s.fileStore.Delete(relPath)
		if errors.Is(parseErr, parser.ErrNoParser) {
			return nil, fmt.Errorf("unsupported format: %s", format)
		}
		return nil, fmt.Errorf("parse failed: %w", parseErr)
	}
	segmentCount := len(parsedSegments)

	// 事务包裹：Resource 创建 + Segment 插入，保证原子性
	tx, err := s.client.Tx(ctx)
	if err != nil {
		_ = s.fileStore.Delete(relPath)
		return nil, fmt.Errorf("resource: begin transaction: %w", err)
	}

	res, err := tx.Resource.Create().
		SetPath(resourcePath).
		SetFormat(format).
		SetStoragePath(relPath).
		SetNillableProjectID(&projectID).
		SetTotalSegments(segmentCount).
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
	if err := replaceResourceSegmentsBatch(ctx, tx.Segment, res.ID, parsedSegments); err != nil {
		_ = tx.Rollback()
		_ = s.fileStore.Delete(relPath)
		return nil, fmt.Errorf("resource: create segments: %w", err)
	}

	if err := tx.Commit(); err != nil {
		_ = s.fileStore.Delete(relPath)
		return nil, fmt.Errorf("resource: commit transaction: %w", err)
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
// 在事务中按依赖顺序级联删除关联记录（JobResource → Segment → Resource），
// 避免 FOREIGN KEY constraint failed 错误。
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

	// 使用事务保证级联删除的原子性
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("resource: begin transaction: %w", err)
	}

	// 1. 删除关联的 JobResource 记录（引用 resource_id 外键）
	if _, err := tx.JobResource.Delete().
		Where(jobresource.HasResourceWith(resource.ID(res.ID))).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("resource: delete job_resources: %w", err)
	}

	// 2. 删除关联的 Segment 记录（引用 resource_id 外键）
	if _, err := tx.Segment.Delete().
		Where(segment.ResourceID(res.ID)).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("resource: delete segments: %w", err)
	}

	// 3. 删除 Resource 记录本身
	if err := tx.Resource.DeleteOneID(res.ID).Exec(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("resource: delete resource: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("resource: commit transaction: %w", err)
	}

	// 删除存储文件（事务提交成功后再删除物理文件）
	if res.StoragePath != "" {
		_ = s.fileStore.Delete(res.StoragePath)
	}

	return nil
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

	// 保存新文件到新路径（不先删除旧文件，保证解析失败时原资源不受影响）
	newRelPath := s.buildResourcePath(projectID, fmt.Sprintf("resource-%d", res.ID), resourcePathForStorage(res.Path, cleanName))
	if err := s.fileStore.Write(ctx, newRelPath, file.Reader); err != nil {
		return nil, fmt.Errorf("resource: save file: %w", err)
	}

	// 解析新文件
	parsedSegments, parseErr := s.parseResourceSegments(newRelPath)
	if parseErr != nil {
		// 解析失败：删除新文件，旧文件保持不变，不更新 DB
		_ = s.fileStore.Delete(newRelPath)
		if errors.Is(parseErr, parser.ErrNoParser) {
			return nil, fmt.Errorf("unsupported format: %s", format)
		}
		return nil, fmt.Errorf("parse failed: %w", parseErr)
	}
	segmentCount := len(parsedSegments)

	// 删除旧文件（解析成功后才替换）
	if res.StoragePath != "" && res.StoragePath != newRelPath {
		_ = s.fileStore.Delete(res.StoragePath)
	}

	// 事务包裹：段落替换 + Resource 更新，保证原子性
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("resource: begin transaction: %w", err)
	}

	if err := replaceResourceSegmentsBatch(ctx, tx.Segment, res.ID, parsedSegments); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("resource: replace segments: %w", err)
	}

	updated, err := tx.Resource.UpdateOneID(res.ID).
		SetFormat(format).
		SetStoragePath(newRelPath).
		SetTotalSegments(segmentCount).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("resource: update record: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("resource: commit transaction: %w", err)
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

	// 保存新文件到新路径（不先删除旧文件，保证解析失败时原资源不受影响）
	cleanName := sanitizeFilename(pathBase(res.Path))
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(cleanName)), ".")
	newRelPath := s.buildResourcePath(projectID, fmt.Sprintf("resource-%d", res.ID), resourcePathForStorage(res.Path, cleanName))
	if err := s.fileStore.Write(ctx, newRelPath, file.Reader); err != nil {
		return nil, nil, fmt.Errorf("resource: save file: %w", err)
	}

	// 解析新文件段落
	newSegments, parseErr := s.parseResourceSegments(newRelPath)
	if parseErr != nil {
		// 解析失败：删除新文件，旧文件保持不变，不更新 DB
		_ = s.fileStore.Delete(newRelPath)
		if errors.Is(parseErr, parser.ErrNoParser) {
			return nil, nil, fmt.Errorf("unsupported format: %s", format)
		}
		return nil, nil, fmt.Errorf("parse failed: %w", parseErr)
	}

	// 删除旧文件（解析成功后才替换）
	if res.StoragePath != "" && res.StoragePath != newRelPath {
		_ = s.fileStore.Delete(res.StoragePath)
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
		SetStoragePath(newRelPath).
		SetTotalSegments(len(newSegments)).
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

// normalizeMeta 将 json.Unmarshal 产生的 float64 / []interface{}
// 转换回 int / []int，以便 Parser Render 阶段的类型断言成功。
func normalizeMeta(meta map[string]any) map[string]any {
	for k, v := range meta {
		switch val := v.(type) {
		case float64:
			if val == float64(int(val)) {
				meta[k] = int(val)
			}
		case []interface{}:
			ints := make([]int, 0, len(val))
			allInt := true
			for _, item := range val {
				if f, ok := item.(float64); ok && f == float64(int(f)) {
					ints = append(ints, int(f))
				} else {
					allInt = false
					break
				}
			}
			if allInt {
				meta[k] = ints
			}
		}
	}
	return meta
}

// BuildSegmentInputsWithTarget 将 DB segments 转换为引擎输入，包含 Target 文本。
func BuildSegmentInputsWithTarget(rows []*ent.Segment) []engine.SegmentInput {
	inputs := make([]engine.SegmentInput, len(rows))
	for i, row := range rows {
		var meta map[string]any
		if row.Meta != nil {
			_ = json.Unmarshal([]byte(*row.Meta), &meta)
			meta = normalizeMeta(meta)
		}
		var target string
		if row.TargetText != nil {
			target = *row.TargetText
		}
		inputs[i] = engine.SegmentInput{
			ID:         strconv.Itoa(row.SegmentIndex),
			SourceText: row.SourceText,
			Meta:       meta,
			TargetText: target,
		}
	}
	return inputs
}

// loadOriginalFile 加载原始文件流。
func (s *ResourceService) loadOriginalFile(storagePath string) (io.ReadCloser, error) {
	absolutePath, err := s.fileStore.Absolute(storagePath)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	f, err := os.Open(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("open original file: %w", err)
	}
	return f, nil
}

// RenderTranslatedResource 渲染资源的翻译结果并写入 writer。
// 不依赖翻译任务，直接基于资源当前的 segments 和项目语言配置。
func (s *ResourceService) RenderTranslatedResource(
	ctx context.Context,
	actorUserID int,
	res *ent.Resource,
	writer io.Writer,
) error {
	// 1. 加载资源的所有 segments
	segments, err := s.client.Segment.Query().
		Where(segment.ResourceIDEQ(res.ID)).
		Order(ent.Asc(segment.FieldSegmentIndex)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("load segments: %w", err)
	}
	if len(segments) == 0 {
		return ErrNoTranslatedSegments
	}

	// 2. 获取语言配置（从 Project）
	if res.ProjectID == nil {
		return fmt.Errorf("resource %d has no project association", res.ID)
	}
	project, err := s.client.Project.Get(ctx, *res.ProjectID)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	sourceLang := project.SourceLang
	targetLang := project.TargetLang

	// 3. 加载原始文件
	original, err := s.loadOriginalFile(res.StoragePath)
	if err != nil {
		return fmt.Errorf("原始文件不存在或已被删除，无法渲染资源 %d: %w", res.ID, err)
	}
	defer original.Close()

	// 4. 构建 Document 并填充 Target
	inputs := BuildSegmentInputsWithTarget(segments)
	doc := engine.BuildDocumentFromSegments(inputs, sourceLang, targetLang, res.Format)

	// 5. 解析格式并渲染
	p, err := parser.Resolve(res.Format)
	if err != nil {
		return fmt.Errorf("resolve parser for format %q: %w", res.Format, err)
	}
	return p.Render(ctx, doc, original, writer)
}
