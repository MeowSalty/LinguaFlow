package workstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
)

func manifestFixture(t *testing.T, n int) (context.Context, *ent.Client, *Store, Scope, []int) {
	t.Helper()
	ctx, c, s, scope, first := fixture(t)
	ids := []int{first.ID}
	for start := 1; start < n; start += bulkRows {
		var builders []*ent.SegmentCreate
		for i := start; i < min(start+bulkRows, n); i++ {
			builders = append(builders, c.Segment.Create().SetResourceID(scope.ResourceID).SetSegmentIndex(i).SetSourceText(fmt.Sprintf("source %d", i)))
		}
		rows, err := c.Segment.CreateBulk(builders...).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
	}
	c.WorkItem.Delete().ExecX(ctx)
	c.JobRound.UpdateOneID(scope.RoundID).SetManifestSealed(false).SetManifestVersion(0).SetSegmentTotal(0).ExecX(ctx)
	return ctx, c, s, scope, ids
}

func TestBulkSealLaterChunkFailureLeavesNoManifest(t *testing.T) {
	ctx, c, s, scope, ids := manifestFixture(t, 501)
	injected := errors.New("second insert chunk failed")
	fail := true
	c.WorkItem.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if id, ok := m.Field(workitem.FieldSegmentID); fail && ok && id == ids[len(ids)-1] {
				return nil, injected
			}
			return next.Mutate(ctx, m)
		})
	})
	if _, err := s.SealRound(ctx, scope, ids); !errors.Is(err, injected) {
		t.Fatalf("error=%v", err)
	}
	if c.WorkItem.Query().CountX(ctx) != 0 || c.JobRound.GetX(ctx, scope.RoundID).ManifestSealed {
		t.Fatal("partial manifest escaped rollback")
	}
	fail = false
	if _, err := s.SealRound(ctx, scope, ids); err != nil {
		t.Fatal(err)
	}
	if c.WorkItem.Query().CountX(ctx) != 501 || c.Job.GetX(ctx, scope.JobID).ProgressTotal != 501 {
		t.Fatal("manifest/count mismatch")
	}
}

func TestBulkCursorsLaterChunkFailureIsAtomic(t *testing.T) {
	ctx, c, s, scope, ids := manifestFixture(t, 1001)
	if _, err := s.SealRound(ctx, scope, ids); err != nil {
		t.Fatal(err)
	}
	var transitions []CursorTransition
	for _, id := range ids {
		transitions = append(transitions, CursorTransition{SegmentID: id, Cursor: Cursor{PoolIndex: 1, State: "pending"}})
	}
	injected := errors.New("later cursor update failed")
	calls := 0
	fail := true
	c.WorkItem.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if _, ok := m.Field(workitem.FieldPoolIndex); ok {
				calls++
				if fail && calls == 2 {
					return nil, injected
				}
			}
			return next.Mutate(ctx, m)
		})
	})
	if err := s.SaveCursors(ctx, scope, transitions); !errors.Is(err, injected) {
		t.Fatalf("error=%v", err)
	}
	if c.WorkItem.Query().Where(workitem.PoolIndexNEQ(0)).CountX(ctx) != 0 {
		t.Fatal("earlier cursor chunk committed")
	}
	fail = false
	if err := s.SaveCursors(ctx, scope, transitions); err != nil {
		t.Fatal(err)
	}
	if c.WorkItem.Query().Where(workitem.PoolIndexEQ(1)).CountX(ctx) != 1001 {
		t.Fatal("cursor chunk missing")
	}
}

func TestBulkCursorOversizedJSONPreservesInputContract(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	data, err := json.Marshal(map[string]string{"value": strings.Repeat("x", bulkJSONBytes+1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCursors(ctx, scope, []CursorTransition{{SegmentID: seg.ID, Cursor: Cursor{State: "pending", Data: data}}}); err != nil {
		t.Fatal(err)
	}
	if string(c.WorkItem.Query().OnlyX(ctx).Cursor) != string(data) {
		t.Fatal("oversized cursor changed")
	}
}

func TestBulkReserveLaterGroupFailureRollsBackEveryDebit(t *testing.T) {
	ctx, c, s, _, _, request := batchFixture(t)
	setMixedBatchCursor(ctx, c, &request)
	before := batchCounters(ctx, c)
	calls := 0
	injected := errors.New("second debit group failed")
	c.WorkItem.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if _, ok := m.Field(workitem.FieldNetworkAttempts); ok {
				calls++
				if calls == 2 {
					return nil, injected
				}
			}
			return next.Mutate(ctx, m)
		})
	})
	if err := s.ReserveRequest(ctx, request); !errors.Is(err, injected) {
		t.Fatalf("error=%v", err)
	}
	after := batchCounters(ctx, c)
	for i := range before {
		if before[i] != after[i] {
			t.Fatal("partial debit escaped rollback")
		}
	}
	if c.WorkRequest.Query().Where(workrequest.IdentityEQ(request.ID)).ExistX(ctx) {
		t.Fatal("failed request persisted")
	}
}

