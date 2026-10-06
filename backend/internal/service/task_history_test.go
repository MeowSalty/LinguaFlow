package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/usagerecord"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

var historyTestNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

type taskHistoryFixture struct {
	c        *ent.Client
	s        *TaskHistoryService
	owner, p int
}

func newHistoryFixture(t *testing.T) taskHistoryFixture {
	t.Helper()
	return historyFixtureWithClient(t, testClient(t))
}

func historyFixtureWithClient(t *testing.T, c *ent.Client) taskHistoryFixture {
	t.Helper()
	ctx := context.Background()
	u := createTestUser(t, c, "history-owner")
	p := createTestProject(t, c, "history-project", u.ID)
	c.SystemSetting.Create().SetKey(SettingTaskRetention).SetValue(`{"enabled":false,"retention_days":30,"revision":1}`).SaveX(ctx)
	s := NewTaskHistoryService(c, NewProjectService(c, NewUserService(c, nil)), &tasklife.Coordinator{}, event.NewBroker(event.NewRingBufferStore(event.RingBufferConfig{Capacity: 32})))
	s.now = func() time.Time { return historyTestNow }
	s.SetReady(true)
	s.SetLogger(discardLogger())
	return taskHistoryFixture{c, s, u.ID, p.ID}
}

func (f taskHistoryFixture) job(t *testing.T, status string, days int) *ent.Job {
	t.Helper()
	q := f.c.Job.Create().SetProjectID(f.p).SetExecutionPlanID(1).SetStatus(status)
	if days >= 0 {
		anchor := historyTestNow.Add(-time.Duration(days) * 24 * time.Hour)
		q.SetFinishedAt(anchor).SetRetentionAnchorAt(anchor)
	}
	return q.SaveX(context.Background())
}
func (f taskHistoryFixture) target(j *ent.Job) HistoryTarget {
	return HistoryTarget{Kind: OperationTranslation, ID: strconv.Itoa(j.ID), ProjectID: f.p}
}
func (f taskHistoryFixture) policy(t *testing.T, enabled bool, days int, revision int64) {
	t.Helper()
	raw, err := json.Marshal(TaskRetentionPolicy{enabled, days, revision})
	if err != nil {
		t.Fatal(err)
	}
	f.c.SystemSetting.Update().Where(systemsetting.KeyEQ(SettingTaskRetention)).SetValue(string(raw)).ExecX(context.Background())
}

