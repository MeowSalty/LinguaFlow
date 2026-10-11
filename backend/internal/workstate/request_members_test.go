package workstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
)

func batchFixture(t *testing.T) (context.Context, *ent.Client, *Store, Scope, []Candidate, Request) {
	t.Helper()
	ctx, client, store, scope, first := fixture(t)
	second := client.Segment.Create().SetResourceID(scope.ResourceID).SetSegmentIndex(1).SetSourceText("second source").SaveX(ctx)
	client.WorkItem.Create().SetJobID(scope.JobID).SetResourceID(scope.ResourceID).SetJobRoundID(scope.RoundID).SetSegmentID(second.ID).SetRetryEpoch(scope.RetryEpoch).ExecX(ctx)
	client.JobRound.UpdateOneID(scope.RoundID).SetSegmentTotal(2).ExecX(ctx)
	request := Request{ID: "batch", Scope: scope, Stage: "ruby_alignment", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "batch-input", MaxAlignmentAttempts: 5, MaxNetworkAttempts: 3}
	var candidates []Candidate
	for i, seg := range []*ent.Segment{first, second} {
		c := ready(scope, seg)
		c.ID = fmt.Sprintf("batch-candidate-%d", i)
		c.WorkID = WorkIdentity(scope, seg.ID)
		c.State = "pending_alignment"
		c.DTOVersion = 2
		if err := store.SaveCandidate(ctx, c); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, c)
		request.SegmentIDs = append(request.SegmentIDs, seg.ID)
		request.Members = append(request.Members, RequestMember{SegmentID: seg.ID, WorkID: c.WorkID, CandidateID: c.ID, CandidateVersion: 1})
	}
	return ctx, client, store, scope, candidates, request
}

func setMixedBatchCursor(ctx context.Context, client *ent.Client, r *Request) {
	m := &r.Members[1]
	m.LogicalAttempt = 2
	m.NetworkAttempt = 1
	client.WorkItem.Update().Where(workitem.SegmentIDEQ(m.SegmentID)).SetAlignmentAttempts(3).SetAlignmentNetworkAttempts(1).SetNetworkAttempts(1).ExecX(ctx)
}

func batchCounters(ctx context.Context, client *ent.Client) []attemptCounters {
	rows := client.WorkItem.Query().Order(ent.Asc(workitem.FieldSegmentID)).AllX(ctx)
	result := make([]attemptCounters, len(rows))
	for i, w := range rows {
		result[i] = attemptsFromRow(w)
	}
	return result
}

func TestBatchRequestDebitsDistinctCursorsAndCountsUsageOnce(t *testing.T) {
	ctx, client, store, _, _, r := batchFixture(t)
	setMixedBatchCursor(ctx, client, &r)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	want := []attemptCounters{{Alignment: 1, AlignmentNetwork: 1, Network: 1}, {Alignment: 3, AlignmentNetwork: 2, Network: 2}}
	if got := batchCounters(ctx, client); !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed batch counters=%+v, want %+v", got, want)
	}
	// Reordering is the same invocation, and never debits any member twice.
	r.Members[0], r.Members[1] = r.Members[1], r.Members[0]
	r.SegmentIDs[0], r.SegmentIDs[1] = r.SegmentIDs[1], r.SegmentIDs[0]
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := batchCounters(ctx, client); !reflect.DeepEqual(got, want) {
		t.Fatalf("idempotent reservation changed counters: %+v", got)
	}
	for _, result := range []RequestResult{{State: "sent"}, {State: "received", UsageKnown: true, InputTokens: 100, OutputTokens: 40}, {State: "completed", UsageKnown: true, InputTokens: 100, OutputTokens: 40}} {
		if err := store.RecordRequest(ctx, r.ID, result); err != nil {
			t.Fatal(err)
		}
	}
	usage := client.UsageRecord.Query().OnlyX(ctx)
	if usage.APICalls != 1 || usage.SegmentCount != 2 || usage.InputTokens != 100 || usage.OutputTokens != 40 || client.WorkRequest.Query().CountX(ctx) != 1 {
		t.Fatalf("batch usage was multiplied by members: %+v", usage)
	}
	row := client.WorkRequest.Query().OnlyX(ctx)
	members, err := decodeRequestMembers(row.Members)
	if err != nil || len(members) != 2 || members[1].LogicalAttempt != 2 || members[1].NetworkAttempt != 1 {
		t.Fatalf("stored invocation members=%+v, %v", members, err)
	}
}

