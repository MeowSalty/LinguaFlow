package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sourcerevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagemigrationitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func contractFile(key, name, body string) UploadedFile {
	return UploadedFile{IdempotencyKey: key, Filename: name, Path: name, Size: int64(len(body)), Reader: bytes.NewReader([]byte(body))}
}

func TestStorageContractConfirmationRechecksCandidateBytes(t *testing.T) {
	ctx, c, s, p, u, d := storageLifecycleFixture(t)
	res := storageUpload(t, ctx, s, p, u, "one.txt", "one")
	preview, e := s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("preview", "one.txt", "new"))
	if e != nil {
		t.Fatal(e)
	}
	w := c.StorageWrite.Query().Where(storagewrite.TaskIDEQ(preview.TaskID)).OnlyX(ctx)
	d.mu.Lock()
	d.objects[w.ObjectKey] = []byte("BAD")
	d.mu.Unlock()
	if _, _, e = s.CommitSourceUpdate(ctx, u.ID, p.ID, res.ID, preview.TaskID, preview.SourceGeneration, preview.TranslationGeneration); !errors.Is(e, storage.ErrCorrupt) {
		t.Fatalf("corrupt candidate published: %v", e)
	}
	if *c.Resource.GetX(ctx, res.ID).CurrentSourceRevisionID != *res.CurrentSourceRevisionID {
		t.Fatal("bad confirmation changed current source")
	}
}

func TestStorageContractBatchFailureIsFixedAndNotRetryable(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	files := func() []UploadedFile {
		return []UploadedFile{contractFile("", "bad.json", "{"), contractFile("", "good.txt", "good")}
	}
	first, e := s.UploadResourceBatch(ctx, u.ID, p.ID, "errors", files())
	if e != nil {
		t.Fatal(e)
	}
	if first.Items[0].Action != "failed" || first.Items[1].Action != "created" {
		t.Fatalf("unexpected items: %+v", first.Items)
	}
	if e = s.storage.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	again, e := s.UploadResourceBatch(ctx, u.ID, p.ID, "errors", files())
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(first, again) {
		t.Fatal("fixed failure changed on replay")
	}
	tasks := c.StorageTask.Query().Where(storagetask.KindEQ("upload"), storagetask.PhaseEQ("batch_failed")).AllX(ctx)
	if len(tasks) != 1 {
		t.Fatalf("expected terminal failed item: %d", len(tasks))
	}
	if _, e = s.storage.Retry(ctx, u.ID, p.ID, tasks[0].ID); e == nil {
		t.Fatal("batch failure can publish behind fixed result")
	}
	next, e := s.UploadResourceBatch(ctx, u.ID, p.ID, "corrected", []UploadedFile{contractFile("", "bad.json", `{"value":"good"}`)})
	if e != nil || next.Items[0].Action != "created" {
		t.Fatalf("corrected sub-batch: %v", e)
	}
}

