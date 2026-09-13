package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segmentrevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/markup"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service/segmatch"
)

// 搜索替换跳过原因。与 OpenAPI SearchReplaceSkippedItemReason 对齐。
const (
	skipReasonNoLongerMatches = "no_longer_matches" // apply 时当前译文已不含匹配
	skipReasonEmptyResult     = "empty_result"      // 替换后译文经 trim 为空
	skipReasonTargetDiverged  = "target_diverged"   // undo 时该段已被后续编辑改变
	// skipReasonInvalidMarkup 替换后译文在需要 well-formed 的格式下结构非法。
	skipReasonInvalidMarkup = "invalid_markup"
)

// replaceResultMarkupInvalid 报告替换结果在需要 well-formed 译文的格式下是否结构非法。
// preview 与 apply 必须共用同一判定，否则预览显示会改、实际却被跳过，误导用户。
func replaceResultMarkupInvalid(format, newTarget string) bool {
	return markup.RequiresWellFormedTargets(format) && markup.ValidateFragment(newTarget) != nil
}

var (
	// ErrRevisionNotFound 表示撤销目标 operation 不存在或已被按龄裁剪。
	ErrRevisionNotFound = errors.New("search-replace operation not found or pruned")
	// ErrNoReversibleSegments 表示该 operation 的全部段落都已被后续编辑改变，无可撤销段落。
	ErrNoReversibleSegments = errors.New("no reversible segments: all diverged since the operation")
)

// SearchReplaceOptions 是预览/应用共享的匹配与过滤选项。字段与生成的 api 请求类型一一对应，
// 由 handler 层映射；service 层不依赖 api 包，沿用本包内部 DTO 约定（见 ListResourceSegments）。
type SearchReplaceOptions struct {
	Find            string
	ReplaceWith     string
	MatchMode       string // "substring" | "regex"，空值默认 substring
	CaseSensitive   *bool
	WholeWord       *bool
	MaxResults      *int // 仅预览用
	Status          string
	QualityIssues   string
	QualitySeverity string
	QualityCode     string
	GroupKey        string
	SegmentIDs      []int
}

// SearchReplacePreviewItem 是预览样本：单段替换前后的对照。
type SearchReplacePreviewItem struct {
	SegmentID    int
	SegmentIndex int
	SourceText   string
	Before       string
	After        string
	MatchCount   int
}

// SearchReplacePreviewResult 是预览结果：影响面统计 + 截断样本。
type SearchReplacePreviewResult struct {
	MatchedSegmentCount int
	TotalReplacements   int
	Items               []SearchReplacePreviewItem
}

// SearchReplaceSkip 记录被跳过的段落及原因。
type SearchReplaceSkip struct {
	SegmentID int
	Reason    string
}

// SearchReplaceApplyResult 是应用结果：operation_id 供撤销，items 为成功段落。
type SearchReplaceApplyResult struct {
	OperationID  string
	AppliedCount int
	SkippedCount int
	Items        []*ent.Segment
	Skipped      []SearchReplaceSkip
}

// SearchReplaceUndoResult 是撤销结果：undo_operation_id 可再撤销（=重做）。
type SearchReplaceUndoResult struct {
	UndoOperationID string
	UndoneCount     int
	SkippedCount    int
	Items           []*ent.Segment
	Skipped         []SearchReplaceSkip
}