func TestTaskHistoryDeletesDependenciesAndPreservesResults(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	j := f.job(t, "completed", 0)
	res := createTestResource(t, f.c, f.p, "result.txt")
	seg := f.c.Segment.Create().SetResourceID(res.ID).SetSegmentIndex(0).SetSourceText("original").SetTargetText("translation").SetStatus(segment.StatusTranslated).SaveX(ctx)
	jr := f.c.JobResource.Create().SetJobID(j.ID).SetResourceID(res.ID).SetStatus("completed").SetOutputPath("must-not-delete.txt").SaveX(ctx)
	round := f.c.JobRound.Create().SetJobID(j.ID).SetJobResourceID(jr.ID).SetRoundIndex(0).SetMode("translate").SetStatus("completed").SaveX(ctx)
	f.c.JobRoundSegment.Create().SetJobRoundID(round.ID).SetSegmentID(seg.ID).SaveX(ctx)
	credential := f.c.Credential.Create().SetScope(ScopeUser).SetOwnerID(f.owner).SetProvider("openai").SetEndpoint("https://api.openai.com/v1/").SetCurrentVersion(1).SaveX(ctx)
	version := f.c.CredentialVersion.Create().SetCredentialID(credential.ID).SetVersion(1).SetKeyID("test-key").SetNonce([]byte("test-nonce")).SetCiphertext([]byte("test-ciphertext")).SaveX(ctx)
	f.c.CredentialJobReference.Create().SetJobID(j.ID).SetCredentialVersionID(version.ID).SaveX(ctx)
	memory, err := tm.NewSQLite(f.c, tm.Scope{ProjectID: f.p})
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.Add(ctx, "original", "translation", "en", "zh"); err != nil {
		t.Fatal(err)
	}
	memoryBefore := f.c.TMEntry.Query().OnlyX(ctx)
	f.c.SSEEvent.Create().SetJobID(j.ID).SetSeq(1).SetType("batch").SetLevel("info").SetMessage("diagnostic").SetMetadata(map[string]any{"request": "private"}).SaveX(ctx)
	f.c.UsageRecord.Create().SetProjectID(f.p).SetUserID(f.owner).SetVisibilityScope(usagerecord.VisibilityScopeProject).SetInputTokens(42).SetAPICalls(3).SaveX(ctx)
	f.c.ActivityLog.Create().SetProjectID(f.p).SetActorID(f.owner).SetVisibilityScope(activitylog.VisibilityScopeProject).SetAction("job.create").SetResourceType("job").SetResourceID(j.ID).SaveX(ctx)
	ch := f.s.broker.Subscribe(j.ID)
	f.s.broker.Publish(j.ID, event.Event{Type: "test", CreatedAt: historyTestNow})
	if err := f.s.Delete(ctx, f.owner, f.target(j)); err != nil {
		t.Fatal(err)
	}
	if f.c.Job.Query().ExistX(ctx) || f.c.JobResource.Query().ExistX(ctx) || f.c.JobRound.Query().ExistX(ctx) || f.c.JobRoundSegment.Query().ExistX(ctx) || f.c.SSEEvent.Query().ExistX(ctx) || f.c.CredentialJobReference.Query().ExistX(ctx) {
		t.Fatal("history dependencies remain")
	}
	credentialAfter := f.c.Credential.GetX(ctx, credential.ID)
	if credentialAfter.Scope != credential.Scope || credentialAfter.OwnerID != credential.OwnerID || credentialAfter.Provider != credential.Provider || credentialAfter.Endpoint != credential.Endpoint || credentialAfter.CurrentVersion != credential.CurrentVersion || !credentialAfter.UpdatedAt.Equal(credential.UpdatedAt) {
		t.Fatal("credential changed during history deletion")
	}
	versionAfter := f.c.CredentialVersion.GetX(ctx, version.ID)
	if versionAfter.CredentialID != version.CredentialID || versionAfter.Version != version.Version || versionAfter.EncryptionVersion != version.EncryptionVersion || versionAfter.KeyID != version.KeyID || versionAfter.Revoked != version.Revoked || !bytes.Equal(versionAfter.Nonce, version.Nonce) || !bytes.Equal(versionAfter.Ciphertext, version.Ciphertext) || !versionAfter.UpdatedAt.Equal(version.UpdatedAt) {
		t.Fatal("credential version changed during history deletion")
	}
	memoryAfter := f.c.TMEntry.Query().OnlyX(ctx)
	if memoryAfter.ID != memoryBefore.ID || memoryAfter.ProjectID == nil || *memoryAfter.ProjectID != f.p || memoryAfter.ScopeKey != memoryBefore.ScopeKey || memoryAfter.SourceHash != memoryBefore.SourceHash || memoryAfter.SourceText != memoryBefore.SourceText || memoryAfter.TargetText != memoryBefore.TargetText || memoryAfter.SourceLang != memoryBefore.SourceLang || memoryAfter.TargetLang != memoryBefore.TargetLang || memoryAfter.UsageCount != memoryBefore.UsageCount || !memoryAfter.UpdatedAt.Equal(memoryBefore.UpdatedAt) {
		t.Fatal("project translation memory changed during history deletion")
	}
	if got := f.c.Segment.GetX(ctx, seg.ID); got.SourceText != "original" || got.TargetText == nil || *got.TargetText != "translation" {
		t.Fatal("project contents changed")
	}
	if !f.c.Resource.Query().ExistX(ctx) || f.c.UsageRecord.Query().CountX(ctx) != 1 || f.c.ActivityLog.Query().CountX(ctx) != 2 {
		t.Fatal("project facts were deleted")
	}
	log := f.c.ActivityLog.Query().Where(activitylog.ActionEQ("job.history_deleted")).OnlyX(ctx)
	if log.Metadata["source"] != "manual" || log.Metadata["previous_status"] != "completed" || log.VisibilityScope != activitylog.VisibilityScopeProject {
		t.Fatalf("audit=%+v", log)
	}
	if events, err := f.s.broker.Replay(ctx, j.ID, 0, 100); err != nil || len(events) != 0 {
		t.Fatal("memory history remains")
	}
	for range ch {
	}
	f.s.broker.Unsubscribe(j.ID, ch)
	if err := f.s.Delete(ctx, f.owner, f.target(j)); !errors.Is(err, ErrTaskHistoryNotFound) {
		t.Fatalf("repeat delete=%v", err)
	}
}

