package workstate

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
)

func TestRetirementAtomicallySavesSuccessorAndClearsPayload(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	candidate := ready(scope, seg)
	if err := s.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	successor := Cursor{PoolIndex: 1, State: "pending", LastError: "alignment rejected"}
	if err := s.RetireCandidate(ctx, scope, candidate.ID, candidate.Version, successor); err != nil {
		t.Fatal(err)
	}
	w := c.WorkItem.Query().OnlyX(ctx)
	old := c.WorkCandidate.Query().Where(workcandidate.IdentityEQ(candidate.ID)).OnlyX(ctx)
	if w.CandidateID != "" || w.PoolIndex != 1 || w.State != "pending" || old.State != "rejected" || len(old.Payload) != 0 || old.PayloadBytes != 0 {
		t.Fatal("retirement did not save successor and release payload")
	}
	newCandidate := ready(scope, seg)
	newCandidate.ID = "successor-candidate"
	if err := s.SaveCandidate(ctx, newCandidate); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireCandidate(ctx, scope, candidate.ID, candidate.Version, successor); err != nil {
		t.Fatal(err)
	}
	if c.WorkItem.Query().OnlyX(ctx).CandidateID != newCandidate.ID || c.WorkCandidate.Query().Where(workcandidate.IdentityEQ(newCandidate.ID)).OnlyX(ctx).PayloadBytes == 0 {
		t.Fatal("duplicate retirement changed new candidate")
	}
}

func TestRetirementRollbackPreservesCandidateAndCursor(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	candidate := ready(scope, seg)
	if err := s.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("retire fault")
	c.WorkCandidate.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if state, _ := m.Field("state"); state == "rejected" {
				return nil, injected
			}
			return next.Mutate(ctx, m)
		})
	})
	if err := s.RetireCandidate(ctx, scope, candidate.ID, candidate.Version, Cursor{PoolIndex: 1, State: "pending"}); !errors.Is(err, injected) {
		t.Fatalf("retire=%v", err)
	}
	w := c.WorkItem.Query().OnlyX(ctx)
	old := c.WorkCandidate.Query().OnlyX(ctx)
	if w.PoolIndex != 0 || w.CandidateID != candidate.ID || old.State != "ready_to_commit" || len(old.Payload) == 0 {
		t.Fatal("failed retirement escaped rollback")
	}
}

func TestRetirementCannotDiscardNewCandidateVersion(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	candidate := ready(scope, seg)
	if err := s.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Version++
	if err := s.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireCandidate(ctx, scope, candidate.ID, 1, Cursor{PoolIndex: 1, State: "pending"}); !errors.Is(err, ErrCandidateVersion) {
		t.Fatalf("old retirement=%v", err)
	}
	if c.WorkCandidate.Query().OnlyX(ctx).PayloadBytes == 0 || c.WorkItem.Query().OnlyX(ctx).PoolIndex != 0 {
		t.Fatal("old retirement consumed new version")
	}
}
