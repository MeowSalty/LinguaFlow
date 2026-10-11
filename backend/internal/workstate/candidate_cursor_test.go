package workstate

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
)

func TestCandidateWithCursorFailureRollsBackEntireHandoff(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "create"
		if existing {
			name = "update"
		}
		t.Run(name, func(t *testing.T) {
			ctx, client, store, scope, seg := fixture(t)
			request := Request{ID: "main", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "main", MainAttempt: true}
			if err := store.ReserveRequest(ctx, request); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordRequest(ctx, request.ID, RequestResult{State: "received"}); err != nil {
				t.Fatal(err)
			}
			candidate := ready(scope, seg)
			candidate.ParentRequestID = request.ID
			candidate.State = "pending_alignment"
			originalPayload := bytes.Clone(candidate.Payload)
			if existing {
				if err := store.SaveCandidate(ctx, candidate); err != nil {
					t.Fatal(err)
				}
				candidate.Version++
				candidate.Payload = []byte(`{"version":2}`)
				candidate.State = "ready_to_commit"
			}
			before := client.WorkItem.Query().OnlyX(ctx)
			cursor := cursorFromRow(before)
			deadline := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
			cursor.State = "candidate"
			cursor.NextAttemptAt = &deadline
			cursor.PromptPhase = "waiting_alignment"
			cursor.Data = []byte(`{"phase":"waiting_alignment"}`)
			injected := errors.New("cursor write failed after candidate save")
			fail := true
			client.WorkItem.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					if _, cursorWrite := mutation.Field(workitem.FieldPoolIndex); cursorWrite && fail {
						return nil, injected
					}
					return next.Mutate(ctx, mutation)
				})
			})
			if err := store.SaveCandidateWithCursor(ctx, candidate, cursor); !errors.Is(err, injected) {
				t.Fatalf("candidate/cursor failure: %v", err)
			}
			after := client.WorkItem.GetX(ctx, before.ID)
			if before.CandidateID != after.CandidateID || !reflect.DeepEqual(cursorFromRow(before), cursorFromRow(after)) {
				t.Fatal("failed cursor save leaked candidate ownership or attempt state")
			}
			if existing {
				saved := client.WorkCandidate.Query().Where(workcandidate.IdentityEQ(candidate.ID)).OnlyX(ctx)
				if saved.Version != 1 || saved.State != "pending_alignment" || !bytes.Equal(saved.Payload, originalPayload) || saved.PayloadBytes != int64(len(originalPayload)) {
					t.Fatal("failed cursor save committed an updated candidate")
				}
			} else if client.WorkCandidate.Query().CountX(ctx) != 0 {
				t.Fatal("failed cursor save created an unaccounted candidate")
			}
			if client.Segment.GetX(ctx, seg.ID).ContentVersion != 1 || client.Resource.GetX(ctx, scope.ResourceID).TranslationGeneration != 0 || client.JobRoundSegment.Query().CountX(ctx) != 0 || client.Job.GetX(ctx, scope.JobID).ProgressCompleted != 0 {
				t.Fatal("failed handoff changed accepted content or completion facts")
			}
			fail = false
			if err := store.SaveCandidateWithCursor(ctx, candidate, cursor); err != nil {
				t.Fatalf("storage-only handoff retry: %v", err)
			}
			after = client.WorkItem.GetX(ctx, before.ID)
			saved := client.WorkCandidate.Query().Where(workcandidate.IdentityEQ(candidate.ID)).OnlyX(ctx)
			if saved.Version != candidate.Version || !bytes.Equal(saved.Payload, candidate.Payload) || after.CandidateID != candidate.ID || !reflect.DeepEqual(cursorFromRow(after), cursor) {
				t.Fatal("successful retry did not durably transfer candidate and cursor together")
			}
			if after.MainAttempts != 1 || after.MainNetworkAttempts != 1 || client.WorkRequest.Query().CountX(ctx) != 1 {
				t.Fatal("storage retry changed the already charged main attempt")
			}
		})
	}
}

func TestCandidateWithCursorReplayStillValidatesAndSavesCursor(t *testing.T) {
	ctx, client, store, scope, seg := fixture(t)
	request := Request{ID: "main", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "main", MainAttempt: true}
	if err := store.ReserveRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	candidate := ready(scope, seg)
	candidate.ParentRequestID = request.ID
	if err := store.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	cursor, err := store.LoadCursor(ctx, scope, seg.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale := cursor
	stale.MainAttempts = 0
	if err := store.SaveCandidateWithCursor(ctx, candidate, stale); !errors.Is(err, ErrStale) {
		t.Fatalf("identical candidate bypassed stale cursor validation: %v", err)
	}
	deadline := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
	cursor.NextAttemptAt = &deadline
	cursor.PromptPhase = "waiting_alignment"
	if err := store.SaveCandidateWithCursor(ctx, candidate, cursor); err != nil {
		t.Fatalf("identical candidate cursor handoff: %v", err)
	}
	if err := store.SaveCandidateWithCursor(ctx, candidate, cursor); err != nil {
		t.Fatalf("identical candidate/cursor replay: %v", err)
	}
	row := client.WorkItem.Query().OnlyX(ctx)
	if row.NextAttemptAt == nil || !row.NextAttemptAt.Equal(deadline) || row.PromptPhase != cursor.PromptPhase || row.MainAttempts != 1 {
		t.Fatal("identical candidate replay skipped cursor persistence")
	}
	if client.WorkCandidate.Query().CountX(ctx) != 1 || client.WorkCandidate.Query().OnlyX(ctx).Version != 1 || client.WorkRequest.Query().CountX(ctx) != 1 {
		t.Fatal("handoff replay duplicated candidate or model attempts")
	}
}
