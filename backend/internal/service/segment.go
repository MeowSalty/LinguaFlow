package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
)

type SegmentService struct {
	client   *ent.Client
	projects *ProjectService
}

type ResourceSegmentPage struct {
	Items      []*ent.Segment
	NextCursor int
}

type ResourceSegmentListOptions struct {
	AfterID  int
	Limit    int
	Status   string
	Search   string
	GroupKey string
}

type ResourceSegmentUpdateInput struct {
	SourceText *string
	TargetText *string
	Comment    *string
}

func NewSegmentService(client *ent.Client, projects *ProjectService) *SegmentService {
	return &SegmentService{client: client, projects: projects}
}

func (s *SegmentService) ListResourceSegments(ctx context.Context, actorUserID, projectID, resourceID int, opts ResourceSegmentListOptions) (*ResourceSegmentPage, error) {
	if _, err := s.requireResourceAccess(ctx, actorUserID, projectID, resourceID, false); err != nil {
		return nil, err
	}
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}

	q := s.client.Segment.Query().Where(segment.ResourceIDEQ(resourceID))
	if opts.Status != "" {
		q = q.Where(segment.StatusEQ(segment.Status(opts.Status)))
	}
	if opts.Search != "" {
		q = q.Where(segment.Or(segment.SourceTextContains(opts.Search), segment.TargetTextContains(opts.Search)))
	}

	if opts.GroupKey != "" {
		// group_key 过滤需要在应用层解析 JSON meta 字段
		// 先加载所有匹配基础条件的 segments，再按 meta.epub_file 过滤后分页
		allRows, err := q.Order(ent.Asc(segment.FieldID)).WithReviewedBy().WithResource().All(ctx)
		if err != nil {
			return nil, err
		}

		var filtered []*ent.Segment
		for _, row := range allRows {
			if row.Meta != nil {
				var meta map[string]any
				if err := json.Unmarshal([]byte(*row.Meta), &meta); err == nil {
					if epubFile, ok := meta["epub_file"].(string); ok && epubFile == opts.GroupKey {
						filtered = append(filtered, row)
					}
				}
			}
		}

		// 在过滤后的结果中应用游标分页
		start := 0
		if opts.AfterID > 0 {
			for i, row := range filtered {
				if row.ID > opts.AfterID {
					start = i
					break
				}
			}
		}

		page := &ResourceSegmentPage{}
		end := start + opts.Limit
		if end > len(filtered) {
			end = len(filtered)
		}
		page.Items = filtered[start:end]

		if end < len(filtered) {
			page.NextCursor = page.Items[len(page.Items)-1].ID
		}
		return page, nil
	}

	// 默认路径：无 group_key 过滤，使用数据库分页
	if opts.AfterID > 0 {
		q = q.Where(segment.IDGT(opts.AfterID))
	}
	rows, err := q.Order(ent.Asc(segment.FieldID)).Limit(opts.Limit + 1).WithReviewedBy().WithResource().All(ctx)
	if err != nil {
		return nil, err
	}
	page := &ResourceSegmentPage{Items: rows}
	if len(rows) > opts.Limit {
		page.NextCursor = rows[opts.Limit-1].ID
		page.Items = rows[:opts.Limit]
	}
	return page, nil
}

func (s *SegmentService) UpdateResourceSegment(ctx context.Context, actorUserID, projectID, resourceID, segmentID int, input ResourceSegmentUpdateInput) (*ent.Segment, error) {
	if _, err := s.requireResourceAccess(ctx, actorUserID, projectID, resourceID, true); err != nil {
		return nil, err
	}
	current, err := s.client.Segment.Query().Where(segment.IDEQ(segmentID), segment.ResourceIDEQ(resourceID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}

	update := s.client.Segment.UpdateOneID(current.ID)
	changed := false
	sourceChanged := false
	targetChanged := false

	if input.SourceText != nil {
		source := strings.TrimSpace(*input.SourceText)
		if source == "" {
			return nil, ErrInvalidInput
		}
		update.SetSourceText(source).ClearTargetText().SetStatus(SegmentStatusPending)
		changed = true
		sourceChanged = true
	}
	if input.TargetText != nil {
		target := strings.TrimSpace(*input.TargetText)
		if target == "" {
			return nil, ErrInvalidInput
		}
		update.SetTargetText(target).SetStatus(SegmentStatusEdited).SetReviewedByID(actorUserID)
		changed = true
		targetChanged = true
	}
	if input.Comment != nil {
		comment := strings.TrimSpace(*input.Comment)
		if comment == "" {
			update.ClearReviewComment()
		} else {
			update.SetReviewComment(comment)
		}
		changed = true
	}
	if !changed {
		return nil, ErrInvalidInput
	}
	if sourceChanged && !targetChanged {
		update.ClearReviewedBy()
	}

	updated, err := update.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}
	return s.client.Segment.Query().Where(segment.IDEQ(updated.ID)).WithReviewedBy().WithResource().Only(ctx)
}

