package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func TestResourceSegmentEditedStatus(t *testing.T) {
	if SegmentStatusEdited != "edited" {
		t.Fatalf("SegmentStatusEdited = %q, want edited", SegmentStatusEdited)
	}
}

func TestBuildQualityPredicateNilWhenEmptyOrInvalid(t *testing.T) {
	cases := []ResourceSegmentListOptions{
		{},
		{QualityIssues: "maybe"},
		{QualitySeverity: "critical"},
		{QualityCode: "unknown"},
		{QualityIssues: "HAS"},
	}
	for _, opts := range cases {
		if p := buildQualityPredicate(opts, dialect.SQLite); p != nil {
			t.Fatalf("buildQualityPredicate(%+v) = non-nil, want nil", opts)
		}
	}
}

func TestBuildQualityPredicateNonNilForValidFilters(t *testing.T) {
	cases := []ResourceSegmentListOptions{
		{QualityIssues: "has"},
		{QualityIssues: "none"},
		{QualitySeverity: "warning"},
		{QualitySeverity: "error"},
		{QualityCode: "untranslated"},
		{QualityCode: "length_ratio"},
		{QualityCode: "duplicate"},
		{QualityCode: "source_residual"},
		{QualityCode: "calque"},
		{QualityCode: "term_fidelity"},
		{QualityCode: "naturalness"},
		{QualityCode: "mistranslation"},
		{QualityCode: "omission"},
		{QualityCode: "addition"},
		{QualityCode: "grammar"},
		{QualityCode: "register"},
		{QualityCode: "punctuation_pairing"},
		{QualityCode: "whitespace_irregular"},
		{QualityCode: "repeated_space"},
		{QualityCode: "width_mix"},
		{QualityCode: "number_mismatch"},
		{QualityCode: "url_email_mismatch"},
		{QualityCode: "subtitle_line_count"},
		{QualityCode: "forbidden_term"},
		{QualityCode: "term_inconsistency"},
		{QualityCode: "leftover_placeholder"},
		{QualityCode: "xml_tag_mismatch"},
		{QualityCode: "duplicate_source_divergence"},
		{QualitySeverity: "error", QualityCode: "duplicate"},
	}
	for _, opts := range cases {
		if p := buildQualityPredicate(opts, dialect.SQLite); p == nil {
			t.Fatalf("buildQualityPredicate(%+v) = nil, want predicate", opts)
		}
	}
}

// renderPredicate 把一个 predicate.Segment 应用到指定 dialect 的 Selector 上，
// 返回最终渲染出的 WHERE 子句 SQL，供断言占位符与函数名是否正确。
func renderPredicate(t *testing.T, opts ResourceSegmentListOptions, d string) string {
	t.Helper()
	p := buildQualityPredicate(opts, d)
	if p == nil {
		return ""
	}
	sel := sql.Dialect(d).Select().From(sql.Table(segment.Table))
	p(sel) // predicate.Segment 通过 s.Where 副作用注入谓词
	query, _ := sel.Query()
	return query
}

// TestBuildQualityPredicatePostgresSQL 防回归：PostgreSQL 下带 severity/code 过滤
// 的谓词不能残留 "?"（在 PG 中是 jsonb 键存在运算符，会触发 SQLSTATE 42601），
// 且必须使用 jsonb_* 函数。这是 linguaflow.log:94/98 生产 500 的根因。
func TestBuildQualityPredicatePostgresSQL(t *testing.T) {
	cases := []struct {
		name string
		opts ResourceSegmentListOptions
		// mustContain 为必须出现的子串；列名用不含表前缀的字段名匹配，
		// 避免 s.C() 渲染成 "table"."col" 导致断言脆裂。
		mustContain []string
	}{
		{"severity_warning", ResourceSegmentListOptions{QualitySeverity: "warning"},
			[]string{"jsonb_array_elements", "v ->> 'severity' = 'warning'"}},
		{"severity_error", ResourceSegmentListOptions{QualitySeverity: "error"},
			[]string{"jsonb_array_elements", "v ->> 'severity' = 'error'"}},
		{"code_untranslated", ResourceSegmentListOptions{QualityCode: "untranslated"},
			[]string{"jsonb_array_elements", "v ->> 'code' = 'untranslated'"}},
		{"issues_has", ResourceSegmentListOptions{QualityIssues: "has"},
			[]string{"jsonb_typeof", "jsonb_array_elements", "v ->> 'disposition' != 'dismissed'"}},
		{"issues_none", ResourceSegmentListOptions{QualityIssues: "none"},
			[]string{"jsonb_typeof", "NOT EXISTS", "v ->> 'disposition' != 'dismissed'"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			query := renderPredicate(t, c.opts, dialect.Postgres)
			if strings.Contains(query, "?") {
				t.Fatalf("postgres predicate must not contain raw '?', got: %s", query)
			}
			for _, want := range c.mustContain {
				if !strings.Contains(query, want) {
					t.Fatalf("postgres predicate missing expected fragment %q\nquery: %s", want, query)
				}
			}
		})
	}
}

