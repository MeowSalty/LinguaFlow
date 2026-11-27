package worker

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func TestMergeSemanticQAIssuesReplacesPriorScan(t *testing.T) {
	existing := []qa.QualityIssue{
		{Code: "source_residual", Message: "rule"},
		{Code: "calque", Message: "old calque"},
		{Code: "naturalness", Message: "old naturalness"},
	}
	fresh := []qa.QualityIssue{{Code: "term_fidelity", Message: "new term issue"}}

	got := mergeSemanticQAIssues(existing, fresh)
	want := []qa.QualityIssue{
		{Code: "source_residual", Message: "rule"},
		{Code: "term_fidelity", Message: "new term issue"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged=%#v want %#v", got, want)
	}
}

func TestMergeSemanticQAIssuesEmptyScanClearsPriorSemanticIssues(t *testing.T) {
	existing := []qa.QualityIssue{
		{Code: "length_ratio", Message: "rule"},
		{Code: "calque", Message: "old calque"},
	}

	got := mergeSemanticQAIssues(existing, []qa.QualityIssue{})
	want := []qa.QualityIssue{{Code: "length_ratio", Message: "rule"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged=%#v want %#v", got, want)
	}
}

func TestMergeQualityIssuesByFingerprint(t *testing.T) {
	existing := []qa.QualityIssue{{Code: "source_residual", Span: &qa.Span{MatchedText: "x"}}}
	fresh := []qa.QualityIssue{
		{Code: "source_residual", Span: &qa.Span{MatchedText: "x"}},
		{Code: qa.CodeDuplicateSourceDivergence},
	}
	got := mergeQualityIssuesByFingerprint(existing, fresh)
	if len(got) != 2 || got[1].Code != qa.CodeDuplicateSourceDivergence {
		t.Fatalf("unexpected merge: %#v", got)
	}
}

func TestPersistDuplicateSourceDivergenceMergesWithoutChangingStatus(t *testing.T) {
	client := newSemanticQATestClient(t)
	ctx := context.Background()
	resource, err := client.Resource.Create().
		SetPath("sample.srt").
		SetFormat("srt").
		SetStoragePath("sample.srt").
		Save(ctx)
	if err != nil {
		t.Fatalf("create resource: %v", err)
	}
	first, err := client.Segment.Create().
		SetResourceID(resource.ID).
		SetSegmentIndex(0).
		SetSourceText("Same source").
		SetTargetText("译文一").
		SetStatus(segment.StatusApproved).
		SetQualityIssues([]qa.QualityIssue{{Code: "source_residual", Severity: qa.SeverityWarning}}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create first segment: %v", err)
	}
	second, err := client.Segment.Create().
		SetResourceID(resource.ID).
		SetSegmentIndex(1).
		SetSourceText("  Same   source ").
		SetTargetText("译文二").
		SetStatus(segment.StatusTranslated).
		Save(ctx)
	if err != nil {
		t.Fatalf("create second segment: %v", err)
	}

	runner := &JobRunner{client: client}
	if err := runner.persistDuplicateSourceDivergence(ctx, resource.ID); err != nil {
		t.Fatalf("persist divergence: %v", err)
	}

	afterFirst, _ := client.Segment.Get(ctx, first.ID)
	afterSecond, _ := client.Segment.Get(ctx, second.ID)
	if afterFirst.Status != segment.StatusApproved || len(afterFirst.QualityIssues) != 1 {
		t.Fatalf("first segment changed unexpectedly: status=%s issues=%#v", afterFirst.Status, afterFirst.QualityIssues)
	}
	if afterSecond.Status != segment.StatusTranslated || len(afterSecond.QualityIssues) != 1 || afterSecond.QualityIssues[0].Code != qa.CodeDuplicateSourceDivergence {
		t.Fatalf("second segment not merged correctly: status=%s issues=%#v", afterSecond.Status, afterSecond.QualityIssues)
	}

	if err := client.Segment.UpdateOneID(first.ID).SetTargetText("译文二").Exec(ctx); err != nil {
		t.Fatalf("make translations consistent: %v", err)
	}
	if err := runner.persistDuplicateSourceDivergence(ctx, resource.ID); err != nil {
		t.Fatalf("replace divergence after translations converge: %v", err)
	}
	afterFirst, _ = client.Segment.Get(ctx, first.ID)
	afterSecond, _ = client.Segment.Get(ctx, second.ID)
	if len(afterFirst.QualityIssues) != 1 || afterFirst.QualityIssues[0].Code != "source_residual" {
		t.Fatalf("unrelated issue changed unexpectedly: %#v", afterFirst.QualityIssues)
	}
	if len(afterSecond.QualityIssues) != 0 {
		t.Fatalf("stale divergence issue was not cleared: %#v", afterSecond.QualityIssues)
	}
}

// newSemanticQATestClient 创建内存 SQLite ent 客户端并自动迁移。
func newSemanticQATestClient(t *testing.T) *ent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	driver := database.NewDriver(entsql.OpenDB(dialect.SQLite, db))
	client := ent.NewClient(ent.Driver(driver))
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = db.Close()
	})
	return client
}