func TestBulkConfirmChunkAndEmptyMetadataReplay(t *testing.T) {
	ctx, c, s, scope, ids := manifestFixture(t, 2001)
	if _, err := s.SealRound(ctx, scope, ids); err != nil {
		t.Fatal(err)
	}
	facts := make([]Confirmation, len(ids))
	for i, id := range ids {
		facts[i] = Confirmation{SegmentID: id, CommitID: fmt.Sprintf("commit-%d", id), Outcome: Committed}
	}
	confirm := func(facts []Confirmation) error {
		return Transaction(ctx, c, func(tx *ent.Client) error {
			if err := LockJob(ctx, tx, scope.JobID); err != nil {
				return err
			}
			_, err := Confirm(ctx, tx, scope.JobID, scope.RoundID, facts)
			return err
		})
	}
	injected := errors.New("later confirmation chunk failed")
	fail := true
	c.JobRoundSegment.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if id, ok := m.Field("segment_id"); fail && ok && id == ids[500] {
				return nil, injected
			}
			return next.Mutate(ctx, m)
		})
	})
	if err := confirm(facts); !errors.Is(err, injected) {
		t.Fatalf("error=%v", err)
	}
	if c.JobRoundSegment.Query().CountX(ctx) != 0 || c.JobRound.GetX(ctx, scope.RoundID).SegmentCompleted != 0 || c.Job.GetX(ctx, scope.JobID).ProgressCompleted != 0 {
		t.Fatal("partial confirmation/count escaped rollback")
	}
	fail = false
	if err := confirm(facts); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		facts[i] = Confirmation{SegmentID: id}
	}
	if err := confirm(facts); err != nil {
		t.Fatal(err)
	}
	if c.JobRoundSegment.Query().CountX(ctx) != 2001 || c.Job.GetX(ctx, scope.JobID).ProgressCompleted != 2001 {
		t.Fatal("duplicate facts changed progress")
	}
	if err := confirm([]Confirmation{{SegmentID: ids[0], CommitID: "different"}}); !errors.Is(err, ErrManifest) {
		t.Fatalf("conflicting identity=%v", err)
	}
	c.JobRound.UpdateOneID(scope.RoundID).SetSegmentCompleted(0).SetSegmentTotal(7).ExecX(ctx)
	if err := Transaction(ctx, c, func(tx *ent.Client) error {
		if err := LockJob(ctx, tx, scope.JobID); err != nil {
			return err
		}
		return Calibrate(ctx, tx, scope.JobID)
	}); err != nil {
		t.Fatal(err)
	}
	if row := c.JobRound.GetX(ctx, scope.RoundID); row.SegmentCompleted != 2001 || row.SegmentTotal != 2001 {
		t.Fatal("aggregate calibration mismatch")
	}
}

func TestBulkUpdateMissingCASMatchRollsBack(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	intervene := true
	c.WorkItem.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if _, ok := m.Field(workitem.FieldNetworkAttempts); ok && intervene {
				intervene = false
				mutation := m.(*ent.WorkItemMutation)
				if err := mutation.Client().WorkItem.Update().Where(workitem.SegmentIDEQ(seg.ID)).SetState("stale").Exec(ctx); err != nil {
					return nil, err
				}
			}
			return next.Mutate(ctx, m)
		})
	})
	r := Request{ID: "cas", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "input", MainAttempt: true, MaxMainAttempts: 3}
	if err := s.ReserveRequest(ctx, r); !errors.Is(err, ErrStale) {
		t.Fatalf("CAS error=%v", err)
	}
	w := c.WorkItem.Query().OnlyX(ctx)
	if w.State != "pending" || w.MainAttempts != 0 || c.WorkRequest.Query().CountX(ctx) != 0 {
		t.Fatal("failed CAS acknowledged a debit")
	}
}