// TestBuildQualityPredicateSQLiteSQL 确认 SQLite 路径仍以单引号字面量内联
// （修复后两 dialect 统一为字面量，SQLite 不受影响）。
func TestBuildQualityPredicateSQLiteSQL(t *testing.T) {
	query := renderPredicate(t, ResourceSegmentListOptions{QualityCode: "untranslated"}, dialect.SQLite)
	if strings.Contains(query, "?") {
		t.Fatalf("sqlite predicate must not contain raw '?', got: %s", query)
	}
	for _, want := range []string{"json_each", "json_extract(value, '$.code') = 'untranslated'"} {
		if !strings.Contains(query, want) {
			t.Fatalf("sqlite predicate missing expected fragment %q\nquery: %s", want, query)
		}
	}
}

func TestListResourceSegmentsQualityFilter(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-qa-user")
	project := createTestProject(t, client, "seg-qa-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/a.txt")

	// 0: NULL quality_issues
	createTestSegment(t, client, res.ID, 0, "src0", nil)
	// 1: empty array []
	createTestSegment(t, client, res.ID, 1, "src1", []qa.QualityIssue{})
	// 2: warning + untranslated
	createTestSegment(t, client, res.ID, 2, "src2", []qa.QualityIssue{
		{SegmentIndex: 2, Severity: qa.SeverityWarning, Code: "untranslated", Message: "not translated"},
	})
	// 3: error + length_ratio
	createTestSegment(t, client, res.ID, 3, "src3", []qa.QualityIssue{
		{SegmentIndex: 3, Severity: qa.SeverityError, Code: "length_ratio", Message: "too long"},
	})
	// 4: warning + duplicate AND error + untranslated (two issues)
	createTestSegment(t, client, res.ID, 4, "src4", []qa.QualityIssue{
		{SegmentIndex: 4, Severity: qa.SeverityWarning, Code: "duplicate", Message: "dup"},
		{SegmentIndex: 4, Severity: qa.SeverityError, Code: "untranslated", Message: "empty"},
	})
	// 5: warning + source_residual
	createTestSegment(t, client, res.ID, 5, "src5", []qa.QualityIssue{
		{SegmentIndex: 5, Severity: qa.SeverityWarning, Code: "source_residual", Message: "residual"},
	})
	// 6: warning + calque
	createTestSegment(t, client, res.ID, 6, "src6", []qa.QualityIssue{
		{SegmentIndex: 6, Severity: qa.SeverityWarning, Code: "calque", Message: "calque"},
	})
	// 7: warning + term_fidelity
	createTestSegment(t, client, res.ID, 7, "src7", []qa.QualityIssue{
		{SegmentIndex: 7, Severity: qa.SeverityWarning, Code: "term_fidelity", Message: "term"},
	})
	// 8: warning + naturalness
	createTestSegment(t, client, res.ID, 8, "src8", []qa.QualityIssue{
		{SegmentIndex: 8, Severity: qa.SeverityWarning, Code: "naturalness", Message: "awkward"},
	})

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	assertIndexes := func(t *testing.T, opts ResourceSegmentListOptions, want []int) {
		t.Helper()
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, opts)
		if err != nil {
			t.Fatalf("ListResourceSegments: %v", err)
		}
		got := make([]int, 0, len(page.Items))
		for _, row := range page.Items {
			got = append(got, row.SegmentIndex)
		}
		if len(got) != len(want) {
			t.Fatalf("indexes=%v want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("indexes=%v want %v", got, want)
			}
		}
	}

	t.Run("has", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualityIssues: "has", Limit: 50}, []int{2, 3, 4, 5, 6, 7, 8})
	})
	t.Run("none", func(t *testing.T) {
		// NULL and [] both count as none
		assertIndexes(t, ResourceSegmentListOptions{QualityIssues: "none", Limit: 50}, []int{0, 1})
	})
	t.Run("severity_warning", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualitySeverity: "warning", Limit: 50}, []int{2, 4, 5, 6, 7, 8})
	})
	t.Run("severity_error", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualitySeverity: "error", Limit: 50}, []int{3, 4})
	})
	t.Run("code_untranslated", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualityCode: "untranslated", Limit: 50}, []int{2, 4})
	})
	t.Run("code_length_ratio", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualityCode: "length_ratio", Limit: 50}, []int{3})
	})
	t.Run("code_duplicate", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualityCode: "duplicate", Limit: 50}, []int{4})
	})
	t.Run("code_source_residual", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualityCode: "source_residual", Limit: 50}, []int{5})
	})
	t.Run("code_calque", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualityCode: "calque", Limit: 50}, []int{6})
	})
	t.Run("code_term_fidelity", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualityCode: "term_fidelity", Limit: 50}, []int{7})
	})
	t.Run("code_naturalness", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualityCode: "naturalness", Limit: 50}, []int{8})
	})
	t.Run("severity_and_code_independent_exists", func(t *testing.T) {
		// segment 4 has (warning, duplicate) and (error, untranslated) on different issues.
		// Independent EXISTS: matches severity=error AND code=duplicate.
		// Same-issue AND would match none.
		assertIndexes(t, ResourceSegmentListOptions{
			QualitySeverity: "error",
			QualityCode:     "duplicate",
			Limit:           50,
		}, []int{4})
	})
	t.Run("has_with_cursor", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{
			QualityIssues: "has",
			AfterID:       2,
			Limit:         50,
		}, []int{3, 4, 5, 6, 7, 8})
	})
	t.Run("invalid_ignored", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualityIssues: "maybe", Limit: 50}, []int{0, 1, 2, 3, 4, 5, 6, 7, 8})
	})
}