// PreviewSearchReplace 在资源段落译文上执行只读搜索替换预览，不持久化。
// 只对 target_text 匹配；source_text 不参与。空译文段不参与。
func (s *SegmentService) PreviewSearchReplace(ctx context.Context, actorUserID, projectID, resourceID int, opts SearchReplaceOptions) (*SearchReplacePreviewResult, error) {
	res, err := s.requireResourceAccess(ctx, actorUserID, projectID, resourceID, false)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.Find) == "" {
		return nil, ErrInvalidInput
	}
	matcher, err := segmatch.NewMatcher(segmatch.Options{
		Find:          opts.Find,
		MatchMode:     opts.MatchMode,
		CaseSensitive: opts.CaseSensitive,
		WholeWord:     opts.WholeWord,
	})
	if err != nil {
		return nil, err
	}

	maxResults := 20
	if opts.MaxResults != nil {
		maxResults = *opts.MaxResults
	}
	if maxResults < 1 {
		maxResults = 1
	}
	if maxResults > 100 {
		maxResults = 100
	}

	q := s.client.Segment.Query().Where(segment.ResourceIDEQ(resourceID))
	if opts.Status != "" {
		q = q.Where(segment.StatusEQ(segment.Status(opts.Status)))
	}
	if len(opts.SegmentIDs) > 0 {
		q = q.Where(segment.IDIn(opts.SegmentIDs...))
	}
	if p := buildQualityPredicate(ResourceSegmentListOptions{
		QualityIssues:   opts.QualityIssues,
		QualitySeverity: opts.QualitySeverity,
		QualityCode:     opts.QualityCode,
	}, s.dialect); p != nil {
		q = q.Where(p)
	}

	rows, err := q.Order(ent.Asc(segment.FieldSegmentIndex)).All(ctx)
	if err != nil {
		return nil, err
	}

	// group_key 需在应用层解析 JSON meta.epub_file 过滤（与 ListResourceSegments 一致）。
	candidates := rows
	if opts.GroupKey != "" {
		candidates = nil
		for _, row := range rows {
			if row.Meta == nil {
				continue
			}
			var meta map[string]any
			if json.Unmarshal([]byte(*row.Meta), &meta) != nil {
				continue
			}
			if v, ok := meta["epub_file"].(string); ok && v == opts.GroupKey {
				candidates = append(candidates, row)
			}
		}
	}

	result := &SearchReplacePreviewResult{}
	for _, seg := range candidates {
		if seg.TargetText == nil {
			continue
		}
		matches := matcher.Find(*seg.TargetText)
		if len(matches) == 0 {
			continue
		}
		after, _ := matcher.ReplaceAll(*seg.TargetText, opts.ReplaceWith)
		// 与 apply 同一判定：结构非法的替换结果 apply 会跳过，预览就不该展示它，
		// 否则用户看到的改动数与实际生效数不一致。注意判定作用于替换「结果」，
		// 所以用搜索替换修补已损坏标签（结果合法）不受影响。
		if replaceResultMarkupInvalid(res.Format, after) {
			continue
		}
		result.MatchedSegmentCount++
		result.TotalReplacements += len(matches)
		if len(result.Items) < maxResults {
			result.Items = append(result.Items, SearchReplacePreviewItem{
				SegmentID:    seg.ID,
				SegmentIndex: seg.SegmentIndex,
				SourceText:   seg.SourceText,
				Before:       *seg.TargetText,
				After:        after,
				MatchCount:   len(matches),
			})
		}
	}
	return result, nil
}

