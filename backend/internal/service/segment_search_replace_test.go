package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segmentrevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service/segmatch"
)

// searchReplaceSetup 建立独立的 in-memory client、用户/项目/资源，并按 targets 段落。
// 返回 svc 与同一 client，便于测试直接查询 SegmentRevision 历史表。
func searchReplaceSetup(t *testing.T, targets ...string) (*SegmentService, *ent.Client, context.Context, *ent.User, *ent.Project, *ent.Resource) {
	t.Helper()
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "sr-user")
	project := createTestProject(t, client, "sr-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/sr.txt")
	for i, tgt := range targets {
		src := "source " + strings.Repeat("a", i)
		if tgt != "" {
			createTestSegmentWithTarget(t, client, res.ID, i, src, tgt, nil)
		} else {
			createTestSegment(t, client, res.ID, i, src, nil)
		}
	}
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	return svc, client, ctx, user, project, res
}

func TestPreviewSearchReplaceCountsAndSamples(t *testing.T) {
	svc, _, ctx, user, project, res := searchReplaceSetup(t,
		"colour pen colour", // 2 命中
		"Colour pen",        // 大小写不同
		"plain text",        // 无命中
	)
	boolPtr := func(b bool) *bool { return &b }

	t.Run("case_sensitive_default", func(t *testing.T) {
		result, err := svc.PreviewSearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
			Find:        "colour",
			ReplaceWith: "color",
		})
		if err != nil {
			t.Fatalf("PreviewSearchReplace: %v", err)
		}
		if result.MatchedSegmentCount != 1 {
			t.Fatalf("matched_segment_count=%d want 1", result.MatchedSegmentCount)
		}
		if result.TotalReplacements != 2 {
			t.Fatalf("total_replacements=%d want 2", result.TotalReplacements)
		}
		if len(result.Items) != 1 || result.Items[0].MatchCount != 2 {
			t.Fatalf("items=%v want 1 item match_count=2", result.Items)
		}
		if result.Items[0].After != "color pen color" {
			t.Fatalf("after=%q want %q", result.Items[0].After, "color pen color")
		}
	})

	t.Run("case_insensitive_matches_uppercase", func(t *testing.T) {
		result, err := svc.PreviewSearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
			Find:          "colour",
			ReplaceWith:   "color",
			CaseSensitive: boolPtr(false),
		})
		if err != nil {
			t.Fatalf("PreviewSearchReplace: %v", err)
		}
		if result.MatchedSegmentCount != 2 {
			t.Fatalf("matched_segment_count=%d want 2 (case-insensitive)", result.MatchedSegmentCount)
		}
	})

	t.Run("max_results_truncates_items", func(t *testing.T) {
		max := 1
		result, err := svc.PreviewSearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
			Find:          "colour",
			ReplaceWith:   "color",
			CaseSensitive: boolPtr(false),
			MaxResults:    &max,
		})
		if err != nil {
			t.Fatalf("PreviewSearchReplace: %v", err)
		}
		if result.MatchedSegmentCount != 2 {
			t.Fatalf("matched_segment_count=%d want 2 (full count even if sample truncated)", result.MatchedSegmentCount)
		}
		if len(result.Items) > 1 {
			t.Fatalf("items=%d want <=1 (truncated)", len(result.Items))
		}
	})
}

func TestPreviewSearchReplaceRegexAndCapture(t *testing.T) {
	svc, _, ctx, user, project, res := searchReplaceSetup(t, "alpha bravo")
	result, err := svc.PreviewSearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
		Find:        `(\w+)\s+(\w+)`,
		ReplaceWith: "$2 $1",
		MatchMode:   "regex",
	})
	if err != nil {
		t.Fatalf("PreviewSearchReplace regex: %v", err)
	}
	if result.MatchedSegmentCount == 0 {
		t.Fatalf("expected regex match")
	}
	if result.Items[0].After != "bravo alpha" {
		t.Fatalf("after=%q want %q", result.Items[0].After, "bravo alpha")
	}
}

func TestPreviewSearchReplaceInvalidPattern(t *testing.T) {
	svc, _, ctx, user, project, res := searchReplaceSetup(t, "x")
	_, err := svc.PreviewSearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
		Find:        `[`,
		ReplaceWith: "y",
		MatchMode:   "regex",
	})
	if !errors.Is(err, segmatch.ErrInvalidPattern) {
		t.Fatalf("err=%v want ErrInvalidPattern", err)
	}
}

