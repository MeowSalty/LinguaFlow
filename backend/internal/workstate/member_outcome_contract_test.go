package workstate

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
)

func TestMemberOutcomeReplayRejectsChangedTypedFacts(t *testing.T) {
	ctx, client, store, scope, seg := fixture(t)
	currentExecution(t, ctx, client, scope)
	req, capture, _ := rawRequest(scope, seg)
	if err := store.ReserveRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CaptureResponse(ctx, capture); err != nil {
		t.Fatal(err)
	}
	publishFixtureBusiness(t, ctx, store, req)
	next, err := store.LoadCursor(ctx, scope, seg.ID)
	if err != nil {
		t.Fatal(err)
	}
	key := requestMemberKey(req, 0)
	original := MemberPromotion{Scope: scope, Key: key, Next: next, Outcome: responsecontract.MemberOutcome{Key: key, Reason: responsecontract.MemberTooLarge, Limit: 4096, Observed: 8192}}
	if _, err = store.PromoteMember(ctx, original); err != nil {
		t.Fatal(err)
	}
	baseline := client.WorkResponseMember.Query().OnlyX(ctx)
	for _, mutate := range []func(*MemberPromotion){func(p *MemberPromotion) { p.Outcome.Reason = responsecontract.MemberInvalid }, func(p *MemberPromotion) { p.Outcome.Limit++ }, func(p *MemberPromotion) { p.Outcome.Observed++ }, func(p *MemberPromotion) { p.Key.CaptureEpoch++; p.Outcome.Key = p.Key }, func(p *MemberPromotion) { p.Outcome.Accepted = true }} {
		changed := original
		mutate(&changed)
		if _, err = store.PromoteMember(ctx, changed); !errors.Is(err, ErrManifest) {
			t.Fatalf("changed typed replay accepted: %+v err=%v", changed.Outcome, err)
		}
		actual := client.WorkResponseMember.Query().OnlyX(ctx)
		if actual.OutcomeKind != baseline.OutcomeKind || actual.OutcomeLimit != baseline.OutcomeLimit || actual.OutcomeObserved != baseline.OutcomeObserved || actual.Failure != baseline.Failure || client.UsageRecord.Query().CountX(ctx) != 1 {
			t.Fatal("rejected replay changed authoritative outcome or usage")
		}
	}
}

func TestMemberOutcomeUnknownACKRejectsChangedCanonicalIdentity(t *testing.T) {
	for _, field := range []string{"retry_epoch", "response_format_version", "resource_id", "job_round_id", "promotion_count"} {
		t.Run(field, func(t *testing.T) { testMemberOutcomeUnknownIdentity(t, field) })
	}
}

func TestMemberPromotionUnknownACKRejectsFullStableIdentity(t *testing.T) {
	for _, field := range []string{"promotion_count_increased", "snapshot_digest", "freeze_digest"} {
		t.Run(field, func(t *testing.T) { testMemberOutcomeUnknownIdentity(t, field) })
	}
}

