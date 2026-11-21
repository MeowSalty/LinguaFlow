package workstate

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
)

type promotionSQLFixtureState struct {
	ctx       context.Context
	client    *ent.Client
	store     *Store
	scope     Scope
	request   Request
	candidate Candidate
	cursor    Cursor
	memberID  int
	probe     *storageSQLProbe
	budget    *StorageBudget
}

func promotionSQLFixture(t *testing.T) promotionSQLFixtureState {
	t.Helper()
	ctx, client, budget, owned, request, probe, _, _ := storageSQLFixture(t)
	j := client.Job.GetX(ctx, request.JobID)
	r := client.Resource.GetX(ctx, request.ResourceID)
	round := client.JobRound.GetX(ctx, request.JobRoundID)
	scope := Scope{JobID: j.ID, ResourceID: r.ID, RoundID: round.ID, JobResourceID: round.JobResourceID, RetryEpoch: j.RetryEpoch, SourceGeneration: r.SourceGeneration, SourceRevisionID: r.CurrentSourceRevisionID}
	currentExecution(t, ctx, client, scope)
	store := NewStore(client)
	segment := client.Segment.Query().OnlyX(ctx)
	req, capture, candidate := rawRequest(scope, segment)
	if _, err := store.CaptureResponse(owned, capture); err != nil {
		t.Fatal(err)
	}
	publishFixtureBusiness(t, owned, store, req)
	w := client.WorkItem.Query().OnlyX(ctx)
	return promotionSQLFixtureState{ctx: owned, client: client, store: store, scope: scope, request: req, candidate: candidate, cursor: cursorFromRow(w), memberID: w.ID, probe: probe, budget: budget}
}

func (f promotionSQLFixtureState) promotion(accepted bool) MemberPromotion {
	in := MemberPromotion{Scope: f.scope, Key: requestMemberKey(f.request, 0), Next: f.cursor}
	if accepted {
		in.Candidate = &f.candidate
	} else {
		in.Next.State = "rejected"
	}
	return fixturePromotion(in)
}

func TestPromotionReusesLockedScopeSQLFacts(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		name := "candidate"
		if !accepted {
			name = "rejected"
		}
		t.Run(name, func(t *testing.T) {
			f := promotionSQLFixture(t)
			f.probe.reset()
			header, err := f.store.PromoteMember(f.ctx, f.promotion(accepted))
			physical := f.probe.stop()
			if err != nil || header.Cursor != 1 || !header.Phase.Terminal() {
				t.Fatalf("promotion=%+v err=%v", header, err)
			}
			jobLocks, resourceLocks, roundReads, memberReads := 0, 0, 0, 0
			for _, call := range physical.calls {
				q := strings.ToLower(strings.NewReplacer("\"", "", "`", "").Replace(call.query))
				parts := strings.SplitN(q, " where ", 2)
				if call.kind == "exec" && strings.HasPrefix(q, "update jobs set ") && strings.Contains(parts[0], "retry_epoch") {
					jobLocks++
				}
				if call.kind == "exec" && strings.HasPrefix(q, "update resources set ") && strings.Contains(parts[0], "translation_generation") {
					resourceLocks++
				}
				if call.kind == "query" && strings.Contains(q, " from job_rounds ") && strings.Contains(parts[0], "job_resource_id") {
					roundReads++
				}
				if call.kind == "query" && strings.Contains(q, " from work_items ") && len(parts) == 2 && strings.Contains(parts[1], "segment_id") && strings.Contains(parts[1], "job_round_id") {
					memberReads++
				}
			}
			t.Logf("actual database/sql boundary: accepted=%t job_locks=%d resource_locks=%d scope_round_reads=%d scoped_member_reads=%d calls=%d queries=%d rows=%d value_bytes=%d begin/commit=%d/%d", accepted, jobLocks, resourceLocks, roundReads, memberReads, len(physical.calls), physical.queries, physical.rows, physical.valueBytes, physical.begins, physical.commits)
			wantMemberReads := 1
			if accepted {
				wantMemberReads = 2 // The cursor must read the newly published candidate state.
			}
			if jobLocks != 1 || resourceLocks != 1 || roundReads != 1 || memberReads != wantMemberReads || physical.begins != 1 || physical.commits != 1 {
				t.Fatal("promotion repeated locked scope facts or skipped the current cursor read")
			}
			member := f.client.WorkItem.GetX(f.ctx, f.memberID)
			if member.ResponseRequestID != "" || member.MainAttempts != f.cursor.MainAttempts || member.NetworkAttempts != f.cursor.NetworkAttempts {
				t.Fatalf("promotion changed charged attempts or retained response ownership: %+v", member)
			}
			if accepted && (member.State != "candidate" || member.CandidateID != f.candidate.ID || !member.MemberOwned) {
				t.Fatalf("cursor did not accept the newly published candidate state: %+v", member)
			}
			if !accepted && (member.State != "rejected" || member.MemberOwned || f.client.WorkCandidate.Query().CountX(f.ctx) != 0) {
				t.Fatalf("rejected promotion retained a candidate: %+v", member)
			}
			_, actual, err := f.budget.Source.Job(f.ctx, f.scope.JobID)
			storage, live := f.budget.Root.Usage(f.budget.Scope, StorageIdentity(f.scope.JobID))
			if err != nil || !live || storage.LogicalStorageBytes != actual {
				t.Fatalf("promotion storage settlement=%d actual=%d live=%t err=%v", storage.LogicalStorageBytes, actual, live, err)
			}
		})
	}
}