// ApplySearchReplace 对资源段落译文应用搜索替换。无状态：apply 接收匹配参数，
// 在事务内对每段当前 target_text 重新匹配后替换。替换成功的段落状态固定为 edited、
// reviewed_by 为当前用户，并重跑零配置确定性 QA 与既有裁决对账。每次成功应用写入
// 一笔可撤销的 SegmentRevision 历史（operation_id）。
func (s *SegmentService) ApplySearchReplace(ctx context.Context, actorUserID, projectID, resourceID int, opts SearchReplaceOptions) (*SearchReplaceApplyResult, error) {
	res, err := s.requireResourceAccess(ctx, actorUserID, projectID, resourceID, true)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.Find) == "" {
		return nil, ErrInvalidInput
	}
	matcher, err := segmatch.NewMatcher(segmatch.Options{
		Find:          opts.Find,
		MatchMode:     opts.MatchMode,
		CaseSensitive: opts.CaseSensitive,
		WholeWord:     opts.WholeWord,
	})
	if err != nil {
		return nil, err
	}

	// 入口资源级按龄裁剪：写时自清洁，无需后台任务。
	s.pruneResourceRevisions(ctx, resourceID)

	operationID := newOperationID("sr-")
	result := &SearchReplaceApplyResult{OperationID: operationID}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("search-replace: begin transaction: %w", err)
	}

	// 候选集：segment_ids 非空则限定，否则资源全部段。apply 不应用 status/quality 过滤
	//（这些仅服务预览展示；应用范围由 segment_ids 或全部段决定）。
	candidateQuery := tx.Segment.Query().Where(segment.ResourceIDEQ(resourceID))
	if len(opts.SegmentIDs) > 0 {
		candidateQuery = candidateQuery.Where(segment.IDIn(opts.SegmentIDs...))
	}
	candidates, err := candidateQuery.Order(ent.Asc(segment.FieldSegmentIndex)).WithReviewedBy().All(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("search-replace: load candidates: %w", err)
	}

	// 第一遍：分流跳过与待应用。
	type pending struct {
		seg       *ent.Segment
		newTarget string
	}
	var pendings []pending
	for _, seg := range candidates {
		if seg.TargetText == nil {
			continue
		}
		if len(matcher.Find(*seg.TargetText)) == 0 {
			result.Skipped = append(result.Skipped, SearchReplaceSkip{SegmentID: seg.ID, Reason: skipReasonNoLongerMatches})
			continue
		}
		newTarget, _ := matcher.ReplaceAll(*seg.TargetText, opts.ReplaceWith)
		if strings.TrimSpace(newTarget) == "" {
			result.Skipped = append(result.Skipped, SearchReplaceSkip{SegmentID: seg.ID, Reason: skipReasonEmptyResult})
			continue
		}
		// 跳过而非阻断：一整批里因 1 段结构非法而整批失败太粗暴，而 Skipped 通道
		// 本就是为「这段不该改」设计的，预览也走同一判定（见 replaceResultMarkupInvalid），
		// 用户在预览里看不到该段的改动，apply 结果中会给出原因。
		if replaceResultMarkupInvalid(res.Format, newTarget) {
			result.Skipped = append(result.Skipped, SearchReplaceSkip{SegmentID: seg.ID, Reason: skipReasonInvalidMarkup})
			continue
		}
		pendings = append(pendings, pending{seg: seg, newTarget: newTarget})
	}

	// 批量 QA：一次 Project.Get + 一个 engine + 一次 Run，逐段分发 fresh issues。
	// project 走 tx 连接，复用当前事务占用的唯一连接，避免 MaxOpenConns(1) 下
	// 用 s.client 另开连接查询造成的死锁。
	inputs := make([]qaBatchInput, 0, len(pendings))
	for _, p := range pendings {
		inputs = append(inputs, qaBatchInput{
			Index:      p.seg.SegmentIndex,
			SourceText: p.seg.SourceText,
			TargetText: p.newTarget,
			OldTarget:  oldTargetText(p.seg),
			Meta:       p.seg.Meta,
		})
	}
	project, perr := tx.Project.Get(ctx, projectID)
	var freshByIndex map[int][]qa.QualityIssue
	if perr != nil {
		s.logger.Warn("search-replace QA: load project failed", "projectID", projectID, "error", perr)
	} else {
		freshByIndex, _ = s.runManualEditQABatch(ctx, project, res, inputs)
	}

	// 第二遍：在事务内逐段更新译文 + 写历史快照。
	appliedIDs := make([]int, 0, len(pendings))
	for _, p := range pendings {
		seg := p.seg
		fresh := freshByIndex[seg.SegmentIndex]
		reconciled := qa.ReconcileIssues(fresh, seg.QualityIssues)

		upd := tx.Segment.UpdateOneID(seg.ID).
			SetTargetText(p.newTarget).
			SetStatus(SegmentStatusEdited).
			SetReviewedByID(actorUserID)
		if len(reconciled) > 0 {
			upd.SetQualityIssues(reconciled)
		} else {
			upd.ClearQualityIssues()
		}
		if _, err := upd.Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("search-replace: update segment %d: %w", seg.ID, err)
		}

		// 写 replace 历史快照：before=当前（替换前），after=新值。
		afterReviewer := actorUserID
		newTargetCopy := p.newTarget
		revCreate := tx.SegmentRevision.Create().
			SetSegmentID(seg.ID).
			SetResourceID(resourceID).
			SetOperationID(operationID).
			SetKind(segmentrevision.KindReplace).
			SetNillableBeforeTarget(seg.TargetText).
			SetNillableAfterTarget(&newTargetCopy).
			SetBeforeStatus(segmentrevision.BeforeStatus(string(seg.Status))).
			SetAfterStatus(segmentrevision.AfterStatusEdited).
			SetNillableBeforeReviewerID(currentReviewerID(seg)).
			SetNillableAfterReviewerID(&afterReviewer).
			SetActorID(actorUserID)
		if len(seg.QualityIssues) > 0 {
			revCreate.SetBeforeIssues(seg.QualityIssues)
		}
		if len(reconciled) > 0 {
			revCreate.SetAfterIssues(reconciled)
		}
		if _, err := revCreate.Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("search-replace: create revision for segment %d: %w", seg.ID, err)
		}
		appliedIDs = append(appliedIDs, seg.ID)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("search-replace: commit: %w", err)
	}

	result.AppliedCount = len(appliedIDs)
	result.SkippedCount = len(result.Skipped)
	items, err := s.client.Segment.Query().Where(segment.IDIn(appliedIDs...)).WithReviewedBy().WithResource().All(ctx)
	if err != nil {
		return nil, err
	}
	result.Items = items
	return result, nil
}