func testMemberOutcomeUnknownIdentity(t *testing.T, field string) {
	t.Helper()
	ctx, client, store, scope, seg, _ := memberDeltaFixture(t)
	currentExecution(t, ctx, client, scope)
	_, _, _, otherScope, _ := seedFixture(t, ctx, client)
	req, capture, candidate := rawRequest(scope, seg)
	budget, owned := memberDeltaBudget(t, ctx, client, scope)
	if err := store.ReserveRequest(owned, req); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CaptureResponse(owned, capture); err != nil {
		t.Fatal(err)
	}
	publishFixtureBusiness(t, owned, store, req)
	row := client.WorkRequest.Query().Where(workrequest.IdentityEQ(req.ID)).OnlyX(ctx)
	databaseField := field
	var original, corrupted any
	switch field {
	case "retry_epoch":
		original = int64(row.RetryEpoch)
		corrupted = int64(row.RetryEpoch) + 1
	case "response_format_version":
		original = int64(row.ResponseFormatVersion)
		corrupted = 1
	case "resource_id":
		original = int64(row.ResourceID)
		corrupted = int64(otherScope.ResourceID)
	case "job_round_id":
		original = int64(row.JobRoundID)
		corrupted = int64(otherScope.RoundID)
	case "promotion_count":
		original = int64(row.PromotionCount)
		corrupted = 0
	case "promotion_count_increased":
		databaseField = "promotion_count"
		original, corrupted = row.PromotionCount, row.PromotionCount+1
	case "snapshot_digest":
		original, corrupted = row.SnapshotDigest, "changed-snapshot-digest"
	case "freeze_digest":
		original, corrupted = row.FreezeDigest, "changed-freeze-digest"
	}
	var armed atomic.Bool
	armed.Store(true)
	ack := errors.New("committed promotion acknowledgement lost")
	client.WorkRequest.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(mctx context.Context, m ent.Mutation) (ent.Value, error) {
			if _, ok := m.Field("promotion_cursor"); ok && armed.CompareAndSwap(true, false) {
				tx, e := m.(*ent.WorkRequestMutation).Tx()
				if e != nil {
					return nil, e
				}
				tx.OnCommit(func(next ent.Committer) ent.Committer {
					return ent.CommitFunc(func(cctx context.Context, tx *ent.Tx) error {
						if e := next.Commit(cctx, tx); e != nil {
							return e
						}
						if _, e := client.ExecContext(context.Background(), "UPDATE work_requests SET "+databaseField+"=? WHERE id=?", corrupted, row.ID); e != nil {
							return e
						}
						return ack
					})
				})
			}
			return next.Mutate(mctx, m)
		})
	})
	next, err := store.LoadCursor(ctx, scope, seg.ID)
	if err != nil {
		t.Fatal(err)
	}
	promotion := fixturePromotion(MemberPromotion{Scope: scope, Key: requestMemberKey(req, 0), Candidate: &candidate, Next: next})
	before := budget.Root.Snapshot(budget.Scope.ActorID).Instance
	if _, err = store.PromoteMember(owned, promotion); !errors.Is(err, ErrManifest) {
		t.Fatalf("unknown ACK trusted changed %s: %v", field, err)
	}
	if n := client.WorkCandidate.Query().CountX(ctx); n != 1 {
		t.Fatalf("unknown identity candidate_count=%d expected=1", n)
	}
	if n := client.UsageRecord.Query().CountX(ctx); n != 1 {
		t.Fatalf("unknown identity usage_count=%d expected=1", n)
	}
	if after := budget.Root.Snapshot(budget.Scope.ActorID).Instance.LogicalStorageBytes; after < before.LogicalStorageBytes {
		t.Fatalf("unknown identity refunded pending storage: before=%d after=%d", before.LogicalStorageBytes, after)
	}
	retained := budget.Root.Snapshot(budget.Scope.ActorID).Instance
	if !budget.pending || budget.pendingConfirmation == nil {
		t.Fatal("unknown canonical identity lost pending confirmation")
	}
	if err = budget.Settle(ctx); !errors.Is(err, ErrManifest) {
		t.Fatalf("settlement accepted changed canonical identity: %v", err)
	}
	if err = budget.Close(); !errors.Is(err, ErrManifest) {
		t.Fatalf("close accepted changed canonical identity: %v", err)
	}
	entered := false
	if err = Transaction(owned, client, func(*ent.Client) error { entered = true; return nil }); !errors.Is(err, ErrManifest) || entered {
		t.Fatalf("later transaction bypassed pending identity: entered=%t err=%v", entered, err)
	}
	if budget.Root.Snapshot(budget.Scope.ActorID).Instance != retained || !budget.pending || budget.closed {
		t.Fatal("failed pending confirmation changed conservative fees or lifecycle")
	}
	if _, err = client.ExecContext(context.Background(), "UPDATE work_requests SET "+databaseField+"=? WHERE id=?", original, row.ID); err != nil {
		t.Fatal(err)
	}
	if err = budget.Settle(ctx); err != nil || budget.pending || budget.pendingConfirmation != nil {
		t.Fatalf("repaired canonical identity failed same-attempt settlement: %v", err)
	}
	if _, err = store.PromoteMember(owned, promotion); err != nil {
		t.Fatal("restored authoritative identity did not reconcile", err)
	}
	memberDeltaAudit(t, ctx, budget)
	if client.WorkCandidate.Query().CountX(ctx) != 1 || client.UsageRecord.Query().CountX(ctx) != 1 {
		t.Fatal("reconfirmation duplicated committed member")
	}
	if err = budget.Close(); err != nil {
		t.Fatal("repaired canonical identity failed close", err)
	}
}

