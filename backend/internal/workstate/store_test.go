package workstate

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
)

func fixture(t *testing.T) (context.Context, *ent.Client, *Store, Scope, *ent.Segment) {
	t.Helper()
	ctx := context.Background()
	cfg := config.DefaultServerConfig()
	cfg.DataDir = t.TempDir()
	db, c, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); db.Close() })
	if err := c.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	return seedFixture(t, ctx, c)
}

func seedFixture(t *testing.T, ctx context.Context, c *ent.Client) (context.Context, *ent.Client, *Store, Scope, *ent.Segment) {
	t.Helper()
	p := c.Project.Create().SetName("workstate").SaveX(ctx)
	r := c.Resource.Create().SetProjectID(p.ID).SetPath("work.txt").SetStoragePath("work.txt").SetFormat("txt").SaveX(ctx)
	seg := c.Segment.Create().SetResourceID(r.ID).SetSegmentIndex(0).SetSourceText("source").SaveX(ctx)
	j := c.Job.Create().SetProjectID(p.ID).SetExecutionPlanID(1).SetStatus("running").SaveX(ctx)
	jr := c.JobResource.Create().SetJobID(j.ID).SetResourceID(r.ID).SetStatus("running").SaveX(ctx)
	round := c.JobRound.Create().SetJobID(j.ID).SetJobResourceID(jr.ID).SetRoundIndex(0).SetMode("translate").SetStatus("running").SaveX(ctx)
	scope := Scope{JobID: j.ID, ResourceID: r.ID, JobResourceID: jr.ID, RoundID: round.ID}
	store := NewStore(c)
	if _, err := store.SealRound(ctx, scope, []int{seg.ID}); err != nil {
		t.Fatal(err)
	}
	return ctx, c, store, scope, seg
}

type uncertainDriver struct {
	dialect.Driver
	lose *atomic.Bool
}

func (d uncertainDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return uncertainTx{Tx: tx, lose: d.lose}, nil
}

type uncertainTx struct {
	dialect.Tx
	lose *atomic.Bool
}

func (t uncertainTx) Commit() error {
	if err := t.Tx.Commit(); err != nil {
		return err
	}
	if t.lose.Swap(false) {
		return errors.New("connection lost after commit")
	}
	return nil
}

func TestCommitAcknowledgementLostIsVerifiedWithoutReapplying(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "unknown.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	var lose atomic.Bool
	c := ent.NewClient(ent.Driver(uncertainDriver{Driver: database.NewDriver(entsql.OpenDB(dialect.SQLite, db)), lose: &lose}))
	t.Cleanup(func() { c.Close(); db.Close() })
	if err := c.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, s, scope, seg := seedFixture(t, ctx, c)
	candidate := ready(scope, seg)
	if err := s.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	lose.Store(true)
	result, err := s.Commit(ctx, input(candidate))
	if err != nil || result.Outcome != AlreadyCommitted {
		t.Fatalf("verify=%+v %v", result, err)
	}
	if row := c.Segment.GetX(ctx, seg.ID); row.ContentVersion != 2 || *row.TargetText != "translated" {
		t.Fatal("unknown commit was repeated")
	}
	if c.Job.GetX(ctx, scope.JobID).ProgressCompleted != 1 || c.JobRoundSegment.Query().CountX(ctx) != 1 {
		t.Fatal("unknown commit was recounted")
	}
}

