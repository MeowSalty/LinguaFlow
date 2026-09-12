package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service/segmatch"
)

// strPtr 供测试构造 *string 字段。
func strPtr(s string) *string { return &s }

// ptrInt 供测试构造期望 total。
func ptrInt(v int) *int { return &v }

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

// TestListResourceSegmentsQualityFilterIsolation 防回归：质量谓词注入后必须与
// resource_id 限定以 AND 原子组合。quality_issues=none 分支含顶层 OR，若整体不加
// 括号，`resource_id = ? AND quality_issues IS NULL OR NOT EXISTS (...)` 会被解析为
// `(resource_id = ? AND quality_issues IS NULL) OR NOT EXISTS (...)`——OR 分支没有
// resource 限定，导致列出其他资源甚至其他项目的"干净"段，include_total 计数同样膨胀。
func TestListResourceSegmentsQualityFilterIsolation(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-iso-user")
	project1 := createTestProject(t, client, "seg-iso-p1", user.ID)
	project2 := createTestProject(t, client, "seg-iso-p2", user.ID)
	resA := createTestResource(t, client, project1.ID, "chapters/iso-a.txt")
	resB := createTestResource(t, client, project1.ID, "chapters/iso-b.txt")
	resC := createTestResource(t, client, project2.ID, "chapters/iso-c.txt")

	// resA：index 0 干净（NULL issues），index 1 有 pending issue
	createTestSegment(t, client, resA.ID, 0, "a-clean", nil)
	createTestSegment(t, client, resA.ID, 1, "a-issue", []qa.QualityIssue{
		{SegmentIndex: 1, Severity: qa.SeverityWarning, Code: "duplicate", Message: "dup"},
	})
	// resB（同项目另一资源）：全部干净；index 2 超过 resA 的最大 index，
	// 供游标子测试验证 index 大于游标的外资源段也不得泄漏。
	createTestSegment(t, client, resB.ID, 0, "b-clean-0", nil)
	createTestSegment(t, client, resB.ID, 1, "b-clean-1", nil)
	createTestSegment(t, client, resB.ID, 2, "b-clean-2", nil)
	// resC（其他项目）：全部干净
	createTestSegment(t, client, resC.ID, 0, "c-clean-0", nil)
	createTestSegment(t, client, resC.ID, 1, "c-clean-1", nil)

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	// resourceIDOf 安全解引用 Segment.ResourceID（*int），nil 记为 -1 便于报告泄漏。
	resourceIDOf := func(row *ent.Segment) int {
		if row.ResourceID == nil {
			return -1
		}
		return *row.ResourceID
	}
	// dumpRows 把结果行压缩成可读摘要，泄漏时逐条列出归属资源与源文，便于定位。
	dumpRows := func(rows []*ent.Segment) string {
		parts := make([]string, 0, len(rows))
		for _, row := range rows {
			parts = append(parts, fmt.Sprintf("{id=%d resource_id=%d index=%d source=%q}",
				row.ID, resourceIDOf(row), row.SegmentIndex, row.SourceText))
		}
		return strings.Join(parts, " ")
	}
	// assertScoped 断言结果恰好是 resA 的 wantIndex 段：任何 resource_id 不是 resA
	// 的行都是跨资源/跨项目泄漏。
	assertScoped := func(t *testing.T, page *ResourceSegmentPage, wantIndex int) {
		t.Helper()
		if len(page.Items) != 1 {
			t.Fatalf("got %d items want 1 (resA index %d), rows=%s", len(page.Items), wantIndex, dumpRows(page.Items))
		}
		row := page.Items[0]
		if got := resourceIDOf(row); got != resA.ID {
			t.Fatalf("leaked item from other resource/project: id=%d resource_id=%d want %d (source=%q)", row.ID, got, resA.ID, row.SourceText)
		}
		if row.SegmentIndex != wantIndex {
			t.Fatalf("segment_index=%d want %d", row.SegmentIndex, wantIndex)
		}
	}

	t.Run("none", func(t *testing.T) {
		page, err := svc.ListResourceSegments(ctx, user.ID, project1.ID, resA.ID, ResourceSegmentListOptions{
			QualityIssues: "none",
			IncludeTotal:  true,
			Limit:         50,
		})
		if err != nil {
			t.Fatalf("ListResourceSegments: %v", err)
		}
		assertScoped(t, page, 0)
		if page.Total == nil {
			t.Fatalf("total=nil want non-nil (IncludeTotal=true)")
		}
		if *page.Total != 1 {
			t.Fatalf("total=%d want 1 (计数不得跨资源/项目膨胀)", *page.Total)
		}
	})

	t.Run("has", func(t *testing.T) {
		page, err := svc.ListResourceSegments(ctx, user.ID, project1.ID, resA.ID, ResourceSegmentListOptions{
			QualityIssues: "has",
			IncludeTotal:  true,
			Limit:         50,
		})
		if err != nil {
			t.Fatalf("ListResourceSegments: %v", err)
		}
		assertScoped(t, page, 1)
		if page.Total == nil || *page.Total != 1 {
			t.Fatalf("total=%v want 1", page.Total)
		}
	})

	t.Run("none_with_cursor", func(t *testing.T) {
		// 游标语义：AfterID 为已消费的最后一个 segment index，返回严格大于它的行。
		// 取 1（resA 最后一段）后 resA 已无 "none" 匹配行；resB 的 index 2 段是
		// 唯一 index > 1 的干净段，若 resource_id 限定失效（谓词未括号化）它会
		// 直接出现在结果里，行断言与 Total 断言（计数不含游标）双绊线。
		// 注意 AfterID=0 是"无游标"哨兵，无法表达"干净段（index 0）之后"，故取最后 index。
		page, err := svc.ListResourceSegments(ctx, user.ID, project1.ID, resA.ID, ResourceSegmentListOptions{
			QualityIssues: "none",
			AfterID:       1,
			IncludeTotal:  true,
			Limit:         50,
		})
		if err != nil {
			t.Fatalf("ListResourceSegments: %v", err)
		}
		if len(page.Items) != 0 {
			t.Fatalf("cursor 之后应无匹配行, got %d items, rows=%s", len(page.Items), dumpRows(page.Items))
		}
		if page.Total == nil || *page.Total != 1 {
			t.Fatalf("total=%v want 1 (游标不影响计数，且计数不得跨资源膨胀)", page.Total)
		}
	})
}

// TestBuildQualityPredicateParenthesized 防回归：质量谓词是经 sql.ExprP 注入的
// 原始 SQL，ent 不会给单函数原始谓词自动加括号。因此四个分支的谓词必须自带外层
// 括号、作为原子单元加入 AND 链；否则 quality_issues=none 的顶层 OR 会截断 AND 链
// （行为影响见 TestListResourceSegmentsQualityFilterIsolation）。对 SQLite/Postgres
// 两个 dialect 的全部分支断言 " AND ("，锁定所有分支均以括号化形式拼接。
func TestBuildQualityPredicateParenthesized(t *testing.T) {
	modes := []struct {
		name string
		opts ResourceSegmentListOptions
	}{
		{"issues_none", ResourceSegmentListOptions{QualityIssues: "none"}},
		{"issues_has", ResourceSegmentListOptions{QualityIssues: "has"}},
		{"severity_error", ResourceSegmentListOptions{QualitySeverity: "error"}},
		{"code_untranslated", ResourceSegmentListOptions{QualityCode: "untranslated"}},
	}
	for _, d := range []string{dialect.SQLite, dialect.Postgres} {
		for _, m := range modes {
			t.Run(d+"/"+m.name, func(t *testing.T) {
				sel := sql.Dialect(d).Select().From(sql.Table(segment.Table))
				segment.ResourceIDEQ(12)(sel) // AND 链首项：资源限定
				p := buildQualityPredicate(m.opts, d)
				if p == nil {
					t.Fatalf("buildQualityPredicate(%+v) = nil, want predicate", m.opts)
				}
				p(sel)
				query, _ := sel.Query()
				if !strings.Contains(query, " AND (") {
					t.Fatalf("quality predicate must join the AND chain as a parenthesized unit\ndialect=%s mode=%s\nquery: %s", d, m.name, query)
				}
			})
		}
	}
}