func TestBatchRequestIdentityCannotRefreshOrChangeMembers(t *testing.T) {
	ctx, client, store, _, _, r := batchFixture(t)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	before := batchCounters(ctx, client)
	r.Members[1].CandidateVersion++
	if err := store.ReserveRequest(ctx, r); err == nil {
		t.Fatal("request identity accepted changed candidate version")
	}
	r.Members[1].CandidateVersion--
	r.ID = "different-request-same-old-cursors"
	if err := store.ReserveRequest(ctx, r); !errors.Is(err, ErrCandidateVersion) {
		t.Fatalf("new request refreshed old member cursor: %v", err)
	}
	if got := batchCounters(ctx, client); !reflect.DeepEqual(got, before) || client.WorkRequest.Query().CountX(ctx) != 1 {
		t.Fatal("identity conflict mutated invocation accounting")
	}
}

func TestBatchReservationRollsBackEveryMemberOnFinalMemberFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(context.Context, *ent.Client, *Request)
		want   error
	}{
		{"candidate_version", func(_ context.Context, _ *ent.Client, r *Request) { r.Members[1].CandidateVersion++ }, ErrCandidateVersion},
		{"pool", func(_ context.Context, _ *ent.Client, r *Request) { r.Members[1].Pool++ }, ErrManifest},
		{"work_identity", func(_ context.Context, _ *ent.Client, r *Request) { r.Members[1].WorkID += "other" }, ErrManifest},
		{"logical_budget", func(ctx context.Context, c *ent.Client, r *Request) {
			r.Members[1].LogicalAttempt = 5
			c.WorkItem.Update().Where(workitem.SegmentIDEQ(r.Members[1].SegmentID)).SetAlignmentAttempts(5).ExecX(ctx)
		}, ErrBudget},
		{"network_budget", func(ctx context.Context, c *ent.Client, r *Request) {
			setMixedBatchCursor(ctx, c, r)
			r.MaxNetworkAttempts = 1
		}, ErrBudget},
		{"resolved", func(ctx context.Context, c *ent.Client, r *Request) {
			c.WorkItem.Update().Where(workitem.SegmentIDEQ(r.Members[1].SegmentID)).SetState("resolved").ExecX(ctx)
		}, ErrStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, client, store, _, _, r := batchFixture(t)
			tc.change(ctx, client, &r)
			before := batchCounters(ctx, client)
			if err := store.ReserveRequest(ctx, r); !errors.Is(err, tc.want) {
				t.Fatalf("reserve=%v, want %v", err, tc.want)
			}
			if got := batchCounters(ctx, client); !reflect.DeepEqual(got, before) || client.WorkRequest.Query().CountX(ctx) != 0 {
				t.Fatal("rejected batch left an earlier member debited")
			}
		})
	}
}

func TestBatchRefundRestoresEveryCursorAndUnknownKeepsEveryDebit(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprintf("unknown=%v", unknown), func(t *testing.T) {
			ctx, client, store, scope, _, r := batchFixture(t)
			setMixedBatchCursor(ctx, client, &r)
			before := batchCounters(ctx, client)
			if err := store.ReserveRequest(ctx, r); err != nil {
				t.Fatal(err)
			}
			debited := batchCounters(ctx, client)
			if unknown {
				if err := MarkUnknown(ctx, client, scope.JobID); err != nil {
					t.Fatal(err)
				}
				if err := store.AbortRequest(ctx, r.ID); !errors.Is(err, ErrStopped) {
					t.Fatalf("unknown refunded: %v", err)
				}
				before = debited
			} else {
				if err := store.AbortRequest(ctx, r.ID); err != nil {
					t.Fatal(err)
				}
				if err := store.AbortRequest(ctx, r.ID); err != nil {
					t.Fatal(err)
				}
				if err := store.ReserveRequest(ctx, r); !errors.Is(err, ErrStopped) {
					t.Fatalf("refunded identity reused: %v", err)
				}
			}
			if got := batchCounters(ctx, client); !reflect.DeepEqual(got, before) || client.UsageRecord.Query().CountX(ctx) != 0 {
				t.Fatalf("refund/unknown counters=%+v, want %+v", got, before)
			}
		})
	}
}