func TestStorageContractDurableLeaseProtectsOtherProcessCleanup(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	task, e := s.storage.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "upload", Path: "one.txt", Size: 3})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.storage.Receive(ctx, u.ID, p.ID, task.ID, bytes.NewBufferString("one"), 3); e != nil {
		t.Fatal(e)
	}
	release, e := s.storage.ClaimTaskExecution(ctx, task.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if _, e = s.storage.ClaimTaskExecution(ctx, task.ID); !errors.Is(e, ErrStorageInProgress) {
		t.Fatalf("second execution claimed: %v", e)
	}
	c.StorageTask.UpdateOneID(task.ID).SetStatus(storagetask.StatusCancelled).ExecX(ctx)
	peer, e := NewStorageService(c, s.projects, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	w := c.StorageWrite.Query().Where(storagewrite.TaskIDEQ(task.ID)).OnlyX(ctx)
	if claimed, e := peer.claimCleanup(ctx, w.ID); e != nil || claimed != nil {
		t.Fatalf("other process cleaned leased write: %v %v", claimed, e)
	}
	release()
	if claimed, e := peer.claimCleanup(ctx, w.ID); e != nil || claimed == nil {
		t.Fatalf("cleanup did not resume after lease: %v %v", claimed, e)
	}
}

func TestStorageContractPreviewFrozenAcrossLaterChanges(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	res := storageUpload(t, ctx, s, p, u, "one.txt", "one\ntwo")
	first, e := s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("preview", "one.txt", "one\nnew"))
	if e != nil {
		t.Fatal(e)
	}
	row := c.Segment.Query().Where(segment.ResourceIDEQ(res.ID)).FirstX(ctx)
	c.Segment.UpdateOne(row).SetTargetText("changed").ExecX(ctx)
	c.Resource.UpdateOneID(res.ID).AddTranslationGeneration(1).ExecX(ctx)
	replay, e := s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("preview", "one.txt", "one\nnew"))
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(first, replay) {
		t.Fatalf("preview changed: %#v %#v", first, replay)
	}
	if _, _, e = s.CommitSourceUpdate(ctx, u.ID, p.ID, res.ID, first.TaskID, first.SourceGeneration, first.TranslationGeneration); !errors.Is(e, ErrSourceRevisionConflict) {
		t.Fatalf("stale commit: %v", e)
	}
	if _, e = s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("preview", "one.txt", "one\nBAD")); !errors.Is(e, ErrStorageIdempotency) {
		t.Fatalf("same-length different bytes: %v", e)
	}
	actions := s.storage.TaskActions(ctx, u.ID, c.StorageTask.GetX(ctx, first.TaskID))
	for _, a := range actions {
		if a == "commit" || a == "retry" {
			t.Fatal("stale confirmation is actionable")
		}
	}
}

func TestStorageContractStatsWhitespaceAndDuplicates(t *testing.T) {
	for _, tc := range []struct {
		name, old, next string
		want            IncrementalUpdateStats
	}{{"whitespace", " one ", "one", IncrementalUpdateStats{Updated: 1}}, {"duplicate", "same\n\nsame", "same\n\nsame", IncrementalUpdateStats{Added: 2, Deleted: 2}}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, c, s, p, u, _ := storageLifecycleFixture(t)
			res := storageUpload(t, ctx, s, p, u, "one.txt", tc.old)
			c.Segment.Update().Where(segment.ResourceIDEQ(res.ID)).SetTargetText("translation").SetStatus(segment.StatusApproved).SetReviewComment("old review").SaveX(ctx)
			preview, e := s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("stats", "one.txt", tc.next))
			if e != nil {
				t.Fatal(e)
			}
			if preview.Stats != tc.want {
				t.Fatalf("stats=%+v want=%+v", preview.Stats, tc.want)
			}
			if _, _, e = s.CommitSourceUpdate(ctx, u.ID, p.ID, res.ID, preview.TaskID, preview.SourceGeneration, preview.TranslationGeneration); e != nil {
				t.Fatal(e)
			}
			for _, row := range c.Segment.Query().Where(segment.ResourceIDEQ(res.ID)).AllX(ctx) {
				if row.TargetText != nil || row.ReviewComment != nil || row.Status != segment.StatusPending {
					t.Fatal("changed or ambiguous source inherited business data")
				}
			}
		})
	}
}

func TestStorageContractPreparationRejectsConcurrentBaselineChange(t *testing.T) {
	ctx, c, s, p, u, d := storageLifecycleFixture(t)
	res := storageUpload(t, ctx, s, p, u, "one.txt", "one")
	d.beforePut = func(string) { c.Resource.UpdateOneID(res.ID).AddTranslationGeneration(1).ExecX(ctx) }
	if _, e := s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("during", "one.txt", "new")); !errors.Is(e, ErrSourceRevisionConflict) {
		t.Fatalf("mixed baseline accepted: %v", e)
	}
	task := c.StorageTask.Query().Where(storagetask.KindEQ("source_update")).OnlyX(ctx)
	if len(task.SourcePlan) > 0 || task.Phase == "prepared" {
		t.Fatal("published confirmation with mismatched baseline")
	}
	if c.Resource.GetX(ctx, res.ID).SourceGeneration != res.SourceGeneration {
		t.Fatal("preview published source")
	}
}