func TestListResourceSegmentsQualityFilterNewSemanticCodes(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-qa-new-user")
	project := createTestProject(t, client, "seg-qa-new-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/new.txt")

	codes := []string{"mistranslation", "omission", "addition", "grammar", "register"}
	for i, code := range codes {
		createTestSegment(t, client, res.ID, i, "src"+code, []qa.QualityIssue{
			{SegmentIndex: i, Severity: qa.SeverityWarning, Code: code, Message: code},
		})
	}

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	assertIndexes := func(t *testing.T, opts ResourceSegmentListOptions, want []int) {
		t.Helper()
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, opts)
		if err != nil {
			t.Fatalf("ListResourceSegments: %v", err)
		}
		got := make([]int, 0, len(page.Items))
		for _, row := range page.Items {
			got = append(got, row.SegmentIndex)
		}
		if len(got) != len(want) {
			t.Fatalf("indexes=%v want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("indexes=%v want %v", got, want)
			}
		}
	}

	for i, code := range codes {
		t.Run("code_"+code, func(t *testing.T) {
			assertIndexes(t, ResourceSegmentListOptions{QualityCode: code, Limit: 50}, []int{i})
		})
	}
	t.Run("all_has", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualityIssues: "has", Limit: 50}, []int{0, 1, 2, 3, 4})
	})
}

func TestListResourceSegmentsQualityFilterDeterministicCodes(t *testing.T) {
	// 覆盖新纳入筛选的确定性 checker code（原仅 12 个筛选键，现为 23 个）
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-qa-det-user")
	project := createTestProject(t, client, "seg-qa-det-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/det.txt")

	codes := []string{"punctuation_pairing", "whitespace_irregular", "repeated_space",
		"width_mix", "number_mismatch", "url_email_mismatch", "subtitle_line_count",
		"forbidden_term", "term_inconsistency", "leftover_placeholder", "xml_tag_mismatch",
		"duplicate_source_divergence"}
	for i, code := range codes {
		createTestSegment(t, client, res.ID, i, "src"+code, []qa.QualityIssue{
			{SegmentIndex: i, Severity: qa.SeverityWarning, Code: code, Message: code},
		})
	}

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	assertIndexes := func(t *testing.T, opts ResourceSegmentListOptions, want []int) {
		t.Helper()
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, opts)
		if err != nil {
			t.Fatalf("ListResourceSegments: %v", err)
		}
		got := make([]int, 0, len(page.Items))
		for _, row := range page.Items {
			got = append(got, row.SegmentIndex)
		}
		if len(got) != len(want) {
			t.Fatalf("indexes=%v want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("indexes=%v want %v", got, want)
			}
		}
	}

	for i, code := range codes {
		t.Run("code_"+code, func(t *testing.T) {
			assertIndexes(t, ResourceSegmentListOptions{QualityCode: code, Limit: 50}, []int{i})
		})
	}
}

