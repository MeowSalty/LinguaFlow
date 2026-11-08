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
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/jackc/pgx/v5/pgconn"
)

type acknowledgementFault struct {
	mode             atomic.Int32
	failVerification atomic.Bool
	cancel           context.CancelFunc
	afterCommit      func()
}

func TestTransactionRetryHasOneOwnerAndOneTotalBudget(t *testing.T) {
	ctx, c, _, _, _ := fixture(t)
	for _, owned := range []bool{false, true} {
		calls := 0
		current := ctx
		if owned {
			current = persistence.WithRetryOwner(current)
		}
		err := Transaction(current, c, func(*ent.Client) error { calls++; return &pgconn.PgError{Code: "40001", Message: "retry conflict"} })
		want := 4
		if owned {
			want = 1
		}
		if err == nil || calls != want {
			t.Fatalf("owned=%v calls=%d err=%v", owned, calls, err)
		}
	}
	calls := 0
	started := time.Now()
	bounded := persistence.WithDeadline(ctx, started.Add(30*time.Millisecond))
	err := Transaction(bounded, c, func(*ent.Client) error { calls++; return &pgconn.PgError{Code: "40001", Message: "retry conflict"} })
	if err == nil || calls != 1 || time.Since(started) > time.Second {
		t.Fatalf("total budget calls=%d duration=%s err=%v", calls, time.Since(started), err)
	}
}

func TestUnknownVerificationJoinsWorkerLifetime(t *testing.T) {
	ctx, c, s, scope, seg, fault := acknowledgementFixture(t)
	lifetime, cancel := context.WithCancel(ctx)
	cancel()
	r := Request{ID: "lifetime", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "input", MainAttempt: true, MaxMainAttempts: 3}
	fault.mode.Store(1)
	err := s.ReserveRequest(persistence.WithLifetime(ctx, lifetime), r)
	var unknown *VerificationError
	if !errors.As(err, &unknown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if c.WorkRequest.Query().CountX(ctx) != 1 || c.WorkItem.Query().OnlyX(ctx).MainAttempts != 1 {
		t.Fatal("lifetime cancellation refunded unknown attempt")
	}
}

type acknowledgementDriver struct {
	dialect.Driver
	fault *acknowledgementFault
}

func (d acknowledgementDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	if d.fault.failVerification.Load() {
		return nil, errors.New("verification connection unavailable")
	}
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return acknowledgementTx{Tx: tx, fault: d.fault}, nil
}

type acknowledgementTx struct {
	dialect.Tx
	fault *acknowledgementFault
}

func (t acknowledgementTx) Commit() error {
	mode := t.fault.mode.Swap(0)
	if mode == 2 {
		return errors.New("commit connection lost before delivery")
	}
	if err := t.Tx.Commit(); err != nil {
		return err
	}
	if mode == 0 {
		return nil
	}
	if t.fault.afterCommit != nil {
		t.fault.afterCommit()
	}
	if t.fault.cancel != nil {
		t.fault.cancel()
	}
	if mode == 3 {
		t.fault.failVerification.Store(true)
	}
	return errors.New("commit acknowledgement lost")
}

func acknowledgementFixture(t *testing.T) (context.Context, *ent.Client, *Store, Scope, *ent.Segment, *acknowledgementFault) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ack.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	fault := &acknowledgementFault{}
	c := ent.NewClient(ent.Driver(acknowledgementDriver{Driver: database.NewDriver(entsql.OpenDB(dialect.SQLite, db)), fault: fault}))
	t.Cleanup(func() { c.Close(); db.Close() })
	if err := c.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	ctx, c, s, scope, seg := seedFixture(t, ctx, c)
	return ctx, c, s, scope, seg, fault
}

