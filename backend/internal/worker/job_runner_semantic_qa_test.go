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

func TestDuplicateSourceDivergenceEnabled(t *testing.T) {
	if !duplicateSourceDivergenceEnabled(qa.Config{Enabled: true}) {
		t.Fatal("nil checks should enable document checks")
	}
	if duplicateSourceDivergenceEnabled(qa.Config{Enabled: true, Checks: []string{qa.CheckUntranslated}}) {
		t.Fatal("explicit checker list should filter document check")
	}
	if !duplicateSourceDivergenceEnabled(qa.Config{Enabled: true, Checks: []string{qa.CodeDuplicateSourceDivergence}}) {
		t.Fatal("explicit document checker should be enabled")
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

func TestPersistReviseSegmentResult_CASAndReconcile(t *testing.T) {
	client := newSemanticQATestClient(t)
	ctx := context.Background()
	decidedBy := 42
	row, err := client.Segment.Create().SetSegmentIndex(301).SetSourceText("source").SetTargetText("旧译文").SetStatus(segment.StatusTranslated).SetQualityIssues([]qa.QualityIssue{
		// 确定性 dismissed（无 Span，指纹与 fresh 相同）：裁决应被继承。
		{Code: qa.CheckUntranslated, Message: "old", Disposition: qa.DispositionDismissed, DecidedBy: &decidedBy},
		// 确定性 pending 且 fresh 未检出：随重算消失。
		{Code: qa.CheckWidthMix, Message: "width", Disposition: qa.DispositionPending},
		// 范围外语义 pending：新契约下由 ReviseFinalIssues 显式保留，
		// 不再被 ReconcileIssues 副作用清掉。
		{Code: qa.IssueCodeGrammar, Message: "语法", Disposition: qa.DispositionPending},
	}).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fresh := []qa.QualityIssue{{Code: qa.CheckUntranslated, Message: "fresh", Severity: qa.SeverityError}}
	targetedCodes := []string{qa.IssueCodeMistranslation}
	updated, err := persistReviseSegmentResult(ctx, client, row, "旧译文", "新译文", fresh, targetedCodes, true)
	if err != nil || updated != 1 {
		t.Fatalf("updated=%d err=%v want one update", updated, err)
	}
	after, err := client.Segment.Get(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *after.TargetText != "新译文" || len(after.QualityIssues) != 2 {
		t.Fatalf("after=%+v issues=%#v", *after.TargetText, after.QualityIssues)
	}
	if after.QualityIssues[0].Code != qa.CheckUntranslated || !after.QualityIssues[0].Dismissed() ||
		after.QualityIssues[0].DecidedBy == nil || *after.QualityIssues[0].DecidedBy != decidedBy ||
		after.QualityIssues[0].Message != "fresh" {
		t.Fatalf("reconciled deterministic issue should inherit dismissed verdict: %#v", after.QualityIssues[0])
	}
	if after.QualityIssues[1].Code != qa.IssueCodeGrammar || !after.QualityIssues[1].IsPending() {
		t.Fatalf("out-of-scope semantic pending must survive: %#v", after.QualityIssues[1])
	}

	updated, err = persistReviseSegmentResult(ctx, client, after, "旧译文", "再次改写", nil, targetedCodes, true)
	if err != nil || updated != 0 {
		t.Fatalf("stale CAS updated=%d err=%v want zero", updated, err)
	}
}

func TestPersistReviseSegmentResult_QANotRunRemovesTargetedPending(t *testing.T) {
	client := newSemanticQATestClient(t)
	ctx := context.Background()
	row, err := client.Segment.Create().SetSegmentIndex(303).SetSourceText("source").SetTargetText("旧译文").SetStatus(segment.StatusTranslated).SetQualityIssues([]qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Disposition: qa.DispositionPending},  // targeted pending → 移除
		{Code: qa.IssueCodeGrammar, Message: "语法", Disposition: qa.DispositionPending}, // 范围外 pending → 保留
	}).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 计划未启用确定性 QA（qaRan=false，fresh=nil）：按声明式契约移除 targeted
	// pending，其余（范围外 pending / dismissed）原样保留；确定性 issue 不重算。
	updated, err := persistReviseSegmentResult(ctx, client, row, "旧译文", "新译文", nil, []string{qa.IssueCodeCalque}, false)
	if err != nil || updated != 1 {
		t.Fatalf("updated=%d err=%v want one update", updated, err)
	}
	after, _ := client.Segment.Get(ctx, row.ID)
	if *after.TargetText != "新译文" || len(after.QualityIssues) != 1 {
		t.Fatalf("after=%+v issues=%#v want only out-of-scope issue", *after.TargetText, after.QualityIssues)
	}
	if after.QualityIssues[0].Code != qa.IssueCodeGrammar || !after.QualityIssues[0].IsPending() {
		t.Fatalf("issues=%#v want preserved out-of-scope pending", after.QualityIssues)
	}
}

func TestPersistReviseSegmentResultSameTextWritesIssues(t *testing.T) {
	client := newSemanticQATestClient(t)
	ctx := context.Background()
	row, err := client.Segment.Create().SetSegmentIndex(302).SetSourceText("source").SetTargetText("同译文").SetStatus(segment.StatusEdited).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := persistReviseSegmentResult(ctx, client, row, "同译文", "同译文", []qa.QualityIssue{{Code: qa.IssueCodeCalque, Severity: qa.SeverityWarning}}, nil, true)
	if err != nil || updated != 1 {
		t.Fatalf("updated=%d err=%v want one update", updated, err)
	}
	after, _ := client.Segment.Get(ctx, row.ID)
	if len(after.QualityIssues) != 1 {
		t.Fatalf("issues=%#v want persisted", after.QualityIssues)
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

// TestPersistSemanticQASegmentIssues_ReconcilePreservesDismissed 验证语义 QA 重扫
// 同译文时对账逻辑保留既有裁决（persistSemanticQASegmentIssues 内部先
// qa.ReconcileIssues 再 mergeSemanticQAIssues）：
//   - existing 中非语义 issue（source_residual，dismissed）不能被
//     mergeSemanticQAIssues 丢弃，也不能被 ReconcileIssues 改动；
//   - existing 中同指纹的语义 issue（calque，dismissed）的裁决被 fresh 同指纹
//     issue 继承：disposition/decided_by/decided_at/note 保留，message 等用新值。
func TestPersistSemanticQASegmentIssues_ReconcilePreservesDismissed(t *testing.T) {
	client := newSemanticQATestClient(t)
	ctx := context.Background()

	decidedAt := time.Now().UTC()
	var decidedBy = 42
	existing := []qa.QualityIssue{
		{SegmentIndex: 0, Severity: qa.SeverityWarning, Code: qa.CheckSourceResidual, Message: "rule",
			Disposition: qa.DispositionDismissed, DecidedBy: &decidedBy, DecidedAt: &decidedAt, Note: "专有名词"},
		{SegmentIndex: 0, Severity: qa.SeverityWarning, Code: qa.IssueCodeCalque, Message: "旧 calque",
			Span: &qa.Span{MatchedText: "hoge"}, Disposition: qa.DispositionDismissed,
			DecidedBy: &decidedBy, DecidedAt: &decidedAt, Note: "保留"},
	}
	created, err := client.Segment.Create().
		SetSegmentIndex(0).
		SetSourceText("テスト").
		SetTargetText("测试").
		SetStatus(segment.StatusTranslated).
		SetQualityIssues(existing).
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

	// fresh 只含与旧 calque 同指纹的语义 issue（pending），模拟重扫该段的新结果。
	fresh := []qa.QualityIssue{
		{SegmentIndex: 0, Severity: qa.SeverityWarning, Code: qa.IssueCodeCalque, Message: "新 calque",
			Span: &qa.Span{MatchedText: "hoge"}},
	}

	updated, err := persistSemanticQASegmentIssues(ctx, client, row, target, fresh)
	if err != nil {
		t.Fatalf("persistSemanticQASegmentIssues: %v", err)
	}
	if updated != 1 {
		t.Fatalf("CAS UPDATE matched %d rows, want 1", updated)
	}

	after, err := client.Segment.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("reload segment: %v", err)
	}
	if len(after.QualityIssues) != 2 {
		t.Fatalf("quality_issues=%#v want exactly [source_residual, calque]", after.QualityIssues)
	}

	var sr, cq *qa.QualityIssue
	for i := range after.QualityIssues {
		switch after.QualityIssues[i].Code {
		case qa.CheckSourceResidual:
			sr = &after.QualityIssues[i]
		case qa.IssueCodeCalque:
			cq = &after.QualityIssues[i]
		}
	}
	if sr == nil || !sr.Dismissed() || sr.DecidedBy == nil || *sr.DecidedBy != decidedBy || sr.Note != "专有名词" {
		t.Fatalf("source_residual dismissed state lost: %#v", sr)
	}
	if cq == nil {
		t.Fatalf("fresh calque got dropped: %#v", after.QualityIssues)
	}
	if cq.Message != "新 calque" {
		t.Fatalf("calque message=%q want fresh value %q", cq.Message, "新 calque")
	}
	if !cq.Dismissed() || cq.DecidedBy == nil || *cq.DecidedBy != decidedBy || cq.DecidedAt == nil || cq.Note != "保留" {
		t.Fatalf("calque disposition not inherited through reconcile: %#v", *cq)
	}
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