func TestPauseAllowsOnlyAdmittedResultHandoff(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	r := Request{ID: "admitted", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "request", MainAttempt: true}
	if err := s.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	c.Job.UpdateOneID(scope.JobID).SetStatus("pausing").SetPauseRequested(true).ExecX(ctx)
	candidate := ready(scope, seg)
	if err := s.SaveCandidate(ctx, candidate); !errors.Is(err, ErrStopped) {
		t.Fatalf("unattributed handoff=%v", err)
	}
	candidate.ParentRequestID = r.ID
	if err := s.SaveCandidate(ctx, candidate); !errors.Is(err, ErrStopped) {
		t.Fatalf("reserved-only handoff=%v", err)
	}
	if err := s.RecordRequest(ctx, r.ID, RequestResult{State: "received"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	r.ID = "new"
	if err := s.ReserveRequest(ctx, r); !errors.Is(err, ErrStopped) {
		t.Fatalf("paused dispatch=%v", err)
	}
	result, err := s.Commit(ctx, input(candidate))
	if err != nil || result.Outcome != Committed {
		t.Fatalf("admitted commit=%+v %v", result, err)
	}
}

func TestCascadeCalibratesManifestAndProgress(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	candidate := ready(scope, seg)
	if err := s.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(ctx, input(candidate)); err != nil {
		t.Fatal(err)
	}
	err := Transaction(ctx, c, func(tx *ent.Client) error {
		ids, err := LockResourceJobs(ctx, tx, scope.ResourceID)
		if err != nil {
			return err
		}
		if err := BeforeDeleteSegments(ctx, tx, []int{seg.ID}); err != nil {
			return err
		}
		if err := tx.Segment.DeleteOneID(seg.ID).Exec(ctx); err != nil {
			return err
		}
		return CalibrateJobs(ctx, tx, ids)
	})
	if err != nil {
		t.Fatal(err)
	}
	if row := c.Job.GetX(ctx, scope.JobID); row.ProgressTotal != 0 || row.ProgressCompleted != 0 {
		t.Fatalf("cascade progress=%d/%d", row.ProgressCompleted, row.ProgressTotal)
	}
	if c.WorkItem.Query().CountX(ctx) != 0 || c.WorkCandidate.Query().CountX(ctx) != 0 || c.JobRoundSegment.Query().CountX(ctx) != 0 {
		t.Fatal("cascade left orphan work")
	}
}

func ready(scope Scope, seg *ent.Segment) Candidate {
	return Candidate{ID: "candidate-1", Version: 1, Scope: scope, SegmentID: seg.ID, DTOVersion: 1, Mode: "translate", SnapshotDigest: "frozen", BaselineVersion: seg.ContentVersion, BaselineTarget: seg.TargetText, BaselineStatus: string(seg.Status), State: "ready_to_commit", Payload: []byte(`{"version":1}`)}
}
func input(c Candidate) CommitInput {
	return CommitInput{Scope: c.Scope, SegmentID: c.SegmentID, CommitID: "commit-1", CandidateID: c.ID, CandidateVersion: c.Version, BaselineVersion: c.BaselineVersion, BaselineTarget: c.BaselineTarget, BaselineStatus: c.BaselineStatus, Target: "translated", Status: "translated"}
}

func TestAtomicConfirmationAndDuplicateAfterHumanEdit(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	candidate := ready(scope, seg)
	if err := s.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	result, err := s.Commit(ctx, input(candidate))
	if err != nil || result.Outcome != Committed {
		t.Fatalf("commit=%+v err=%v", result, err)
	}
	if got := c.Job.GetX(ctx, scope.JobID); got.ProgressTotal != 1 || got.ProgressCompleted != 1 {
		t.Fatalf("progress=%d/%d", got.ProgressCompleted, got.ProgressTotal)
	}
	if row := c.Segment.GetX(ctx, seg.ID); row.ContentVersion != 2 || row.TargetText == nil || *row.TargetText != "translated" {
		t.Fatalf("segment=%+v", row)
	}
	if c.Resource.GetX(ctx, scope.ResourceID).TranslationGeneration != 1 {
		t.Fatal("translation generation not atomic")
	}
	if row := c.WorkCandidate.Query().Where(workcandidate.IdentityEQ(candidate.ID)).OnlyX(ctx); len(row.Payload) != 0 || row.State != "completed" {
		t.Fatal("completed payload retained")
	}
	c.Segment.UpdateOneID(seg.ID).SetTargetText("human").ExecX(ctx)
	result, err = s.Commit(ctx, input(candidate))
	if err != nil || result.Outcome != AlreadyCommitted {
		t.Fatalf("replay=%+v %v", result, err)
	}
	if *c.Segment.GetX(ctx, seg.ID).TargetText != "human" || c.Job.GetX(ctx, scope.JobID).ProgressCompleted != 1 {
		t.Fatal("replay overwrote a human edit or recounted")
	}
	other := input(candidate)
	other.CommitID = "other"
	result, err = s.Commit(ctx, other)
	if err != nil || result.Outcome != Stale {
		t.Fatalf("other=%+v %v", result, err)
	}
	if c.WorkItem.Query().Where(workitem.SegmentIDEQ(seg.ID)).OnlyX(ctx).State != "resolved" {
		t.Fatal("late candidate downgraded completed work")
	}
}

func TestNoopRejectsABAAndIssueOnlyChanges(t *testing.T) {
	for _, kind := range []string{"aba", "review"} {
		t.Run(kind, func(t *testing.T) {
			ctx, c, s, scope, seg := fixture(t)
			candidate := ready(scope, seg)
			if err := s.SaveCandidate(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			if kind == "aba" {
				c.Segment.UpdateOneID(seg.ID).SetTargetText("intermediate").ExecX(ctx)
				c.Segment.UpdateOneID(seg.ID).ClearTargetText().ExecX(ctx)
			} else {
				c.Segment.UpdateOneID(seg.ID).SetReviewComment("human decision").ExecX(ctx)
			}
			in := input(candidate)
			in.Noop = true
			result, err := s.Commit(ctx, in)
			if err != nil || result.Outcome != Stale {
				t.Fatalf("commit=%+v %v", result, err)
			}
			if c.JobRoundSegment.Query().CountX(ctx) != 0 || c.Job.GetX(ctx, scope.JobID).ProgressCompleted != 0 {
				t.Fatal("stale no-op was counted")
			}
		})
	}
}

func TestCheckpointFailureRollsBackAcceptedContent(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	candidate := ready(scope, seg)
	if err := s.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	fail := true
	injected := errors.New("checkpoint fault")
	c.JobRoundSegment.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if fail {
				return nil, injected
			}
			return next.Mutate(ctx, m)
		})
	})
	result, err := s.Commit(ctx, input(candidate))
	if !errors.Is(err, injected) || result.Outcome != RetryableStorage {
		t.Fatalf("result=%+v %v", result, err)
	}
	if row := c.Segment.GetX(ctx, seg.ID); row.TargetText != nil || row.ContentVersion != 1 {
		t.Fatal("segment write escaped rollback")
	}
	if c.Resource.GetX(ctx, scope.ResourceID).TranslationGeneration != 0 {
		t.Fatal("generation escaped rollback")
	}
	if c.WorkCandidate.Query().Where(workcandidate.IdentityEQ(candidate.ID)).OnlyX(ctx).State != "ready_to_commit" {
		t.Fatal("candidate consumed on rollback")
	}
	fail = false
	if result, err = s.Commit(ctx, input(candidate)); err != nil || result.Outcome != Committed {
		t.Fatalf("storage retry=%+v %v", result, err)
	}
}

