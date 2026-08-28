package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

type SegmentService struct {
	client            *ent.Client
	projects          *ProjectService
	dialect           string
	revisionRetention time.Duration
	logger            *slog.Logger
}

type ResourceSegmentPage struct {
	Items      []*ent.Segment
	NextCursor int
	Total      *int // 仅 IncludeTotal=true 时填充；nil 表示未请求总数
}

type ResourceSegmentListOptions struct {
	AfterID         int
	Limit           int
	Status          string
	Search          string
	SearchField     string // ""/"both"（默认，行为不变）/"source"/"target"
	CaseSensitive   *bool  // nil/true（默认）大小写敏感；false 用 ContainsFold
	IncludeTotal    bool
	GroupKey        string
	QualityIssues   string
	QualitySeverity string
	QualityCode     string
}

type ResourceSegmentUpdateInput struct {
	SourceText *string
	TargetText *string
	Comment    *string
}

type qaBatchInput struct {
	Index      int
	SourceText string
	TargetText string
	OldTarget  string
	Meta       *string
}

func NewSegmentService(client *ent.Client, projects *ProjectService, dialectName string, revisionRetention time.Duration, logger *slog.Logger) *SegmentService {
	if logger == nil {
		logger = slog.Default()
	}
	if revisionRetention <= 0 {
		revisionRetention = 90 * 24 * time.Hour
	}
	return &SegmentService{client: client, projects: projects, dialect: dialectName, revisionRetention: revisionRetention, logger: logger}
}

// applySegmentFilters 把资源范围与 status/search/quality 过滤应用到查询。
// 列表与 include_total 计数必须共享同一套过滤条件，避免两处拼装漂移。
func applySegmentFilters(q *ent.SegmentQuery, resourceID int, opts ResourceSegmentListOptions, dialectName string) *ent.SegmentQuery {
	q = q.Where(segment.ResourceIDEQ(resourceID))
	if opts.Status != "" {
		q = q.Where(segment.StatusEQ(segment.Status(opts.Status)))
	}
	if opts.Search != "" {
		caseSensitive := true
		if opts.CaseSensitive != nil {
			caseSensitive = *opts.CaseSensitive
		}
		sourcePred := segment.SourceTextContains(opts.Search)
		targetPred := segment.TargetTextContains(opts.Search)
		if !caseSensitive {
			sourcePred = segment.SourceTextContainsFold(opts.Search)
			targetPred = segment.TargetTextContainsFold(opts.Search)
		}
		switch opts.SearchField {
		case "source":
			q = q.Where(sourcePred)
		case "target":
			q = q.Where(targetPred)
		default: // "" / "both"：保持原有 OR 语义
			q = q.Where(segment.Or(sourcePred, targetPred))
		}
	}
	if p := buildQualityPredicate(opts, dialectName); p != nil {
		q = q.Where(p)
	}
	return q
}

func (s *SegmentService) ListResourceSegments(ctx context.Context, actorUserID, projectID, resourceID int, opts ResourceSegmentListOptions) (*ResourceSegmentPage, error) {
	if _, err := s.requireResourceAccess(ctx, actorUserID, projectID, resourceID, false); err != nil {
		return nil, err
	}
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}

	q := applySegmentFilters(s.client.Segment.Query(), resourceID, opts, s.dialect)

	if opts.GroupKey != "" {
		// group_key 过滤需要在应用层解析 JSON meta 字段
		// 先加载所有匹配基础条件的 segments，再按 meta.epub_file 过滤后分页
		allRows, err := q.Order(ent.Asc(segment.FieldSegmentIndex)).WithReviewedBy().WithResource().All(ctx)
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
				if row.SegmentIndex > opts.AfterID {
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
			page.NextCursor = page.Items[len(page.Items)-1].SegmentIndex
		}
		if opts.IncludeTotal {
			total := len(filtered)
			page.Total = &total
		}
		return page, nil
	}

	// 默认路径：无 group_key 过滤，使用数据库分页
	if opts.AfterID > 0 {
		q = q.Where(segment.SegmentIndexGT(opts.AfterID))
	}
	rows, err := q.Order(ent.Asc(segment.FieldSegmentIndex)).Limit(opts.Limit + 1).WithReviewedBy().WithResource().All(ctx)
	if err != nil {
		return nil, err
	}
	page := &ResourceSegmentPage{Items: rows}
	if len(rows) > opts.Limit {
		page.NextCursor = rows[opts.Limit-1].SegmentIndex
		page.Items = rows[:opts.Limit]
	}
	if opts.IncludeTotal {
		// 计数需复用列表的 status/search/quality 过滤，但不能复用 q：默认路径的
		// q 可能已叠加游标分页（SegmentIndexGT）与 Limit，会影响计数结果，
		// 因此基于 applySegmentFilters 重新构造计数查询。group_key 已在上面分支处理。
		countQ := applySegmentFilters(s.client.Segment.Query(), resourceID, opts, s.dialect)
		total, cerr := countQ.Count(ctx)
		if cerr != nil {
			return nil, cerr
		}
		page.Total = &total
	}
	return page, nil
}