func TestStorageContractReceiveExpiryAndEmptyIntent(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	task, e := s.storage.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "upload", IdempotencyKey: "expired", Path: "one.txt", Size: 3})
	if e != nil {
		t.Fatal(e)
	}
	c.StorageTask.UpdateOneID(task.ID).SetDeadline(time.Now().Add(-time.Hour)).ExecX(ctx)
	if e = s.storage.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if got := c.StorageTask.GetX(ctx, task.ID); got.Status != storagetask.StatusFailed || got.ErrorCode != "storage_intent_expired" || got.CleanupStatus != storagetask.CleanupStatusDone {
		t.Fatalf("empty expired task not finalized: %+v", got)
	}
	task, e = s.storage.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "upload", IdempotencyKey: "prepared-expired", Path: "two.txt", Size: 3})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.storage.Receive(ctx, u.ID, p.ID, task.ID, bytes.NewBufferString("two"), 3); e != nil {
		t.Fatal(e)
	}
	c.StorageTask.UpdateOneID(task.ID).SetDeadline(time.Now().Add(-time.Hour)).ExecX(ctx)
	if _, e = s.storage.Receive(ctx, u.ID, p.ID, task.ID, bytes.NewBufferString("two"), 3); !errors.Is(e, ErrStorageExpired) {
		t.Fatalf("expired prepared content accepted: %v", e)
	}
	if _, e = s.storage.Cancel(ctx, u.ID, p.ID, task.ID); !errors.Is(e, ErrStorageExpired) {
		t.Fatalf("cancel overwrote expiry: %v", e)
	}
}

func TestStorageContractTaskActionsReflectTargetState(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	res := storageUpload(t, ctx, s, p, u, "one.txt", "one")
	preview, e := s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("preview", "one.txt", "new"))
	if e != nil {
		t.Fatal(e)
	}
	c.StorageSpace.UpdateOneID(*p.StorageSpaceID).SetStatus(storagespace.StatusReadOnly).ExecX(ctx)
	actions := s.storage.TaskActions(ctx, u.ID, c.StorageTask.GetX(ctx, preview.TaskID))
	for _, a := range actions {
		if a != "cancel" {
			t.Fatalf("read-only target allowed %s", a)
		}
	}
}

func TestStorageContractBatchRestartsAfterPartialPublish(t *testing.T) {
	ctx, c, s, p, u, d := storageLifecycleFixture(t)
	files := func() []UploadedFile {
		return []UploadedFile{contractFile("", "one.txt", "one"), contractFile("", "two.txt", "two")}
	}
	request, cancel := context.WithCancel(ctx)
	count := 0
	d.beforePut = func(string) {
		count++
		if count == 2 {
			cancel()
		}
	}
	if _, e := s.UploadResourceBatch(request, u.ID, p.ID, "partial", files()); e == nil {
		t.Fatal("fixture failed to interrupt second upload")
	}
	d.beforePut = nil
	if c.Resource.Query().CountX(ctx) != 1 {
		t.Fatal("first item was not durably published")
	}
	if e := s.storage.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	replay, e := s.UploadResourceBatch(ctx, u.ID, p.ID, "partial", files())
	if e != nil {
		t.Fatal(e)
	}
	if len(replay.Items) != 2 || replay.Items[0].Action != "created" || replay.Items[1].Action != "created" || c.Resource.Query().CountX(ctx) != 2 {
		t.Fatalf("partial recovery failed: %#v", replay)
	}
}

func TestStorageContractMigrationCanFillTargetExactly(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	storageUpload(t, ctx, s, p, u, "one.txt", "one")
	target, e := s.storage.InstallSiteSpace(ctx, "target", &connectionTestDriver{objects: map[string][]byte{}})
	if e != nil {
		t.Fatal(e)
	}
	c.StorageSpace.UpdateOneID(target.ID).SetCapacityBytes(target.ReservedBytes + target.CandidateBytes + target.LiveBytes + target.PendingDeleteBytes + 3).ExecX(ctx)
	task, e := s.storage.StartMigration(ctx, u.ID, p.ID, target.ID, p.StorageGeneration, "exact")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.storage.ContinueMigration(ctx, task.ID); e != nil {
		t.Fatal(e)
	}
	if got := c.StorageTask.GetX(ctx, task.ID); got.Status != storagetask.StatusCompleted {
		t.Fatalf("migration stuck at full capacity: %+v", got)
	}
}