// TestUpdateResourceSegmentMarkupGuard 验证手动编辑译文的写入守卫：
//   - epub 等译文会被原样嵌入 XML 文档的格式，结构非法的译文在写入边界即被拒绝，
//     且数据库不受影响（放行会让导出时整章降级为原文）；
//   - txt 等纯文本格式不受门禁影响，译文里的 <color=red>、裸 & 是合法内容；
//   - epub 上合法（含平衡 ruby 标签）的译文照常保存。
func TestUpdateResourceSegmentMarkupGuard(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	// 缺 </ruby> 的损坏译文，epub 上必须被拒绝。
	brokenTarget := "「等级６：<ruby>劣化雷神皇<rt>雷瑟</rt>！」"

	setup := func(t *testing.T, format string) (*ent.Client, *SegmentService, context.Context, *ent.User, *ent.Project, *ent.Resource, *ent.Segment) {
		client := testClient(t)
		ctx := context.Background()
		user := createTestUser(t, client, "seg-markup-user")
		project := createTestProject(t, client, "seg-markup-proj", user.ID)
		var res *ent.Resource
		if format == "epub" {
			res = createTestEpubResource(t, client, project.ID, "chapters/markup.epub")
		} else {
			res = createTestResource(t, client, project.ID, "chapters/markup.txt")
		}
		seg := createTestSegmentWithTarget(t, client, res.ID, 0, "Level 6.", "旧译文", nil)
		svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
		return client, svc, ctx, user, project, res, seg
	}

	t.Run("epub_rejects_broken_target", func(t *testing.T) {
		client, svc, ctx, user, project, res, seg := setup(t, "epub")
		updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
			TargetText: strPtr(brokenTarget),
		})
		if updated != nil {
			t.Fatalf("expected error, got updated segment %d", updated.ID)
		}
		if !errors.Is(err, ErrSegmentMarkupInvalid) {
			t.Fatalf("err=%v want ErrSegmentMarkupInvalid", err)
		}
		var markupErr *SegmentMarkupError
		if !errors.As(err, &markupErr) {
			t.Fatalf("err=%T does not unwrap to *SegmentMarkupError", err)
		}
		// handler 用它拼装可读的 problem detail，必须携带具体语法错误。
		if markupErr.Err == nil {
			t.Fatal("SegmentMarkupError.Err is nil, handler cannot build a readable detail")
		}
		// 写入被拒后数据库里的译文不得被改动。
		dbSeg, err := client.Segment.Get(ctx, seg.ID)
		if err != nil {
			t.Fatalf("reload segment: %v", err)
		}
		if dbSeg.TargetText == nil || *dbSeg.TargetText != "旧译文" {
			t.Fatalf("target_text in DB=%v want %q (unchanged)", dbSeg.TargetText, "旧译文")
		}
	})

	t.Run("txt_allows_non_xml_target", func(t *testing.T) {
		_, svc, ctx, user, project, res, seg := setup(t, "txt")
		updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
			TargetText: strPtr(brokenTarget),
		})
		if err != nil {
			t.Fatalf("UpdateResourceSegment on txt resource: %v", err)
		}
		if updated.TargetText == nil || *updated.TargetText != brokenTarget {
			t.Fatalf("target_text=%v want %q", updated.TargetText, brokenTarget)
		}
		if updated.Status != SegmentStatusEdited {
			t.Fatalf("status=%q want %q", updated.Status, SegmentStatusEdited)
		}
	})

	t.Run("epub_allows_balanced_target", func(t *testing.T) {
		_, svc, ctx, user, project, res, seg := setup(t, "epub")
		target := "<ruby>劣化雷神皇<rt>れいさ</rt></ruby>"
		updated, err := svc.UpdateResourceSegment(ctx, user.ID, project.ID, res.ID, seg.ID, ResourceSegmentUpdateInput{
			TargetText: strPtr(target),
		})
		if err != nil {
			t.Fatalf("UpdateResourceSegment with balanced ruby: %v", err)
		}
		if updated.TargetText == nil || *updated.TargetText != target {
			t.Fatalf("target_text=%v want %q", updated.TargetText, target)
		}
		if updated.Status != SegmentStatusEdited {
			t.Fatalf("status=%q want %q", updated.Status, SegmentStatusEdited)
		}
	})
}

// assertSegmentPage 断言分页结果的 indexes、prev/next cursor 与可选 total。
// wantTotal 为 nil 表示本次请求未携带 IncludeTotal，不校验 Total 字段。
func assertSegmentPage(t *testing.T, page *ResourceSegmentPage, wantIndexes []int, wantPrev, wantNext int, wantTotal *int) {
	t.Helper()
	got := make([]int, 0, len(page.Items))
	for _, row := range page.Items {
		got = append(got, row.SegmentIndex)
	}
	if len(got) != len(wantIndexes) {
		t.Fatalf("indexes=%v want %v", got, wantIndexes)
	}
	for i := range wantIndexes {
		if got[i] != wantIndexes[i] {
			t.Fatalf("indexes=%v want %v", got, wantIndexes)
		}
	}
	if page.PrevCursor != wantPrev {
		t.Fatalf("prev_cursor=%d want %d", page.PrevCursor, wantPrev)
	}
	if page.NextCursor != wantNext {
		t.Fatalf("next_cursor=%d want %d", page.NextCursor, wantNext)
	}
	if page.HasPrevCursor != (wantPrev > 0) {
		t.Fatalf("has_prev_cursor=%v want %v", page.HasPrevCursor, wantPrev > 0)
	}
	if page.HasNextCursor != (wantNext > 0) {
		t.Fatalf("has_next_cursor=%v want %v", page.HasNextCursor, wantNext > 0)
	}
	if wantTotal != nil && (page.Total == nil || *page.Total != *wantTotal) {
		t.Fatalf("total=%v want %d", page.Total, *wantTotal)
	}
}

// TestListResourceSegmentsPagination 覆盖无 group_key 的数据库分页路径：
// 首页、asc/desc 游标窗口、cursor 越界回退与 IncludeTotal 计数（不受游标影响）。
func TestListResourceSegmentsPagination(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-page-user")
	project := createTestProject(t, client, "seg-page-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/page.txt")

	for i := 1; i <= 12; i++ {
		createTestSegment(t, client, res.ID, i, fmt.Sprintf("src%d", i), nil)
	}
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	fetch := func(t *testing.T, opts ResourceSegmentListOptions, want []int, wantPrev, wantNext int, wantTotal *int) *ResourceSegmentPage {
		t.Helper()
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, opts)
		if err != nil {
			t.Fatalf("ListResourceSegments(%+v): %v", opts, err)
		}
		assertSegmentPage(t, page, want, wantPrev, wantNext, wantTotal)
		return page
	}
	total12 := 12

	t.Run("first_page", func(t *testing.T) {
		fetch(t, ResourceSegmentListOptions{Limit: 5, IncludeTotal: true},
			[]int{1, 2, 3, 4, 5}, 0, 5, &total12)
	})

	t.Run("asc_cursor_5", func(t *testing.T) {
		page := fetch(t, ResourceSegmentListOptions{AfterID: 5, HasCursor: true, Limit: 5, IncludeTotal: true},
			[]int{6, 7, 8, 9, 10}, 6, 10, &total12)
		// prev_cursor 必须与 direction=desc 配合，取得紧贴当前窗口之前的一页。
		fetch(t, ResourceSegmentListOptions{Direction: "desc", AfterID: page.PrevCursor, HasCursor: true, Limit: 5},
			[]int{1, 2, 3, 4, 5}, 0, 5, nil)
	})

	t.Run("desc_cursor_10", func(t *testing.T) {
		// desc 窗口：取 index<10 的尾端 5 条，响应仍按升序返回。
		fetch(t, ResourceSegmentListOptions{Direction: "desc", AfterID: 10, HasCursor: true, Limit: 5},
			[]int{5, 6, 7, 8, 9}, 5, 9, nil)
	})

	t.Run("desc_cursor_4", func(t *testing.T) {
		// index<4 只剩 3 条，窗口不满 limit；起点之前无数据 → prev=0。
		fetch(t, ResourceSegmentListOptions{Direction: "desc", AfterID: 4, HasCursor: true, Limit: 5},
			[]int{1, 2, 3}, 0, 3, nil)
	})
}

// TestListResourceSegmentsAnchor 覆盖数据库 ID 锚点窗口：
// 锚点本身不满足筛选条件时窗口仍从其 segment_index 起；锚点不存在于资源时报 ErrSegmentNotFound。
func TestListResourceSegmentsAnchor(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-anchor-user")
	project := createTestProject(t, client, "seg-anchor-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/anchor.txt")
	resB := createTestResource(t, client, project.ID, "chapters/anchor-other.txt")

	idsByIndex := make(map[int]int)
	for i := 1; i <= 12; i++ {
		row := createTestSegment(t, client, res.ID, i, fmt.Sprintf("src%d", i), nil)
		idsByIndex[i] = row.ID
	}
	// 8/10/12 已审核通过，供 status=approved 过滤下的锚点窗口使用。
	for _, i := range []int{8, 10, 12} {
		if err := client.Segment.UpdateOneID(idsByIndex[i]).SetStatus(segment.StatusApproved).Exec(ctx); err != nil {
			t.Fatalf("approve segment %d: %v", idsByIndex[i], err)
		}
	}
	// 其他资源里的段落：其数据库 ID 不属于 res，必须被锚点查询拒绝。
	foreign := createTestSegment(t, client, resB.ID, 0, "foreign", nil)

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	fetch := func(t *testing.T, opts ResourceSegmentListOptions, want []int, wantPrev, wantNext int, wantTotal *int) {
		t.Helper()
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, opts)
		if err != nil {
			t.Fatalf("ListResourceSegments(%+v): %v", opts, err)
		}
		assertSegmentPage(t, page, want, wantPrev, wantNext, wantTotal)
	}
	anchorID := idsByIndex[7]
	total12, totalApproved := 12, 3

	t.Run("anchor_window", func(t *testing.T) {
		fetch(t, ResourceSegmentListOptions{AnchorSegmentID: &anchorID, Limit: 5, IncludeTotal: true},
			[]int{7, 8, 9, 10, 11}, 7, 11, &total12)
	})

	t.Run("anchor_pending_with_approved_filter", func(t *testing.T) {
		// 锚点 index 7 本身是 pending，不满足 status=approved；
		// 窗口仍从其 index 起，返回首个满足条件的 8 及之后的 approved 段。
		// approved 计数不含窗口，为全量 3 条。
		fetch(t, ResourceSegmentListOptions{AnchorSegmentID: &anchorID, Status: "approved", Limit: 5, IncludeTotal: true},
			[]int{8, 10, 12}, 0, 0, &totalApproved)
	})

	t.Run("anchor_not_in_resource", func(t *testing.T) {
		_, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
			AnchorSegmentID: &foreign.ID,
			Limit:           5,
		})
		if !errors.Is(err, ErrSegmentNotFound) {
			t.Fatalf("err=%v want ErrSegmentNotFound", err)
		}
	})
}