// buildQualityPredicate 按 quality_issues / quality_severity / quality_code 构造 SQL 谓词。
// 非法枚举值安全降级为不过滤（返回 nil）。severity 与 code 使用独立 EXISTS（AND）。
// 三个维度均只统计"待处理"的 issue：disposition 为 dismissed 的已被用户驳回，
// 不算问题（与 qa.NeedsAction 语义一致）；disposition 缺失（旧数据）视为 pending。
// SQLite 与 PostgreSQL 的 JSON 函数不同，按 dialectName 分支：SQLite 使用 JSON1
// （json_array_length / json_each / json_extract），PostgreSQL 使用 jsonb_*
// （jsonb_typeof / jsonb_array_length / jsonb_array_elements / ->>）。
//
// 注意：不能使用 sql.ExprP("... = ?", val)。ent 的原始表达式（sql.Expr）直接把
// 模板字符串原样输出，不会把 "?" 重新编号为 Postgres 的 "$N"；而 "?" 在 Postgres
// 里是 jsonb 的"键存在"运算符，会导致 "syntax error at or near ")""（SQLSTATE 42601）。
// severity（warning|error）与 code（qa.IsFilterableIssueCode 白名单）均已强校验，
// 直接以单引号字面量内联即可，避免占位符冲突。
func buildQualityPredicate(opts ResourceSegmentListOptions, dialectName string) predicate.Segment {
	usePostgres := dialectName == dialect.Postgres
	var preds []predicate.Segment

	switch opts.QualityIssues {
	case "has":
		preds = append(preds, predicate.Segment(func(s *sql.Selector) {
			col := s.C(segment.FieldQualityIssues)
			if usePostgres {
				// 存在至少一个未驳回（disposition 缺失或 != 'dismissed'）的 issue
				s.Where(sql.ExprP(fmt.Sprintf("jsonb_typeof(%s) = 'array' AND EXISTS (SELECT 1 FROM jsonb_array_elements(%s) AS v WHERE v ->> 'disposition' IS NULL OR v ->> 'disposition' != 'dismissed')", col, col)))
				return
			}
			s.Where(sql.ExprP(fmt.Sprintf("EXISTS (SELECT 1 FROM json_each(%s) WHERE json_extract(value, '$.disposition') IS NULL OR json_extract(value, '$.disposition') != 'dismissed')", col)))
		}))
	case "none":
		preds = append(preds, predicate.Segment(func(s *sql.Selector) {
			col := s.C(segment.FieldQualityIssues)
			if usePostgres {
				s.Where(sql.ExprP(fmt.Sprintf("%s IS NULL OR jsonb_typeof(%s) != 'array' OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(%s) AS v WHERE v ->> 'disposition' IS NULL OR v ->> 'disposition' != 'dismissed')", col, col, col)))
				return
			}
			s.Where(sql.ExprP(fmt.Sprintf("%s IS NULL OR NOT EXISTS (SELECT 1 FROM json_each(%s) WHERE json_extract(value, '$.disposition') IS NULL OR json_extract(value, '$.disposition') != 'dismissed')", col, col)))
		}))
	}

	switch opts.QualitySeverity {
	case "warning", "error":
		sev := opts.QualitySeverity
		preds = append(preds, predicate.Segment(func(s *sql.Selector) {
			col := s.C(segment.FieldQualityIssues)
			if usePostgres {
				s.Where(sql.ExprP(
					fmt.Sprintf("jsonb_typeof(%s) = 'array' AND EXISTS (SELECT 1 FROM jsonb_array_elements(%s) AS v WHERE (v ->> 'disposition' IS NULL OR v ->> 'disposition' != 'dismissed') AND v ->> 'severity' = '%s')", col, col, sev),
				))
				return
			}
			s.Where(sql.ExprP(
				fmt.Sprintf("EXISTS (SELECT 1 FROM json_each(%s) WHERE (json_extract(value, '$.disposition') IS NULL OR json_extract(value, '$.disposition') != 'dismissed') AND json_extract(value, '$.severity') = '%s')", col, sev),
			))
		}))
	}

	if qa.IsFilterableIssueCode(opts.QualityCode) {
		code := opts.QualityCode
		preds = append(preds, predicate.Segment(func(s *sql.Selector) {
			col := s.C(segment.FieldQualityIssues)
			if usePostgres {
				s.Where(sql.ExprP(
					fmt.Sprintf("jsonb_typeof(%s) = 'array' AND EXISTS (SELECT 1 FROM jsonb_array_elements(%s) AS v WHERE (v ->> 'disposition' IS NULL OR v ->> 'disposition' != 'dismissed') AND v ->> 'code' = '%s')", col, col, code),
				))
				return
			}
			s.Where(sql.ExprP(
				fmt.Sprintf("EXISTS (SELECT 1 FROM json_each(%s) WHERE (json_extract(value, '$.disposition') IS NULL OR json_extract(value, '$.disposition') != 'dismissed') AND json_extract(value, '$.code') = '%s')", col, code),
			))
		}))
	}

	switch len(preds) {
	case 0:
		return nil
	case 1:
		return preds[0]
	default:
		return segment.And(preds...)
	}
}