func TestCancellationOrdersAndRequestUsage(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	req := Request{ID: "request", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "input", MainAttempt: true, MaxMainAttempts: 1}
	if err := s.ReserveRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	w := c.WorkItem.Query().Where(workitem.SegmentIDEQ(seg.ID)).OnlyX(ctx)
	if w.MainAttempts != 1 || w.MainNetworkAttempts != 1 {
		t.Fatal("reservation charged twice")
	}
	candidate := ready(scope, seg)
	if err := s.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	c.Job.UpdateOneID(scope.JobID).SetStatus("cancelled").ExecX(ctx)
	result, err := s.Commit(ctx, input(candidate))
	if !errors.Is(err, ErrStopped) || result.Outcome != Fatal {
		t.Fatalf("cancelled commit=%+v %v", result, err)
	}
	usage := RequestResult{State: "completed", UsageKnown: true, InputTokens: 9, OutputTokens: 3}
	for range 2 {
		if err := s.RecordRequest(ctx, req.ID, usage); err != nil {
			t.Fatal(err)
		}
	}
	if rows := c.UsageRecord.Query().AllX(ctx); len(rows) != 1 || rows[0].APICalls != 1 || rows[0].InputTokens != 9 {
		t.Fatalf("usage=%+v", rows)
	}
	if c.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(scope.RoundID)).CountX(ctx) != 0 {
		t.Fatal("cancelled job acquired new completion")
	}
}

