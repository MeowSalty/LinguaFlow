package service

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
)

// testClient 创建内存 SQLite ent 客户端并自动迁移。
func testClient(t *testing.T) *ent.Client {
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

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// seedGlossaryEntries 在项目中创建术语条目。
func seedGlossaryEntries(t *testing.T, client *ent.Client, projectID int, entries []GlossaryEntryInput) []*ent.GlossaryEntry {
	t.Helper()
	out := make([]*ent.GlossaryEntry, 0, len(entries))
	for _, e := range entries {
		n, err := normalizeGlossaryEntryInput(e)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		created, err := client.GlossaryEntry.Create().
			SetProjectID(projectID).
			SetSourceKey(glossarySourceKey(n.Source)).
			SetSource(n.Source).
			SetTarget(n.Target).
			SetCaseSensitive(n.CaseSensitive).
			SetNotes(n.Notes).
			Save(context.Background())
		if err != nil {
			t.Fatalf("create entry: %v", err)
		}
		out = append(out, created)
	}
	return out
}

// ---- computePruneDiff 测试 ----

func TestComputePruneDiff_AllCategories(t *testing.T) {
	existing := []*ent.GlossaryEntry{
		{ID: 1, Source: "Gemini", Target: "哈基米", Notes: "company"},
		{ID: 2, Source: "API", Target: "接口", Notes: ""},
		{ID: 3, Source: "Common", Target: "普通词", Notes: ""},
		{ID: 4, Source: "Brand", Target: "品牌", Notes: "keep"},
	}
	refined := []prompt.BootstrapEntry{
		{Source: "Gemini", Target: "哈基米", Notes: "company"}, // keep: unchanged
		{Source: "API", Target: "应用程序接口", Notes: "updated"}, // update: target+notes changed
		// Common: not in refined -> delete
		{Source: "Brand", Target: "品牌", Notes: "keep"}, // keep: unchanged
		{Source: "Unknown", Target: "未知", Notes: ""},   // ignore: not in existing
	}
	preview := computePruneDiff(existing, refined)
	if preview.Total != 4 {
		t.Errorf("total = %d, want 4", preview.Total)
	}
	if preview.ToKeep != 2 {
		t.Errorf("to_keep = %d, want 2", preview.ToKeep)
	}
	if preview.ToUpdate != 1 {
		t.Errorf("to_update = %d, want 1", preview.ToUpdate)
	}
	if preview.ToDelete != 1 {
		t.Errorf("to_delete = %d, want 1", preview.ToDelete)
	}
	if len(preview.Suggestions) != 2 {
		t.Fatalf("suggestions = %d, want 2", len(preview.Suggestions))
	}
	// verify update suggestion
	var upd *PruneSuggestion
	var del *PruneSuggestion
	for i := range preview.Suggestions {
		switch preview.Suggestions[i].Action {
		case "update":
			upd = &preview.Suggestions[i]
		case "delete":
			del = &preview.Suggestions[i]
		}
	}
	if upd == nil {
		t.Fatal("missing update suggestion")
	}
	if upd.EntryID != 2 || upd.NewTarget != "应用程序接口" || upd.NewNotes != "updated" {
		t.Errorf("update suggestion mismatch: %+v", upd)
	}
	if del == nil {
		t.Fatal("missing delete suggestion")
	}
	if del.EntryID != 3 || del.OldTarget != "普通词" {
		t.Errorf("delete suggestion mismatch: %+v", del)
	}
}

func TestComputePruneDiff_EmptyRefined(t *testing.T) {
	existing := []*ent.GlossaryEntry{
		{ID: 1, Source: "A", Target: "a", Notes: ""},
		{ID: 2, Source: "B", Target: "b", Notes: ""},
	}
	preview := computePruneDiff(existing, nil)
	if preview.Total != 2 || preview.ToDelete != 2 || preview.ToKeep != 0 || preview.ToUpdate != 0 {
		t.Errorf("unexpected: %+v", preview)
	}
}

func TestComputePruneDiff_EmptyExisting(t *testing.T) {
	preview := computePruneDiff(nil, []prompt.BootstrapEntry{
		{Source: "X", Target: "x", Notes: ""},
	})
	if preview.Total != 0 || preview.ToDelete != 0 || preview.ToKeep != 0 || preview.ToUpdate != 0 {
		t.Errorf("unexpected: %+v", preview)
	}
	if len(preview.Suggestions) != 0 {
		t.Errorf("want 0 suggestions, got %d", len(preview.Suggestions))
	}
}

func TestComputePruneDiff_CaseInsensitiveMatch(t *testing.T) {
	existing := []*ent.GlossaryEntry{
		{ID: 1, Source: "Hello", Target: "你好", Notes: ""},
	}
	refined := []prompt.BootstrapEntry{
		{Source: "hello", Target: "你好", Notes: ""}, // different case, same target -> keep
	}
	preview := computePruneDiff(existing, refined)
	if preview.ToKeep != 1 || preview.ToUpdate != 0 || preview.ToDelete != 0 {
		t.Errorf("case-insensitive match should keep: %+v", preview)
	}
}

// ---- Apply 测试 ----

func newTestPruneService(t *testing.T) (*GlossaryPruneService, *ent.Client) {
	client := testClient(t)
	users := NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client)))
	projects := NewProjectService(client, users)
	svc := NewGlossaryPruneService(client, projects, NewBackendService(client, users, nil), NewPrunePromptTemplateService(client), nil, discardLogger())
	return svc, client
}