func (s *SegmentService) UpdateResourceSegment(ctx context.Context, actorUserID, projectID, resourceID, segmentID int, input ResourceSegmentUpdateInput) (*ent.Segment, error) {
	res, err := s.requireResourceAccess(ctx, actorUserID, projectID, resourceID, true)
	if err != nil {
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
	// QA 输入：source/target 取变更后的值（若未变更则用现状），供 targetChanged 时重算。
	newSource := current.SourceText
	var newTarget string

	if input.SourceText != nil {
		source := strings.TrimSpace(*input.SourceText)
		if source == "" {
			return nil, ErrInvalidInput
		}
		update.SetSourceText(source).SetStatus(SegmentStatusPending)
		changed = true
		sourceChanged = true
		newSource = source
	}
	if input.TargetText != nil {
		target := strings.TrimSpace(*input.TargetText)
		if target == "" {
			return nil, ErrInvalidInput
		}
		update.SetTargetText(target).SetStatus(SegmentStatusEdited).SetReviewedByID(actorUserID)
		changed = true
		targetChanged = true
		newTarget = target
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
		// 原文变更使旧译文失效：清空译文与审核信息。
		// 必须在未显式设置 target_text 时执行，否则会与 SetTargetText
		// 同时存在，触发 PostgreSQL "multiple assignments to same column" 错误。
		update.ClearTargetText().ClearReviewedBy()
	}

	// quality_issues 统一处理（避免与 SetTargetText 同列多次赋值）：
	//   - targetChanged：重跑零配置确定性 QA，与旧 issues 对账后用新结果覆盖；
	//   - sourceChanged && !targetChanged：旧译文失效，无译文不跑 QA，清空旧 issues；
	//   - 仅 comment 变更：不触碰 quality_issues，保持现状。
	switch {
	case targetChanged:
		project, perr := s.client.Project.Get(ctx, projectID)
		var freshIssuesByIndex map[int][]qa.QualityIssue
		if perr != nil {
			s.logger.Warn("manual edit QA: load project failed", "projectID", projectID, "error", perr)
		} else {
			freshIssuesByIndex, _ = s.runManualEditQABatch(ctx, project, res, []qaBatchInput{{
				Index:      current.SegmentIndex,
				SourceText: newSource,
				TargetText: newTarget,
				OldTarget:  oldTargetText(current),
				Meta:       current.Meta,
			}})
		}
		issues := freshIssuesByIndex[current.SegmentIndex]
		// 对账：手动编辑改了译文，重跑零配置确定性 QA。同指纹 issue 的裁决
		// （dismissed 等）应继承，避免用户标了"不是问题"的模式在下次编辑后被冲掉。
		// 注意：runManualEditQABatch 只跑 ZeroConfigDeterministicChecks 白名单，不跑
		// length_ratio/术语表/文档级检查，所以旧的非白名单 issues 会被自然清除
		// （这些 checker 未运行，指纹消失）。这是预期行为。
		issues = qa.ReconcileIssues(issues, current.QualityIssues)
		if len(issues) > 0 {
			update.SetQualityIssues(issues)
		} else {
			update.ClearQualityIssues()
		}
	case sourceChanged:
		update.ClearQualityIssues()
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

// runManualEditQABatch 对手动编辑的译文批量运行零配置确定性 QA 检查。
//
// 仅跑 ZeroConfigDeterministicChecks 白名单，避免 length_ratio 用默认阈值与正式
// 翻译流程判定矛盾；不加载术语表，也不跑文档级检查。Protected 区不持久化无法
// 重建，依赖它的 checker 退化为基础扫描。
//
// ZeroConfigDeterministicChecks 白名单排除了 duplicate_source_divergence（文档级、
// 需多段输入）等跨段检查，因此 engine.Run 对 N 个 CheckInput 等价于 N 次单段调用，
// 批量不会改变任何单段的 QA 结果。这是批量复用安全性的依据。
//
// 调用方负责在合适的连接上预查 project（单段编辑走 s.client；事务内批量替换走 tx），
// 避免 MaxOpenConns(1) 下事务占用唯一连接后再用 s.client 查询造成的死锁。
// QA 规则执行本身不阻塞编辑：按现有手动编辑约定记录日志并返回 nil issues。
func (s *SegmentService) runManualEditQABatch(ctx context.Context, project *ent.Project, res *ent.Resource, inputs []qaBatchInput) (map[int][]qa.QualityIssue, error) {
	cfg := qa.DefaultConfig()
	cfg.Enabled = true
	cfg.SourceLang = project.SourceLang
	cfg.TargetLang = project.TargetLang
	cfg.Format = res.Format
	cfg.Checks = qa.ZeroConfigDeterministicChecks()
	engine := qa.NewEngine(cfg, s.logger)

	checkInputs := make([]qa.CheckInput, 0, len(inputs))
	for _, input := range inputs {
		var metaMap map[string]any
		if input.Meta != nil {
			if err := json.Unmarshal([]byte(*input.Meta), &metaMap); err != nil {
				s.logger.Warn("manual edit QA: parse meta failed", "segmentIndex", input.Index, "error", err)
			}
		}
		checkInputs = append(checkInputs, qa.CheckInput{
			Index:      input.Index,
			SourceText: input.SourceText,
			TargetText: input.TargetText,
			Meta:       metaMap,
		})
	}

	issuesByIndex := make(map[int][]qa.QualityIssue)
	for _, issue := range engine.Run(ctx, checkInputs) {
		issuesByIndex[issue.SegmentIndex] = append(issuesByIndex[issue.SegmentIndex], issue)
	}
	// DedupIssues 的指纹是 (code, matched_text)，不含段索引；必须先按段分组再
	// 各自去重，否则不同段的同指纹 issue（如相同的双空格触发 repeated_space）
	// 会被整批去重吞掉，违背上方"批量等价于 N 次单段调用"的约定。
	for idx, issues := range issuesByIndex {
		issuesByIndex[idx] = qa.DedupIssues(issues)
	}
	for _, input := range inputs {
		if input.OldTarget == "" {
			continue
		}
		issuesByIndex[input.Index] = append(issuesByIndex[input.Index], qa.RubyTagLossIssues(input.Index, input.OldTarget, input.TargetText)...)
	}
	return issuesByIndex, nil
}

func oldTargetText(seg *ent.Segment) string {
	if seg.TargetText == nil {
		return ""
	}
	return *seg.TargetText
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
	ApprovedCount   int    `json:"approved_count"`
}

type segmentGroupEntry struct {
	groupKey   string
	groupTitle string
	minIndex   int
	count      int
	translated int
	approved   int
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
		if row.Status == SegmentStatusApproved {
			g.approved++
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
			ApprovedCount:   g.approved,
		})
	}

	return result, nil
}