func TestBulkVersionHook(t *testing.T) {
	ctx, c, _, scope, seg := fixture(t)
	c.Segment.Update().Where(segment.ResourceIDEQ(scope.ResourceID)).SetStatus(segment.StatusApproved).ExecX(ctx)
	if c.Segment.GetX(ctx, seg.ID).ContentVersion != 2 {
		t.Fatal("bulk review did not increment version")
	}
	c.Segment.UpdateOneID(seg.ID).SetUpdatedAt(time.Now()).ExecX(ctx)
	if c.Segment.GetX(ctx, seg.ID).ContentVersion != 2 {
		t.Fatal("timestamp-only update invalidated content")
	}
	if err := c.Segment.UpdateOneID(seg.ID).SetContentVersion(1).Exec(ctx); err == nil {
		t.Fatal("version replacement was accepted")
	}
	if err := c.Segment.UpdateOneID(seg.ID).AddContentVersion(-1).Exec(ctx); err == nil {
		t.Fatal("version decrement was accepted")
	}
}

func TestLegacyDeletionCountsUnresolvedOriginalMembers(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	candidate := ready(scope, seg)
	if err := s.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(ctx, input(candidate)); err != nil {
		t.Fatal(err)
	}
	unresolved := c.Segment.Create().SetResourceID(scope.ResourceID).SetSegmentIndex(1).SetSourceText("unresolved").SaveX(ctx)
	c.WorkItem.Delete().ExecX(ctx)
	c.JobRound.UpdateOneID(scope.RoundID).SetManifestSealed(false).SetSegmentTotal(2).ExecX(ctx)
	c.Job.UpdateOneID(scope.JobID).SetProgressTotal(2).ExecX(ctx)
	err := Transaction(ctx, c, func(tx *ent.Client) error {
		ids, err := LockResourceJobs(ctx, tx, scope.ResourceID)
		if err != nil {
			return err
		}
		if err := BeforeDeleteSegments(ctx, tx, []int{unresolved.ID}); err != nil {
			return err
		}
		if err := tx.Segment.DeleteOneID(unresolved.ID).Exec(ctx); err != nil {
			return err
		}
		return CalibrateJobs(ctx, tx, ids)
	})
	if err != nil {
		t.Fatal(err)
	}
	row := c.Job.GetX(ctx, scope.JobID)
	if row.ProgressTotal != 1 || row.ProgressCompleted != 1 {
		t.Fatalf("legacy deletion progress=%d/%d", row.ProgressCompleted, row.ProgressTotal)
	}
}

func TestAmbiguousLegacyDeletionRollsBack(t *testing.T) {
	ctx, c, _, scope, seg := fixture(t)
	c.Segment.Create().SetResourceID(scope.ResourceID).SetSegmentIndex(1).SetSourceText("unselected").ExecX(ctx)
	c.WorkItem.Delete().ExecX(ctx)
	c.JobRound.UpdateOneID(scope.RoundID).SetManifestSealed(false).ExecX(ctx)
	err := Transaction(ctx, c, func(tx *ent.Client) error {
		if _, err := LockResourceJobs(ctx, tx, scope.ResourceID); err != nil {
			return err
		}
		if err := BeforeDeleteSegments(ctx, tx, []int{seg.ID}); err != nil {
			return err
		}
		return tx.Segment.DeleteOneID(seg.ID).Exec(ctx)
	})
	if !errors.Is(err, ErrManifest) {
		t.Fatalf("ambiguous deletion=%v", err)
	}
	if !c.Segment.Query().Where(segment.IDEQ(seg.ID)).ExistX(ctx) || c.JobRound.GetX(ctx, scope.RoundID).SegmentTotal != 1 {
		t.Fatal("ambiguous deletion escaped rollback")
	}
}