func TestExpiredAttemptCommitAcknowledgementUsesFreshIdentityBarrier(t *testing.T) {
	ctx, c, s, scope, seg, fault := acknowledgementFixture(t)
	candidate := ready(scope, seg)
	if err := s.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	attempt, cancel := context.WithCancel(persistence.WithDeadline(ctx, time.Now().Add(time.Minute)))
	defer cancel()
	fault.cancel = cancel
	fault.mode.Store(1)
	result, err := s.Commit(attempt, input(candidate))
	if err != nil || result.Outcome != AlreadyCommitted || attempt.Err() == nil {
		t.Fatalf("result=%+v err=%v attempt=%v", result, err, attempt.Err())
	}
	if c.JobRoundSegment.Query().CountX(ctx) != 1 || c.Segment.GetX(ctx, seg.ID).ContentVersion != 2 {
		t.Fatal("verified commit was duplicated")
	}
}

func TestReservationAcknowledgementUnknownKeepsDebitAndIdentity(t *testing.T) {
	for _, mode := range []int32{1, 2, 3} {
		t.Run(string(rune('0'+mode)), func(t *testing.T) {
			ctx, c, s, scope, seg, fault := acknowledgementFixture(t)
			r := Request{ID: "fixed", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "input", MainAttempt: true, MaxMainAttempts: 3}
			fault.mode.Store(mode)
			err := s.ReserveRequest(ctx, r)
			fault.failVerification.Store(false)
			switch mode {
			case 1:
				if err != nil {
					t.Fatal(err)
				}
				if err := s.ReserveRequest(ctx, r); err != nil {
					t.Fatal(err)
				}
			case 2:
				var uncommitted *NotCommittedError
				if !errors.As(err, &uncommitted) {
					t.Fatalf("error=%v", err)
				}
				if c.WorkRequest.Query().CountX(ctx) != 0 || c.WorkItem.Query().OnlyX(ctx).MainAttempts != 0 {
					t.Fatal("uncommitted reservation debited")
				}
				if err := s.ReserveRequest(ctx, r); err != nil {
					t.Fatal(err)
				}
			case 3:
				var unknown *VerificationError
				if !errors.As(err, &unknown) || !unknown.Permanent() {
					t.Fatalf("error=%v", err)
				}
			}
			if c.WorkRequest.Query().CountX(ctx) != 1 || c.WorkItem.Query().OnlyX(ctx).MainAttempts != 1 {
				t.Fatal("unknown reservation retried or refunded")
			}
		})
	}
}