func TestListResourceSegmentsQualityFilterWithGroupKey(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-qa-gk-user")
	project := createTestProject(t, client, "seg-qa-gk-proj", user.ID)
	res := createTestResource(t, client, project.ID, "book.epub")

	metaA := `{"epub_file":"ch1.xhtml"}`
	metaB := `{"epub_file":"ch2.xhtml"}`

	// ch1: has issues
	createTestSegmentWithMeta(t, client, res.ID, 0, "a0", metaA, []qa.QualityIssue{
		{SegmentIndex: 0, Severity: qa.SeverityError, Code: "untranslated", Message: "x"},
	})
	// ch1: no issues
	createTestSegmentWithMeta(t, client, res.ID, 1, "a1", metaA, nil)
	// ch2: has issues (should be excluded by group_key)
	createTestSegmentWithMeta(t, client, res.ID, 2, "b0", metaB, []qa.QualityIssue{
		{SegmentIndex: 2, Severity: qa.SeverityWarning, Code: "duplicate", Message: "y"},
	})

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
		GroupKey:      "ch1.xhtml",
		QualityIssues: "has",
		Limit:         50,
	})
	if err != nil {
		t.Fatalf("ListResourceSegments: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].SegmentIndex != 0 {
		indexes := make([]int, 0, len(page.Items))
		for _, row := range page.Items {
			indexes = append(indexes, row.SegmentIndex)
		}
		t.Fatalf("indexes=%v want [0]", indexes)
	}
}

func createTestResource(t *testing.T, client *ent.Client, projectID int, path string) *ent.Resource {
	t.Helper()
	r, err := client.Resource.Create().
		SetProjectID(projectID).
		SetPath(path).
		SetFormat("txt").
		SetStoragePath("storage/" + path).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create resource: %v", err)
	}
	return r
}

func createTestSegment(t *testing.T, client *ent.Client, resourceID, index int, source string, issues []qa.QualityIssue) *ent.Segment {
	t.Helper()
	c := client.Segment.Create().
		SetResourceID(resourceID).
		SetSegmentIndex(index).
		SetSourceText(source).
		SetStatus(segment.StatusPending)
	if issues != nil {
		c = c.SetQualityIssues(issues)
	}
	row, err := c.Save(context.Background())
	if err != nil {
		t.Fatalf("create segment: %v", err)
	}
	return row
}

// TestUpdateResourceSegmentRegression 覆盖 UpdateResourceSegment 各字段组合，
// 重点防回归同时传 source_text 与 target_text 的场景：原实现会在同一 mutation
// 上对 target_text 同时 Clear + Set，PostgreSQL 报 "multiple assignments to
// same column target_text" (SQLSTATE 42601)，API 返回 500。
// SQLite 容忍重复赋值，故本测试用于锁定修复后的业务语义不退化。
func TestUpdateResourceSegmentRegression(t *testing.T) {
	strPtr := func(s string) *string { return &s }

	setup := func(t *testing.T) (*SegmentService, context.Context, *ent.User, *ent.Project, *ent.Resource, *ent.Segment) {
		client := testClient(t)
		ctx := context.Background()
		user := createTestUser(t, client, "seg-update-user")
		project := createTestProject(t, client, "seg-update-proj", user.ID)
		res := createTestResource(t, client, project.ID, "chapters/upd.txt")
		// 初始：已审核通过、有译文、有审核人（模拟用户编辑已审核段落的场景）
		seg, err := client.Segment.Create().
			SetResourceID(res.ID).
			SetSegmentIndex(0).
			SetSourceText("Hello").
			SetTargetText("你好").
			SetStatus(segment.StatusApproved).
			SetReviewedByID(user.ID).
			Save(ctx)
		if err != nil {
			t.Fatalf("create segment: %v", err)
		}
		svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
		return svc, ctx, user, project, res, seg
	}

	t.Run("source_and_target_together", func(t *testing.T) {
		svc, ctx, user, project, res, seg := setup(t)
		updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
			SourceText: strPtr("Hi there"),
			TargetText: strPtr("你好啊"),
		})
		if err != nil {
			t.Fatalf("UpdateResourceSegment with both source+target: %v", err)
		}
		if updated.SourceText != "Hi there" {
			t.Fatalf("source_text=%q want %q", updated.SourceText, "Hi there")
		}
		if updated.TargetText == nil || *updated.TargetText != "你好啊" {
			t.Fatalf("target_text=%v want %q", updated.TargetText, "你好啊")
		}
		if updated.Status != SegmentStatusEdited {
			t.Fatalf("status=%q want %q", updated.Status, SegmentStatusEdited)
		}
		if updated.Edges.ReviewedBy == nil || updated.Edges.ReviewedBy.ID != user.ID {
			t.Fatalf("reviewed_by=%v want %d", updated.Edges.ReviewedBy, user.ID)
		}
	})

	t.Run("source_only_clears_target_and_reviewer", func(t *testing.T) {
		svc, ctx, user, project, res, seg := setup(t)
		updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
			SourceText: strPtr("New source"),
		})
		if err != nil {
			t.Fatalf("UpdateResourceSegment source-only: %v", err)
		}
		if updated.TargetText != nil {
			t.Fatalf("target_text=%v want nil (cleared)", updated.TargetText)
		}
		if updated.Edges.ReviewedBy != nil {
			t.Fatalf("reviewed_by=%v want nil (cleared)", updated.Edges.ReviewedBy)
		}
		if updated.Status != SegmentStatusPending {
			t.Fatalf("status=%q want %q", updated.Status, SegmentStatusPending)
		}
	})

	t.Run("target_only_keeps_source", func(t *testing.T) {
		svc, ctx, user, project, res, seg := setup(t)
		updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
			TargetText: strPtr("新译文"),
		})
		if err != nil {
			t.Fatalf("UpdateResourceSegment target-only: %v", err)
		}
		if updated.SourceText != "Hello" {
			t.Fatalf("source_text=%q want %q (unchanged)", updated.SourceText, "Hello")
		}
		if updated.TargetText == nil || *updated.TargetText != "新译文" {
			t.Fatalf("target_text=%v want %q", updated.TargetText, "新译文")
		}
		if updated.Status != SegmentStatusEdited {
			t.Fatalf("status=%q want %q", updated.Status, SegmentStatusEdited)
		}
	})
}

