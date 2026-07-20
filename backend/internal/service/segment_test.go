package service

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"

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
		{QualitySeverity: "error", QualityCode: "duplicate"},
	}
	for _, opts := range cases {
		if p := buildQualityPredicate(opts, dialect.SQLite); p == nil {
			t.Fatalf("buildQualityPredicate(%+v) = nil, want predicate", opts)
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
		assertIndexes(t, ResourceSegmentListOptions{QualityIssues: "has", Limit: 50}, []int{2, 3, 4})
	})
	t.Run("none", func(t *testing.T) {
		// NULL and [] both count as none
		assertIndexes(t, ResourceSegmentListOptions{QualityIssues: "none", Limit: 50}, []int{0, 1})
	})
	t.Run("severity_warning", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualitySeverity: "warning", Limit: 50}, []int{2, 4})
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
		}, []int{3, 4})
	})
	t.Run("invalid_ignored", func(t *testing.T) {
		assertIndexes(t, ResourceSegmentListOptions{QualityIssues: "maybe", Limit: 50}, []int{0, 1, 2, 3, 4})
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