func TestStorageContractLegacyBackupFollowsRevisionRetention(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(map[bool]string{false: "extended", true: "backup_pin"}[pinned], func(t *testing.T) {
			ctx, c, s, p, u, _ := storageLifecycleFixture(t)
			res := storageUpload(t, ctx, s, p, u, "one.txt", "one")
			revisionID := *res.CurrentSourceRevisionID
			c.SourceRevision.UpdateOneID(revisionID).SetVerificationState(sourcerevision.VerificationStateLegacyUnverified).ExecX(ctx)
			preview, e := s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("legacy-retain", "one.txt", "new"))
			if e != nil {
				t.Fatal(e)
			}
			if _, _, e = s.CommitSourceUpdate(ctx, u.ID, p.ID, res.ID, preview.TaskID, preview.SourceGeneration, preview.TranslationGeneration); e != nil {
				t.Fatal(e)
			}
			past := time.Now().Add(-time.Hour)
			c.StorageTask.UpdateOneID(preview.TaskID).SetLegacySnapshotExpiresAt(past).ExecX(ctx)
			if pinned {
				c.SourceRevision.UpdateOneID(revisionID).SetRetainUntil(past).ExecX(ctx)
				rev := c.SourceRevision.GetX(ctx, revisionID)
				b := c.Blob.GetX(ctx, rev.SourceBlobID)
				c.BackupPin.Create().SetLocationID(*b.ActiveLocationID).SetBackupID("retained-legacy").SetExpiresAt(time.Now().Add(time.Hour)).SaveX(ctx)
			} else {
				c.SourceRevision.UpdateOneID(revisionID).SetRetainUntil(time.Now().Add(time.Hour)).ExecX(ctx)
			}
			if e = s.storage.Reconcile(ctx); e != nil {
				t.Fatal(e)
			}
			if _, e = s.LegacySnapshot(ctx, u.ID, p.ID, preview.TaskID); e != nil {
				t.Fatalf("retained revision lost backup: %v", e)
			}
			state, e := s.SourcePreviewForTask(ctx, u.ID, p.ID, preview.TaskID)
			if e != nil {
				t.Fatal(e)
			}
			if !state.LegacySnapshotAvailable || state.LegacySnapshotExpiresAt != nil && !state.LegacySnapshotExpiresAt.After(time.Now()) {
				t.Fatal("preview advertised expired backup")
			}
			c.BackupPin.Delete().ExecX(ctx)
			c.SourceRevision.UpdateOneID(revisionID).SetRetainUntil(past).ExecX(ctx)
			if e = s.storage.Reconcile(ctx); e != nil {
				t.Fatal(e)
			}
			if e = s.storage.Reconcile(ctx); e != nil {
				t.Fatal(e)
			}
			if _, e = s.LegacySnapshot(ctx, u.ID, p.ID, preview.TaskID); !errors.Is(e, ErrResourceNotFound) {
				t.Fatalf("retired backup not collected: %v", e)
			}
		})
	}
}

func TestStorageContractCommitReplaysOriginalResult(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	res := storageUpload(t, ctx, s, p, u, "one.txt", "one")
	first, e := s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("p1", "one.txt", "two"))
	if e != nil {
		t.Fatal(e)
	}
	original, stats, e := s.CommitSourceUpdate(ctx, u.ID, p.ID, res.ID, first.TaskID, first.SourceGeneration, first.TranslationGeneration)
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("p2", "one.txt", "new"))
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.CommitSourceUpdate(ctx, u.ID, p.ID, res.ID, second.TaskID, second.SourceGeneration, second.TranslationGeneration); e != nil {
		t.Fatal(e)
	}
	replay, got, e := s.CommitSourceUpdate(ctx, u.ID, p.ID, res.ID, first.TaskID, first.SourceGeneration, first.TranslationGeneration)
	if e != nil {
		t.Fatal(e)
	}
	if *original.CurrentSourceRevisionID != *replay.CurrentSourceRevisionID || original.SourceGeneration != replay.SourceGeneration || !reflect.DeepEqual(stats, got) {
		t.Fatal("replay used current resource instead of original result")
	}
	if *c.Resource.GetX(ctx, res.ID).CurrentSourceRevisionID == *replay.CurrentSourceRevisionID {
		t.Fatal("fixture did not advance")
	}
	if _, _, e = s.CommitSourceUpdate(ctx, u.ID, p.ID, res.ID, first.TaskID, first.SourceGeneration+1, first.TranslationGeneration); !errors.Is(e, ErrSourceRevisionConflict) {
		t.Fatalf("wrong confirmation accepted: %v", e)
	}
	if _, e = s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("p1", "one.txt", "two")); e != nil {
		t.Fatal(e)
	}
}

