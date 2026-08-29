package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
)

// 任务创建与 QA 重检共享的段落选择解析：
// 按 segment_group_keys > segment_ids > resource_ids 的优先级把选择条件
// 解析为 resourceID → segmentIDs 的映射，二者共享同一套权限与匹配语义。

func resolveJobSelection(ctx context.Context, client *ent.Client, projectID int, input CreateJobInput) (map[int][]int, error) {
	if len(input.SegmentGroupKeys) > 0 {
		return resolveGroupKeySelection(ctx, client, projectID, input.SegmentGroupKeys, input.ResourceIDs)
	}
	if len(input.SegmentIDs) > 0 {
		return resolveSegmentSelection(ctx, client, projectID, input.SegmentIDs)
	}
	return resolveResourceSelection(ctx, client, projectID, input.ResourceIDs)
}

// selectionQueryChunkSize 选择解析查询的 IN 分片大小。SQLite 驱动的绑定变量
// 上限为 32766，segment_ids 选择（去重后）可合法达到重检上限 50000，必须分片
// 查询后合并，否则大选择直接报 "too many SQL variables"。
const selectionQueryChunkSize = 2000

func resolveSegmentSelection(ctx context.Context, client *ent.Client, projectID int, segmentIDs []int) (map[int][]int, error) {
	ids := uniqueInts(segmentIDs)
	var rows []*ent.Segment
	for start := 0; start < len(ids); start += selectionQueryChunkSize {
		end := start + selectionQueryChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk, err := client.Segment.Query().
			Where(segment.IDIn(ids[start:end]...), segment.HasResourceWith(resource.ProjectIDEQ(projectID))).
			All(ctx)
		if err != nil {
			return nil, err
		}
		rows = append(rows, chunk...)
	}
	if len(rows) != len(ids) {
		return nil, ErrSegmentNotFound
	}
	selection := make(map[int][]int)
	for _, row := range rows {
		if row.ResourceID == nil {
			continue
		}
		selection[*row.ResourceID] = append(selection[*row.ResourceID], row.ID)
	}
	return selection, nil
}

func resolveGroupKeySelection(ctx context.Context, client *ent.Client, projectID int, groupKeys []string, resourceIDs []int) (map[int][]int, error) {
	uniqueKeys := make(map[string]struct{}, len(groupKeys))
	for _, key := range groupKeys {
		k := strings.TrimSpace(key)
		if k != "" {
			uniqueKeys[k] = struct{}{}
		}
	}
	if len(uniqueKeys) == 0 {
		return nil, fmt.Errorf("%w: segment_group_keys 不能为空", ErrInvalidInput)
	}

	// 查询该项目下指定资源的 segments（带 meta 字段）
	segQuery := client.Segment.Query().
		Where(segment.HasResourceWith(resource.ProjectIDEQ(projectID)))
	if len(resourceIDs) > 0 {
		segQuery = segQuery.Where(segment.HasResourceWith(resource.IDIn(uniqueInts(resourceIDs)...)))
	}
	rows, err := segQuery.
		Select(segment.FieldID, segment.FieldMeta, segment.FieldResourceID).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询 segments 失败：%w", err)
	}

	selection := make(map[int][]int)
	matchedCount := 0
	for _, row := range rows {
		if row.Meta == nil || row.ResourceID == nil {
			continue
		}
		var meta map[string]any
		if err := json.Unmarshal([]byte(*row.Meta), &meta); err != nil {
			continue
		}
		epubFile, ok := meta["epub_file"].(string)
		if !ok {
			continue
		}
		if _, matched := uniqueKeys[epubFile]; matched {
			selection[*row.ResourceID] = append(selection[*row.ResourceID], row.ID)
			matchedCount++
			slog.Debug("[resolveGroupKeySelection] resource matched",
				"resource_id", *row.ResourceID,
				"segment_count", len(selection[*row.ResourceID]),
				"segment_ids", selection[*row.ResourceID])
		}
	}

	slog.Debug("[resolveGroupKeySelection] diagnostic",
		"project_id", projectID,
		"group_keys", groupKeys,
		"total_segments_in_project", len(rows),
		"matched_segments", matchedCount,
		"matched_resources", len(selection))

	if matchedCount == 0 {
		return nil, fmt.Errorf("%w: 未找到匹配指定章节的 segments", ErrInvalidInput)
	}

	return selection, nil
}

func resolveResourceSelection(ctx context.Context, client *ent.Client, projectID int, resourceIDs []int) (map[int][]int, error) {
	resourceQuery := client.Resource.Query().Where(resource.ProjectIDEQ(projectID))
	if len(resourceIDs) > 0 {
		ids := uniqueInts(resourceIDs)
		resourceQuery = resourceQuery.Where(resource.IDIn(ids...))
		count, err := resourceQuery.Clone().Count(ctx)
		if err != nil {
			return nil, err
		}
		if count != len(ids) {
			return nil, ErrResourceNotFound
		}
	}
	resources, err := resourceQuery.All(ctx)
	if err != nil {
		return nil, err
	}
	selection := make(map[int][]int)
	for _, res := range resources {
		ids, err := client.Segment.Query().
			Where(segment.ResourceIDEQ(res.ID)).
			Order(ent.Asc(segment.FieldID)).
			IDs(ctx)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			continue
		}
		selection[res.ID] = ids
	}
	return selection, nil
}

func uniqueInts(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}