func TestBatchRefundDoesNotRewindChangedCandidate(t *testing.T) {
	ctx, client, store, _, candidates, r := batchFixture(t)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	before := batchCounters(ctx, client)
	client.WorkCandidate.Update().Where(workcandidate.IdentityEQ(candidates[1].ID)).AddVersion(1).ExecX(ctx)
	if err := store.AbortRequest(ctx, r.ID); !errors.Is(err, ErrCandidateVersion) {
		t.Fatalf("changed candidate refunded: %v", err)
	}
	if got := batchCounters(ctx, client); !reflect.DeepEqual(got, before) || client.WorkRequest.Query().OnlyX(ctx).State != "reserved" {
		t.Fatal("rejected batch refund partially restored earlier members")
	}
}

func responseCandidate(t *testing.T, c Candidate, requestID string, forceSingle bool) Candidate {
	t.Helper()
	c.Version++
	c.LastAlignmentRequestID = requestID
	data, err := json.Marshal(map[string]any{"version": c.Version, "retry_epoch": c.Scope.RetryEpoch, "last_alignment_request_id": requestID, "force_single_alignment": forceSingle, "validated": []string{"ruby-1"}})
	if err != nil {
		t.Fatal(err)
	}
	c.Payload = data
	return c
}

func TestBatchPartialHandoffRecoveryKeepsSavedProgressAndUnknownDebit(t *testing.T) {
	ctx, client, store, scope, candidates, r := batchFixture(t)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRequest(ctx, r.ID, RequestResult{State: "received", UsageKnown: true, InputTokens: 12, OutputTokens: 9}); err != nil {
		t.Fatal(err)
	}
	first := responseCandidate(t, candidates[0], r.ID, true)
	cur, err := store.LoadCursor(ctx, scope, first.SegmentID)
	if err != nil {
		t.Fatal(err)
	}
	cur.PromptPhase, cur.NetworkAttempts, cur.AlignmentNetworkAttempts = "alignment_complete", 0, 0
	if err := store.SaveCandidateWithCursor(ctx, first, cur); err != nil {
		t.Fatal(err)
	}
	if err := MarkUnknown(ctx, client, scope.JobID); err != nil {
		t.Fatal(err)
	}
	// Recreate the store as process recovery does; no response is synthesized
	// for the unsaved member, and neither candidate nor usage is duplicated.
	recovered := NewStore(client)
	loaded, _, err := recovered.LoadCandidates(ctx, scope, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded[0].Version != 2 || string(loaded[0].Payload) != string(first.Payload) || loaded[1].Version != 1 {
		t.Fatalf("partial handoff lost per-member progress: %+v", loaded)
	}
	want := []attemptCounters{{Alignment: 1}, {Alignment: 1, AlignmentNetwork: 1, Network: 1}}
	if got := batchCounters(ctx, client); !reflect.DeepEqual(got, want) {
		t.Fatalf("recovered attempts=%+v", got)
	}
	if client.UsageRecord.Query().CountX(ctx) != 1 || client.JobRoundSegment.Query().CountX(ctx) != 0 {
		t.Fatal("recovery duplicated usage or confirmed a private draft")
	}
}

func TestBatchPausedRetryHandoffRequiresExactCurrentMember(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*Candidate, *Request)
		state   string
		allowed bool
	}{
		{name: "received", state: "received", allowed: true},
		{name: "sent_network_failure", state: "sent", allowed: true},
		{name: "failed", state: "failed", allowed: true},
		{name: "reserved", state: "reserved"},
		{name: "unknown", state: "unknown"},
		{name: "other_request", state: "received", change: func(c *Candidate, _ *Request) { c.LastAlignmentRequestID = "other" }},
		{name: "other_member", state: "received", change: func(c *Candidate, r *Request) { c.WorkID = r.Members[1].WorkID }},
		{name: "other_version", state: "received", change: func(c *Candidate, _ *Request) { c.Version++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, client, store, scope, candidates, r := batchFixture(t)
			scope.RetryEpoch = 1
			client.Job.UpdateOneID(scope.JobID).SetRetryEpoch(1).ExecX(ctx)
			client.WorkItem.Update().SetRetryEpoch(1).ExecX(ctx)
			r.Scope = scope
			if err := store.ReserveRequest(ctx, r); err != nil {
				t.Fatal(err)
			}
			if tc.state != "reserved" {
				client.WorkRequest.Update().SetState(tc.state).ExecX(ctx)
			}
			client.Job.UpdateOneID(scope.JobID).SetStatus("pausing").SetPauseRequested(true).ExecX(ctx)
			candidates[0].Scope = scope
			c := responseCandidate(t, candidates[0], r.ID, true)
			if tc.change != nil {
				tc.change(&c, &r)
			}
			err := store.SaveCandidate(ctx, c)
			if tc.allowed {
				if err != nil {
					t.Fatalf("valid member proof failed: %v", err)
				}
				if err := store.SaveCandidate(ctx, c); err != nil {
					t.Fatalf("proof replay: %v", err)
				}
			} else if !errors.Is(err, ErrStopped) {
				t.Fatalf("invalid batch proof accepted: %v", err)
			}
		})
	}
}