func TestPreviewSearchReplaceEmptyFind(t *testing.T) {
	svc, _, ctx, user, project, res := searchReplaceSetup(t, "x")
	_, err := svc.PreviewSearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
		Find:        "   ",
		ReplaceWith: "y",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err=%v want ErrInvalidInput", err)
	}
}

func TestApplySearchReplaceBasic(t *testing.T) {
	svc, client, ctx, user, project, res := searchReplaceSetup(t,
		"colour pen",
		"plain text", // 无命中
		"colour colour",
	)

	result, err := svc.ApplySearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
		Find:        "colour",
		ReplaceWith: "color",
	})
	if err != nil {
		t.Fatalf("ApplySearchReplace: %v", err)
	}
	if result.AppliedCount != 2 {
		t.Fatalf("applied_count=%d want 2", result.AppliedCount)
	}
	if result.SkippedCount != 1 || result.Skipped[0].Reason != "no_longer_matches" {
		t.Fatalf("skipped=%v want [no_longer_matches]", result.Skipped)
	}
	for _, seg := range result.Items {
		if seg.TargetText == nil || !strings.Contains(*seg.TargetText, "color") {
			t.Fatalf("segment %d target not replaced: %v", seg.ID, seg.TargetText)
		}
		if seg.Status != segment.StatusEdited {
			t.Fatalf("segment %d status=%q want edited", seg.ID, seg.Status)
		}
		if seg.Edges.ReviewedBy == nil || seg.Edges.ReviewedBy.ID != user.ID {
			t.Fatalf("segment %d reviewer=%v want %d", seg.ID, seg.Edges.ReviewedBy, user.ID)
		}
	}
	count, _ := client.SegmentRevision.Query().Where(segmentrevision.OperationIDEQ(result.OperationID)).Count(ctx)
	if count != 2 {
		t.Fatalf("revision count=%d want 2", count)
	}
}

func TestApplySearchReplaceEmptyResultSkipped(t *testing.T) {
	svc, _, ctx, user, project, res := searchReplaceSetup(t, "xxx")
	result, err := svc.ApplySearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
		Find:        "x",
		ReplaceWith: "",
	})
	if err != nil {
		t.Fatalf("ApplySearchReplace: %v", err)
	}
	if result.AppliedCount != 0 {
		t.Fatalf("applied_count=%d want 0 (all empty_result)", result.AppliedCount)
	}
	if result.SkippedCount != 1 || result.Skipped[0].Reason != "empty_result" {
		t.Fatalf("skipped=%v want [empty_result]", result.Skipped)
	}
}

func TestApplySearchReplaceRerunsQA(t *testing.T) {
	// 源文无引号类标点，译文替换后仍多出引号 → 触发 punctuation_surplus
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "sr-qa-user")
	project := createTestProject(t, client, "sr-qa-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/qa.txt")
	createTestSegmentWithTarget(t, client, res.ID, 0, "hello world", "””hello””", nil)
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	result, err := svc.ApplySearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
		Find:        "hello",
		ReplaceWith: "world",
	})
	if err != nil {
		t.Fatalf("ApplySearchReplace: %v", err)
	}
	if result.AppliedCount != 1 {
		t.Fatalf("applied_count=%d want 1", result.AppliedCount)
	}
	if !hasIssueCode(result.Items[0].QualityIssues, qa.CheckPunctuationSurplus) {
		t.Fatalf("expected punctuation_surplus after replace, got %v", result.Items[0].QualityIssues)
	}
}

func TestApplySearchReplaceQADedupPerSegment(t *testing.T) {
	// 两个段替换后的译文都含相同的连续空格，产生同指纹 (repeated_space, "  ")。
	// 批量 QA 必须按段独立去重，任一段的 issue 都不能被跨段整批去重吞掉。
	svc, _, ctx, user, project, res := searchReplaceSetup(t,
		"colour  pen",
		"colour  hat",
	)

	result, err := svc.ApplySearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
		Find:        "colour",
		ReplaceWith: "color",
	})
	if err != nil {
		t.Fatalf("ApplySearchReplace: %v", err)
	}
	if result.AppliedCount != 2 {
		t.Fatalf("applied_count=%d want 2", result.AppliedCount)
	}
	for _, seg := range result.Items {
		if !hasIssueCode(seg.QualityIssues, qa.CheckRepeatedSpace) {
			t.Fatalf("segment %d missing repeated_space issue, got %v", seg.ID, seg.QualityIssues)
		}
	}
}