// TestListResourceSegmentsGroupPagination 覆盖 group_key 过滤的应用层分页路径：
// 两章节 index 交错，验证窗口取自同章节序列、desc 窗口算法、锚点同章节校验与 cursor 越界。
func TestListResourceSegmentsGroupPagination(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-group-user")
	project := createTestProject(t, client, "seg-group-proj", user.ID)
	res := createTestResource(t, client, project.ID, "book-group.epub")

	metaA := `{"epub_file":"ch1.xhtml"}`
	metaB := `{"epub_file":"ch2.xhtml"}`
	idsByIndex := make(map[int]int)
	for i := 1; i <= 12; i++ {
		meta := metaA
		if i%2 == 0 {
			meta = metaB
		}
		row := createTestSegmentWithMeta(t, client, res.ID, i, fmt.Sprintf("src%d", i), meta, nil)
		idsByIndex[i] = row.ID
	}
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	// ch1 序列：[1,3,5,7,9,11]；ch2 序列：[2,4,6,8,10,12]
	fetch := func(t *testing.T, opts ResourceSegmentListOptions, want []int, wantPrev, wantNext int, wantTotal *int) {
		t.Helper()
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, opts)
		if err != nil {
			t.Fatalf("ListResourceSegments(%+v): %v", opts, err)
		}
		assertSegmentPage(t, page, want, wantPrev, wantNext, wantTotal)
	}
	anchor5 := idsByIndex[5]
	anchor2 := idsByIndex[2]
	totalCh1 := 6

	t.Run("first_page", func(t *testing.T) {
		fetch(t, ResourceSegmentListOptions{GroupKey: "ch1.xhtml", Limit: 4, IncludeTotal: true},
			[]int{1, 3, 5, 7}, 0, 7, &totalCh1)
	})

	t.Run("asc_cursor_5", func(t *testing.T) {
		// 窗口从 ch1 序列中 index>5 的行起，不混入 ch2 的偶数 index。
		fetch(t, ResourceSegmentListOptions{GroupKey: "ch1.xhtml", AfterID: 5, HasCursor: true, Limit: 3},
			[]int{7, 9, 11}, 7, 0, nil)
	})

	t.Run("desc_cursor_within_group", func(t *testing.T) {
		// desc 窗口在章内取 index<9 的尾端 2 条；再用 prev_cursor=5
		// 链式回退到 [1,3]，窗口无重叠且全程不跨组。
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
			GroupKey: "ch1.xhtml", Direction: "desc", AfterID: 9, HasCursor: true, Limit: 2,
		})
		if err != nil {
			t.Fatalf("ListResourceSegments: %v", err)
		}
		assertSegmentPage(t, page, []int{5, 7}, 5, 7, nil)
		fetch(t, ResourceSegmentListOptions{GroupKey: "ch1.xhtml", Direction: "desc", AfterID: page.PrevCursor, HasCursor: true, Limit: 2},
			[]int{1, 3}, 0, 3, nil)
	})

	t.Run("desc_cursor_near_chapter_start", func(t *testing.T) {
		// ch1 序列中 index<3 的只有 1 条，窗口不满 limit 且起点前无数据。
		fetch(t, ResourceSegmentListOptions{GroupKey: "ch1.xhtml", Direction: "desc", AfterID: 3, HasCursor: true, Limit: 2},
			[]int{1}, 0, 1, nil)
	})

	t.Run("anchor_within_group", func(t *testing.T) {
		// 锚点 index 5 属于 ch1，窗口从 ch1 序列中首个 >=5 的行起。
		fetch(t, ResourceSegmentListOptions{GroupKey: "ch1.xhtml", AnchorSegmentID: &anchor5, Limit: 3, IncludeTotal: true},
			[]int{5, 7, 9}, 5, 9, &totalCh1)
	})

	t.Run("anchor_group_mismatch", func(t *testing.T) {
		// 锚点 index 2 属于 ch2，请求 ch1 章节必须报 ErrSegmentGroupMismatch。
		_, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
			GroupKey:        "ch1.xhtml",
			AnchorSegmentID: &anchor2,
			Limit:           3,
		})
		if !errors.Is(err, ErrSegmentGroupMismatch) {
			t.Fatalf("err=%v want ErrSegmentGroupMismatch", err)
		}
	})

	t.Run("cursor_past_end_returns_empty", func(t *testing.T) {
		// 游标越过章内末尾：返回空页而非回到首页，prev/next 均无。
		fetch(t, ResourceSegmentListOptions{GroupKey: "ch1.xhtml", AfterID: 100, HasCursor: true, Limit: 4},
			[]int{}, 0, 0, nil)
	})
}

// boolPtr 供搜索选项指针字段使用。
func boolPtr(v bool) *bool { return &v }

// TestSegmentScanBatchSize 锁定批次计算：初始 clamp(limit*4, 128, 1000)，
// 低命中逐批倍增封顶 1000。
func TestSegmentScanBatchSize(t *testing.T) {
	cases := []struct {
		limit, n, want int
	}{
		{50, 0, 200},
		{50, 1, 400},
		{50, 2, 800},
		{50, 3, 1000},
		{50, 5, 1000},
		{10, 0, 128},   // clamp 下限
		{10, 1, 256},   // 128*2
		{10, 2, 512},   // 256*2
		{10, 3, 1000},  // 512*2 clamp
		{300, 0, 1000}, // clamp 上限
	}
	for _, c := range cases {
		if got := segmentScanBatchSize(c.limit, c.n); got != c.want {
			t.Fatalf("segmentScanBatchSize(%d, %d) = %d, want %d", c.limit, c.n, got, c.want)
		}
	}
}

// TestListResourceSegmentsSearch 覆盖搜索路径的核心语义：字段/大小写/整词/regex
// /nil target/精确匹配对 SQLite LIKE 假阳性的纠正。limit=2 触发跨批次扫描。
func TestListResourceSegmentsSearch(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-search-user")
	project := createTestProject(t, client, "seg-search-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/search.txt")

	// index → source / target
	rows := []struct {
		index  int
		source string
		target *string
	}{
		{0, "Alpha cat", strPtr("第一批")},
		{1, "beta dog", strPtr("ALPHA 狗")},     // target 大写（大小写敏感时不命中 alpha）
		{2, "alphabet soup", strPtr("字母汤")},    // source 含 "alpha" 但非整词
		{3, "CAT and cat", nil},                // nil target
		{4, "犬", strPtr("alpha 猫")},            // target 小写 alpha
		{5, "nothing here", strPtr("无命中")},     // 完全不命中
		{6, "ALPHA force", strPtr("ALPHA 队")},  // 全大写
		{7, "alphabetic", strPtr("词汇的")},       // 前缀整词歧义
		{8, "cat%", strPtr("百分号%")},            // LIKE 通配符假阳性候选
		{9, "under_score word", strPtr("下划线")}, // LIKE _ 通配符
	}
	for _, r := range rows {
		c := client.Segment.Create().
			SetResourceID(res.ID).
			SetSegmentIndex(r.index).
			SetSourceText(r.source).
			SetStatus(segment.StatusPending)
		if r.target != nil {
			c = c.SetTargetText(*r.target)
		}
		if _, err := c.Save(ctx); err != nil {
			t.Fatalf("create segment %d: %v", r.index, err)
		}
	}

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	// fetch 断言页面内容 + prev/next（total 可选）。
	fetch := func(t *testing.T, opts ResourceSegmentListOptions, want []int, wantPrev, wantNext int, wantTotal *int) *ResourceSegmentPage {
		t.Helper()
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, opts)
		if err != nil {
			t.Fatalf("ListResourceSegments(%+v): %v", opts, err)
		}
		assertSegmentPage(t, page, want, wantPrev, wantNext, wantTotal)
		return page
	}

	// 大小写敏感 substring "alpha"。SQLite LIKE 对 ASCII 大小写不敏感，粗筛会把
	// "Alpha cat"(0)、target "ALPHA 狗"(1)、"ALPHA force"(6) 全当成候选（假阳性），
	// 由 Go 精确匹配纠正：真实命中只有 2(alphabet)、4(alpha 猫)、7(alphabetic)。
	totalAlpha := 3
	t.Run("case_sensitive_substring", func(t *testing.T) {
		fetch(t, ResourceSegmentListOptions{Search: "alpha", Limit: 50, IncludeTotal: true},
			[]int{2, 4, 7}, 0, 0, &totalAlpha)
	})

	// 大小写不敏感 substring：0,1(target ALPHA 狗),4,6 命中；2/7 含 alpha 子串但那是
	// source 的 "alphabet"，其实也含 "alpha" —— 注意 2 的 source "alphabet soup"
	// 含 "alpha"，7 的 "alphabetic" 亦含。全部命中集合：0,1,2,4,6,7。
	totalAlphaFold := 6
	t.Run("case_insensitive_substring", func(t *testing.T) {
		fetch(t, ResourceSegmentListOptions{Search: "alpha", CaseSensitive: boolPtr(false), Limit: 4, IncludeTotal: true},
			[]int{0, 1, 2, 4}, 0, 4, &totalAlphaFold)
		// 下一页
		fetch(t, ResourceSegmentListOptions{Search: "alpha", CaseSensitive: boolPtr(false), AfterID: 4, HasCursor: true, Limit: 4, IncludeTotal: true},
			[]int{6, 7}, 6, 0, &totalAlphaFold)
	})

	// 大小写不敏感的 Unicode 折叠：É 与 é 经 Go 匹配折叠。SQLite LIKE 对
	// 非 ASCII 完全不折叠（'Café' LIKE '%cafe%' 为假），因此 case-insensitive
	// 路径（无粗筛、全量后过滤）必须由 Go StringEqFold 兜底。
	uni := createTestResource(t, client, project.ID, "chapters/unicode.txt")
	for i, src := range []string{"Café", "cafe", "CAFÉ", "récit"} {
		if err := client.Segment.Create().SetResourceID(uni.ID).SetSegmentIndex(i).
			SetSourceText(src).SetStatus(segment.StatusPending).Exec(ctx); err != nil {
			t.Fatalf("create unicode segment %d: %v", i, err)
		}
	}
	totalAccent := 3 // é 不敏感折叠：Café / CAFÉ / récit 都含 "é"
	t.Run("case_insensitive_unicode", func(t *testing.T) {
		fetchUni := func(t *testing.T, opts ResourceSegmentListOptions, want []int, wantPrev, wantNext int, wantTotal *int) {
			t.Helper()
			page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, uni.ID, opts)
			if err != nil {
				t.Fatalf("ListResourceSegments(%+v): %v", opts, err)
			}
			assertSegmentPage(t, page, want, wantPrev, wantNext, wantTotal)
		}
		// search "café" 不敏感：Café(0)、CAFÉ(2) 折叠命中；cafe(1) 无重音不命中。
		// 恰 2 命中=limit，窗口后无更多候选 → next 无。
		fetchUni(t, ResourceSegmentListOptions{Search: "café", CaseSensitive: boolPtr(false), Limit: 2},
			[]int{0, 2}, 0, 0, nil)
		fetchUni(t, ResourceSegmentListOptions{Search: "café", CaseSensitive: boolPtr(false), Limit: 50, IncludeTotal: true},
			[]int{0, 2}, 0, 0, ptrInt(2))
		// search "é" 不敏感：0,2,3 全含 é。
		fetchUni(t, ResourceSegmentListOptions{Search: "é", CaseSensitive: boolPtr(false), Limit: 50, IncludeTotal: true},
			[]int{0, 2, 3}, 0, 0, &totalAccent)
		// 大小写敏感（默认）：仅字面量精确命中。0 "Café" 与 2 "CAFÉ" 均不出现
		// 小写 "café" => 无命中（大小写敏感路径的精确性）。
		fetchUni(t, ResourceSegmentListOptions{Search: "café", Limit: 50},
			[]int{}, 0, 0, nil)
	})

	// 整词匹配：cat 命中 3(CAT and cat) 与 8(cat%)；0 "Alpha cat" 含独立 cat 也命中；
	// 2 "alphabet soup" 不含 cat 不命中。词边界按 unicode letter/digit 判定，
	// "cat%" 的 % 非字母 → 命中。命中集：0,3,8。
	totalWord := 3
	t.Run("whole_word", func(t *testing.T) {
		fetch(t, ResourceSegmentListOptions{Search: "cat", WholeWord: boolPtr(true), Limit: 50, IncludeTotal: true},
			[]int{0, 3, 8}, 0, 0, &totalWord)
		// 大小写不敏感整词：同样 0,3,8。
		fetch(t, ResourceSegmentListOptions{Search: "CAT", WholeWord: boolPtr(true), CaseSensitive: boolPtr(false), Limit: 2},
			[]int{0, 3}, 0, 3, nil)
	})

	// regex：`alp.a` 精确匹配 alpha（a-l-p-?-a）。命中 2(alphabet)、4(alpha 猫)、
	// 7(alphabetic)；0 "Alpha" 首字母大写不命中，6 "ALPHA" 不命中，1 target
	// "ALPHA 狗" 不命中。LiteralPrefix 给出字面前缀 "alp"，粗筛同样生效。
	totalRegex := 3
	t.Run("regex", func(t *testing.T) {
		fetch(t, ResourceSegmentListOptions{Search: `alp.a`, MatchMode: "regex", Limit: 50, IncludeTotal: true},
			[]int{2, 4, 7}, 0, 0, &totalRegex)
		// regex 不敏感：0,1,2,4,6,7。
		totalReFold := 6
		fetch(t, ResourceSegmentListOptions{Search: "alpha", MatchMode: "regex", CaseSensitive: boolPtr(false), Limit: 50, IncludeTotal: true},
			[]int{0, 1, 2, 4, 6, 7}, 0, 0, &totalReFold)
	})

	// 候选粗筛特殊字符：search 含 LIKE 通配符 % 与 _。SQLite LIKE 里 %/_ 是模式字符，
	// 粗筛假阳性（8 source "cat%"、9 "under_score word" 对 search "_"）不能泄漏为命中。
	t.Run("candidate_wildcard_chars", func(t *testing.T) {
		// search "%"：整词不敏感 → 无任何含字面 % 的段（8 有！source "cat%"）。命中 8。
		fetch(t, ResourceSegmentListOptions{Search: "%", Limit: 50}, []int{8}, 0, 0, nil)
		// search "_"：source 9 含下划线。命中 9。
		fetch(t, ResourceSegmentListOptions{Search: "_", Limit: 50}, []int{9}, 0, 0, nil)
		// search "cat_x"：LIKE 里 _ 匹配任意字符会假阳性 "cat and cat"？不——
		// "cat and cat" 无 "cat?x" 模式。安全验证无命中。
		fetch(t, ResourceSegmentListOptions{Search: "cat_x", Limit: 50}, []int{}, 0, 0, nil)
	})

	// nil target：target 字段搜索时 3（nil target）不命中；source 搜索命中。
	totalTarget := 4
	t.Run("nil_target_excluded", func(t *testing.T) {
		// target 含 "猫" 或 "狗" 的：1(ALPHA 狗)、4(alpha 猫)。不敏感。
		fetch(t, ResourceSegmentListOptions{Search: "狗", SearchField: "target", CaseSensitive: boolPtr(false), Limit: 50},
			[]int{1}, 0, 0, nil)
		fetch(t, ResourceSegmentListOptions{Search: "猫", SearchField: "target", CaseSensitive: boolPtr(false), Limit: 50},
			[]int{4}, 0, 0, nil)
		// source 字段只搜 source：3 的 "CAT and cat" 命中 cat（大小写不敏感）。
		// 命中集 0,2,3,7,8（含 alpha* 或 cat）。
		fetch(t, ResourceSegmentListOptions{Search: "cat", SearchField: "source", CaseSensitive: boolPtr(false), Limit: 50},
			[]int{0, 3, 8}, 0, 0, nil)
		// 默认 both：target 上的 alpha 也算（1,4,6）。
		fetch(t, ResourceSegmentListOptions{Search: "alpha", SearchField: "both", CaseSensitive: boolPtr(false), Limit: 50, IncludeTotal: true},
			[]int{0, 1, 2, 4, 6, 7}, 0, 0, &totalAlphaFold)
		_ = totalTarget
	})
}