func TestPromotionPreservesFreshMemberEpochAndCursorCAS(t *testing.T) {
	for _, name := range []string{"epoch_after_candidate_write", "cursor_compare_and_swap"} {
		t.Run(name, func(t *testing.T) {
			f := promotionSQLFixture(t)
			before := f.client.WorkItem.GetX(f.ctx, f.memberID)
			f.client.WorkItem.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					m := mutation.(*ent.WorkItemMutation)
					if name == "cursor_compare_and_swap" && m.Op() == ent.OpUpdate {
						if _, cursorWrite := m.Field(workitem.FieldPoolIndex); cursorWrite {
							if err := m.Client().WorkItem.UpdateOneID(f.memberID).AddMainAttempts(1).Exec(ctx); err != nil {
								return nil, err
							}
						}
					}
					value, err := next.Mutate(ctx, mutation)
					if err == nil && name == "epoch_after_candidate_write" {
						if id, _ := m.Field(workitem.FieldCandidateID); id == f.candidate.ID {
							err = m.Client().WorkItem.UpdateOneID(f.memberID).SetRetryEpoch(f.scope.RetryEpoch + 1).Exec(ctx)
						}
					}
					return value, err
				})
			})
			f.probe.reset()
			_, err := f.store.PromoteMember(f.ctx, f.promotion(true))
			physical := f.probe.stop()
			wantErr := ErrStopped
			if name == "cursor_compare_and_swap" {
				wantErr = ErrStale
			}
			if !errors.Is(err, wantErr) || physical.begins != 1 || physical.commits != 0 {
				t.Fatalf("changed cursor facts accepted: err=%v begin/commit=%d/%d", err, physical.begins, physical.commits)
			}
			after := f.client.WorkItem.GetX(f.ctx, f.memberID)
			if after.RetryEpoch != before.RetryEpoch || after.CandidateID != before.CandidateID || after.ResponseRequestID != before.ResponseRequestID || !reflect.DeepEqual(cursorFromRow(after), cursorFromRow(before)) {
				t.Fatal("failed cursor transition leaked candidate, epoch or attempt changes")
			}
			request := f.client.WorkRequest.Query().Where(workrequest.IdentityEQ(f.request.ID)).OnlyX(f.ctx)
			output := f.client.WorkResponseMember.Query().OnlyX(f.ctx)
			input := f.client.WorkRequestInputMember.Query().OnlyX(f.ctx)
			if request.PromotionCursor != 0 || output.State != "pending" || len(output.Payload) == 0 || len(input.Payload) == 0 || f.client.WorkCandidate.Query().CountX(f.ctx) != 0 {
				t.Fatal("failed promotion retired paid response data or published a candidate")
			}
		})
	}
}
