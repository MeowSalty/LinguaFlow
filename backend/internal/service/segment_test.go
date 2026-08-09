package service

import (
	"context"
	"strings"
	"testing"

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
			[]string{"jsonb_typeof", "jsonb_array_length", "> 0"}},
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

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite)

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

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite)

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

	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite)
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
		svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite)
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