// TestListResourceSegmentsSearchPagination 覆盖扫描路径的窗口语义：
// asc/desc 游标、锚点、prev/next 链、空页、低命中跨批次。
func TestListResourceSegmentsSearchPagination(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-spage-user")
	project := createTestProject(t, client, "seg-spage-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/spage.txt")

	// 12 段，偶数段 source 含 "needle"。
	idsByIndex := make(map[int]int)
	for i := 1; i <= 12; i++ {
		src := fmt.Sprintf("haystack %d", i)
		if i%2 == 0 {
			src = fmt.Sprintf("needle %d", i)
		}
		row := createTestSegment(t, client, res.ID, i, src, nil)
		idsByIndex[i] = row.ID
	}
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	fetch := func(t *testing.T, opts ResourceSegmentListOptions, want []int, wantPrev, wantNext int, wantTotal *int) *ResourceSegmentPage {
		t.Helper()
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, opts)
		if err != nil {
			t.Fatalf("ListResourceSegments(%+v): %v", opts, err)
		}
		assertSegmentPage(t, page, want, wantPrev, wantNext, wantTotal)
		return page
	}
	totalNeedles := 6

	t.Run("first_page_asc", func(t *testing.T) {
		fetch(t, ResourceSegmentListOptions{Search: "needle", Limit: 3, IncludeTotal: true},
			[]int{2, 4, 6}, 0, 6, &totalNeedles)
	})

	t.Run("asc_cursor_chain", func(t *testing.T) {
		// 用 next_cursor 逐页前进，再以 prev_cursor（desc 方向）回退。
		page := fetch(t, ResourceSegmentListOptions{Search: "needle", AfterID: 6, HasCursor: true, Limit: 3},
			[]int{8, 10, 12}, 8, 0, nil)
		fetch(t, ResourceSegmentListOptions{Search: "needle", Direction: "desc", AfterID: page.PrevCursor, HasCursor: true, Limit: 3},
			[]int{2, 4, 6}, 0, 6, nil)
	})

	t.Run("desc_cursor", func(t *testing.T) {
		// index<9 的 needle 是 2,4,6,8：尾端 3 条 = 4,6,8，响应升序。
		fetch(t, ResourceSegmentListOptions{Search: "needle", Direction: "desc", AfterID: 9, HasCursor: true, Limit: 3},
			[]int{4, 6, 8}, 4, 8, nil)
		// prev 链：index<4 的 needle 只剩 2。
		fetch(t, ResourceSegmentListOptions{Search: "needle", Direction: "desc", AfterID: 4, HasCursor: true, Limit: 3},
			[]int{2}, 0, 2, nil)
	})

	t.Run("anchor_window", func(t *testing.T) {
		// 锚点 index 5（odd，不满足搜索条件），窗口从首个 >=5 的 needle（6）起。
		anchor := idsByIndex[5]
		fetch(t, ResourceSegmentListOptions{Search: "needle", AnchorSegmentID: &anchor, Limit: 2, IncludeTotal: true},
			[]int{6, 8}, 6, 8, &totalNeedles)
	})

	t.Run("cursor_past_end_empty", func(t *testing.T) {
		fetch(t, ResourceSegmentListOptions{Search: "needle", AfterID: 100, HasCursor: true, Limit: 3},
			[]int{}, 0, 0, nil)
	})

	t.Run("no_match_empty_page", func(t *testing.T) {
		total0 := 0
		fetch(t, ResourceSegmentListOptions{Search: "nonexistent", Limit: 3, IncludeTotal: true},
			[]int{}, 0, 0, &total0)
	})

	t.Run("search_with_status", func(t *testing.T) {
		// needle 段 2,6,10 设为 approved；status=approved 过滤后命中 2,6,10。
		for _, i := range []int{2, 6, 10} {
			if err := client.Segment.UpdateOneID(idsByIndex[i]).SetStatus(segment.StatusApproved).Exec(ctx); err != nil {
				t.Fatalf("approve segment %d: %v", i, err)
			}
		}
		totalApproved := 3
		fetch(t, ResourceSegmentListOptions{Search: "needle", Status: "approved", Limit: 2, IncludeTotal: true},
			[]int{2, 6}, 0, 6, &totalApproved)
		// 清理恢复 pending，避免影响其它子测试（共享 res）。
		for _, i := range []int{2, 6, 10} {
			if err := client.Segment.UpdateOneID(idsByIndex[i]).SetStatus(segment.StatusPending).Exec(ctx); err != nil {
				t.Fatalf("reset segment %d: %v", i, err)
			}
		}
	})
}

