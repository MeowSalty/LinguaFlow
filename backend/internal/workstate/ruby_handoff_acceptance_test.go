package workstate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
)

func TestRubyHandoffAcceptanceAuxGrowthReservationAbortAndUnknown(t *testing.T) {
	for _, state := range []string{"unsent", "prior_grant", "sent", "unknown"} {
		t.Run(state, func(t *testing.T) {
			ctx, client, store, _, candidates, r := batchFixture(t)
			if state == "prior_grant" {
				prior := r
				prior.ID = "prior-real-aux-grant"
				if err := store.ReserveRequest(ctx, prior); err != nil {
					t.Fatal(err)
				}
				if err := store.RecordRequest(ctx, prior.ID, RequestResult{State: "sent"}); err != nil {
					t.Fatal(err)
				}
				if err := store.RecordRequest(ctx, prior.ID, RequestResult{State: "failed", LastError: "429"}); err != nil {
					t.Fatal(err)
				}
				for i := range r.Members {
					r.Members[i].NetworkAttempt = 1
				}
			}
			if err := store.ReserveRequest(ctx, r); err != nil {
				t.Fatal(err)
			}
			if state == "sent" || state == "unknown" {
				if err := store.RecordRequest(ctx, r.ID, RequestResult{State: state}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.AbortRequest(ctx, r.ID); state == "sent" || state == "unknown" {
				if !errors.Is(err, ErrStopped) {
					t.Fatalf("possibly granted aux was refunded: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range candidates {
				row := client.WorkCandidate.Query().Where(workcandidate.IdentityEQ(candidate.ID)).OnlyX(ctx)
				if row.GrowthReserved != (state != "unsent") {
					t.Fatalf("%s changed durable Q credit: candidate=%s reserved=%v", state, candidate.ID, row.GrowthReserved)
				}
			}
			t.Logf("real_SQLite_aux_reserve state=%s members=2 durable_growth_retained=%v", state, state != "unsent")
		})
	}
}

func TestRubyHandoffAcceptanceAuxGrowthLostReserveAndAbortACK(t *testing.T) {
	for _, mode := range []int32{1, 2, 3} {
		t.Run(string(rune('0'+mode)), func(t *testing.T) {
			ctx, client, store, scope, seg, fault := acknowledgementFixture(t)
			candidate := ready(scope, seg)
			candidate.State = "pending_alignment"
			candidate.DTOVersion = 2
			if err := store.SaveCandidate(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			r := Request{ID: "aux-unknown-reserve", Scope: scope, Stage: "ruby_alignment", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "frozen-aux", SegmentIDs: []int{seg.ID}, Members: []RequestMember{{SegmentID: seg.ID, WorkID: WorkIdentity(scope, seg.ID), CandidateID: candidate.ID, CandidateVersion: 1}}, MaxAlignmentAttempts: 3}
			fault.mode.Store(mode)
			err := store.ReserveRequest(ctx, r)
			fault.failVerification.Store(false)
			if mode == 2 {
				var absent *NotCommittedError
				if !errors.As(err, &absent) || client.WorkCandidate.Query().OnlyX(ctx).GrowthReserved {
					t.Fatalf("uncommitted aux reservation retained Q: %v", err)
				}
				if err := store.ReserveRequest(ctx, r); err != nil {
					t.Fatal(err)
				}
			} else if mode == 3 {
				var unknown *VerificationError
				if !errors.As(err, &unknown) {
					t.Fatalf("missing unknown outcome: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !client.WorkCandidate.Query().OnlyX(ctx).GrowthReserved || client.WorkItem.Query().OnlyX(ctx).AlignmentAttempts != 1 {
				t.Fatal("lost ACK refunded durable Q or repeated aux debit")
			}
			if mode == 3 {
				if err := MarkUnknown(ctx, client, scope.JobID); err != nil {
					t.Fatal(err)
				}
				if err := store.AbortRequest(ctx, r.ID); !errors.Is(err, ErrStopped) || !client.WorkCandidate.Query().OnlyX(ctx).GrowthReserved {
					t.Fatalf("unknown aux credit unsafe refund: %v", err)
				}
			} else {
				fault.mode.Store(1)
				if err := store.AbortRequest(ctx, r.ID); err != nil {
					t.Fatal(err)
				}
				if client.WorkCandidate.Query().OnlyX(ctx).GrowthReserved || client.WorkItem.Query().OnlyX(ctx).AlignmentAttempts != 0 {
					t.Fatal("lost Abort ACK did not restore original ungranted Q/debit")
				}
			}
			t.Logf("real_Commit_ACK_fault=%d durable_Q_checked=true unsafe_unknown_refund_rejected=%v", mode, mode == 3)
		})
	}
}

func TestRubyHandoffAcceptanceSentBackoffKeepsSparseMemberS(t *testing.T) {
	ctx, client, store, scope, seg := fixture(t)
	currentExecution(t, ctx, client, scope)
	r, _, _ := rawRequest(scope, seg)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRequestForScope(ctx, scope, r.ID, RequestResult{State: "sent"}); err != nil {
		t.Fatal(err)
	}
	check := func(stage string) OwnedMemberHeader {
		headers, _, err := NewStore(client).LoadOwnedMemberHeaders(ctx, scope.JobID, 0, 16)
		if err != nil || len(headers) != 1 {
			t.Fatalf("%s lost sent/backoff ownership header: %+v %v", stage, headers, err)
		}
		candidates, _, err := store.LoadCandidateHeaders(ctx, scope.JobID, 0, 16)
		if err != nil || len(candidates) != 0 {
			t.Fatalf("%s unexpected candidate carrier", stage)
		}
		raws, _, err := store.LoadResponseHeaders(ctx, scope.JobID, 0, 16)
		if err != nil || len(raws) != 0 {
			t.Fatalf("%s unexpected reliable raw carrier", stage)
		}
		return headers[0]
	}
	sent := check("sent")
	if err := store.RecordRequestForScope(ctx, scope, r.ID, RequestResult{State: "failed", LastError: "429"}); err != nil {
		t.Fatal(err)
	}
	cur, err := store.LoadCursor(ctx, scope, seg.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Hour)
	cur.State = "pending"
	cur.NextAttemptAt = &deadline
	if err := store.SaveFailureCursors(ctx, scope, r.ID, []CursorTransition{{SegmentID: seg.ID, Cursor: cur}}); err != nil {
		t.Fatal(err)
	}
	backoff := check("persisted_backoff_restart")
	if backoff.Identity != sent.Identity {
		t.Fatal("retry deadline allocated a new ownership identity")
	}
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}, AlignmentConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: 2, Candidates: 16 << 10, Item: 1024, Responses: 1024, Handoff: 64 << 10}); err != nil {
		t.Fatal(err)
	}
	if err := a.RestoreBudgetEntry(backoff.Identity, backend.PipelineBudgetEntry{ResourceID: scope.ResourceID, RoundID: scope.RoundID, Count: 1, Reservation: true, Durable: true}); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.ReserveMembers("fresh", 1, scope.ResourceID, scope.RoundID, false); err != nil || ok {
		t.Fatalf("ordinary S-1 allowed new member beside sent/backoff: %v %v", ok, err)
	}
	if ok, err := a.ReserveMembersReplacing("retry", 1, scope.ResourceID, scope.RoundID, false, []string{backoff.Identity}); err != nil || !ok {
		t.Fatalf("retry failed to transfer existing S: %v %v", ok, err)
	}
	if a.PipelineSnapshot().Segments != 1 {
		t.Fatal("retry double-counted or refunded member S")
	}
	a.ReleaseBudgetEntry("retry")
	if a.PipelineSnapshot().Segments != 1 || len(a.BudgetEntries()) != 1 {
		t.Fatal("never dispatched abort did not restore original single ownership")
	}
	t.Logf("sent_and_backoff_without_raw_or_candidate=true identity=%s restored_S=1 ordinary_limit=1 retry_transfer_S=1 abort_restores_S=1", backoff.Identity)
}

func acceptanceGrowthAdmission(t *testing.T) *backend.RequestAdmission {
	t.Helper()
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}, AlignmentConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: 4, Candidates: 16 << 10, Item: 1024, Responses: 1024, Handoff: 64 << 10}); err != nil {
		t.Fatal(err)
	}
	return a
}
func TestRubyHandoffAcceptanceJoinedOrdinaryLocalAndGrantedAuxGrowth(t *testing.T) {
	for _, granted := range []bool{false, true} {
		name := "ordinary_unstarted_local"
		if granted {
			name = "aux_granted"
		}
		t.Run(name, func(t *testing.T) {
			a := acceptanceGrowthAdmission(t)
			if err := a.RestoreBudgetEntry("member", backend.PipelineBudgetEntry{ResourceID: 1, RoundID: 1, Count: 1, Bytes: 128, Durable: true}); err != nil {
				t.Fatal(err)
			}
			if ok, err := a.ReserveCandidateGrowth("member"); err != nil || !ok {
				t.Fatalf("reserve growth: %v/%v", ok, err)
			}
			if granted {
				permit, wait, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{Stage: backend.RequestStageAlignment, RoundIndex: 0, CandidateIDs: []string{"member"}, InputBytes: 128}, nil)
				if err != nil || permit == nil {
					t.Fatalf("aux grant: %+v %v", wait, err)
				}
				permit.RequestDone()
				defer permit.Release()
			}
			// The production caller is reviewed separately: this API is only reached
			// after scope workers join. It must distinguish local from granted credit.
			a.ReleaseLocalGrowth([]string{"member"})
			want := int64(0)
			if granted {
				want = 896
			}
			if snap := a.PipelineSnapshot(); snap.GrowthBytes != want {
				t.Fatalf("joined local refund changed granted lifetime: got%d want%d", snap.GrowthBytes, want)
			}
			a.ReleaseBudgetEntry("member")
			if a.PipelineSnapshot().GrowthBytes != 0 {
				t.Fatal("terminal member retained Q")
			}
		})
	}
}