func TestApply_DeleteAndUpdate(t *testing.T) {
	svc, client := newTestPruneService(t)
	ctx := context.Background()

	// 创建用户和项目
	u := createTestUser(t, client, "pruneuser")
	p := createTestProject(t, client, "prune-proj", u.ID)

	entries := seedGlossaryEntries(t, client, p.ID, []GlossaryEntryInput{
		{Source: "DeleteMe", Target: "删除我", Notes: ""},
		{Source: "UpdateMe", Target: "旧译", Notes: "旧注"},
		{Source: "KeepMe", Target: "保留", Notes: ""},
	})

	result, err := svc.Apply(ctx, u.ID, p.ID, []PruneChange{
		{EntryID: entries[0].ID, Action: "delete"},
		{EntryID: entries[1].ID, Action: "update", Target: "新译", Notes: "新注"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Deleted != 1 || result.Updated != 1 || result.Failed != 0 {
		t.Errorf("result mismatch: %+v", result)
	}

	// 验证删除
	count, _ := client.GlossaryEntry.Query().Where(glossaryentry.IDEQ(entries[0].ID)).Count(ctx)
	if count != 0 {
		t.Errorf("entry %d should be deleted", entries[0].ID)
	}

	// 验证更新
	updated, _ := client.GlossaryEntry.Get(ctx, entries[1].ID)
	if updated.Target != "新译" || updated.Notes != "新注" {
		t.Errorf("update mismatch: target=%q notes=%q", updated.Target, updated.Notes)
	}
	// source 和 case_sensitive 应保留
	if updated.Source != "UpdateMe" {
		t.Errorf("source should be preserved, got %q", updated.Source)
	}
}

func TestApply_NonexistentEntryFailed(t *testing.T) {
	svc, client := newTestPruneService(t)
	ctx := context.Background()

	u := createTestUser(t, client, "pruneuser2")
	p := createTestProject(t, client, "prune-proj2", u.ID)

	entries := seedGlossaryEntries(t, client, p.ID, []GlossaryEntryInput{
		{Source: "Real", Target: "真", Notes: ""},
	})

	result, err := svc.Apply(ctx, u.ID, p.ID, []PruneChange{
		{EntryID: 999999, Action: "delete"},              // 不存在 -> failed
		{EntryID: 999998, Action: "update", Target: "x"}, // 不存在 -> failed
		{EntryID: entries[0].ID, Action: "delete"},       // 成功
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Deleted != 1 || result.Updated != 0 || result.Failed != 2 {
		t.Errorf("result mismatch: %+v", result)
	}
}

func TestApply_UnknownActionFailed(t *testing.T) {
	svc, client := newTestPruneService(t)
	ctx := context.Background()

	u := createTestUser(t, client, "pruneuser3")
	p := createTestProject(t, client, "prune-proj3", u.ID)

	result, err := svc.Apply(ctx, u.ID, p.ID, []PruneChange{
		{EntryID: 1, Action: "unknown"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Failed != 1 || result.Deleted != 0 || result.Updated != 0 {
		t.Errorf("result mismatch: %+v", result)
	}
}

// ---- Preview 测试 ----

func TestPreview_EmptyGlossary(t *testing.T) {
	svc, client := newTestPruneService(t)
	ctx := context.Background()

	u := createTestUser(t, client, "pruneuser4")
	p := createTestProject(t, client, "prune-proj4", u.ID)

	preview, err := svc.Preview(ctx, u.ID, p.ID, 1, 0)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Suggestions) != 0 || preview.Total != 0 {
		t.Errorf("want empty preview, got %+v", preview)
	}
}

func TestPreview_ContextCanceled(t *testing.T) {
	svc, client := newTestPruneService(t)

	u := createTestUser(t, client, "pruneuser5")
	p := createTestProject(t, client, "prune-proj5", u.ID)
	seedGlossaryEntries(t, client, p.ID, []GlossaryEntryInput{
		{Source: "Term", Target: "术语", Notes: ""},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 已取消

	_, err := svc.Preview(ctx, u.ID, p.ID, 1, 0)
	if err == nil {
		t.Fatal("want error from cancelled context")
	}
	// 应返回项目访问检查的 context.Canceled 或 ErrPruneLLMCallFailed 包装的 context.Canceled
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

// ---- 辅助函数 ----

func createTestUser(t *testing.T, client *ent.Client, username string) *ent.User {
	t.Helper()
	u, err := client.User.Create().
		SetUsername(username).
		SetPasswordHash("$2a$10$dummyhash").
		SetEmail(username + "@test.com").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func createTestProject(t *testing.T, client *ent.Client, name string, ownerID int) *ent.Project {
	t.Helper()
	p, err := client.Project.Create().
		SetName(name).
		SetSourceLang("en").
		SetTargetLang("zh").
		SetOwnerUserID(ownerID).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return p
}

// 确保 headSnippetLocal 编译可用（间接验证）。
var _ = headSnippetLocal

// 确保 strings 被引用（用于 diff 测试中的 string 比较）。
var _ = strings.TrimSpace