func TestTaskHistoryAuthorizationStatusAndClaims(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	outsider := createTestUser(t, f.c, "history-outsider")
	f.c.User.UpdateOneID(outsider.ID).SetRole(SystemRoleAdmin).ExecX(ctx)
	for _, status := range []string{"pending", "running", "paused", "completed", "failed", "cancelled"} {
		j := f.job(t, status, -1)
		target := f.target(j)
		if err := f.s.Delete(ctx, outsider.ID, target); !errors.Is(err, ErrForbidden) {
			t.Fatalf("system admin gained project delete: %v", err)
		}
		if !historyTerminal(status) {
			if err := f.s.Delete(ctx, f.owner, target); !errors.Is(err, ErrTaskNotTerminal) {
				t.Fatalf("status %s: %v", status, err)
			}
			continue
		}
		guard, err := f.s.lifecycle.Lock(ctx, OperationTranslation, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		release := guard.Claim()
		guard.Release()
		if err = f.s.Delete(ctx, f.owner, target); !errors.Is(err, tasklife.ErrBusy) {
			t.Fatalf("active claim=%v", err)
		}
		if f.s.CanDelete(ctx, f.owner, target.Kind, j.ID, f.p, status) {
			t.Fatal("claimed task projected deletable")
		}
		release()
		if err = f.s.Delete(ctx, f.owner, target); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTaskHistoryDeletionRollsBackDependenciesWithAudit(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	j := f.job(t, "failed", 50)
	f.c.SSEEvent.Create().SetJobID(j.ID).SetSeq(1).SetType("error").SetLevel("error").SetMessage("keep on rollback").SaveX(ctx)
	failure := errors.New("audit failure")
	f.c.ActivityLog.Use(func(ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) { return nil, failure })
	})
	if err := f.s.Delete(ctx, f.owner, f.target(j)); !errors.Is(err, failure) {
		t.Fatalf("delete=%v", err)
	}
	if !f.c.Job.Query().ExistX(ctx) || f.c.SSEEvent.Query().CountX(ctx) != 1 || f.c.ActivityLog.Query().CountX(ctx) != 0 {
		t.Fatal("partial deletion committed")
	}
}

func TestTaskHistorySyncDeletionPreservesGlossary(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	entry := f.c.GlossaryEntry.Create().SetProjectID(f.p).SetSourceKey("term").SetSource("term").SetTarget("translation").SaveX(ctx)
	task := f.c.SyncTask.Create().SetProjectID(f.p).SetEntryID(entry.ID).SetActorUserID(f.owner).SetOldTarget("old").SetNewTarget("new").SetTotalSegments(0).SetSegmentIds("[]").SetResourceIds("[]").SetStatus("cancelled").SaveX(ctx)
	target := HistoryTarget{OperationGlossarySync, strconv.Itoa(task.ID), f.p}
	wrong := target
	wrong.ProjectID++
	if err := f.s.Delete(ctx, f.owner, wrong); !errors.Is(err, ErrTaskHistoryNotFound) {
		t.Fatalf("wrong project=%v", err)
	}
	if err := f.s.Delete(ctx, f.owner, target); err != nil {
		t.Fatal(err)
	}
	if !f.c.GlossaryEntry.Query().ExistX(ctx) || f.c.SyncTask.Query().ExistX(ctx) {
		t.Fatal("wrong sync deletion scope")
	}
}

func TestTaskHistoryPreviewPartitionsAndUnknownCounts(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	f.job(t, "running", -1)
	f.job(t, "completed", -1)
	f.job(t, "completed", 3)
	legacy := f.job(t, "cancelled", 40)
	f.c.Job.UpdateOneID(legacy.ID).ClearFinishedAt().ExecX(ctx)
	busy := f.job(t, "failed", 35)
	g, _ := f.s.lifecycle.Lock(ctx, OperationTranslation, busy.ID)
	release := g.Claim()
	g.Release()
	defer release()
	before := f.c.Job.GetX(ctx, legacy.ID).UpdatedAt
	preview, err := f.s.Preview(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	p := preview.ByType[OperationTranslation]
	if preview.Partial || *p.Active != 1 || *p.Terminal != 4 || *p.MissingAnchor != 1 || *p.NotExpired != 1 || *p.Expired.Busy != 1 || *p.Expired.Deletable != 1 || *p.TimingSources.LegacyAnchor != 1 {
		t.Fatalf("preview=%+v %+v", preview, p)
	}
	if !f.c.Job.GetX(ctx, legacy.ID).UpdatedAt.Equal(before) || f.c.Job.GetX(ctx, 2).RetentionAnchorAt != nil {
		t.Fatal("preview mutated data")
	}
	f.s.previewLimit = 1
	preview, err = f.s.Preview(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Partial || preview.ByType[OperationTranslation].Expired.Deletable != nil || preview.ByType[OperationTranslation].Terminal == nil {
		t.Fatal("partial preview reported sample as a total")
	}
}

func TestTaskHistoryLegacyAnchorsAndDisabledScan(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	legacy := f.job(t, "completed", -1)
	active := f.job(t, "paused", -1)
	before := legacy.UpdatedAt
	if err := f.s.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	first := f.c.Job.GetX(ctx, legacy.ID)
	if first.FinishedAt != nil || first.RetentionAnchorAt == nil || !first.RetentionAnchorAt.Equal(historyTestNow) || !first.UpdatedAt.Equal(before) {
		t.Fatalf("legacy=%+v", first)
	}
	if f.c.Job.GetX(ctx, active.ID).RetentionAnchorAt != nil {
		t.Fatal("active task received legacy anchor")
	}
	f.s.now = func() time.Time { return historyTestNow.Add(60 * 24 * time.Hour) }
	if err := f.s.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if !f.c.Job.GetX(ctx, legacy.ID).RetentionAnchorAt.Equal(*first.RetentionAnchorAt) {
		t.Fatal("legacy timer reset")
	}
	state, err := f.s.Status(ctx)
	if err != nil || state.State != "disabled" || state.LastScan != nil {
		t.Fatalf("disabled status=%+v %v", state, err)
	}
	f.policy(t, true, 30, 2)
	if err = f.s.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if f.c.Job.Query().Where(job.IDEQ(legacy.ID)).ExistX(ctx) {
		t.Fatal("enabled retention did not use original anchor")
	}
}

func TestTaskHistoryScannerFiniteDomainRechecksBusy(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	f.policy(t, true, 30, 1)
	f.s.scanLimit = 1
	first := f.job(t, "completed", 50)
	second := f.job(t, "completed", 40)
	g, _ := f.s.lifecycle.Lock(ctx, OperationTranslation, first.ID)
	release := g.Claim()
	g.Release()
	if err := f.s.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	newer := f.job(t, "completed", 35)
	release()
	if err := f.s.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if f.c.Job.Query().Where(job.IDEQ(second.ID)).ExistX(ctx) || !f.c.Job.Query().Where(job.IDEQ(newer.ID)).ExistX(ctx) {
		t.Fatal("continuation did not preserve ID upper bound")
	}
	if err := f.s.Scan(ctx); err != nil {
		t.Fatal(err)
	} // completes the original finite domain
	if err := f.s.Scan(ctx); err != nil {
		t.Fatal(err)
	} // new traversal revisits oldest busy task
	if f.c.Job.Query().Where(job.IDEQ(first.ID)).ExistX(ctx) {
		t.Fatal("busy task starved behind growing tail")
	}
	log := f.c.ActivityLog.Query().Where(activitylog.ResourceIDEQ(first.ID)).OnlyX(ctx)
	if log.QueryActor().ExistX(ctx) || log.Metadata["source"] != "retention" || log.Metadata["scan_id"] == nil || log.Metadata["policy_revision"] == nil {
		t.Fatalf("automatic audit=%+v", log)
	}
}

func TestTaskHistoryBatchBudgetAndMaintenance(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	first := f.job(t, "completed", 0)
	second := f.job(t, "failed", 0)
	f.s.SetMaintenance(func() bool { return true })
	if err := f.s.Delete(ctx, f.owner, f.target(first)); !errors.Is(err, ErrStorageMaintenance) {
		t.Fatalf("maintenance=%v", err)
	}
	preview, err := f.s.Preview(ctx, 1)
	if err != nil || preview.Partial {
		t.Fatalf("maintenance preview=%v", err)
	}
	f.s.SetMaintenance(nil)
	f.s.batchBudget = 0
	results := f.s.BatchDelete(ctx, f.owner, []HistoryTarget{f.target(first), f.target(first), f.target(second)})
	if len(results) != 2 || results[0].Status != "deferred" || results[1].Status != "deferred" || f.c.Job.Query().CountX(ctx) != 2 {
		t.Fatalf("budget results=%+v", results)
	}
}

func TestTaskHistoryBatchLockWaitConsumesBudget(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	first, second := f.job(t, "completed", 0), f.job(t, "failed", 0)
	guard, err := f.s.lifecycle.Lock(ctx, OperationTranslation, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	f.s.batchBudget = 25 * time.Millisecond
	started := time.Now()
	results := f.s.BatchDelete(ctx, f.owner, []HistoryTarget{f.target(first), f.target(second)})
	t.Logf("batch lifecycle lock wait including cancellation: %s", time.Since(started))
	if len(results) != 2 || results[0].Status != "deferred" || results[1].Status != "deferred" || f.c.Job.Query().CountX(ctx) != 2 {
		t.Fatalf("lock wait budget results=%+v", results)
	}
}

func TestTaskHistoryCleanupDeadlineRollsBack(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	j := f.job(t, "completed", 40)
	f.c.SSEEvent.Create().SetJobID(j.ID).SetSeq(1).SetType("batch").SetLevel("info").SetMessage("keep").SaveX(ctx)
	f.s.deleteBudget = 25 * time.Millisecond
	f.c.Job.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if m.Op().Is(ent.OpDeleteOne) {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return next.Mutate(ctx, m)
		})
	})
	started := time.Now()
	if err := f.s.Delete(ctx, f.owner, f.target(j)); !errors.Is(err, ErrTaskCleanupDeferred) {
		t.Fatalf("deadline=%v", err)
	}
	t.Logf("deadline including rollback: %s", time.Since(started))
	if f.c.Job.Query().CountX(ctx) != 1 || f.c.SSEEvent.Query().CountX(ctx) != 1 || f.c.ActivityLog.Query().CountX(ctx) != 0 {
		t.Fatal("timed-out cleanup left partial history")
	}
}

func TestTaskHistoryScannerBacklogSurvivesContinuation(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	f.policy(t, true, 30, 1)
	f.s.scanLimit = 1
	j := f.job(t, "completed", 40)
	g, _ := f.s.lifecycle.Lock(ctx, OperationTranslation, j.ID)
	release := g.Claim()
	g.Release()
	defer release()
	if err := f.s.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := f.s.Status(ctx)
	if err != nil || state.Backlog == nil || !*state.Backlog || state.LastScan == nil || !state.LastScan.Completed {
		t.Fatalf("backlog lost on last empty continuation: %+v %v", state, err)
	}
}

func TestTaskHistoryLegacyMaintenanceDoesNotStarveDeletion(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	f.policy(t, true, 30, 1)
	for i := 0; i < 120; i++ {
		j := f.job(t, "completed", -1)
		g, _ := f.s.lifecycle.Lock(ctx, OperationTranslation, j.ID)
		release := g.Claim()
		g.Release()
		defer release()
	}
	expired := f.job(t, "completed", 40)
	if err := f.s.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if f.c.Job.Query().Where(job.IDEQ(expired.ID)).ExistX(ctx) {
		t.Fatal("legacy maintenance starved anchored history")
	}
	state, err := f.s.Status(ctx)
	if err != nil || state.LastScan.MissingAnchorCount == nil || *state.LastScan.MissingAnchorCount != 120 {
		t.Fatalf("missing anchors not reported: %+v %v", state, err)
	}
}

func TestTaskHistoryStoppedPreparationCannotReopenDeletion(t *testing.T) {
	f := newHistoryFixture(t)
	f.s.Stop()
	if err := f.s.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.s.Ready() {
		t.Fatal("recovery reopened a stopping server")
	}
}

// This synthetic upper fixture exercises payload and checkpoint costs together;
// it is not a measurement of the user's deployment database or physical reclaim.
func TestTaskHistoryLargeAtomicDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("large retention fixture")
	}
	ctx := context.Background()
	filename := filepath.Join(t.TempDir(), "history.sqlite")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filename)+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close(); _ = db.Close() })
	if err = client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	f := historyFixtureWithClient(t, client)
	j := f.job(t, "completed", 40)
	res := createTestResource(t, f.c, f.p, "large-history.txt")
	jr := f.c.JobResource.Create().SetJobID(j.ID).SetResourceID(res.ID).SetStatus("completed").SaveX(ctx)
	segments := make([]int, 100)
	for i := range segments {
		segments[i] = f.c.Segment.Create().SetResourceID(res.ID).SetSegmentIndex(i).SetSourceText("source").SaveX(ctx).ID
	}
	for round := 0; round < 100; round++ {
		r := f.c.JobRound.Create().SetJobID(j.ID).SetJobResourceID(jr.ID).SetRoundIndex(round).SetMode("translate").SetStatus("completed").SaveX(ctx)
		rows := make([]*ent.JobRoundSegmentCreate, 0, 100)
		for _, segmentID := range segments {
			rows = append(rows, f.c.JobRoundSegment.Create().SetJobRoundID(r.ID).SetSegmentID(segmentID))
		}
		f.c.JobRoundSegment.CreateBulk(rows...).ExecX(ctx)
	}
	payload := strings.Repeat("x", 20*1024)
	for start := 0; start < 10000; start += 100 {
		rows := make([]*ent.SSEEventCreate, 0, 100)
		for i := start; i < start+100; i++ {
			rows = append(rows, f.c.SSEEvent.Create().SetJobID(j.ID).SetSeq(int64(i+1)).SetType("batch").SetLevel("info").SetMessage("diagnostic").SetMetadata(map[string]any{"response": payload}))
		}
		f.c.SSEEvent.CreateBulk(rows...).ExecX(ctx)
	}
	logSpace := func(label string) {
		var pages, free, pageSize int64
		for pragma, dest := range map[string]*int64{"page_count": &pages, "freelist_count": &free, "page_size": &pageSize} {
			if err = db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(dest); err != nil {
				t.Fatal(err)
			}
		}
		info, err := os.Stat(filename)
		if err != nil {
			t.Fatal(err)
		}
		var walSize int64
		if wal, err := os.Stat(filename + "-wal"); err == nil {
			walSize = wal.Size()
		}
		t.Logf("%s: pages=%d free_pages=%d page_size=%d db_bytes=%d wal_bytes=%d", label, pages, free, pageSize, info.Size(), walSize)
	}
	logSpace("before")
	// Force cancellation after dependency deletion has started, then verify the
	// same large record can be read in full and deleted successfully afterward.
	forceRollback := true
	f.c.Job.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if forceRollback && m.Op().Is(ent.OpDeleteOne) {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return next.Mutate(ctx, m)
		})
	})
	f.s.deleteBudget = 500 * time.Millisecond
	rollbackStarted := time.Now()
	if err := f.s.Delete(ctx, f.owner, f.target(j)); !errors.Is(err, ErrTaskCleanupDeferred) {
		t.Fatalf("large cancellation=%v", err)
	}
	t.Logf("large cancellation including confirmed rollback: %s (500ms budget)", time.Since(rollbackStarted))
	if f.c.Job.Query().CountX(ctx) != 1 || f.c.SSEEvent.Query().CountX(ctx) != 10000 || f.c.JobRoundSegment.Query().CountX(ctx) != 10000 || f.c.ActivityLog.Query().CountX(ctx) != 0 {
		t.Fatal("large cancelled deletion left partial history")
	}
	forceRollback = false
	f.s.deleteBudget = taskCleanupBudget
	started := time.Now()
	if err := f.s.Delete(ctx, f.owner, f.target(j)); err != nil {
		t.Fatal(err)
	}
	t.Logf("atomic SQLite deletion: %s; 10000 events x 20 KiB payload and 10000 round checkpoints", time.Since(started))
	logSpace("after")
	if f.c.Job.Query().ExistX(ctx) || f.c.SSEEvent.Query().ExistX(ctx) || f.c.JobRoundSegment.Query().ExistX(ctx) || f.c.Segment.Query().CountX(ctx) != 100 {
		t.Fatal("large deletion scope incorrect")
	}
}