func TestStorageContractLegacySnapshotAndNoInheritance(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	res := storageUpload(t, ctx, s, p, u, "one.txt", "one")
	c.SourceRevision.UpdateOneID(*res.CurrentSourceRevisionID).SetVerificationState(sourcerevision.VerificationStateLegacyUnverified).ExecX(ctx)
	old := c.Segment.Query().Where(segment.ResourceIDEQ(res.ID)).OnlyX(ctx)
	c.Segment.UpdateOneID(old.ID).SetTargetText("kept in backup").SetReviewComment("review").SetStatus(segment.StatusApproved).ExecX(ctx)
	preview, e := s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("legacy", "one.txt", "one"))
	if e != nil {
		t.Fatal(e)
	}
	if preview.Stats.Added != 1 || preview.Stats.Deleted != 1 || preview.Stats.Unchanged != 0 || !preview.LegacySnapshotAvailable || preview.BaselineTrust != "legacy_unverified" {
		t.Fatalf("unsafe legacy plan: %#v", preview)
	}
	if _, _, e = s.CommitSourceUpdate(ctx, u.ID, p.ID, res.ID, preview.TaskID, preview.SourceGeneration, preview.TranslationGeneration); e != nil {
		t.Fatal(e)
	}
	current := c.Segment.Query().Where(segment.ResourceIDEQ(res.ID)).OnlyX(ctx)
	if current.ID == old.ID || current.TargetText != nil {
		t.Fatal("legacy translation inherited")
	}
	data, e := s.LegacySnapshot(ctx, u.ID, p.ID, preview.TaskID)
	if e != nil {
		t.Fatal(e)
	}
	var saved legacySourceSnapshot
	if e = json.Unmarshal(data, &saved); e != nil {
		t.Fatal(e)
	}
	if len(saved.Segments) != 1 || saved.Segments[0].Target == nil || *saved.Segments[0].Target != "kept in backup" || saved.Segments[0].ReviewComment == nil {
		t.Fatal("backup lost business data")
	}
}

func TestStorageContractContentReplayVerifiesCommittedBytes(t *testing.T) {
	ctx, _, s, p, u, _ := storageLifecycleFixture(t)
	first, e := s.uploadStoredResource(ctx, u.ID, p.ID, contractFile("upload", "one.txt", "one"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.uploadStoredResource(ctx, u.ID, p.ID, contractFile("upload", "one.txt", "two")); !errors.Is(e, ErrStorageIdempotency) {
		t.Fatalf("different bytes accepted: %v", e)
	}
	replay, e := s.uploadStoredResource(ctx, u.ID, p.ID, contractFile("upload", "one.txt", "one"))
	if e != nil || replay.Resource.ID != first.Resource.ID {
		t.Fatalf("replay: %v", e)
	}
	task, e := s.storage.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "upload", IdempotencyKey: "upload", Path: "one.txt", Size: 3})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.storage.Receive(ctx, u.ID, p.ID, task.ID, bytes.NewBufferString("two"), 3); !errors.Is(e, ErrStorageIdempotency) {
		t.Fatalf("content replay accepted wrong bytes: %v", e)
	}
}