func TestMemberCaptureArchiveBusinessUnknownACKPreservesIdentityProof(t *testing.T) {
	for _, operation := range []string{"capture", "archive", "business"} {
		t.Run(operation, func(t *testing.T) { testMemberUnknownPublication(t, operation, false) })
	}
}

func TestMemberUnknownACKProofAcceptsPromotedSuccessor(t *testing.T) {
	for _, operation := range []string{"capture", "archive", "business"} {
		t.Run(operation, func(t *testing.T) { testMemberUnknownPublication(t, operation, true) })
	}
}

func testMemberUnknownPublication(t *testing.T, operation string, successor bool) {
	t.Helper()
	ctx, client, store, scope, seg, _ := memberDeltaFixture(t)
	currentExecution(t, ctx, client, scope)
	req, capture, _ := rawRequest(scope, seg)
	budget, owned := memberDeltaBudget(t, ctx, client, scope)
	if err := store.ReserveRequest(owned, req); err != nil {
		t.Fatal(err)
	}
	key := requestMemberKey(req, 0)
	model := []byte(`{"target":"translated"}`)
	digest := payloadDigest(model)
	publication := OutputPublication{Key: key, Members: []OutputMember{{Key: key, Payload: model, Bytes: int64(len(model)), Digest: digest}}, Next: 1, Digest: payloadDigest([]byte(digest)), Complete: true, Glossary: []byte("[]")}
	if operation != "capture" {
		if _, err := store.CaptureResponse(owned, capture); err != nil {
			t.Fatal(err)
		}
	}
	if operation == "business" {
		if _, err := store.PublishOutputs(owned, publication); err != nil {
			t.Fatal(err)
		}
	}
	row := client.WorkRequest.Query().Where(workrequest.IdentityEQ(req.ID)).OnlyX(ctx)
	field := map[string]string{"capture": "capture_payload", "archive": "archive_cursor", "business": "promotion_state"}[operation]
	var commitCharge int64
	var armed atomic.Bool
	armed.Store(true)
	t.Cleanup(func() {
		_, _ = client.ExecContext(context.Background(), "UPDATE work_requests SET retry_epoch=? WHERE id=?", row.RetryEpoch, row.ID)
	})
	client.WorkRequest.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(mctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if _, ok := mutation.Field(field); ok && armed.CompareAndSwap(true, false) {
				tx, err := mutation.(*ent.WorkRequestMutation).Tx()
				if err != nil {
					return nil, err
				}
				tx.OnCommit(func(next ent.Committer) ent.Committer {
					return ent.CommitFunc(func(cctx context.Context, tx *ent.Tx) error {
						if err := next.Commit(cctx, tx); err != nil {
							return err
						}
						commitCharge = budget.Root.Snapshot(budget.Scope.ActorID).Instance.LogicalStorageBytes
						if _, err := client.ExecContext(context.Background(), "UPDATE work_requests SET retry_epoch=? WHERE id=?", row.RetryEpoch+1, row.ID); err != nil {
							return err
						}
						return errors.New("actual committed member acknowledgement lost")
					})
				})
			}
			return next.Mutate(mctx, mutation)
		})
	})
	apply := func() error {
		switch operation {
		case "capture":
			_, err := store.CaptureResponse(owned, capture)
			return err
		case "archive":
			_, err := store.PublishOutputs(owned, publication)
			return err
		default:
			return store.PublishMemberBusiness(owned, req.ID, nil)
		}
	}
	before := budget.Root.Snapshot(budget.Scope.ActorID).Instance.LogicalStorageBytes
	if err := apply(); !errors.Is(err, ErrManifest) {
		t.Fatalf("%s unknown ACK accepted changed epoch: %v", operation, err)
	}
	retained := budget.Root.Snapshot(budget.Scope.ActorID).Instance
	if retained.LogicalStorageBytes < before || retained.LogicalStorageBytes != commitCharge || !budget.pending || budget.pendingConfirmation == nil || client.UsageRecord.Query().CountX(ctx) != 1 || client.WorkCandidate.Query().CountX(ctx) != 0 {
		t.Fatalf("%s unknown ACK changed fees/accounting: before=%d after=%d pending=%t usage=%d candidates=%d", operation, before, retained.LogicalStorageBytes, budget.pending, client.UsageRecord.Query().CountX(ctx), client.WorkCandidate.Query().CountX(ctx))
	}
	if err := budget.Settle(ctx); !errors.Is(err, ErrManifest) {
		t.Fatalf("%s settlement bypassed identity: %v", operation, err)
	}
	if err := budget.Close(); !errors.Is(err, ErrManifest) {
		t.Fatalf("%s close bypassed identity: %v", operation, err)
	}
	entered := false
	if err := Transaction(owned, client, func(*ent.Client) error { entered = true; return nil }); !errors.Is(err, ErrManifest) || entered {
		t.Fatalf("%s later transaction bypassed proof: entered=%t err=%v", operation, entered, err)
	}
	if budget.Root.Snapshot(budget.Scope.ActorID).Instance != retained || budget.closed {
		t.Fatalf("%s failed proof refunded ownership", operation)
	}
	if _, err := client.ExecContext(ctx, "UPDATE work_requests SET retry_epoch=? WHERE id=?", row.RetryEpoch, row.ID); err != nil {
		t.Fatal(err)
	}
	var side *StorageBudget
	if successor {
		// A separate joined writer can advance the canonical response while
		// the original invocation still retains its unknown-ACK proof.
		side = &StorageBudget{Root: budget.Root, Scope: budget.Scope, Source: budget.Source}
		sideCtx := WithStorageBudget(ctx, side)
		if operation == "capture" {
			if _, err := store.PublishOutputs(sideCtx, publication); err != nil {
				t.Fatal(err)
			}
		}
		if operation != "business" {
			if err := store.PublishMemberBusiness(sideCtx, req.ID, nil); err != nil {
				t.Fatal(err)
			}
		}
		next, err := store.LoadCursor(ctx, scope, seg.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.PromoteMember(sideCtx, MemberPromotion{Scope: scope, Key: key, Next: next, Outcome: responsecontract.MemberOutcome{Key: key, Reason: responsecontract.MemberInvalid}})
		if err != nil {
			t.Fatal("legitimate successor did not promote", err)
		}
		if h, err := store.ResponseHeader(ctx, req.ID); err != nil || h.Phase != responsecontract.Promoted || h.PublicKind != responsecontract.PublicNone || h.PublicBytes != 0 {
			t.Fatalf("successor did not retain terminal facts: phase=%s err=%v", h.Phase, err)
		}
	}
	if err := budget.Settle(ctx); err != nil {
		t.Fatal("restored proof did not settle", err)
	}
	memberDeltaAudit(t, ctx, budget)
	if err := apply(); err != nil {
		t.Fatal("confirmed publication replay failed", err)
	}
	memberDeltaAudit(t, ctx, budget)
	if client.UsageRecord.Query().CountX(ctx) != 1 || client.WorkCandidate.Query().CountX(ctx) != 0 {
		t.Fatal("confirmed replay changed accounting")
	}
	if err := budget.Close(); err != nil {
		t.Fatal(err)
	}
	if side != nil {
		if err := side.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