func (s *SegmentService) requireResourceAccess(ctx context.Context, actorUserID, projectID, resourceID int, write bool) (*ent.Resource, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, write); err != nil {
		return nil, err
	}
	row, err := s.client.Resource.Query().Where(resource.IDEQ(resourceID), resource.ProjectIDEQ(projectID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrResourceNotFound
		}
		return nil, err
	}
	return row, nil
}

// ResourceSegmentGroup 表示按章节分组的段落统计信息。
type ResourceSegmentGroup struct {
	GroupKey        string `json:"group_key"`
	GroupTitle      string `json:"group_title"`
	SegmentCount    int    `json:"segment_count"`
	TranslatedCount int    `json:"translated_count"`
}

type segmentGroupEntry struct {
	groupKey   string
	groupTitle string
	minIndex   int
	count      int
	translated int
}

// ListResourceSegmentGroups 按 meta["epub_file"] 将 segments 归为章节组，返回每组的统计信息。
// 非 EPUB 资源会返回一个包含所有 segments 的单一组。
func (s *SegmentService) ListResourceSegmentGroups(ctx context.Context, actorUserID, projectID, resourceID int) ([]ResourceSegmentGroup, error) {
	if _, err := s.requireResourceAccess(ctx, actorUserID, projectID, resourceID, false); err != nil {
		return nil, err
	}

	rows, err := s.client.Segment.Query().
		Where(segment.ResourceIDEQ(resourceID)).
		Order(ent.Asc(segment.FieldSegmentIndex)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	// 按 groupKey 分组
	groupMap := make(map[string]*segmentGroupEntry)
	var groupOrder []string

	translatedStatuses := map[segment.Status]bool{
		SegmentStatusTranslated: true,
		SegmentStatusEdited:     true,
		SegmentStatusApproved:   true,
	}

	for _, row := range rows {
		groupKey := ""
		groupTitle := ""

		if row.Meta != nil {
			var meta map[string]any
			if err := json.Unmarshal([]byte(*row.Meta), &meta); err == nil {
				if v, ok := meta["epub_file"].(string); ok && v != "" {
					groupKey = v
				}
				// 优先使用章节标题，无法提取时回退到书籍标题
				if v, ok := meta["epub_chapter_title"].(string); ok && v != "" {
					groupTitle = v
				} else if v, ok := meta["epub_chapter_title"].(string); ok && v != "" {
					groupTitle = v
				}
			}
		}

		g, exists := groupMap[groupKey]
		if !exists {
			if groupTitle == "" {
				groupTitle = groupKey
			}
			g = &segmentGroupEntry{
				groupKey:   groupKey,
				groupTitle: groupTitle,
				minIndex:   row.SegmentIndex,
			}
			groupMap[groupKey] = g
			groupOrder = append(groupOrder, groupKey)
		}

		g.count++
		if translatedStatuses[row.Status] {
			g.translated++
		}
	}

	// 按 minIndex 排序，保持 spine 顺序
	sort.SliceStable(groupOrder, func(i, j int) bool {
		return groupMap[groupOrder[i]].minIndex < groupMap[groupOrder[j]].minIndex
	})

	result := make([]ResourceSegmentGroup, 0, len(groupOrder))
	for _, key := range groupOrder {
		g := groupMap[key]
		result = append(result, ResourceSegmentGroup{
			GroupKey:        g.groupKey,
			GroupTitle:      g.groupTitle,
			SegmentCount:    g.count,
			TranslatedCount: g.translated,
		})
	}

	return result, nil
}