func TestUndoSearchReplaceBasic(t *testing.T) {
	svc, client, ctx, user, project, res := searchReplaceSetup(t, "colour pen", "colour colour")
	apply, err := svc.ApplySearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
		Find:        "colour",
		ReplaceWith: "color",
	})
	if err != nil {
		t.Fatalf("ApplySearchReplace: %v", err)
	}

	undo, err := svc.UndoSearchReplace(ctx, user.ID, project.ID, res.ID, apply.OperationID)
	if err != nil {
		t.Fatalf("UndoSearchReplace: %v", err)
	}
	if undo.UndoneCount != 2 {
		t.Fatalf("undone_count=%d want 2", undo.UndoneCount)
	}
	for _, seg := range undo.Items {
		if seg.TargetText == nil || !strings.Contains(*seg.TargetText, "colour") {
			t.Fatalf("segment %d target not restored: %v", seg.ID, seg.TargetText)
		}
	}
	rcount, _ := client.SegmentRevision.Query().Where(segmentrevision.OperationIDEQ(undo.UndoOperationID)).Count(ctx)
	if rcount != 2 {
		t.Fatalf("reverse revision count=%d want 2", rcount)
	}
}

func TestUndoSearchReplaceRedo(t *testing.T) {
	svc, _, ctx, user, project, res := searchReplaceSetup(t, "colour pen")
	apply, err := svc.ApplySearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
		Find:        "colour",
		ReplaceWith: "color",
	})
	if err != nil {
		t.Fatalf("ApplySearchReplace: %v", err)
	}
	undo, err := svc.UndoSearchReplace(ctx, user.ID, project.ID, res.ID, apply.OperationID)
	if err != nil {
		t.Fatalf("Undo #1: %v", err)
	}
	redo, err := svc.UndoSearchReplace(ctx, user.ID, project.ID, res.ID, undo.UndoOperationID)
	if err != nil {
		t.Fatalf("Undo #2 (redo): %v", err)
	}
	if redo.UndoneCount != 1 {
		t.Fatalf("redo undone_count=%d want 1", redo.UndoneCount)
	}
	if redo.Items[0].TargetText == nil || !strings.Contains(*redo.Items[0].TargetText, "color") {
		t.Fatalf("redo target=%v want contains 'color'", redo.Items[0].TargetText)
	}
}

func TestUndoSearchReplaceDivergedSkipped(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "sr-div-user")
	project := createTestProject(t, client, "sr-div-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/div.txt")
	createTestSegmentWithTarget(t, client, res.ID, 0, "s0", "colour a", nil)
	createTestSegmentWithTarget(t, client, res.ID, 1, "s1", "colour b", nil)
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	apply, err := svc.ApplySearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
		Find:        "colour",
		ReplaceWith: "color",
	})
	if err != nil {
		t.Fatalf("ApplySearchReplace: %v", err)
	}
	// 手动改段 0 的译文，使其与 after 快照发散
	if _, err := client.Segment.UpdateOneID(apply.Items[0].ID).SetTargetText("manually changed").Save(ctx); err != nil {
		t.Fatalf("manual edit: %v", err)
	}

	undo, err := svc.UndoSearchReplace(ctx, user.ID, project.ID, res.ID, apply.OperationID)
	if err != nil {
		t.Fatalf("UndoSearchReplace: %v", err)
	}
	if undo.UndoneCount != 1 {
		t.Fatalf("undone_count=%d want 1 (one diverged)", undo.UndoneCount)
	}
	if undo.SkippedCount != 1 || undo.Skipped[0].Reason != "target_diverged" {
		t.Fatalf("skipped=%v want [target_diverged]", undo.Skipped)
	}
}

func TestUndoSearchReplaceAllDivergedConflict(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "sr-alldiv-user")
	project := createTestProject(t, client, "sr-alldiv-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/alldiv.txt")
	createTestSegmentWithTarget(t, client, res.ID, 0, "s0", "colour a", nil)
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	apply, err := svc.ApplySearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
		Find:        "colour",
		ReplaceWith: "color",
	})
	if err != nil {
		t.Fatalf("ApplySearchReplace: %v", err)
	}
	if _, err := client.Segment.UpdateOneID(apply.Items[0].ID).SetTargetText("manually changed").Save(ctx); err != nil {
		t.Fatalf("manual edit: %v", err)
	}

	_, err = svc.UndoSearchReplace(ctx, user.ID, project.ID, res.ID, apply.OperationID)
	if !errors.Is(err, ErrNoReversibleSegments) {
		t.Fatalf("err=%v want ErrNoReversibleSegments", err)
	}
}