func createTestSegmentWithMeta(t *testing.T, client *ent.Client, resourceID, index int, source, meta string, issues []qa.QualityIssue) *ent.Segment {
	t.Helper()
	c := client.Segment.Create().
		SetResourceID(resourceID).
		SetSegmentIndex(index).
		SetSourceText(source).
		SetStatus(segment.StatusPending).
		SetMeta(meta)
	if issues != nil {
		c = c.SetQualityIssues(issues)
	}
	row, err := c.Save(context.Background())
	if err != nil {
		t.Fatalf("create segment with meta: %v", err)
	}
	return row
}

// createTestSegmentWithTarget 构造带译文与可选 issues 的段落，供 UpdateResourceSegment 的
// 手动编辑 QA 重跑测试使用。status 默认 translated，模拟"已有翻译结果供用户编辑"场景。
func createTestSegmentWithTarget(t *testing.T, client *ent.Client, resourceID, index int, source, target string, issues []qa.QualityIssue) *ent.Segment {
	t.Helper()
	c := client.Segment.Create().
		SetResourceID(resourceID).
		SetSegmentIndex(index).
		SetSourceText(source).
		SetTargetText(target).
		SetStatus(segment.StatusTranslated)
	if issues != nil {
		c = c.SetQualityIssues(issues)
	}
	row, err := c.Save(context.Background())
	if err != nil {
		t.Fatalf("create segment with target: %v", err)
	}
	return row
}

func hasIssueCode(issues []qa.QualityIssue, code string) bool {
	for _, iss := range issues {
		if iss.Code == code {
			return true
		}
	}
	return false
}

// TestUpdateResourceSegmentRerunsQA 验证编辑译文触发零配置确定性 QA。
// 场景：源文含数字"3"，手动编辑译文为"三只猫"（无阿拉伯数字）→ 触发 number_mismatch。
func TestUpdateResourceSegmentRerunsQA(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-qa-rerun-user")
	project := createTestProject(t, client, "seg-qa-rerun-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/qa.txt")
	seg := createTestSegmentWithTarget(t, client, res.ID, 0, "3 cats", "3只猫", nil)

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
		TargetText: strPtr("三只猫"),
	})
	if err != nil {
		t.Fatalf("UpdateResourceSegment: %v", err)
	}
	if !hasIssueCode(updated.QualityIssues, qa.CheckNumberMismatch) {
		t.Fatalf("expected number_mismatch issue after manual edit, got %v", updated.QualityIssues)
	}
}

