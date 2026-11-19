package persistence

import (
	"context"
	"testing"
)

func TestMemberResponseOperationsRemainDistinct(t *testing.T) {
	c := &Collector{}
	s := New(DefaultPolicy(), nil)
	defer s.Close()
	for _, op := range []Operation{ResponseCapture, OutputArchive, BusinessPublication, MemberPromotion} {
		if !RegisteredOperation(string(op)) {
			t.Fatal("unregistered member operation", op)
		}
		if err := s.Save(WithObserver(t.Context(), c), Metadata{Operation: string(op), Class: Reliable, RequestID: "fixed-request"}, func(ctx context.Context) error {
			ObserveTransaction(ctx, "query", "", 0, nil)
			ObserveTransaction(ctx, "transaction", "committed", 0, nil)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if rows := s.Snapshot().Aggregates; len(rows) != 4 {
		t.Fatalf("scheduler merged member operations: %+v", rows)
	}
	for _, row := range c.Snapshot().Operations {
		if row.Operation == "other" || row.Queue != 1 || row.Executions != 1 || row.Calls != 1 || row.Transactions != 1 {
			t.Fatalf("collector lost actual operation signals: %+v", row)
		}
	}
	if aggregateOperation("arbitrary-request-id") != "other" {
		t.Fatal("unbounded operation admitted")
	}
}