// TestPersistDuplicateSourceDivergence_ReconcilePreservesDismissed 验证文档级
// dup-divergence 重算（persistDuplicateSourceDivergence → replaceQualityIssuesByCode
// + qa.ReconcileIssues）时：
//   - 非 dup-divergence 的既有裁决（source_residual，dismissed）被保留；
//   - 被重算结果取代的失效 dup-divergence issue（此处单段落资源计算不出 dup 问题）
//     被清除。
func TestPersistDuplicateSourceDivergence_ReconcilePreservesDismissed(t *testing.T) {
	client := newSemanticQATestClient(t)
	ctx := context.Background()

	resource, err := client.Resource.Create().
		SetPath("dup.srt").
		SetFormat("srt").
		SetStoragePath("dup.srt").
		Save(ctx)
	if err != nil {
		t.Fatalf("create resource: %v", err)
	}

	decidedAt := time.Now().UTC()
	var decidedBy = 7
	seg, err := client.Segment.Create().
		SetResourceID(resource.ID).
		SetSegmentIndex(0).
		SetSourceText("source").
		SetTargetText("译文").
		SetStatus(segment.StatusTranslated).
		SetQualityIssues([]qa.QualityIssue{
			{Code: qa.CheckSourceResidual, Severity: qa.SeverityWarning,
				Disposition: qa.DispositionDismissed, DecidedBy: &decidedBy, DecidedAt: &decidedAt, Note: "已裁决"},
			{Code: qa.CodeDuplicateSourceDivergence, Severity: qa.SeverityWarning, Span: &qa.Span{MatchedText: "译文"}},
		}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create segment: %v", err)
	}

	runner := &JobRunner{client: client}
	if err := runner.persistDuplicateSourceDivergence(ctx, resource.ID); err != nil {
		t.Fatalf("persistDuplicateSourceDivergence: %v", err)
	}

	after, err := client.Segment.Get(ctx, seg.ID)
	if err != nil {
		t.Fatalf("reload segment: %v", err)
	}
	if len(after.QualityIssues) != 1 {
		t.Fatalf("quality_issues=%#v want only source_residual", after.QualityIssues)
	}
	kept := after.QualityIssues[0]
	if kept.Code != qa.CheckSourceResidual {
		t.Fatalf("kept issue code=%q want %q", kept.Code, qa.CheckSourceResidual)
	}
	if !kept.Dismissed() || kept.DecidedBy == nil || *kept.DecidedBy != decidedBy || kept.Note != "已裁决" {
		t.Fatalf("dismissed source_residual lost through reconcile: %#v", kept)
	}
	for _, iss := range after.QualityIssues {
		if iss.Code == qa.CodeDuplicateSourceDivergence {
			t.Fatalf("stale dup-divergence not cleared: %#v", after.QualityIssues)
		}
	}
}