// TestUpdateResourceSegmentRubyTagLossGuard 验证人工编辑的注音守恒软守卫：
//   - 编辑前译文含 <ruby> 注音、编辑后全丢 → 产出 ruby_tag_loss warning；
//   - 编辑后仍保留注音 → 不产出；
//   - 此前已把该 warning 标记为 dismissed → 再次全丢失时继承裁决，不再以
//     pending 复活（指纹稳定为 code 本身）。
func TestUpdateResourceSegmentRubyTagLossGuard(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-qa-ruby-user")
	project := createTestProject(t, client, "seg-qa-ruby-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/ruby.txt")
	rubyTarget := `<ruby>漢<rt>かん</rt></ruby>字`

	t.Run("all_ruby_lost_warns", func(t *testing.T) {
		seg := createTestSegmentWithTarget(t, client, res.ID, 0, "漢字", rubyTarget, nil)
		svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
		updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
			TargetText: strPtr("汉字"),
		})
		if err != nil {
			t.Fatalf("UpdateResourceSegment: %v", err)
		}
		var found *qa.QualityIssue
		for i := range updated.QualityIssues {
			if updated.QualityIssues[i].Code == qa.CodeRubyTagLoss {
				found = &updated.QualityIssues[i]
				break
			}
		}
		if found == nil {
			t.Fatalf("expected ruby_tag_loss warning after stripping all ruby, got %v", updated.QualityIssues)
		}
		if found.Severity != qa.SeverityWarning {
			t.Fatalf("severity=%q want %q", found.Severity, qa.SeverityWarning)
		}
		if found.SegmentIndex != 0 {
			t.Fatalf("segment_index=%d want 0", found.SegmentIndex)
		}
	})

	t.Run("ruby_kept_no_warning", func(t *testing.T) {
		seg := createTestSegmentWithTarget(t, client, res.ID, 1, "漢字", rubyTarget, nil)
		svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
		updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
			TargetText: strPtr(`<ruby>漢<rt>かん</rt></ruby>文字`),
		})
		if err != nil {
			t.Fatalf("UpdateResourceSegment: %v", err)
		}
		if hasIssueCode(updated.QualityIssues, qa.CodeRubyTagLoss) {
			t.Fatalf("editing while keeping ruby must not raise ruby_tag_loss, got %v", updated.QualityIssues)
		}
	})

	t.Run("dismissed_inherited_on_repeat_loss", func(t *testing.T) {
		seg := createTestSegmentWithTarget(t, client, res.ID, 2, "漢字", rubyTarget, []qa.QualityIssue{{
			SegmentIndex: 2,
			Severity:     qa.SeverityWarning,
			Code:         qa.CodeRubyTagLoss,
			Message:      "译文注音全部丢失：编辑前 1 条",
			Disposition:  qa.DispositionDismissed,
		}})
		svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
		updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
			TargetText: strPtr("汉字"),
		})
		if err != nil {
			t.Fatalf("UpdateResourceSegment: %v", err)
		}
		for _, iss := range updated.QualityIssues {
			if iss.Code == qa.CodeRubyTagLoss && iss.IsPending() {
				t.Fatalf("dismissed ruby_tag_loss must stay dismissed after repeat loss, got %+v", iss)
			}
		}
	})
}

// TestUpdateResourceSegmentQAReplacesOldIssues 验证手动编辑重跑 QA 后旧 issues 被新结果覆盖。
// 场景：段落遗留一个假 number_mismatch issue，手动编辑后译文数字已匹配 → 旧 issue 被清空。
func TestUpdateResourceSegmentQAReplacesOldIssues(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-qa-replace-user")
	project := createTestProject(t, client, "seg-qa-replace-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/replace.txt")
	seg := createTestSegmentWithTarget(t, client, res.ID, 0, "3 cats", "3只猫", []qa.QualityIssue{{
		SegmentIndex: 0,
		Severity:     qa.SeverityWarning,
		Code:         qa.CheckNumberMismatch,
		Message:      "旧 issue（应被新 QA 结果覆盖）",
	}})

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
		TargetText: strPtr("3只猫"),
	})
	if err != nil {
		t.Fatalf("UpdateResourceSegment: %v", err)
	}
	if len(updated.QualityIssues) > 0 {
		t.Fatalf("expected stale quality_issues replaced (cleared), got %v", updated.QualityIssues)
	}
}

// TestUpdateResourceSegmentSourceOnlyClearsIssues 验证仅改 source 时旧译文与旧 issues 一起清空。
// 场景：sourceChanged && !targetChanged → 无译文不跑 QA，旧 issues 直接清空。
func TestUpdateResourceSegmentSourceOnlyClearsIssues(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-qa-src-user")
	project := createTestProject(t, client, "seg-qa-src-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/src.txt")
	seg := createTestSegmentWithTarget(t, client, res.ID, 0, "3 cats", "3只猫", []qa.QualityIssue{{
		SegmentIndex: 0,
		Severity:     qa.SeverityWarning,
		Code:         qa.CheckNumberMismatch,
		Message:      "旧 issue",
	}})

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
		SourceText: strPtr("4 dogs"),
	})
	if err != nil {
		t.Fatalf("UpdateResourceSegment: %v", err)
	}
	if updated.TargetText != nil {
		t.Fatalf("expected target cleared on source-only change, got %v", updated.TargetText)
	}
	if len(updated.QualityIssues) > 0 {
		t.Fatalf("expected quality_issues cleared on source-only change, got %v", updated.QualityIssues)
	}
}