func TestStorageContractExpiryAndOldPreviewCannotRevive(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "old_preview"}[legacy], func(t *testing.T) {
			ctx, c, s, p, u, _ := storageLifecycleFixture(t)
			res := storageUpload(t, ctx, s, p, u, "one.txt", "one")
			preview, e := s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("preview", "one.txt", "new"))
			if e != nil {
				t.Fatal(e)
			}
			if legacy {
				c.StorageTask.UpdateOneID(preview.TaskID).SetContractVersion(0).ClearSourcePlan().ExecX(ctx)
			} else {
				c.StorageTask.UpdateOneID(preview.TaskID).SetDeadline(time.Now().Add(-time.Hour)).ExecX(ctx)
			}
			if e = s.storage.Reconcile(ctx); e != nil {
				t.Fatal(e)
			}
			task := c.StorageTask.GetX(ctx, preview.TaskID)
			if task.Status != storagetask.StatusFailed {
				t.Fatalf("not terminal: %+v", task)
			}
			if len(StorageAllowedActions(task)) != 0 {
				t.Fatal("dead candidate has actions")
			}
			if _, e = s.storage.Retry(ctx, u.ID, p.ID, task.ID); e == nil {
				t.Fatal("dead candidate retried")
			}
			if _, _, e = s.CommitSourceUpdate(ctx, u.ID, p.ID, res.ID, task.ID, preview.SourceGeneration, preview.TranslationGeneration); e == nil {
				t.Fatal("dead candidate committed")
			}
			if e = s.storage.Reconcile(ctx); e != nil {
				t.Fatal(e)
			}
			if c.StorageTask.GetX(ctx, task.ID).Status != storagetask.StatusFailed {
				t.Fatal("cleanup resurrected task")
			}
		})
	}
}

func TestStorageContractBatchOrderedManifestAndFixedResult(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	files := func() []UploadedFile {
		return []UploadedFile{contractFile("", "one.txt", "one"), contractFile("", "two.txt", "two")}
	}
	first, e := s.UploadResourceBatch(ctx, u.ID, p.ID, "batch", files())
	if e != nil {
		t.Fatal(e)
	}
	if len(first.Items) != 2 || first.Items[0].Action != "created" || first.Items[1].Action != "created" {
		t.Fatalf("batch: %#v", first)
	}
	c.Resource.UpdateOneID(first.Items[0].Resource.ID).AddSourceGeneration(1).ExecX(ctx)
	again, e := s.UploadResourceBatch(ctx, u.ID, p.ID, "batch", files())
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(first, again) {
		t.Fatal("batch replay changed")
	}
	for _, changed := range [][]UploadedFile{{contractFile("", "two.txt", "two"), contractFile("", "one.txt", "one")}, {contractFile("", "one.txt", "one")}, {contractFile("", "one.txt", "ONE"), contractFile("", "two.txt", "two")}} {
		if _, e = s.UploadResourceBatch(ctx, u.ID, p.ID, "batch", changed); !errors.Is(e, ErrStorageIdempotency) {
			t.Fatalf("manifest mismatch accepted: %v", e)
		}
	}
	if c.Resource.Query().CountX(ctx) != 2 {
		t.Fatal("duplicate publish")
	}
}

func TestStorageContractDrainingRepairUsesFrozenManifest(t *testing.T) {
	ctx, c, s, p, u, d := storageLifecycleFixture(t)
	res := storageUpload(t, ctx, s, p, u, "one.txt", "one")
	target, e := s.storage.InstallSiteSpace(ctx, "target", &connectionTestDriver{objects: map[string][]byte{}})
	if e != nil {
		t.Fatal(e)
	}
	migration, e := s.storage.StartMigration(ctx, u.ID, p.ID, target.ID, p.StorageGeneration, "migration")
	if e != nil {
		t.Fatal(e)
	}
	if c.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(migration.ID)).CountX(ctx) != 1 {
		t.Fatal("manifest not frozen at draining")
	}
	rev := c.SourceRevision.GetX(ctx, *res.CurrentSourceRevisionID)
	b := c.Blob.GetX(ctx, rev.SourceBlobID)
	loc := c.BlobLocation.GetX(ctx, *b.ActiveLocationID)
	d.mu.Lock()
	delete(d.objects, loc.ObjectKey)
	d.mu.Unlock()
	repair, e := s.RepairSource(ctx, u.ID, p.ID, res.ID, rev.ID, target.ID, b.LocationGeneration, contractFile("repair", "one.txt", "one"))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.storage.ContinueMigration(ctx, migration.ID); e != nil {
		t.Fatal(e)
	}
	if c.StorageTask.GetX(ctx, repair.ID).Status != storagetask.StatusCompleted {
		t.Fatal("draining cancelled repair")
	}
	if c.StorageTask.GetX(ctx, migration.ID).Status != storagetask.StatusCompleted {
		t.Fatal("migration failed after repair")
	}
	if c.StorageWrite.Query().Where(storagewrite.TaskIDEQ(repair.ID), storagewrite.PhaseEQ("committed")).CountX(ctx) != 1 {
		t.Fatal("repair object lost")
	}
}
