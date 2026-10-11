package workstate

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
)

func TestAbortRestoresOnlyUnsentReservation(t *testing.T) {
	for _, stage := range []string{"main", "alignment"} {
		t.Run(stage, func(t *testing.T) {
			ctx, c, s, scope, seg := fixture(t)
			w := c.WorkItem.Query().OnlyX(ctx)
			c.WorkItem.UpdateOneID(w.ID).SetMainAttempts(2).SetAlignmentAttempts(3).SetNetworkAttempts(5).SetMainNetworkAttempts(4).SetAlignmentNetworkAttempts(5).ExecX(ctx)
			before := attemptsFromRow(c.WorkItem.GetX(ctx, w.ID))
			req := Request{ID: "never-dispatched", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: stage, BackendID: 1, BudgetModel: "stage_separated", InputDigest: "request", MainAttempt: stage == "main", AlignmentAttempt: stage == "alignment", MaxMainAttempts: 4, MaxAlignmentAttempts: 5}
			if err := s.ReserveRequest(ctx, req); err != nil {
				t.Fatal(err)
			}
			c.Job.UpdateOneID(scope.JobID).SetStatus("cancelled").ExecX(ctx)
			for range 2 {
				if err := s.AbortRequest(ctx, req.ID); err != nil {
					t.Fatal(err)
				}
			}
			if after := attemptsFromRow(c.WorkItem.GetX(ctx, w.ID)); after != before {
				t.Fatalf("refund counters=%+v want %+v", after, before)
			}
			if row := c.WorkRequest.Query().OnlyX(ctx); row.State != "cancelled_before_dispatch" {
				t.Fatalf("request=%+v", row)
			}
			if c.UsageRecord.Query().CountX(ctx) != 0 {
				t.Fatal("unmade request acquired usage")
			}
			if err := s.RecordRequest(ctx, req.ID, RequestResult{State: "sent"}); !errors.Is(err, ErrStopped) {
				t.Fatalf("aborted request sent=%v", err)
			}
			c.Job.UpdateOneID(scope.JobID).SetStatus("running").ExecX(ctx)
			if err := s.ReserveRequest(ctx, req); !errors.Is(err, ErrStopped) {
				t.Fatalf("refunded request identity was reused: %v", err)
			}
		})
	}
}

func TestAbortNeverRefundsSentOrUnknown(t *testing.T) {
	for _, state := range []string{"sent", "received", "unknown", "completed", "failed"} {
		t.Run(state, func(t *testing.T) {
			ctx, c, s, scope, seg := fixture(t)
			req := Request{ID: "possibly-dispatched", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "request", MainAttempt: true}
			if err := s.ReserveRequest(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordRequest(ctx, req.ID, RequestResult{State: state}); err != nil {
				t.Fatal(err)
			}
			if err := s.AbortRequest(ctx, req.ID); !errors.Is(err, ErrStopped) {
				t.Fatalf("unsafe refund=%v", err)
			}
			if c.WorkItem.Query().OnlyX(ctx).MainAttempts != 1 {
				t.Fatal("possibly sent attempt was refunded")
			}
		})
	}
}

func TestAbortDoesNotTouchNewEpoch(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	req := Request{ID: "old-epoch", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "request", MainAttempt: true}
	if err := s.ReserveRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	c.Job.UpdateOneID(scope.JobID).SetRetryEpoch(1).ExecX(ctx)
	w := c.WorkItem.Query().OnlyX(ctx)
	c.WorkItem.UpdateOneID(w.ID).SetRetryEpoch(1).SetMainAttempts(2).SetMainNetworkAttempts(2).SetNetworkAttempts(2).ExecX(ctx)
	if err := s.AbortRequest(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	if row := c.WorkItem.GetX(ctx, w.ID); row.MainAttempts != 2 || row.MainNetworkAttempts != 2 || row.RetryEpoch != 1 {
		t.Fatal("old refund altered new budget")
	}
}

func TestAbortFailureRollsBackRefund(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	req := Request{ID: "abort-fault", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "request", MainAttempt: true}
	if err := s.ReserveRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("request finalization fault")
	c.WorkRequest.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if state, _ := m.Field(workrequest.FieldState); state == "cancelled_before_dispatch" {
				return nil, injected
			}
			return next.Mutate(ctx, m)
		})
	})
	if err := s.AbortRequest(ctx, req.ID); !errors.Is(err, injected) {
		t.Fatalf("abort=%v", err)
	}
	if c.WorkItem.Query().OnlyX(ctx).MainAttempts != 1 || c.WorkRequest.Query().OnlyX(ctx).State != "reserved" {
		t.Fatal("refund escaped rollback")
	}
}
