package workstate

import (
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
)

func TestCommitProjectionProofRejectsEditedExistingAndUnknown(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "existing"
		if unknown {
			name = "unknown_ack"
		}
		t.Run(name, func(t *testing.T) {
			ctx, client, store, scope, row, fault := acknowledgementFixture(t)
			candidate := ready(scope, row)
			if err := store.SaveCandidate(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			commit := input(candidate)
			commit.RequireProjectionProof = true
			edit := func() {
				client.Segment.UpdateOneID(row.ID).SetTargetText("newer edited authority").SetStatus(segment.StatusApproved).ExecX(ctx)
			}
			if unknown {
				fault.afterCommit = edit
				fault.mode.Store(1)
			} else {
				if result, err := store.Commit(ctx, commit); err != nil || result.ContentVersion != 2 {
					t.Fatalf("initial commit=%+v %v", result, err)
				}
				commit.ConfirmedVersion = 2
				edit()
			}
			_, err := store.Commit(ctx, commit)
			if !errors.Is(err, ErrStale) {
				t.Fatalf("projection accepted an unrelated version: %v", err)
			}
			if unknown {
				var verification *VerificationError
				if !errors.As(err, &verification) {
					t.Fatalf("unknown acknowledgement cause lost: %v", err)
				}
			}
			stored := client.Segment.GetX(ctx, row.ID)
			if stored.TargetText == nil || *stored.TargetText != "newer edited authority" || stored.ContentVersion != 3 || client.JobRoundSegment.Query().CountX(ctx) != 1 {
				t.Fatalf("proof failure changed authority: %+v", stored)
			}
		})
	}
}