// UndoSearchReplace 撤销一笔搜索替换（或撤销一笔先前的撤销，即重做）。按 operation_id
// 定位历史快照，在事务内逐段乐观校验：当前段的 target_text/status/reviewed_by/
// quality_issues 仍与快照 after 一致才回滚到 before；任一字段被后续编辑改变则跳过
// （target_diverged），不覆盖他人工作。撤销自身写入新的 reverse 历史与新的
// undo_operation_id，故可再撤销。operation 不存在或已被裁剪返回 ErrRevisionNotFound；
// 全部段落发散返回 ErrNoReversibleSegments。
//
// 刻意不加译文结构守卫：撤销的目标是回到历史状态，若因「回滚后的译文结构非法」而
// 拒绝，用户就会被永久锁在当前状态里出不来。历史里的非法译文由导出预检与审校界面
// 的 xml_tag_mismatch 负责暴露。
func (s *SegmentService) UndoSearchReplace(ctx context.Context, actorUserID, projectID, resourceID int, operationID string) (*SearchReplaceUndoResult, error) {
	if _, err := s.requireResourceAccess(ctx, actorUserID, projectID, resourceID, true); err != nil {
		return nil, err
	}

	s.pruneResourceRevisions(ctx, resourceID)

	revs, err := s.client.SegmentRevision.Query().
		Where(segmentrevision.OperationIDEQ(operationID), segmentrevision.ResourceIDEQ(resourceID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("undo: load revisions: %w", err)
	}
	if len(revs) == 0 {
		return nil, ErrRevisionNotFound
	}

	segIDs := make([]int, 0, len(revs))
	for _, rev := range revs {
		segIDs = append(segIDs, rev.SegmentID)
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("undo: begin transaction: %w", err)
	}

	segs, err := tx.Segment.Query().Where(segment.IDIn(segIDs...)).WithReviewedBy().All(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("undo: load segments: %w", err)
	}
	segMap := make(map[int]*ent.Segment, len(segs))
	for _, seg := range segs {
		segMap[seg.ID] = seg
	}

	undoOperationID := newOperationID("sr-u-")
	result := &SearchReplaceUndoResult{UndoOperationID: undoOperationID}
	undoneIDs := make([]int, 0, len(revs))

	for _, rev := range revs {
		seg := segMap[rev.SegmentID]
		if seg == nil {
			// 段落已被删除：无法安全回滚，跳过。
			result.Skipped = append(result.Skipped, SearchReplaceSkip{SegmentID: rev.SegmentID, Reason: skipReasonTargetDiverged})
			continue
		}
		if !segmentMatchesRevision(seg, rev) {
			result.Skipped = append(result.Skipped, SearchReplaceSkip{SegmentID: seg.ID, Reason: skipReasonTargetDiverged})
			continue
		}

		// 恢复 before 快照（4 字段）。
		upd := tx.Segment.UpdateOneID(seg.ID)
		if rev.BeforeTarget == nil {
			upd.ClearTargetText()
		} else {
			upd.SetTargetText(*rev.BeforeTarget)
		}
		if rev.BeforeReviewerID == nil {
			upd.ClearReviewedBy()
		} else {
			upd.SetReviewedByID(*rev.BeforeReviewerID)
		}
		upd.SetStatus(segment.Status(string(rev.BeforeStatus)))
		if len(rev.BeforeIssues) > 0 {
			upd.SetQualityIssues(rev.BeforeIssues)
		} else {
			upd.ClearQualityIssues()
		}
		if _, err := upd.Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("undo: restore segment %d: %w", seg.ID, err)
		}

		// 写 reverse 历史：before=撤销前（原 after 状态），after=撤销后（原 before 状态）。
		revCreate := tx.SegmentRevision.Create().
			SetSegmentID(seg.ID).
			SetResourceID(resourceID).
			SetOperationID(undoOperationID).
			SetKind(segmentrevision.KindReverse).
			SetNillableBeforeTarget(seg.TargetText).
			SetNillableAfterTarget(rev.BeforeTarget).
			SetBeforeStatus(segmentrevision.BeforeStatus(string(seg.Status))).
			SetAfterStatus(segmentrevision.AfterStatus(string(rev.BeforeStatus))).
			SetNillableBeforeReviewerID(currentReviewerID(seg)).
			SetNillableAfterReviewerID(rev.BeforeReviewerID).
			SetActorID(actorUserID)
		if len(seg.QualityIssues) > 0 {
			revCreate.SetBeforeIssues(seg.QualityIssues)
		}
		if len(rev.BeforeIssues) > 0 {
			revCreate.SetAfterIssues(rev.BeforeIssues)
		}
		if _, err := revCreate.Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("undo: create reverse revision for segment %d: %w", seg.ID, err)
		}
		undoneIDs = append(undoneIDs, seg.ID)
	}

	if len(undoneIDs) == 0 {
		_ = tx.Rollback()
		return nil, ErrNoReversibleSegments
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("undo: commit: %w", err)
	}

	result.UndoneCount = len(undoneIDs)
	result.SkippedCount = len(result.Skipped)
	items, err := s.client.Segment.Query().Where(segment.IDIn(undoneIDs...)).WithReviewedBy().WithResource().All(ctx)
	if err != nil {
		return nil, err
	}
	result.Items = items
	return result, nil
}