// TestListResourceSegmentsSearchCrossBatch 低命中跨批次：构造 >128 段数据，
// 命中分散在 128 窗口之外，验证 keyset 推进与倍增。
func TestListResourceSegmentsSearchCrossBatch(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-cross-user")
	project := createTestProject(t, client, "seg-cross-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/cross.txt")

	// 300 段：默认命中零星（每 50 段一个 "rare"），另在尾部集中 5 个 "tail"。
	for i := 1; i <= 300; i++ {
		src := fmt.Sprintf("common %d", i)
		switch {
		case i%50 == 0:
			src = fmt.Sprintf("rare %d", i)
		case i > 295:
			src = fmt.Sprintf("tail %d", i)
		}
		createTestSegment(t, client, res.ID, i, src, nil)
	}
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	// "rare" 命中 50,100,150,200,250,300 —— 首批 128 只含 50/100，跨批推进。
	page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
		Search: "rare", Limit: 4, IncludeTotal: true,
	})
	if err != nil {
		t.Fatalf("ListResourceSegments: %v", err)
	}
	assertSegmentPage(t, page, []int{50, 100, 150, 200}, 0, 200, ptrInt(6))

	// 剩余页
	page2, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
		Search: "rare", AfterID: 200, HasCursor: true, Limit: 4, IncludeTotal: true,
	})
	if err != nil {
		t.Fatalf("ListResourceSegments cursor: %v", err)
	}
	assertSegmentPage(t, page2, []int{250, 300}, 250, 0, ptrInt(6))

	// "tail" 命中 296-299（300 因 %50==0 归入 rare）：首批 128 内零命中，
	// 低命中跨批倍增后才出现。
	page3, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
		Search: "tail", Limit: 3, IncludeTotal: true,
	})
	if err != nil {
		t.Fatalf("ListResourceSegments tail: %v", err)
	}
	assertSegmentPage(t, page3, []int{296, 297, 298}, 0, 298, ptrInt(4))

	// desc 尾端 3 条 tail = 297,298,299
	page4, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
		Search: "tail", Direction: "desc", AfterID: 301, HasCursor: true, Limit: 3,
	})
	if err != nil {
		t.Fatalf("ListResourceSegments tail desc: %v", err)
	}
	assertSegmentPage(t, page4, []int{297, 298, 299}, 297, 0, nil)
}

// TestListResourceSegmentsSearchGroupAndQuality 搜索与 status/quality/group_key 组合，
// 以及 group_key 与搜索共用扫描路径时的窗口语义。
func TestListResourceSegmentsSearchGroupAndQuality(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-sgq-user")
	project := createTestProject(t, client, "seg-sgq-proj", user.ID)
	res := createTestResource(t, client, project.ID, "book-sgq.epub")

	metaA := `{"epub_file":"ch1.xhtml"}`
	metaB := `{"epub_file":"ch2.xhtml"}`
	// ch1: 1,3,5,7,9,11；ch2: 2,4,6,8,10,12。含 "alpha" 的：1,4,7,10。
	for i := 1; i <= 12; i++ {
		src := fmt.Sprintf("text %d", i)
		if i == 1 || i == 4 || i == 7 || i == 10 {
			src = fmt.Sprintf("alpha %d", i)
		}
		meta := metaA
		if i%2 == 0 {
			meta = metaB
		}
		issues := []qa.QualityIssue(nil)
		if i == 4 || i == 7 {
			issues = []qa.QualityIssue{{SegmentIndex: i, Severity: qa.SeverityWarning, Code: "duplicate", Message: "dup"}}
		}
		createTestSegmentWithMeta(t, client, res.ID, i, src, meta, issues)
	}
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	fetch := func(t *testing.T, opts ResourceSegmentListOptions, want []int, wantPrev, wantNext int, wantTotal *int) {
		t.Helper()
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, opts)
		if err != nil {
			t.Fatalf("ListResourceSegments(%+v): %v", opts, err)
		}
		assertSegmentPage(t, page, want, wantPrev, wantNext, wantTotal)
	}

	t.Run("search_with_group", func(t *testing.T) {
		// ch1 内 alpha：1,7。
		totalCh1 := 2
		fetch(t, ResourceSegmentListOptions{Search: "alpha", GroupKey: "ch1.xhtml", Limit: 1, IncludeTotal: true},
			[]int{1}, 0, 1, &totalCh1)
		fetch(t, ResourceSegmentListOptions{Search: "alpha", GroupKey: "ch1.xhtml", AfterID: 1, HasCursor: true, Limit: 5},
			[]int{7}, 7, 0, nil)
		// ch2 内 alpha：4,10。
		fetch(t, ResourceSegmentListOptions{Search: "alpha", GroupKey: "ch2.xhtml", Limit: 50},
			[]int{4, 10}, 0, 0, nil)
	})

	t.Run("search_with_quality", func(t *testing.T) {
		// alpha + has issues：4,7（10 无 issues）。
		totalIssued := 2
		fetch(t, ResourceSegmentListOptions{Search: "alpha", QualityIssues: "has", Limit: 1, IncludeTotal: true},
			[]int{4}, 0, 4, &totalIssued)
	})

	t.Run("group_with_desc_cursor", func(t *testing.T) {
		// ch1 内 index<8 的行（1,3,5,7）：尾端 2 条 = 5,7。
		fetch(t, ResourceSegmentListOptions{GroupKey: "ch1.xhtml", Direction: "desc", AfterID: 8, HasCursor: true, Limit: 2},
			[]int{5, 7}, 5, 7, nil)
	})

	t.Run("group_desc_cursor_below_start", func(t *testing.T) {
		// ch1 内 index<3 只有 1。
		fetch(t, ResourceSegmentListOptions{GroupKey: "ch1.xhtml", Direction: "desc", AfterID: 3, HasCursor: true, Limit: 4},
			[]int{1}, 0, 1, nil)
	})
}

// TestListResourceSegmentsSearchInvalidPattern 非空 search 构造失败必须原样返回
// errors.Is 可识别错误（regex 不可编译 / 未知 match mode）。
func TestListResourceSegmentsSearchInvalidPattern(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-badpat-user")
	project := createTestProject(t, client, "seg-badpat-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/badpat.txt")
	createTestSegment(t, client, res.ID, 0, "hello", nil)
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	_, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
		Search: "([unclosed", MatchMode: "regex", Limit: 10,
	})
	if !errors.Is(err, segmatch.ErrInvalidPattern) {
		t.Fatalf("err=%v want ErrInvalidPattern", err)
	}

	_, err = svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
		Search: "x", MatchMode: "fuzzy", Limit: 10,
	})
	if !errors.Is(err, segmatch.ErrUnsupportedMatchMode) {
		t.Fatalf("err=%v want ErrUnsupportedMatchMode", err)
	}
}

