package worker

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"

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

func TestSemanticQACASPersistsIssues(t *testing.T) {
	client := newSemanticQATestClient(t)
	ctx := context.Background()

	created, err := client.Segment.Create().
		SetSegmentIndex(231).
		SetSourceText("テスト").
		SetTargetText("测试").
		SetStatus(segment.StatusTranslated).
		Save(ctx)
	if err != nil {
		t.Fatalf("create segment: %v", err)
	}

	rows, err := client.Segment.Query().Where(segment.IDIn(created.ID)).All(ctx)
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	row := rows[0]
	target := *row.TargetText

	fresh := []qa.QualityIssue{
		{SegmentIndex: 0, Code: "naturalness", Message: "「女中高生」为日语原词照搬", Severity: qa.SeverityWarning},
	}

	// 调用生产 helper（与 batchHandler 同一函数），验证 CAS 真正落库。
	updated, err := persistSemanticQASegmentIssues(ctx, client, row, target, fresh)
	if err != nil {
		t.Fatalf("persistSemanticQASegmentIssues error: %v", err)
	}
	if updated == 0 {
		t.Fatalf("CAS UPDATE matched 0 rows — semantic_qa issues silently dropped")
	}

	after, err := client.Segment.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("reload segment: %v", err)
	}
	if len(after.QualityIssues) != 1 {
		t.Fatalf("quality_issues not persisted: got %#v", after.QualityIssues)
	}
	if after.QualityIssues[0].Code != "naturalness" {
		t.Fatalf("persisted code=%q want naturalness", after.QualityIssues[0].Code)
	}
}

// TestSemanticQACASSkipsStaleTarget 验证移除 UpdatedAtEQ 后，TargetTextEQ 仍
// 提供防覆盖保护：若段落译文在扫描后、写入前被改写，CAS 应不命中（updated=0），
// 且不覆盖已被改写的译文对应的 quality_issues。
func TestSemanticQACASSkipsStaleTarget(t *testing.T) {
	client := newSemanticQATestClient(t)
	ctx := context.Background()

	created, err := client.Segment.Create().
		SetSegmentIndex(232).
		SetSourceText("テスト").
		SetTargetText("旧译文").
		SetStatus(segment.StatusTranslated).
		Save(ctx)
	if err != nil {
		t.Fatalf("create segment: %v", err)
	}

	rows, err := client.Segment.Query().Where(segment.IDIn(created.ID)).All(ctx)
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	row := rows[0]
	scannedTarget := *row.TargetText // 扫描时看到的译文

	// 模拟扫描后、写入前，外部把译文改写为新值。
	if _, err := client.Segment.UpdateOneID(created.ID).SetTargetText("新译文").Save(ctx); err != nil {
		t.Fatalf("rewrite target: %v", err)
	}

	fresh := []qa.QualityIssue{
		{SegmentIndex: 0, Code: "naturalness", Message: "stale", Severity: qa.SeverityWarning},
	}

	// 用扫描时的旧译文做 CAS 比对，应不命中。
	updated, err := persistSemanticQASegmentIssues(ctx, client, row, scannedTarget, fresh)
	if err != nil {
		t.Fatalf("persistSemanticQASegmentIssues error: %v", err)
	}
	if updated != 0 {
		t.Fatalf("CAS should skip stale target, got updated=%d", updated)
	}

	after, err := client.Segment.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("reload segment: %v", err)
	}
	if len(after.QualityIssues) != 0 {
		t.Fatalf("stale CAS should not write issues, got %#v", after.QualityIssues)
	}
}

// newSemanticQATestClient 创建内存 SQLite ent 客户端并自动迁移。
func newSemanticQATestClient(t *testing.T) *ent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	driver := entsql.OpenDB(dialect.SQLite, db)
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