func TestUndoSearchReplaceNotFound(t *testing.T) {
	svc, _, ctx, user, project, res := searchReplaceSetup(t, "x")
	_, err := svc.UndoSearchReplace(ctx, user.ID, project.ID, res.ID, "nonexistent-op")
	if !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("err=%v want ErrRevisionNotFound", err)
	}
}

func TestUndoSearchReplacePrunedByAge(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "sr-prune-user")
	project := createTestProject(t, client, "sr-prune-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/prune.txt")
	createTestSegmentWithTarget(t, client, res.ID, 0, "s0", "colour a", nil)
	// 极短保留期：undo 入口裁剪会删掉刚写入的 apply 记录
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 1*time.Nanosecond, nil)

	apply, err := svc.ApplySearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
		Find:        "colour",
		ReplaceWith: "color",
	})
	if err != nil {
		t.Fatalf("ApplySearchReplace: %v", err)
	}
	// 记录写入时 created_at≈now，保留期 1ns；稍等使其超过保留期
	time.Sleep(2 * time.Millisecond)

	_, err = svc.UndoSearchReplace(ctx, user.ID, project.ID, res.ID, apply.OperationID)
	if !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("err=%v want ErrRevisionNotFound (pruned)", err)
	}
	count, _ := client.SegmentRevision.Query().Where(segmentrevision.OperationIDEQ(apply.OperationID)).Count(ctx)
	if count != 0 {
		t.Fatalf("revision count=%d want 0 (pruned)", count)
	}
}

// TestPreviewSearchReplaceQualityNoneIsolation 防回归：搜索替换预览带
// quality_issues=none 时，质量谓词必须与 resource_id 限定以 AND 原子组合。
// 若谓词未加括号，`resource_id = ? AND quality_issues IS NULL OR NOT EXISTS (...)`
// 的 OR 分支没有资源限定，会把其他项目资源的"干净"段计入匹配面，
// 预览统计（影响面）跨资源膨胀，还可能误导后续批量替换。
func TestPreviewSearchReplaceQualityNoneIsolation(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "sr-iso-user")
	project1 := createTestProject(t, client, "sr-iso-p1", user.ID)
	project2 := createTestProject(t, client, "sr-iso-p2", user.ID)
	resA := createTestResource(t, client, project1.ID, "chapters/sr-iso-a.txt")
	resC := createTestResource(t, client, project2.ID, "chapters/sr-iso-c.txt")
	// 两段 quality_issues 均为 NULL（即"没问题"），target 均含 "hello"：
	// 只有 resA 的段在资源范围内，resC 的段只能靠 resource_id 限定排除。
	segA := createTestSegmentWithTarget(t, client, resA.ID, 0, "src", "hello world", nil)
	segC := createTestSegmentWithTarget(t, client, resC.ID, 0, "src2", "hello again", nil)

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	result, err := svc.PreviewSearchReplace(ctx, user.ID, project1.ID, resA.ID, SearchReplaceOptions{
		Find:          "hello",
		ReplaceWith:   "hi",
		QualityIssues: "none",
	})
	if err != nil {
		t.Fatalf("PreviewSearchReplace: %v", err)
	}
	if result.MatchedSegmentCount != 1 {
		t.Fatalf("matched_segment_count=%d want 1 (resC 段不得跨项目泄漏)", result.MatchedSegmentCount)
	}
	if len(result.Items) != 1 {
		ids := make([]int, 0, len(result.Items))
		for _, item := range result.Items {
			ids = append(ids, item.SegmentID)
		}
		t.Fatalf("items=%d (segment_ids=%v) want 1, want segment_id=%d (resC 段 %d 不得泄漏)", len(result.Items), ids, segA.ID, segC.ID)
	}
	if result.TotalReplacements != 1 {
		t.Fatalf("total_replacements=%d want 1", result.TotalReplacements)
	}
	if result.Items[0].SegmentID != segA.ID {
		t.Fatalf("item segment_id=%d want %d (resA 的段)", result.Items[0].SegmentID, segA.ID)
	}
}