// TestSegmentSearchCandidateLiteral 粗筛构造矩阵：SQLite 仅对 substring 构造
// 二进制 INSTR 谓词（CAST 列为 BLOB、字面量以 []byte 经 Builder.Arg 绑定，
// 列值/字面量含 NUL 均安全，%/_/\ 与 Unicode 只按字节比较）；regex 在 SQLite
// 一律返回 nil——列可存非法 UTF-8，Go regexp 将非法字节视为 U+FFFD，BLOB 字面
// 前缀粗筛会漏掉建立在 U+FFFD 上的命中；Postgres 在字面量不含 NUL 时构造
// LIKE——大小写敏感 substring 返回原文字面量谓词，不敏感返回 nil（无安全字面量，
// 全量后过滤），regex 取字面前缀；字面量含 NUL 无法作为 text 参数绑定，
// Postgres 退回全量精筛。
func TestSegmentSearchCandidateLiteral(t *testing.T) {
	mk := func(t *testing.T, opts segmatch.Options) segmatch.Matcher {
		t.Helper()
		m, err := segmatch.NewMatcher(opts)
		if err != nil {
			t.Fatalf("NewMatcher: %v", err)
		}
		return m
	}
	render := func(t *testing.T, p predicate.Segment, d string) (string, []any) {
		t.Helper()
		sel := sql.Dialect(d).Select().From(sql.Table(segment.Table))
		p(sel)
		return sel.Query()
	}

	cs := mk(t, segmatch.Options{Find: "Hello", CaseSensitive: boolPtr(true)})

	t.Run("sqlite_instr", func(t *testing.T) {
		for _, tc := range []struct {
			field    string
			wantCols []string // 必须出现的列
		}{
			{field: "", wantCols: []string{"source_text", "target_text"}},
			{field: "both", wantCols: []string{"source_text", "target_text"}},
			{field: "source", wantCols: []string{"source_text"}},
			{field: "target", wantCols: []string{"target_text"}},
		} {
			p := segmentSearchCandidate(cs, tc.field, "substring", dialect.SQLite)
			if p == nil {
				t.Fatalf("sqlite field=%q must produce INSTR candidate", tc.field)
			}
			query, args := render(t, p, dialect.SQLite)
			if strings.Count(query, "INSTR(CAST(") != len(tc.wantCols) {
				t.Fatalf("sqlite field=%q must have one INSTR per field: %s", tc.field, query)
			}
			for _, col := range tc.wantCols {
				if !strings.Contains(query, "`"+col+"` AS BLOB), ?) > 0") {
					t.Fatalf("sqlite field=%q must INSTR(CAST(%s AS BLOB)) with bound arg: %s", tc.field, col, query)
				}
			}
			if strings.Contains(query, "LIKE") || strings.Contains(query, "ESCAPE") {
				t.Fatalf("sqlite candidate must not use LIKE: %s", query)
			}
			if len(args) != len(tc.wantCols) {
				t.Fatalf("sqlite field=%q args=%v want one arg per field", tc.field, args)
			}
			for _, a := range args {
				blob, ok := a.([]byte)
				if !ok || !bytes.Equal(blob, []byte("Hello")) {
					t.Fatalf("sqlite field=%q arg=%v want []byte(Hello)", tc.field, a)
				}
			}
		}
	})

	t.Run("postgres_case_sensitive_substring", func(t *testing.T) {
		p := segmentSearchCandidate(cs, "both", "substring", dialect.Postgres)
		if p == nil {
			t.Fatal("case-sensitive substring must produce candidate predicate")
		}
		query, args := render(t, p, dialect.Postgres)
		if !strings.Contains(query, `"source_text" LIKE $1`) || !strings.Contains(query, `"target_text" LIKE $2`) {
			t.Fatalf("candidate predicate must be a LIKE on both fields: %s", query)
		}
		// 字面量以绑定参数传递（ent escapedLike），应为 %Hello%。
		if len(args) != 2 || args[0] != "%Hello%" || args[1] != "%Hello%" {
			t.Fatalf("candidate args=%v want [%%Hello%% %%Hello%%]", args)
		}
	})
	p := segmentSearchCandidate(cs, "source", "substring", dialect.Postgres)
	query, args := render(t, p, dialect.Postgres)
	if !strings.Contains(query, `"source_text" LIKE $1`) || strings.Contains(query, "target_text") {
		t.Fatalf("source-only candidate must LIKE source_text only: %s", query)
	}
	if len(args) != 1 || args[0] != "%Hello%" {
		t.Fatalf("source-only candidate args=%v want [%%Hello%%]", args)
	}

	ci := mk(t, segmatch.Options{Find: "Hello", CaseSensitive: boolPtr(false)})
	for _, d := range []string{dialect.SQLite, dialect.Postgres} {
		if got := segmentSearchCandidate(ci, "source", "substring", d); got != nil {
			t.Fatalf("case-insensitive must have no candidate (%s), got %v", d, got)
		}
	}

	re := mk(t, segmatch.Options{Find: `abc\d+`, MatchMode: "regex"})
	p = segmentSearchCandidate(re, "target", "regex", dialect.Postgres)
	if p == nil {
		t.Fatal("regex with literal prefix must produce candidate predicate")
	}
	query, args = render(t, p, dialect.Postgres)
	if !strings.Contains(query, "LIKE $1") || len(args) != 1 || args[0] != "%abc%" {
		t.Fatalf("regex candidate must use literal prefix as arg, query=%s args=%v", query, args)
	}
	// SQLite：regex 即便有字面前缀也必须禁用粗筛（非法 UTF-8 列在 Go regexp
	// 眼中是 U+FFFD，BLOB 前缀粗筛会漏掉这类命中）。
	if got := segmentSearchCandidate(re, "target", "regex", dialect.SQLite); got != nil {
		t.Fatalf("sqlite regex must have no candidate even with literal prefix, got %v", got)
	}

	// 谓词矩阵：SQLite 下 regex 一律 nil（有无字面前缀、大小写敏感与否）。
	for _, opts := range []segmatch.Options{
		{Find: "abc", MatchMode: "regex"},
		{Find: `abc\d+`, MatchMode: "regex"},
		{Find: `\d+`, MatchMode: "regex"},
		{Find: "abc", MatchMode: "regex", CaseSensitive: boolPtr(false)},
		{Find: "abc", MatchMode: "regex", WholeWord: boolPtr(true)},
	} {
		m := mk(t, opts)
		for _, field := range []string{"", "source", "target", "both"} {
			if got := segmentSearchCandidate(m, field, "regex", dialect.SQLite); got != nil {
				t.Fatalf("sqlite regex candidate must be nil (find=%q), got %v", opts.Find, got)
			}
		}
	}
	// 同一 regex matcher 在 Postgres（字面量不含 NUL）仍保留 LIKE 粗筛。
	if got := segmentSearchCandidate(mk(t, segmatch.Options{Find: "abc", MatchMode: "regex"}), "both", "regex", dialect.Postgres); got == nil {
		t.Fatal("postgres regex with literal prefix must keep LIKE candidate")
	}

	// 无字面前缀的 regex。
	re2 := mk(t, segmatch.Options{Find: `\d+`, MatchMode: "regex"})
	for _, d := range []string{dialect.SQLite, dialect.Postgres} {
		if got := segmentSearchCandidate(re2, "both", "regex", d); got != nil {
			t.Fatalf("regex without literal prefix must have no candidate (%s), got %v", d, got)
		}
	}

	// 字面量含 NUL：SQLite substring 以 []byte 绑定为 BLOB，INSTR 按字节比较，
	// 仍可粗筛；Postgres 的 text 参数不允许 NUL，退回全量精筛。
	csNul := mk(t, segmatch.Options{Find: "a\x00b", CaseSensitive: boolPtr(true)})
	p = segmentSearchCandidate(csNul, "both", "substring", dialect.SQLite)
	if p == nil {
		t.Fatal("sqlite NUL literal must still produce INSTR candidate")
	}
	query, args = render(t, p, dialect.SQLite)
	if strings.Count(query, "INSTR(CAST(") != 2 || len(args) != 2 {
		t.Fatalf("sqlite NUL candidate must INSTR both fields with two args: %s", query)
	}
	for _, a := range args {
		if !bytes.Equal(argBytes(t, a), []byte("a\x00b")) {
			t.Fatalf("sqlite NUL candidate arg=%v want []byte with NUL", a)
		}
	}
	if got := segmentSearchCandidate(csNul, "both", "substring", dialect.Postgres); got != nil {
		t.Fatalf("postgres NUL literal must have no candidate, got %v", got)
	}

	// 通配符与反斜杠字面量：INSTR 按字节比较，不做 LIKE 转义，参数原样绑定。
	for _, lit := range []string{"100%", "a_c", `\`, "世界"} {
		m := mk(t, segmatch.Options{Find: lit, CaseSensitive: boolPtr(true)})
		p := segmentSearchCandidate(m, "source", "substring", dialect.SQLite)
		if p == nil {
			t.Fatalf("literal %q must produce sqlite candidate", lit)
		}
		query, args := render(t, p, dialect.SQLite)
		if strings.Contains(query, "LIKE") || strings.Contains(query, "ESCAPE") || strings.Contains(query, "LOWER") {
			t.Fatalf("literal %q must not be treated as pattern: %s", lit, query)
		}
		if len(args) != 1 || !bytes.Equal(argBytes(t, args[0]), []byte(lit)) {
			t.Fatalf("literal %q args=%v want single []byte arg", lit, args)
		}
	}

	for _, d := range []string{dialect.SQLite, dialect.Postgres} {
		if got := segmentSearchCandidate(nil, "both", "substring", d); got != nil {
			t.Fatalf("nil matcher must yield nil (%s), got %v", d, got)
		}
	}
}

// argBytes 断言参数为 []byte 并返回其值。
func argBytes(t *testing.T, a any) []byte {
	t.Helper()
	blob, ok := a.([]byte)
	if !ok {
		t.Fatalf("arg %v is %T, want []byte", a, a)
	}
	return blob
}

// TestListResourceSegmentsSearchNUL SQLite 列值内嵌 NUL 的搜索正确性：文本粗筛
// 已改为二进制 INSTR(CAST(column AS BLOB), ?)，列值按字节比较、参数以 []byte
// 绑定为 BLOB，不受 LIKE 在内嵌 NUL 处截断的影响，因此粗筛开启也不会漏行；
// 搜索字面量本身含 NUL 亦可安全绑定，不得报错（返回 500）。
func TestListResourceSegmentsSearchNUL(t *testing.T) {
	client := testClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	user := createTestUser(t, client, "seg-nul-user")
	project := createTestProject(t, client, "seg-nul-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/nul.txt")

	createTestSegment(t, client, res.ID, 0, "a\x00b", nil)
	createTestSegment(t, client, res.ID, 1, "blue sky", nil)
	createTestSegment(t, client, res.ID, 2, "nothing here", nil)

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	assertSearch := func(t *testing.T, search string, want []int, wantTotal *int) {
		t.Helper()
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
			Search: search, Limit: 50, IncludeTotal: wantTotal != nil,
		})
		if err != nil {
			t.Fatalf("ListResourceSegments(search=%q): %v", search, err)
		}
		assertSegmentPage(t, page, want, 0, 0, wantTotal)
	}

	// 列值内嵌 NUL：搜 "b" 必须命中 'a\x00b'——二进制 INSTR 粗筛按字节比较，
	// 不会像 LIKE 那样在 NUL 处截断漏掉该行。
	totalB := 2
	assertSearch(t, "b", []int{0, 1}, &totalB)
	// "a" 位于 NUL 之前，命中。
	totalA := 1
	assertSearch(t, "a", []int{0}, &totalA)
	// 搜索串本身含 NUL：完整字面量命中（[]byte 绑定为 BLOB），不得报错。
	totalNulLiteral := 1
	assertSearch(t, "a\x00b", []int{0}, &totalNulLiteral)
	// 裸 NUL 搜索串。
	assertSearch(t, "\x00", []int{0}, &totalNulLiteral)
	// 无命中词保持无命中。
	totalNone := 0
	assertSearch(t, "zzz", []int{}, &totalNone)
}

// TestListResourceSegmentsSearchSQLiteInstr SQLite INSTR 粗筛的字面行为：
// %/_/\ 按 LiteralPrefix 原文绑定（ent 的 LIKE 会转义通配符，INSTR 无需转义），
// 不得解释为通配符；Unicode 字面量按字节匹配；target 命中路径同样生效。
func TestListResourceSegmentsSearchSQLiteInstr(t *testing.T) {
	client := testClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	user := createTestUser(t, client, "seg-instr-user")
	project := createTestProject(t, client, "seg-instr-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/instr.txt")

	createTestSegment(t, client, res.ID, 0, "100% done", nil)
	createTestSegment(t, client, res.ID, 1, "100x done", nil)
	createTestSegment(t, client, res.ID, 2, "a_c split", nil)
	createTestSegment(t, client, res.ID, 3, "a b split", nil)
	createTestSegment(t, client, res.ID, 4, `back\slash`, nil)
	createTestSegment(t, client, res.ID, 5, "backslash", nil)
	createTestSegment(t, client, res.ID, 6, "世界，你好", nil)
	createTestSegment(t, client, res.ID, 7, "hello world", nil)
	// target 命中：source 不含字面量、target 含。
	createTestSegmentWithTarget(t, client, res.ID, 8, "unrelated source", "目标：世界", nil)

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	assertSearch := func(t *testing.T, search string, want []int) {
		t.Helper()
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
			Search: search, Limit: 50,
		})
		if err != nil {
			t.Fatalf("ListResourceSegments(search=%q): %v", search, err)
		}
		assertSegmentPage(t, page, want, 0, 0, nil)
	}

	// "%" 不是通配符：只命中真实含 % 的行。
	assertSearch(t, "100%", []int{0})
	// "_" 不是单字符通配符：只命中真实含下划线的行。
	assertSearch(t, "a_c", []int{2})
	// 反斜杠按字面匹配。
	assertSearch(t, `\`, []int{4})
	// Unicode 字面量按字节匹配，source 与 target 命中路径均生效。
	assertSearch(t, "世界", []int{6, 8})
	// 大小写敏感（默认）：不命中其它大小写形式。
	assertSearch(t, "Hello", []int{})
}

// TestListResourceSegmentsSearchSQLiteInvalidUTF8 防回归：SQLite TEXT 列可存
// 非法 UTF-8 字节，Go regexp 把非法字节视为 U+FFFD，regex 命中可以完全建立在
// U+FFFD 上；若按 regex 字面前缀做 BLOB INSTR 粗筛，前缀字节并不在列值中，
// 会漏掉这类命中。因此 SQLite regex 必须禁用粗筛、全量精筛，命中数与 total
// 以 Go regexp 为准。
func TestListResourceSegmentsSearchSQLiteInvalidUTF8(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-badutf8-user")
	project := createTestProject(t, client, "seg-badutf8-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/badutf8.txt")

	// 0xFF 为非法 UTF-8 字节；ent/SQLite 原样存入 TEXT 列，Go regexp 读入时
	// 视为 U+FFFD，而字面 U+FFFD 的 UTF-8 编码（EF BF BD）并不在其中。
	if _, err := client.Segment.Create().
		SetResourceID(res.ID).SetSegmentIndex(0).
		SetSourceText("\xff bad").SetStatus(segment.StatusPending).Save(ctx); err != nil {
		t.Fatalf("create invalid-utf8 segment: %v", err)
	}
	createTestSegment(t, client, res.ID, 1, "clean text", nil)

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	totalFFFD := 1
	page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
		Search: "\uFFFD", MatchMode: "regex", Limit: 50, IncludeTotal: true,
	})
	if err != nil {
		t.Fatalf("ListResourceSegments regex U+FFFD: %v", err)
	}
	assertSegmentPage(t, page, []int{0}, 0, 0, &totalFFFD)

	// 反向：不含非法字节的资源对同一 regex 无命中。
	resClean := createTestResource(t, client, project.ID, "chapters/badutf8-clean.txt")
	createTestSegment(t, client, resClean.ID, 0, "clean text", nil)
	totalNone := 0
	pageClean, err := svc.ListResourceSegments(ctx, user.ID, project.ID, resClean.ID, ResourceSegmentListOptions{
		Search: "\uFFFD", MatchMode: "regex", Limit: 50, IncludeTotal: true,
	})
	if err != nil {
		t.Fatalf("ListResourceSegments clean resource: %v", err)
	}
	assertSegmentPage(t, pageClean, []int{}, 0, 0, &totalNone)
}

// TestListResourceSegmentsSameIndexCrossBatch segment_index 不唯一（数据损坏）：
// >128 行共享同一 index 时，内部续扫键必须用 (segment_index, id) 元组跨批推进——
// 仅按 index 续扫会在首批（全为同 index）之后直接判耗尽，漏掉其后所有行，
// countAll 亦随之死循环或漏计。对外 cursor 仍按 index：当重复跨越页面边界
// （limit=10 首页收满后仍命中且与末条匹配同 index）时，next_cursor 无法表达
// 这些行，列表必须报 ErrDuplicateSegmentIndex 而非返回 next cursor + 不可达空页；
// 未跨越边界（整页恰好收满、desc 窗口恰好收满）的部分仍正常返回。
func TestListResourceSegmentsSameIndexCrossBatch(t *testing.T) {
	client := testClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	user := createTestUser(t, client, "seg-sameidx-user")
	project := createTestProject(t, client, "seg-sameidx-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/sameidx.txt")

	// 300 行全部 segment_index=7：前 200 行不命中，后 100 行命中 "needle"。
	// limit=10 时首批大小 128 只含不命中行，命中行全部落在后续批次。
	for i := 1; i <= 300; i++ {
		src := fmt.Sprintf("haystack %d", i)
		if i > 200 {
			src = fmt.Sprintf("needle %d", i)
		}
		createTestSegment(t, client, res.ID, 7, src, nil)
	}
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	totalNeedles := 100

	t.Run("first_page_cross_batch_boundary_error", func(t *testing.T) {
		// limit=10：首批 128 行全不命中，命中行落在后续批次；收满 limit 后
		// 同批剩余仍命中且与末条匹配同 index=7，next_cursor 无法表达这些行。
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
			Search: "needle", Limit: 10, IncludeTotal: true,
		})
		if !errors.Is(err, ErrDuplicateSegmentIndex) {
			t.Fatalf("err=%v want ErrDuplicateSegmentIndex", err)
		}
		if page != nil {
			t.Fatalf("page=%v want nil on duplicate boundary error", page)
		}
	})

	t.Run("cursor_follow_empty_page", func(t *testing.T) {
		// 直接以 cursor=7 请求（跳过报错的首页）：窗口 index>7 无数据，
		// 返回空页且不报错、不挂起；该窗口内确实没有可表达的行。
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
			Search: "needle", AfterID: 7, HasCursor: true, Limit: 10,
		})
		if err != nil {
			t.Fatalf("cursor follow: %v", err)
		}
		if len(page.Items) != 0 || page.HasNextCursor || page.HasPrevCursor {
			t.Fatalf("cursor page must be empty without cursors, got %d items", len(page.Items))
		}
	})

	t.Run("full_page_and_total", func(t *testing.T) {
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
			Search: "needle", Limit: 100, IncludeTotal: true,
		})
		if err != nil {
			t.Fatalf("ListResourceSegments: %v", err)
		}
		if len(page.Items) != 100 {
			t.Fatalf("items=%d want 100", len(page.Items))
		}
		for i, row := range page.Items {
			if row.ID != 201+i {
				t.Fatalf("items[%d].ID=%d want %d (同 index 内按 id 升序)", i, row.ID, 201+i)
			}
		}
		if page.Total == nil || *page.Total != totalNeedles {
			t.Fatalf("total=%v want %d", page.Total, totalNeedles)
		}
		if page.HasPrevCursor || page.HasNextCursor {
			t.Fatalf("prev/next=%v/%v want false/false", page.HasPrevCursor, page.HasNextCursor)
		}
	})

	t.Run("desc_window", func(t *testing.T) {
		// desc 窗口 index<8 覆盖全部 300 行，尾端 100 条匹配即全部命中，
		// 响应仍升序。
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
			Search: "needle", Direction: "desc", AfterID: 8, HasCursor: true, Limit: 100,
		})
		if err != nil {
			t.Fatalf("desc window: %v", err)
		}
		if len(page.Items) != 100 {
			t.Fatalf("items=%d want 100", len(page.Items))
		}
		for i, row := range page.Items {
			if row.ID != 201+i {
				t.Fatalf("items[%d].ID=%d want %d (响应升序)", i, row.ID, 201+i)
			}
		}
		if page.HasPrevCursor || page.HasNextCursor {
			t.Fatalf("prev/next=%v/%v want false/false", page.HasPrevCursor, page.HasNextCursor)
		}
	})

	t.Run("count_all_terminates_on_corrupt_index", func(t *testing.T) {
		// 防御：countAll 沿 (index, id) 元组遍历，重复 index 不允许死循环或漏计。
		sc := &segmentScanner{limit: 10, filter: segmentMatchFilter(nil, "", "")}
		sc.baseQ = func() *ent.SegmentQuery {
			return applySegmentFilters(svc.client.Segment.Query(), res.ID, ResourceSegmentListOptions{}, dialect.SQLite)
		}
		total, err := sc.countAll(ctx)
		if err != nil {
			t.Fatalf("countAll: %v", err)
		}
		if total != 300 {
			t.Fatalf("total=%d want 300", total)
		}
	})
}

// TestListResourceSegmentsSQLPathBoundaryDuplicate SQL 快路径（无 search/group_key）
// 的边界重复检测：Limit+1 截断行与页边界行同 index 时报错，双向（升序 next /
// 降序窗口 prev）都要守卫；行数未越界时正常返回。
func TestListResourceSegmentsSQLPathBoundaryDuplicate(t *testing.T) {
	client := testClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	user := createTestUser(t, client, "seg-sqldup-user")
	project := createTestProject(t, client, "seg-sqldup-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/sqldup.txt")

	// 11 行全部 segment_index=3。
	for i := 0; i < 11; i++ {
		createTestSegment(t, client, res.ID, 3, fmt.Sprintf("row %d", i), nil)
	}
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)

	t.Run("asc_page_boundary_error", func(t *testing.T) {
		// limit=10：第 11 行被截断，next_cursor=3 无法表达 index=3 的剩余行。
		if _, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
			Limit: 10,
		}); !errors.Is(err, ErrDuplicateSegmentIndex) {
			t.Fatalf("err=%v want ErrDuplicateSegmentIndex", err)
		}
	})

	t.Run("desc_window_boundary_error", func(t *testing.T) {
		// desc 窗口 index<8：11 行全部候选，limit=10 截断的是页首之前的最低
		// index 行，prev_cursor=3 无法表达。
		if _, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
			Direction: "desc", AfterID: 8, HasCursor: true, Limit: 10,
		}); !errors.Is(err, ErrDuplicateSegmentIndex) {
			t.Fatalf("err=%v want ErrDuplicateSegmentIndex", err)
		}
	})

	t.Run("no_boundary_crossing_returns_page", func(t *testing.T) {
		// limit=11：窗口内候选全部落在页内，无截断，正常返回且无游标。
		page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
			Limit: 11,
		})
		if err != nil {
			t.Fatalf("ListResourceSegments: %v", err)
		}
		if len(page.Items) != 11 {
			t.Fatalf("items=%d want 11", len(page.Items))
		}
		for i, row := range page.Items {
			if row.SegmentIndex != 3 {
				t.Fatalf("items[%d].segment_index=%d want 3", i, row.SegmentIndex)
			}
			if i > 0 && row.ID <= page.Items[i-1].ID {
				t.Fatalf("ids not strictly increasing within same index: %d after %d", row.ID, page.Items[i-1].ID)
			}
		}
		if page.HasPrevCursor || page.HasNextCursor {
			t.Fatalf("prev/next=%v/%v want false/false", page.HasPrevCursor, page.HasNextCursor)
		}
	})
}

// TestListResourceSegmentsScanHydration 覆盖搜索/group_key 扫描路径的瘦行回填：
// 扫描阶段只取 (id, segment_index, source_text, target_text, meta)，窗口与
// prev/next 确定后应按最终 ID 集合一次性补齐完整行与 reviewed_by 边。插入序
// 与 segment_index 相反，锁定回填后仍按原 items ID 顺序排列，而非数据库按主键
// 返回的顺序；同时验证 resource_id、时间戳、status、target_text、
// quality_issues 与 reviewed_by 均已回填，且没有预加载 resource 边。
func TestListResourceSegmentsScanHydration(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-hydrate-user")
	project := createTestProject(t, client, "seg-hydrate-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/hydrate.txt")

	// 插入序 index 5,3,1 → 主键序与 index 序相反，用于验证回填重排。
	createTestSegmentWithMeta(t, client, res.ID, 5, "match five", `{"epub_file":"ch5.xhtml"}`, nil)
	mid := createTestSegmentWithMeta(t, client, res.ID, 3, "match three", `{"epub_file":"ch3.xhtml"}`, []qa.QualityIssue{
		{SegmentIndex: 3, Severity: qa.SeverityWarning, Code: "duplicate", Message: "dup"},
	})
	createTestSegmentWithMeta(t, client, res.ID, 1, "match one", `{"epub_file":"ch1.xhtml"}`, nil)
	if _, err := client.Segment.UpdateOneID(mid.ID).
		SetReviewedByID(user.ID).
		SetStatus(segment.StatusApproved).
		SetTargetText("译文三").
		Save(ctx); err != nil {
		t.Fatalf("decorate segment: %v", err)
	}

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{
		Search: "match", Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListResourceSegments: %v", err)
	}
	wantIndex := []int{1, 3, 5}
	wantID := []int{3, 2, 1}
	if len(page.Items) != 3 {
		t.Fatalf("items=%d want 3", len(page.Items))
	}
	for i, row := range page.Items {
		if row.SegmentIndex != wantIndex[i] || row.ID != wantID[i] {
			t.Fatalf("items[%d] id/index=%d/%d want %d/%d (回填必须保持 items ID 顺序)",
				i, row.ID, row.SegmentIndex, wantID[i], wantIndex[i])
		}
		if row.ResourceID == nil || *row.ResourceID != res.ID {
			t.Fatalf("items[%d] resource_id=%v want %d", i, row.ResourceID, res.ID)
		}
		if row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
			t.Fatalf("items[%d] timestamps not hydrated: created=%v updated=%v", i, row.CreatedAt, row.UpdatedAt)
		}
	}
	midItem := page.Items[1]
	if midItem.Meta == nil {
		t.Fatalf("meta not hydrated for index 3")
	}
	if midItem.Status != segment.StatusApproved {
		t.Fatalf("status=%q want %q", midItem.Status, segment.StatusApproved)
	}
	if midItem.TargetText == nil || *midItem.TargetText != "译文三" {
		t.Fatalf("target_text=%v want 译文三", midItem.TargetText)
	}
	if len(midItem.QualityIssues) != 1 || midItem.QualityIssues[0].Code != "duplicate" {
		t.Fatalf("quality_issues=%v want one duplicate", midItem.QualityIssues)
	}
	if midItem.Edges.ReviewedBy == nil || midItem.Edges.ReviewedBy.ID != user.ID {
		t.Fatalf("reviewed_by=%v want user %d", midItem.Edges.ReviewedBy, user.ID)
	}
	if page.Items[0].Edges.ReviewedBy != nil || page.Items[2].Edges.ReviewedBy != nil {
		t.Fatalf("only index 3 must carry reviewed_by")
	}
}

// TestListResourceSegmentsSQLPageReviewedBy SQL 直分页路径已移除 WithResource，
// 但必须保留 WithReviewedBy：resource_id 由默认列提供，reviewed_by 边仍需预加载。
func TestListResourceSegmentsSQLPageReviewedBy(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-sqlhydr-user")
	project := createTestProject(t, client, "seg-sqlhydr-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/sqlhydr.txt")

	ids := make([]int, 0, 3)
	for i := 1; i <= 3; i++ {
		row := createTestSegment(t, client, res.ID, i, fmt.Sprintf("src%d", i), nil)
		ids = append(ids, row.ID)
	}
	if _, err := client.Segment.UpdateOneID(ids[1]).SetReviewedByID(user.ID).Save(ctx); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	page, err := svc.ListResourceSegments(ctx, user.ID, project.ID, res.ID, ResourceSegmentListOptions{Limit: 50})
	if err != nil {
		t.Fatalf("ListResourceSegments: %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("items=%d want 3", len(page.Items))
	}
	for i, row := range page.Items {
		if row.ResourceID == nil || *row.ResourceID != res.ID {
			t.Fatalf("items[%d] resource_id=%v want %d", i, row.ResourceID, res.ID)
		}
		if row.Edges.Resource != nil {
			t.Fatalf("items[%d] resource edge must not be eager-loaded", i)
		}
	}
	if page.Items[1].Edges.ReviewedBy == nil || page.Items[1].Edges.ReviewedBy.ID != user.ID {
		t.Fatalf("reviewed_by=%v want user %d", page.Items[1].Edges.ReviewedBy, user.ID)
	}
	if page.Items[0].Edges.ReviewedBy != nil || page.Items[2].Edges.ReviewedBy != nil {
		t.Fatalf("only index 2 must carry reviewed_by")
	}
}

// TestHydrateSegmentsMissingID 锁定回填的不变量：空瘦行切片短路为 nil，
// 缺失 ID 必须返回可 errors.Is 识别的 ErrSegmentHydrationIncomplete，
// 而不是返回长度不匹配或字段残缺的行。
func TestHydrateSegmentsMissingID(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-hydr-miss-user")
	project := createTestProject(t, client, "seg-hydr-miss-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/hydr-miss.txt")
	seg := createTestSegment(t, client, res.ID, 0, "src", nil)
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	baseQ := func() *ent.SegmentQuery {
		return applySegmentFilters(svc.client.Segment.Query(), res.ID, ResourceSegmentListOptions{}, dialect.SQLite)
	}
	filter := segmentMatchFilter(nil, "", "")

	items, err := svc.hydrateSegments(ctx, nil, baseQ, filter)
	if err != nil || items != nil {
		t.Fatalf("empty rows: items=%v err=%v want nil/nil", items, err)
	}
	thin := []*ent.Segment{{ID: seg.ID, SegmentIndex: seg.SegmentIndex}}
	items, err = svc.hydrateSegments(ctx, thin, baseQ, filter)
	if err != nil || len(items) != 1 || items[0].ID != seg.ID {
		t.Fatalf("single id: items=%v err=%v", items, err)
	}
	missing := []*ent.Segment{
		{ID: seg.ID, SegmentIndex: seg.SegmentIndex},
		{ID: 999999, SegmentIndex: 0},
	}
	if _, err := svc.hydrateSegments(ctx, missing, baseQ, filter); !errors.Is(err, ErrSegmentHydrationIncomplete) {
		t.Fatalf("missing id err=%v want ErrSegmentHydrationIncomplete", err)
	}
}

// TestHydrateSegmentsRejectsForeignResourceID 锁定回填的资源范围：thin 行即使
// 携带真实存在的 segment ID，只要它不属于 baseQ 限定的资源，就不得被回填，且
// 必须判为 ErrSegmentHydrationIncomplete，而不是把外资源行塞进页面。
func TestHydrateSegmentsRejectsForeignResourceID(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-hydr-foreign-user")
	project := createTestProject(t, client, "seg-hydr-foreign-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/hydr-foreign.txt")
	other := createTestResource(t, client, project.ID, "chapters/hydr-foreign-other.txt")
	createTestSegment(t, client, res.ID, 0, "src", nil)
	foreign := createTestSegment(t, client, other.ID, 0, "foreign", nil)
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	baseQ := func() *ent.SegmentQuery {
		return applySegmentFilters(svc.client.Segment.Query(), res.ID, ResourceSegmentListOptions{}, dialect.SQLite)
	}
	filter := segmentMatchFilter(nil, "", "")
	thin := []*ent.Segment{{ID: foreign.ID, SegmentIndex: foreign.SegmentIndex}}
	if _, err := svc.hydrateSegments(ctx, thin, baseQ, filter); !errors.Is(err, ErrSegmentHydrationIncomplete) {
		t.Fatalf("foreign resource id err=%v want ErrSegmentHydrationIncomplete", err)
	}
}

// TestHydrateSegmentsRejectsIndexDrift 锁定回填的行身份：完整行的 segment_index
// 与扫描瘦行不一致（并发改写等内部不变量破坏）时必须报 ErrSegmentHydrationIncomplete，
// 不得把漂移行按 ID 顺序塞回页面。
func TestHydrateSegmentsRejectsIndexDrift(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "seg-hydr-drift-user")
	project := createTestProject(t, client, "seg-hydr-drift-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/hydr-drift.txt")
	seg := createTestSegment(t, client, res.ID, 3, "src", nil)
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, 90*24*time.Hour, nil)
	baseQ := func() *ent.SegmentQuery {
		return applySegmentFilters(svc.client.Segment.Query(), res.ID, ResourceSegmentListOptions{}, dialect.SQLite)
	}
	filter := segmentMatchFilter(nil, "", "")
	thin := []*ent.Segment{{ID: seg.ID, SegmentIndex: seg.SegmentIndex + 1}}
	if _, err := svc.hydrateSegments(ctx, thin, baseQ, filter); !errors.Is(err, ErrSegmentHydrationIncomplete) {
		t.Fatalf("index drift err=%v want ErrSegmentHydrationIncomplete", err)
	}
}