// pruneResourceRevisions 删除该资源下超过保留期的整笔历史（按 created_at）。写时自清洁，
// 错误只记日志不阻塞主流程——裁剪失败仅意味着撤销可用期略长，不影响替换/撤销正确性。
func (s *SegmentService) pruneResourceRevisions(ctx context.Context, resourceID int) {
	if s.revisionRetention <= 0 {
		return
	}
	cutoff := time.Now().Add(-s.revisionRetention)
	if _, err := s.client.SegmentRevision.Delete().
		Where(segmentrevision.ResourceIDEQ(resourceID), segmentrevision.CreatedAtLT(cutoff)).
		Exec(ctx); err != nil {
		s.logger.Warn("search-replace: prune revisions failed", "resourceID", resourceID, "error", err)
	}
}

// segmentMatchesRevision 乐观校验：当前段的 4 个被替换改动的字段是否仍与快照 after 一致。
// 任一字段发散即视为已被后续编辑改变，撤销应跳过该段。
func segmentMatchesRevision(seg *ent.Segment, rev *ent.SegmentRevision) bool {
	return ptrStringEq(seg.TargetText, rev.AfterTarget) &&
		string(seg.Status) == string(rev.AfterStatus) &&
		ptrIntEq(currentReviewerID(seg), rev.AfterReviewerID) &&
		equalIssues(seg.QualityIssues, rev.AfterIssues)
}

// currentReviewerID 取段落的审核人 user id。reviewed_by 是 ent edge（非独立 FK 列），
// 需调用方 WithReviewedBy() 预加载；未加载或无审核人时返回 nil。
func currentReviewerID(seg *ent.Segment) *int {
	if seg == nil || seg.Edges.ReviewedBy == nil {
		return nil
	}
	id := seg.Edges.ReviewedBy.ID
	return &id
}

func ptrStringEq(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func ptrIntEq(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// equalIssues 精确比较两批质量问题是否一致。按 qa.Fingerprint 建索引后逐一深度比较全字段
// （含 disposition/decided_by/decided_at/note/span），因此用户在替换后对某 issue 改了裁决
// 会被识别为发散，撤销会跳过该段而非覆盖裁决。不使用 JSON 字节比较——ent 编码往返可能不稳定。
// 假设同段同指纹的 issue 唯一（qa 设计如此）；若出现重复指纹则保守判发散。
func equalIssues(a, b []qa.QualityIssue) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	ma := make(map[string]qa.QualityIssue, len(a))
	for _, i := range a {
		ma[qa.Fingerprint(i)] = i
	}
	mb := make(map[string]qa.QualityIssue, len(b))
	for _, i := range b {
		mb[qa.Fingerprint(i)] = i
	}
	if len(ma) != len(a) || len(mb) != len(b) {
		return false
	}
	if len(ma) != len(mb) {
		return false
	}
	for fp, ia := range ma {
		ib, ok := mb[fp]
		if !ok || !reflect.DeepEqual(ia, ib) {
			return false
		}
	}
	return true
}

// newOperationID 生成带前缀的 operation 标识（crypto/rand 16 字节 hex）。
func newOperationID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// rand.Read 失败极罕见；退化到时间戳保证唯一性与非空。
		return prefix + time.Now().UTC().Format("20060102150405.000000")
	}
	return prefix + hex.EncodeToString(b)
}