// searchReplaceMarkupSetup 在 epub 资源上构造两段含 </ruby> 的译文：
//   - brokenSeg：完整 ruby 标签，把 </ruby> 删掉后结构非法；
//   - repairSeg：历史遗留的 stray 闭合标签，删掉 </ruby> 后反而修复为合法片段。
//
// 同一个 find/replace 一段变坏、一段变好，正好覆盖 skip 通道与正常应用两条路径。
func searchReplaceMarkupSetup(t *testing.T) (*SegmentService, *ent.Client, context.Context, *ent.User, *ent.Project, *ent.Resource, *ent.Segment, *ent.Segment) {
	t.Helper()
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "sr-markup-user")
	project := createTestProject(t, client, "sr-markup-proj", user.ID)
	res := createTestEpubResource(t, client, project.ID, "chapters/sr-markup.epub")
	broken := createTestSegmentWithTarget(t, client, res.ID, 0, "source 0", "<ruby>漢<rt>かん</rt></ruby>字", nil)
	repair := createTestSegmentWithTarget(t, client, res.ID, 1, "source 1", "foo </ruby> bar", nil)
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	return svc, client, ctx, user, project, res, broken, repair
}

// TestApplySearchReplaceSkipsInvalidMarkupResult 验证结构守卫是"跳过"而非整批失败：
// 一段替换结果非法时，另一段照常 applied，非法段进 Skipped 并携带 invalid_markup
// 原因，且数据库中该段译文保持原样。
func TestApplySearchReplaceSkipsInvalidMarkupResult(t *testing.T) {
	svc, client, ctx, user, project, res, broken, repair := searchReplaceMarkupSetup(t)

	result, err := svc.ApplySearchReplace(ctx, user.ID, project.ID, res.ID, SearchReplaceOptions{
		Find:        "</ruby>",
		ReplaceWith: "",
	})
	if err != nil {
		t.Fatalf("ApplySearchReplace: %v", err)
	}
	if result.AppliedCount != 1 || len(result.Items) != 1 || result.Items[0].ID != repair.ID {
		t.Fatalf("applied=%d items=%v want exactly the repair segment %d", result.AppliedCount, result.Items, repair.ID)
	}
	if result.SkippedCount != 1 || len(result.Skipped) != 1 {
		t.Fatalf("skipped_count=%d skipped=%v want exactly 1", result.SkippedCount, result.Skipped)
	}
	if result.Skipped[0].SegmentID != broken.ID {
		t.Fatalf("skipped segment=%d want %d", result.Skipped[0].SegmentID, broken.ID)
	}
	if result.Skipped[0].Reason != skipReasonInvalidMarkup {
		t.Fatalf("skip reason=%q want %q", result.Skipped[0].Reason, skipReasonInvalidMarkup)
	}
	// 被跳过段的 DB 译文不得被改动。
	dbSeg, err := client.Segment.Get(ctx, broken.ID)
	if err != nil {
		t.Fatalf("reload broken segment: %v", err)
	}
	if dbSeg.TargetText == nil || *dbSeg.TargetText != "<ruby>漢<rt>かん</rt></ruby>字" {
		t.Fatalf("target_text in DB=%v want unchanged", dbSeg.TargetText)
	}
}

// TestPreviewSearchReplaceConsistentWithApply 验证 preview 与 apply 共用同一判定：
// 同一参数下 preview 的 MatchedSegmentCount 与 apply 的 AppliedCount 相等，
// 预览不展示实际会被跳过的段，否则用户看到的改动数与实际生效数不一致。
func TestPreviewSearchReplaceConsistentWithApply(t *testing.T) {
	svc, _, ctx, user, project, res, broken, _ := searchReplaceMarkupSetup(t)

	opts := SearchReplaceOptions{
		Find:        "</ruby>",
		ReplaceWith: "",
	}
	preview, err := svc.PreviewSearchReplace(ctx, user.ID, project.ID, res.ID, opts)
	if err != nil {
		t.Fatalf("PreviewSearchReplace: %v", err)
	}
	apply, err := svc.ApplySearchReplace(ctx, user.ID, project.ID, res.ID, opts)
	if err != nil {
		t.Fatalf("ApplySearchReplace: %v", err)
	}
	if preview.MatchedSegmentCount != apply.AppliedCount {
		t.Fatalf("preview matched=%d, apply applied=%d, want equal", preview.MatchedSegmentCount, apply.AppliedCount)
	}
	for _, item := range preview.Items {
		if item.SegmentID == broken.ID {
			t.Fatalf("preview should not show segment %d (its replacement result is invalid)", broken.ID)
		}
	}
}