func TestLostRecordAndAbortAcknowledgementsRemainAtomic(t *testing.T) {
	ctx, c, s, scope, seg, fault := acknowledgementFixture(t)
	r := Request{ID: "record", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "input", MainAttempt: true, MaxMainAttempts: 3}
	if err := s.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	fault.mode.Store(1)
	result := RequestResult{State: "received", UsageKnown: true, InputTokens: 7, OutputTokens: 11}
	if err := s.RecordRequestForScope(ctx, scope, r.ID, result); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRequestForScope(ctx, scope, r.ID, result); err != nil {
		t.Fatal(err)
	}
	if c.UsageRecord.Query().CountX(ctx) != 1 || c.UsageRecord.Query().OnlyX(ctx).APICalls != 1 {
		t.Fatal("record replay counted usage twice")
	}
	r.ID = "abort"
	if err := s.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	fault.mode.Store(1)
	if err := s.AbortRequest(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if c.WorkItem.Query().OnlyX(ctx).MainAttempts != 1 {
		t.Fatal("abort did not restore exact cursor")
	}
}

func TestLegacyCompletionGetsAcceptedIdentityWithoutRecounting(t *testing.T) {
	for _, conflicting := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "conflicting"}[conflicting], func(t *testing.T) {
			ctx, c, s, scope, seg, fault := acknowledgementFixture(t)
			candidate := ready(scope, seg)
			if err := s.SaveCandidate(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			fact := Confirmation{SegmentID: seg.ID}
			if conflicting {
				fact.CommitID = "other-commit"
			}
			if err := Transaction(ctx, c, func(tx *ent.Client) error {
				if err := LockJob(ctx, tx, scope.JobID); err != nil {
					return err
				}
				_, err := Confirm(ctx, tx, scope.JobID, scope.RoundID, []Confirmation{fact})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if !conflicting {
				fault.mode.Store(1)
			}
			result, err := s.Commit(ctx, input(candidate))
			if conflicting {
				if !errors.Is(err, ErrManifest) || result.Outcome != Fatal || c.Segment.GetX(ctx, seg.ID).TargetText != nil {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else {
				if err != nil || result.Outcome != AlreadyCommitted {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				fact := c.JobRoundSegment.Query().OnlyX(ctx)
				if fact.CommitID == nil || *fact.CommitID != input(candidate).CommitID || fact.Outcome != string(Committed) {
					t.Fatal("accepted identity not enriched")
				}
			}
			if c.JobRoundSegment.Query().CountX(ctx) != 1 || c.Job.GetX(ctx, scope.JobID).ProgressCompleted != 1 {
				t.Fatal("existing completion recounted")
			}
		})
	}
}

func TestSuccessfulTransactionPhasesGetIndependentExecutionWindows(t *testing.T) {
	ctx, c, _, scope, _ := fixture(t)
	policy := persistence.DefaultPolicy()
	policy.ExecutionTimeout = 200 * time.Millisecond
	policy.RetryBudget = time.Second
	policy.MaxAttempts = 1
	scheduler := persistence.New(policy, nil)
	err := scheduler.Save(ctx, persistence.Metadata{Operation: "two_transactions"}, func(saveCtx context.Context) error {
		for range 2 {
			if err := Transaction(saveCtx, c, func(tx *ent.Client) error {
				time.Sleep(120 * time.Millisecond)
				return LockJob(saveCtx, tx, scope.JobID)
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || scheduler.Snapshot().Retries != 0 {
		t.Fatalf("sequential phases err=%v stats=%+v", err, scheduler.Snapshot())
	}
	err = scheduler.Save(ctx, persistence.Metadata{Operation: "one_transaction"}, func(saveCtx context.Context) error {
		return Transaction(saveCtx, c, func(tx *ent.Client) error {
			time.Sleep(120 * time.Millisecond)
			if err := LockJob(saveCtx, tx, scope.JobID); err != nil {
				return err
			}
			time.Sleep(120 * time.Millisecond)
			_, err := tx.Job.Get(saveCtx, scope.JobID)
			return err
		})
	})
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("one Tx refreshed SQL budget: %v", err)
	}
}

func TestUnknownSupersededPoolAndRetirementDoNotProveNonCommit(t *testing.T) {
	for _, operation := range []string{"pool", "retirement"} {
		t.Run(operation, func(t *testing.T) {
			ctx, c, s, scope, seg, fault := acknowledgementFixture(t)
			var save func() error
			if operation == "pool" {
				fault.afterCommit = func() { c.JobRound.UpdateOneID(scope.RoundID).SetPoolIndex(2).ExecX(ctx) }
				save = func() error { return s.SavePool(ctx, scope, 1) }
			} else {
				candidate := ready(scope, seg)
				if err := s.SaveCandidate(ctx, candidate); err != nil {
					t.Fatal(err)
				}
				fault.afterCommit = func() { c.WorkItem.UpdateOne(c.WorkItem.Query().OnlyX(ctx)).SetCandidateID("successor").ExecX(ctx) }
				save = func() error {
					return s.RetireCandidate(ctx, scope, candidate.ID, candidate.Version, Cursor{PoolIndex: 1})
				}
			}
			fault.mode.Store(1)
			err := save()
			var unknown *VerificationError
			var uncommitted *NotCommittedError
			if !errors.As(err, &unknown) || errors.As(err, &uncommitted) {
				t.Fatalf("superseded write error=%v", err)
			}
		})
	}
}