func TestBatchCursorResetRequiresItsOwnReceivedMemberProof(t *testing.T) {
	ctx, client, store, scope, candidates, r := batchFixture(t)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRequest(ctx, r.ID, RequestResult{State: "received"}); err != nil {
		t.Fatal(err)
	}
	for i, c := range candidates {
		c = responseCandidate(t, c, r.ID, false)
		cur, err := store.LoadCursor(ctx, scope, c.SegmentID)
		if err != nil {
			t.Fatal(err)
		}
		cur.PromptPhase, cur.NetworkAttempts, cur.AlignmentNetworkAttempts = "alignment_complete", 0, 0
		if i == 0 {
			wrong := c
			wrong.LastAlignmentRequestID = "other-request"
			if err := store.SaveCandidateWithCursor(ctx, wrong, cur); !errors.Is(err, ErrStale) {
				t.Fatalf("unproven reset: %v", err)
			}
			if row := client.WorkCandidate.Query().Where(workcandidate.IdentityEQ(c.ID)).OnlyX(ctx); row.Version != 1 {
				t.Fatal("invalid cursor proof leaked candidate version")
			}
		}
		if err := store.SaveCandidateWithCursor(ctx, c, cur); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCandidateWithCursor(ctx, c, cur); err != nil {
			t.Fatalf("cursor proof replay: %v", err)
		}
	}
	if got := batchCounters(ctx, client); !reflect.DeepEqual(got, []attemptCounters{{Alignment: 1}, {Alignment: 1}}) {
		t.Fatalf("member counters did not clear independently: %+v", got)
	}
}

func TestPausedLocalBudgetCompletionUsesAlreadySavedBatchResponse(t *testing.T) {
	ctx, client, store, scope, candidates, r := batchFixture(t)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRequest(ctx, r.ID, RequestResult{State: "failed"}); err != nil {
		t.Fatal(err)
	}
	c := responseCandidate(t, candidates[0], r.ID, false)
	if err := store.SaveCandidate(ctx, c); err != nil {
		t.Fatal(err)
	}
	client.Job.UpdateOneID(scope.JobID).SetStatus("pausing").SetPauseRequested(true).ExecX(ctx)
	c = responseCandidate(t, c, r.ID, false)
	c.State = "ready_to_commit"
	cur, err := store.LoadCursor(ctx, scope, c.SegmentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCandidateWithCursor(ctx, c, cur); err != nil {
		t.Fatalf("local terminal save: %v", err)
	}
	if err := store.SaveCandidateWithCursor(ctx, c, cur); err != nil {
		t.Fatalf("local terminal replay: %v", err)
	}
	if cur.NetworkAttempts != 1 || client.WorkRequest.Query().Where(workrequest.IdentityEQ(r.ID)).OnlyX(ctx).State != "failed" {
		t.Fatal("local termination refreshed an attempt or invocation")
	}
}