// TestUpdateResourceSegmentCommentOnlyKeepsIssues 验证仅改 comment 不触碰 quality_issues。
func TestUpdateResourceSegmentCommentOnlyKeepsIssues(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-qa-cmt-user")
	project := createTestProject(t, client, "seg-qa-cmt-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/cmt.txt")
	seg := createTestSegmentWithTarget(t, client, res.ID, 0, "3 cats", "三只猫", []qa.QualityIssue{{
		SegmentIndex: 0,
		Severity:     qa.SeverityWarning,
		Code:         qa.CheckNumberMismatch,
		Message:      "应保留",
	}})

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
		Comment: strPtr("备注"),
	})
	if err != nil {
		t.Fatalf("UpdateResourceSegment: %v", err)
	}
	if len(updated.QualityIssues) != 1 || updated.QualityIssues[0].Code != qa.CheckNumberMismatch {
		t.Fatalf("expected quality_issues unchanged on comment-only change, got %v", updated.QualityIssues)
	}
}

// TestUpdateResourceSegmentSourceAndTargetUsesNewSource 验证同时变更时 QA 用新 source + 新 target。
// 场景：源由"3 cats"改为"4 dogs"（含数字 4），译文"三只狗"无阿拉伯数字 → 用新源跑出 number_mismatch。
func TestUpdateResourceSegmentSourceAndTargetUsesNewSource(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-qa-st-user")
	project := createTestProject(t, client, "seg-qa-st-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/st.txt")
	seg := createTestSegmentWithTarget(t, client, res.ID, 0, "3 cats", "3只猫", nil)

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
		SourceText: strPtr("4 dogs"),
		TargetText: strPtr("三只狗"),
	})
	if err != nil {
		t.Fatalf("UpdateResourceSegment: %v", err)
	}
	if !hasIssueCode(updated.QualityIssues, qa.CheckNumberMismatch) {
		t.Fatalf("expected number_mismatch using new source, got %v", updated.QualityIssues)
	}
}

// TestUpdateResourceSegmentExcludesLengthRatio 验证手动编辑不跑 length_ratio。
// 场景：源"a"与译文长串纯中文比率远超默认上限 3.0，若启用必触发 length_ratio error；
// 断言结果中不含 length_ratio 即证明该 checker 被排除（避免与执行计划配置矛盾）。
func TestUpdateResourceSegmentExcludesLengthRatio(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-qa-lr-user")
	project := createTestProject(t, client, "seg-qa-lr-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/lr.txt")
	seg := createTestSegmentWithTarget(t, client, res.ID, 0, "a", "啊", nil)

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
		TargetText: strPtr("啊啊啊啊啊啊啊啊啊啊啊啊啊啊啊啊啊"),
	})
	if err != nil {
		t.Fatalf("UpdateResourceSegment: %v", err)
	}
	if hasIssueCode(updated.QualityIssues, qa.CheckLengthRatio) {
		t.Fatalf("length_ratio must not run on manual edit (no execution-plan config), got %v", updated.QualityIssues)
	}
}

// TestUpdateResourceSegmentTaggedTargetNoWidthMix 验证手动编辑含 HTML 标签的 CJK 译文
// 不产生 width_mix 误报。DB 不持久化 Protected 映射，零配置重跑 QA 时 Protected 为空；
// 此时通用标签屏蔽应兜住标签字符（<、>、" 等），使其不被当作半角标点计入
// CJK 全半角混用检测。目标语言为 zh（createTestProject 默认），命中 cjkTarget 分支。
func TestUpdateResourceSegmentTaggedTargetNoWidthMix(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-qa-tag-user")
	project := createTestProject(t, client, "seg-qa-tag-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/tag.txt")
	// 初始 source/target 均不含标签
	seg := createTestSegmentWithTarget(t, client, res.ID, 0, "雷神皇", "雷神皇", nil)

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
		TargetText: strPtr(`<a href="x">連</a>`),
	})
	if err != nil {
		t.Fatalf("UpdateResourceSegment: %v", err)
	}
	if hasIssueCode(updated.QualityIssues, qa.CheckWidthMix) {
		t.Fatalf("tag characters in manually edited target must not trigger width_mix, got %v", updated.QualityIssues)
	}
}