func TestConfirmationAndRoundCloseShareOneProgressCalculation(t *testing.T) {
	for _, status := range []string{"completed", "skipped"} {
		t.Run(status, func(t *testing.T) {
			ctx, c, s, scope, seg := fixture(t)
			candidate := ready(scope, seg)
			if err := s.SaveCandidate(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			done := make(chan error, 2)
			go func() { <-start; _, err := s.Commit(ctx, input(candidate)); done <- err }()
			go func() {
				<-start
				done <- Transaction(ctx, c, func(tx *ent.Client) error {
					if err := LockJob(ctx, tx, scope.JobID); err != nil {
						return err
					}
					return SetRoundStatus(ctx, tx, scope.RoundID, []string{"running"}, status)
				})
			}()
			close(start)
			for range 2 {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			row := c.JobRound.Query().Where(jobround.IDEQ(scope.RoundID)).OnlyX(ctx)
			if row.Status != status || row.SegmentCompleted != 1 || c.Job.GetX(ctx, scope.JobID).ProgressCompleted != 1 {
				t.Fatal("round close and late confirmation counted twice")
			}
		})
	}
}

func TestAcceptanceRollbackAtEveryRemainingWrite(t *testing.T) {
	for _, stage := range []string{"resource_generation", "round_count", "job_progress", "work_item", "candidate"} {
		t.Run(stage, func(t *testing.T) {
			ctx, c, s, scope, seg := fixture(t)
			candidate := ready(scope, seg)
			if err := s.SaveCandidate(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected " + stage)
			hook := func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
					fail := false
					switch stage {
					case "resource_generation":
						if n, ok := m.AddedField("translation_generation"); ok {
							fail = n.(int64) > 0
						}
					case "round_count":
						_, fail = m.Field("segment_completed")
					case "job_progress":
						_, fail = m.AddedField("progress_completed")
					case "work_item":
						state, _ := m.Field("state")
						fail = state == "resolved"
					case "candidate":
						state, _ := m.Field("state")
						fail = state == "completed"
					}
					if fail {
						return nil, injected
					}
					return next.Mutate(ctx, m)
				})
			}
			switch stage {
			case "resource_generation":
				c.Resource.Use(hook)
			case "round_count":
				c.JobRound.Use(hook)
			case "job_progress":
				c.Job.Use(hook)
			case "work_item":
				c.WorkItem.Use(hook)
			case "candidate":
				c.WorkCandidate.Use(hook)
			}
			result, err := s.Commit(ctx, input(candidate))
			if !errors.Is(err, injected) || result.Outcome != RetryableStorage {
				t.Fatalf("result=%+v %v", result, err)
			}
			if row := c.Segment.GetX(ctx, seg.ID); row.TargetText != nil || row.ContentVersion != 1 {
				t.Fatal("content escaped rollback")
			}
			if c.Resource.GetX(ctx, scope.ResourceID).TranslationGeneration != 0 || c.Job.GetX(ctx, scope.JobID).ProgressCompleted != 0 || c.JobRound.GetX(ctx, scope.RoundID).SegmentCompleted != 0 || c.JobRoundSegment.Query().CountX(ctx) != 0 {
				t.Fatal("progress or generation escaped rollback")
			}
			row := c.WorkCandidate.Query().Where(workcandidate.IdentityEQ(candidate.ID)).OnlyX(ctx)
			if row.State != "ready_to_commit" || len(row.Payload) == 0 || c.WorkItem.Query().OnlyX(ctx).State == "resolved" {
				t.Fatal("rollback consumed candidate")
			}
		})
	}
}
