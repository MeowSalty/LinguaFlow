package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
)

// 任务创建与 QA 重检共享的段落选择解析：
// 按 segment_group_keys > segment_ids > resource_ids 的优先级把选择条件
// 解析为 resourceID → segmentIDs 的映射，二者共享同一套权限与匹配语义。

// workWeightScan 聚合计数的扫描载体（列名经 sql tag 对齐 AS 别名）。
type workWeightScan struct {
	WorkWeight int64 `sql:"work_weight"`
}

// workWeightAggregate 自定义聚合表达式：按驱动方言对 source_text 求字节数
// 后求和——SQLite 用 LENGTH(CAST(x AS BLOB))（UTF-8 字节数），PostgreSQL
// 用 OCTET_LENGTH(x)（PG 无 blob 类型，CAST(x AS BLOB) 会报
// "type blob does not exist"）。两口径均为 UTF-8 字节数，跨驱动一致。
// 经 ent AggregateFunc 注入（Selector 携带驱动方言），避免直接依赖底层 *sql.DB。
func workWeightAggregate(s *entsql.Selector) string {
	col := s.C(segment.FieldSourceText)
	var byteLen string
	if s.Dialect() == dialect.Postgres {
		byteLen = fmt.Sprintf("OCTET_LENGTH(%s)", col)
	} else {
		byteLen = fmt.Sprintf("LENGTH(CAST(%s AS BLOB))", col)
	}
	return fmt.Sprintf("COALESCE(SUM(%s), 0) AS work_weight", byteLen)
}

// sumSegmentWorkWeight 汇总选定段落的 source_text 字节长度，作为任务资源的
// 工作量权重（准入预算用）。segment_ids 为空（动态选择）时返回 0，由 worker
// 侧 back-fill。IN 列表按 selectionQueryChunkSize 分片，避免超过 SQLite
// 绑定变量上限。
func sumSegmentWorkWeight(ctx context.Context, client *ent.Client, segmentIDs []int) (int64, error) {
	if len(segmentIDs) == 0 {
		return 0, nil
	}
	var total int64
	for start := 0; start < len(segmentIDs); start += selectionQueryChunkSize {
		end := start + selectionQueryChunkSize
		if end > len(segmentIDs) {
			end = len(segmentIDs)
		}
		var rows []workWeightScan
		if err := client.Segment.Query().
			Where(segment.IDIn(segmentIDs[start:end]...)).
			Aggregate(workWeightAggregate).
			Scan(ctx, &rows); err != nil {
			return 0, err
		}
		if len(rows) > 0 {
			total += rows[0].WorkWeight
		}
	}
	return total, nil
}

// SumResourceWorkWeight 汇总某资源全部段落的 source_text 字节长度。
// 供 worker 回填动态选择资源（segment_ids 为空）的工作权重——Document
// 每轮全量加载该资源段落，全量字节即内存代理；单资源无 IN 列表，不分片。
func SumResourceWorkWeight(ctx context.Context, client *ent.Client, resourceID int) (int64, error) {
	var rows []workWeightScan
	if err := client.Segment.Query().
		Where(segment.ResourceIDEQ(resourceID)).
		Aggregate(workWeightAggregate).
		Scan(ctx, &rows); err != nil {
		return 0, err
	}
	if len(rows) > 0 {
		return rows[0].WorkWeight, nil
	}
	return 0, nil
}

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
