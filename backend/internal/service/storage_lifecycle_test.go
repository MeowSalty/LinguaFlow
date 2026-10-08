package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/parser/jsonp"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/parser/text"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func storageLifecycleFixture(t *testing.T) (context.Context, *ent.Client, *ResourceService, *ent.Project, *ent.User, *connectionTestDriver) {
	t.Helper()
	ctx := context.Background()
	client := testClient(t)
	user := client.User.Create().SetUsername("files-owner").SetEmail("files@example.test").SetPasswordHash("unused").SaveX(ctx)
	projects := NewProjectService(client, NewUserService(client, nil))
	st, err := NewStorageService(client, projects, t.TempDir())
	if st != nil {
		if initErr := st.EnsureStoragePolicy(context.Background(), true, false, nil, false, nil); initErr != nil {
			t.Fatal(initErr)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	driver := &connectionTestDriver{objects: map[string][]byte{}}
	space, err := st.InstallSiteSpace(ctx, "local", driver)
	if err != nil {
		t.Fatal(err)
	}
	st.Configure(config.DefaultStorageConfig(), "sqlite", space.ID)
	resources := NewResourceService(client, projects, nil)
	resources.SetStorage(st)
	p, err := projects.CreateProject(ctx, user.ID, CreateProjectInput{Name: "files"})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, client, resources, p, user, driver
}
func storageUpload(t *testing.T, ctx context.Context, s *ResourceService, p *ent.Project, u *ent.User, path, content string) *ent.Resource {
	t.Helper()
	out, err := s.uploadStoredResource(ctx, u.ID, p.ID, UploadedFile{Filename: path, Path: path, Size: int64(len(content)), Reader: bytes.NewBufferString(content)})
	if err != nil {
		t.Fatal(err)
	}
	return out.Resource
}

func TestStorageLifecycleRepairPreservesTranslationAndRejectsDifferentBytes(t *testing.T) {
	ctx, client, s, p, u, d := storageLifecycleFixture(t)
	r := storageUpload(t, ctx, s, p, u, "hello.txt", "hello\n")
	row := client.Segment.Query().Where(segment.ResourceIDEQ(r.ID)).OnlyX(ctx)
	row = client.Segment.UpdateOneID(row.ID).SetTargetText("你好").SetStatus(segment.StatusEdited).SaveX(ctx)
	rev := client.SourceRevision.GetX(ctx, *r.CurrentSourceRevisionID)
	b := client.Blob.GetX(ctx, rev.SourceBlobID)
	l := client.BlobLocation.GetX(ctx, *b.ActiveLocationID)
	d.mu.Lock()
	delete(d.objects, l.ObjectKey)
	d.mu.Unlock()
	if _, err := s.OriginalFile(ctx, u.ID, p.ID, r.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing original: %v", err)
	}
	bad := UploadedFile{Filename: "hello.txt", Size: 6, Reader: bytes.NewBufferString("other\n")}
	if _, err := s.RepairSource(ctx, u.ID, p.ID, r.ID, rev.ID, l.SpaceID, b.LocationGeneration, bad); !errors.Is(err, ErrRepairMismatch) {
		t.Fatalf("different repair: %v", err)
	}
	good := UploadedFile{Filename: "hello.txt", Size: 6, Reader: bytes.NewBufferString("hello\n")}
	if _, err := s.RepairSource(ctx, u.ID, p.ID, r.ID, rev.ID, l.SpaceID, b.LocationGeneration, good); err != nil {
		t.Fatal(err)
	}
	after := client.Resource.GetX(ctx, r.ID)
	seg := client.Segment.GetX(ctx, row.ID)
	if *after.CurrentSourceRevisionID != rev.ID || after.SourceGeneration != r.SourceGeneration || after.TranslationGeneration != r.TranslationGeneration || seg.TargetText == nil || *seg.TargetText != "你好" {
		t.Fatal("repair altered source or translation")
	}
	var out bytes.Buffer
	if err := s.RenderTranslatedResource(ctx, u.ID, after, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("你好")) {
		t.Fatal("translation not rendered")
	}
}

func TestStorageLifecyclePreviewConflictAndInvalidReplacementKeepOriginal(t *testing.T) {
	ctx, client, s, p, u, _ := storageLifecycleFixture(t)
	r := storageUpload(t, ctx, s, p, u, "hello.json", `{"a":"hello"}`)
	preview, err := s.PreviewSourceUpdate(ctx, u.ID, p.ID, r.ID, UploadedFile{Size: 13, Reader: bytes.NewBufferString(`{"a":"world"}`)})
	if err != nil {
		t.Fatal(err)
	}
	client.Resource.UpdateOneID(r.ID).AddTranslationGeneration(1).ExecX(ctx)
	if _, _, err = s.CommitSourceUpdate(ctx, u.ID, p.ID, r.ID, preview.TaskID, preview.SourceGeneration, preview.TranslationGeneration); !errors.Is(err, ErrSourceRevisionConflict) {
		t.Fatalf("expected conflict: %v", err)
	}
	if _, err = s.PreviewSourceUpdate(ctx, u.ID, p.ID, r.ID, UploadedFile{Size: 1, Reader: bytes.NewBufferString("{")}); err == nil {
		t.Fatal("malformed replacement accepted")
	}
	f, err := s.OriginalFile(ctx, u.ID, p.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || string(b) != `{"a":"hello"}` {
		t.Fatal("failed replacement destroyed original")
	}
}

func TestStorageLifecyclePreparedUploadResumesWithoutDuplicate(t *testing.T) {
	ctx, client, s, p, u, _ := storageLifecycleFixture(t)
	task, err := s.storage.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "upload", IdempotencyKey: "lost-response", Path: "hello.txt", Size: 6})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.storage.Receive(ctx, u.ID, p.ID, task.ID, bytes.NewBufferString("hello\n"), 6); err != nil {
		t.Fatal(err)
	}
	if err = s.storage.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	task = client.StorageTask.GetX(ctx, task.ID)
	if task.Status != storagetask.StatusCompleted {
		t.Fatalf("not completed: %s %s", task.Status, task.ErrorCode)
	}
	if err = s.storage.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if count := client.Resource.Query().CountX(ctx); count != 1 {
		t.Fatalf("duplicate resources: %d", count)
	}
}

func TestStorageLifecycleFixedExportKeepsCapturedTranslation(t *testing.T) {
	ctx, client, s, p, u, _ := storageLifecycleFixture(t)
	r := storageUpload(t, ctx, s, p, u, "hello.txt", "hello\n")
	row := client.Segment.Query().Where(segment.ResourceIDEQ(r.ID)).OnlyX(ctx)
	client.Segment.UpdateOneID(row.ID).SetTargetText("第一版").SetStatus(segment.StatusEdited).ExecX(ctx)
	task, err := s.CreateExport(ctx, u.ID, p.ID, r.ID, "fixed")
	if err != nil {
		t.Fatal(err)
	}
	client.Segment.UpdateOneID(row.ID).SetTargetText("第二版").ExecX(ctx)
	if err = s.storage.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	task = client.StorageTask.GetX(ctx, task.ID)
	if task.Status != storagetask.StatusCompleted {
		t.Fatalf("export failed: %s %s", task.Status, task.ErrorCode)
	}
	f, _, err := s.DownloadExport(ctx, u.ID, p.ID, *task.ResultArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("第一版")) || bytes.Contains(data, []byte("第二版")) {
		t.Fatalf("export mixed generations: %s", data)
	}
}