// TestListResourceSegmentsQualityFilterDismissed 验证段落筛选的三个维度
// （quality_issues/severity/code）均只统计待处理的 issue：disposition=dismissed
// 的已驳回 issue 不再使段落命中"有问题"，severity/code 匹配也忽略已驳回条目。
// disposition 缺失（旧数据）视为 pending，行为不变。
func TestListResourceSegmentsQualityFilterDismissed(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-qa-dismiss-user")
	project := createTestProject(t, client, "seg-qa-dismiss-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/dismiss.txt")

	// 0: NULL quality_issues
	createTestSegment(t, client, res.ID, 0, "src0", nil)
	// 1: 单条 pending issue（disposition 显式为 pending）
	createTestSegment(t, client, res.ID, 1, "src1", []qa.QualityIssue{
		{SegmentIndex: 1, Severity: qa.SeverityWarning, Code: "untranslated", Message: "not translated", Disposition: qa.DispositionPending},
	})
	// 2: 单条 dismissed issue（唯一问题被驳回 → 应算"没问题"）
	createTestSegment(t, client, res.ID, 2, "src2", []qa.QualityIssue{
		{SegmentIndex: 2, Severity: qa.SeverityWarning, Code: "untranslated", Message: "not translated", Disposition: qa.DispositionDismissed},
	})
	// 3: dismissed(error/untranslated) + pending(warning/duplicate) 混合
	createTestSegment(t, client, res.ID, 3, "src3", []qa.QualityIssue{
		{SegmentIndex: 3, Severity: qa.SeverityError, Code: "untranslated", Message: "empty", Disposition: qa.DispositionDismissed},
		{SegmentIndex: 3, Severity: qa.SeverityWarning, Code: "duplicate", Message: "dup", Disposition: qa.DispositionPending},
	})
	// 4: 全部 dismissed（warning + error 各一条）
	createTestSegment(t, client, res.ID, 4, "src4", []qa.QualityIssue{
		{SegmentIndex: 4, Severity: qa.SeverityWarning, Code: "duplicate", Message: "dup", Disposition: qa.DispositionDismissed},
		{SegmentIndex: 4, Severity: qa.SeverityError, Code: "untranslated", Message: "empty", Disposition: qa.DispositionDismissed},
	})
	// 5: disposition 缺失（零值，旧数据形态；MarshalJSON 会输出 pending，此处等价）
	createTestSegment(t, client, res.ID, 5, "src5", []qa.QualityIssue{
		{SegmentIndex: 5, Severity: qa.SeverityWarning, Code: "calque", Message: "calque"},
	})

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	assertIndexes := func(t *testing.T, opts ResourceSegmentListOptions, want []int) {
		t.Helper()
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, opts)
		if err != nil {
			t.Fatalf("ListResourceSegments: %v", err)
		}
		got := make([]int, 0, len(page.Items))
		for _, row := range page.Items {
			got = append(got, row.SegmentIndex)
		}
		if len(got) != len(want) {
			t.Fatalf("indexes=%v want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("indexes=%v want %v", got, want)
			}
		}
	}

	// "有问题"：pending(1)、混合(3)、缺失视为 pending(5)；纯 dismissed 的 2/4 排除
	t.Run("has_excludes_dismissed_only", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualityIssues: "has", Limit: 50}, []int{1, 3, 5})
	})
	// "没问题"：NULL(0)、纯 dismissed(2、4)
	t.Run("none_includes_all_dismissed", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualityIssues: "none", Limit: 50}, []int{0, 2, 4})
	})
	// severity=warning：1、3（pending duplicate）、5；2/4 的 warning 已驳回
	t.Run("severity_warning_ignores_dismissed", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualitySeverity: "warning", Limit: 50}, []int{1, 3, 5})
	})
	// severity=error：3 的 error 已驳回、4 全驳回 → 无命中
	t.Run("severity_error_all_dismissed_matches_none", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualitySeverity: "error", Limit: 50}, []int{})
	})
	// code=untranslated：1 pending 命中；2/3/4 的 untranslated 已驳回
	t.Run("code_untranslated_ignores_dismissed", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualityCode: "untranslated", Limit: 50}, []int{1})
	})
	// severity=error AND code=duplicate：3 的 error 已驳回，severity=error 不再命中；
	// 4 全驳回 → 无命中。独立 EXISTS 语义不变，各自忽略 dismissed 条目。
	t.Run("severity_and_code_independent_exists_ignores_dismissed", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{
			QualitySeverity: "error",
			QualityCode:     "duplicate",
			Limit:           50,
		}, []int{})
	})
	// severity=warning AND code=duplicate → 3（两条均 pending，命中同一段落）
	t.Run("severity_and_code_both_pending", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{
			QualitySeverity: "warning",
			QualityCode:     "duplicate",
			Limit:           50,
		}, []int{3})
	})
}