func TestRubyHandoffAcceptancePersistedBorrowerKeepsQUntilTerminal(t *testing.T) {
	for _, lowered := range []bool{false, true} {
		name := "ordinary_no_debt"
		if lowered {
			name = "lowered_episode"
		}
		t.Run(name, func(t *testing.T) { runHandoffPersistedBorrower(t, lowered) })
	}
}
func runHandoffPersistedBorrower(t *testing.T, lowered bool) {
	ctx, client, store, scope, seg := fixture(t)
	currentExecution(t, ctx, client, scope)
	r, h, c := rawRequest(scope, seg)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.HandoffResponse(ctx, h); err != nil {
		t.Fatal(err)
	}
	cursor, err := store.LoadCursor(ctx, scope, seg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteMember(ctx, MemberPromotion{Scope: scope, RequestID: r.ID, Cursor: 0, Candidate: &c, Next: cursor}); err != nil {
		t.Fatal(err)
	}
	if lowered {
		// A second real reserved/sent member is historic inventory outside the
		// selected borrower. It forces recovery debt rather than ordinary Q use.
		late := scope
		late.RoundID = client.JobRound.Create().SetJobID(scope.JobID).SetJobResourceID(scope.JobResourceID).SetRoundIndex(1).SetMode("translate").SetStatus("running").SaveX(ctx).ID
		if _, err := store.SealRound(ctx, late, []int{seg.ID}); err != nil {
			t.Fatal(err)
		}
		r2, _, _ := rawRequest(late, seg)
		r2.ID = "other-sent-inventory"
		if err := store.ReserveRequest(ctx, r2); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordRequestForScope(ctx, late, r2.ID, RequestResult{State: "sent"}); err != nil {
			t.Fatal(err)
		}
	}
	limits := backend.PipelineBudgetLimits{Segments: 4, Candidates: 16 << 10, Item: 1024, Responses: 1024, Handoff: 64 << 10}
	if lowered {
		limits.Segments = 1
		limits.Candidates = 1
	}
	state, err := store.BeginRuntimeProgress(ctx, scope.JobID, c.ID, r.ID, scope.ResourceID, 0, limits)
	if err != nil {
		t.Fatal(err)
	}
	if state.ProgressID != c.ID {
		t.Fatalf("borrower fixture debt classification: lowered%v state%+v", lowered, state)
	}
	a := acceptanceGrowthAdmission(t)
	if err := a.RestoreRuntimeState(state); err != nil {
		t.Fatal(err)
	}
	if err := a.RestoreBudgetEntry(c.ID, backend.PipelineBudgetEntry{ResourceID: scope.ResourceID, RoundID: scope.RoundID, Count: 1, Bytes: 128, Durable: true, Progress: true}); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.ReserveCandidateGrowth(c.ID); err != nil || !ok {
		t.Fatalf("borrower Q reserve: %v %v", ok, err)
	}
	client.Job.UpdateOneID(scope.JobID).SetStatus("pausing").SetPauseRequested(true).ExecX(ctx)
	a.ReleaseLocalGrowth([]string{c.ID})
	if snap := a.PipelineSnapshot(); snap.GrowthBytes != 896 || snap.ProgressID != c.ID {
		t.Errorf("joined Pause refunded persisted borrower Q: %+v", snap)
	}
	headers, _, err := store.LoadCandidateHeaders(ctx, scope.JobID, 0, 16)
	if err != nil || len(headers) != 1 {
		t.Fatalf("restart headers: %+v %v", headers, err)
	}
	if !headers[0].GrowthReserved {
		t.Error("restart header lost persisted borrower full Q reservation")
	}
	reloaded, err := NewStore(client).LoadRuntimeState(ctx, scope.JobID)
	if err != nil || reloaded.ProgressID != c.ID || reloaded != state {
		t.Fatalf("Pause/restart rolled borrower episode: %+v/%+v %v", state, reloaded, err)
	}
	if _, err := store.BeginRuntimeProgress(ctx, scope.JobID, "second-owner", r.ID, scope.ResourceID, 0, limits); err == nil {
		t.Error("Pause admitted a second progress owner")
	}
	t.Logf("persisted_borrower=%s Q=%d progress=%s restart_growth_reserved=%v", c.ID, limits.Item, state.ProgressID, headers[0].GrowthReserved)
}
