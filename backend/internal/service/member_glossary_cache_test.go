package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type memberGlossaryCacheFixture struct {
	ctx       context.Context
	env       *jobRoundTestEnv
	g         *executionGlossary
	lease     *ExecutionInputLease
	root      *resourcebudget.RootBudget
	admission *backend.RequestAdmission
	in        glossary.ResponseProcess
}

func newMemberGlossaryCacheFixture(t *testing.T) memberGlossaryCacheFixture {
	t.Helper()
	ctx := t.Context()
	env := newJobRoundTestEnv(t, nil)
	j, resources, rounds := seedJobWithRounds(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{{status: JobResourceStatusRunning, segmentCount: 1, rounds: []jobRoundSpec{{roundIndex: 0, mode: "translate", status: JobRoundStatusRunning}}}})
	jr, round := resources[0], rounds[0][0]
	resource := jr.QueryResource().OnlyX(ctx)
	seg := createTestSegment(t, env.client, resource.ID, 0, "source", nil)
	scope := workstate.Scope{JobID: j.ID, ResourceID: resource.ID, JobResourceID: jr.ID, RoundID: round.ID, SourceGeneration: resource.SourceGeneration, SourceRevisionID: resource.CurrentSourceRevisionID}
	store := workstate.NewStore(env.client)
	if _, err := store.SealRound(ctx, scope, []int{seg.ID}); err != nil {
		t.Fatal(err)
	}
	digest := func(p []byte) string { h := sha256.Sum256(p); return hex.EncodeToString(h[:]) }
	snapshot, err := workstate.ExecutionSnapshotDigest(j.ExecutionConfig)
	if err != nil {
		t.Fatal(err)
	}
	key := workstate.MemberKey{RequestID: "member-cache", Ordinal: 0}
	input := []byte(`{"source":"source"}`)
	member := workstate.PromotionMember{SegmentID: seg.ID, WorkID: workstate.WorkIdentity(scope, seg.ID), CandidateID: "member-candidate", BaselineVersion: seg.ContentVersion, BaselineTarget: seg.TargetText, BaselineStatus: string(seg.Status), SourceGeneration: scope.SourceGeneration, SourceRevisionID: scope.SourceRevisionID}
	if err = store.ReserveRequest(ctx, workstate.Request{ID: key.RequestID, Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "input", MainAttempt: true, MaxMainAttempts: 2, SnapshotDigest: snapshot, BusinessBound: 4096, InputMembers: []workstate.InputMember{{Key: key, PromotionMember: member, PromptID: "1", Payload: input, Digest: digest(input)}}}); err != nil {
		t.Fatal(err)
	}
	capture := []byte("captured model response")
	if err = store.CaptureResponse(ctx, workstate.CapturedResponse{ResponseHeader: workstate.ResponseHeader{RequestID: key.RequestID, Scope: scope}, Payload: capture, Result: workstate.RequestResult{UsageKnown: true, InputTokens: 7, OutputTokens: 3}}); err != nil {
		t.Fatal(err)
	}
	output := []byte(`{"target":"translated"}`)
	if err = store.PublishOutputs(ctx, workstate.OutputPublication{Key: key, Members: []workstate.OutputMember{{Key: key, Payload: output, Bytes: int64(len(output)), Digest: digest(output)}}, Next: 1, Digest: digest(output), Complete: true}); err != nil {
		t.Fatal(err)
	}
	// A nonempty old cache makes Close exercise replacement and the original
	// oldCount slicing boundary, rather than an empty-dictionary shortcut.
	env.client.GlossaryEntry.Create().SetProjectID(env.project.ID).SetSourceKey("existing").SetSource("existing").SetTarget("old target").SaveX(ctx)
	limits := resourcebudget.Limits{MaxPayloadBytes: 1 << 20}
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: limits, Instance: limits})
	if err != nil {
		t.Fatal(err)
	}
	rootScope := resourcebudget.Scope{ActorID: *env.project.OwnerUserID, CallID: "member-cache"}
	ctx = WithExecutionInputBudget(ctx, root, rootScope, DefaultExecutionInputBytes)
	loaded, lease, err := LoadExecutionGlossary(ctx, env.client, env.project, true)
	if err != nil {
		t.Fatal(err)
	}
	g, ok := loaded.(*executionGlossary)
	if !ok {
		t.Fatal("missing execution glossary")
	}
	t.Cleanup(lease.Close)
	const response, wire = int64(64 << 10), int64(4096)
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err = a.AttachRootBudget(root, rootScope); err != nil {
		t.Fatal(err)
	}
	if err = a.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: 1, Candidates: response, Item: response, Responses: response, Handoff: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.PrepareMembers(key.RequestID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.ReleaseBudgetEntry(key.RequestID) })
	if ready, e := a.PrepareMain(key.RequestID, 0, 1, 0, wire, 0, false); e != nil || !ready {
		t.Fatalf("prepare %v/%v", ready, e)
	}
	permit, wait, err := a.TryAdmit(ctx, backend.RequestAdmissionIntent{RequestID: key.RequestID, RoundIndex: 0, ResourceID: 1, Stage: backend.RequestStageMain}, nil)
	if err != nil || permit == nil {
		t.Fatalf("admit %+v/%v", wait, err)
	}
	t.Cleanup(permit.Release)
	ctx = backend.WithBuildOwnership(ctx, a, "member-cache-source")
	ctx = backend.WithBuildReservation(ctx, func(n int64) error { return a.ClaimBuild("member-cache-source", n) }, func(n int64) {
		if e := a.ReleaseBuildBytes("member-cache-source", n); e != nil {
			t.Error(e)
		}
	})
	ctx = backend.WithRequestCredits(ctx, wire, response)
	ctx = backend.WithProviderResponseOwnership(ctx, a, key.RequestID, backend.RequestStageMain)
	ctx, err = backend.WithProviderResponseAllocation(ctx, response)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e := backend.CloseProviderResponseAllocation(ctx); e != nil {
			t.Error(e)
		}
	})
	in := glossary.ResponseProcess{RequestID: key.RequestID, InputDigest: digest(capture), Entries: []glossary.Entry{{Source: "new term", Target: "new target", Notes: "private dictionary backing"}}, ItemLimit: 4096, BusinessLimit: 4096}
	in.Publish = func(ctx context.Context, out glossary.ResponseBusiness, _ func()) error {
		return store.PublishMemberBusiness(ctx, key.RequestID, out.Receipt)
	}
	return memberGlossaryCacheFixture{ctx, env, g, lease, root, a, in}
}

func TestMemberGlossaryCachePaidRollback(t *testing.T) {
	f := newMemberGlossaryCacheFixture(t)
	before := backend.RejectionEvidenceFromContext(f.ctx)
	if !before.MeterCreditKnown || !before.LedgerCreditKnown || before.MeterPaid == 0 || before.MeterWire == 0 {
		t.Fatalf("missing real paid/wire credit: %+v", before)
	}
	owners := f.admission.BuildAllocationSnapshot()
	leaseBefore := f.lease.usage
	actor := *f.env.project.OwnerUserID
	pressure := resourcebudget.Scope{ActorID: actor, CallID: "cache-pressure"}
	n := (1 << 20) - f.root.Snapshot(actor).Instance.PayloadBytes
	if err := f.root.Claim(pressure, "pressure", resourcebudget.Vector{PayloadBytes: n}); err != nil {
		t.Fatal(err)
	}
	filler := make([]byte, int(n))
	t.Cleanup(func() {
		filler = nil
		if err := f.root.Transfer(pressure, "pressure", resourcebudget.Vector{}); err != nil {
			t.Error(err)
		}
	})
	if len(filler) > 0 {
		filler[0] = 1
	}
	rootBefore := f.root.Snapshot(actor).Instance
	fault := errors.New("publication failed after private cache prepared")
	f.in.Publish = func(context.Context, glossary.ResponseBusiness, func()) error { return fault }
	out, err := f.g.ProcessResponse(f.ctx, f.in)
	out.Release()
	if !errors.Is(err, fault) {
		t.Fatalf("rollback error: %v", err)
	}
	after := backend.RejectionEvidenceFromContext(f.ctx)
	if after.LiveBytes != before.LiveBytes || after.MeterPaid != before.MeterPaid || after.LedgerPaid != before.LedgerPaid || after.MeterWire != before.MeterWire || after.LedgerWire != before.LedgerWire {
		t.Fatalf("rollback consumed paid source: before=%+v after=%+v", before, after)
	}
	if !reflect.DeepEqual(owners, f.admission.BuildAllocationSnapshot()) || f.lease.usage != leaseBefore || f.root.Snapshot(actor).Instance != rootBefore {
		t.Fatalf("rollback changed target/lease/root: owners=%v lease=%+v root=%+v", f.admission.BuildAllocationSnapshot(), f.lease.usage, f.root.Snapshot(actor).Instance)
	}
	req := f.env.client.WorkRequest.Query().OnlyX(f.ctx)
	if f.env.client.GlossaryEntry.Query().CountX(f.ctx) != 1 || len(f.g.entries) != 1 || req.PromotionState != "outputs_ready" || len(req.GlossaryReceipt) != 0 {
		t.Fatal("uncommitted dictionary or receipt escaped rollback")
	}
}

func TestMemberGlossaryCacheUnknownACKReconfirmation(t *testing.T) {
	f := newMemberGlossaryCacheFixture(t)
	var failLookup atomic.Bool
	var commits atomic.Int32
	ackFault, lookupFault := errors.New("committed ACK lost"), errors.New("first authority lookup unavailable")
	f.env.client.WorkRequest.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			if failLookup.CompareAndSwap(true, false) {
				return nil, lookupFault
			}
			return next.Query(ctx, q)
		})
	}))
	f.env.client.WorkRequest.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if _, ok := m.Field("glossary_receipt"); ok {
				tx, err := m.(*ent.WorkRequestMutation).Tx()
				if err != nil {
					return nil, err
				}
				tx.OnCommit(func(next ent.Committer) ent.Committer {
					return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
						if err := next.Commit(ctx, tx); err != nil {
							return err
						}
						commits.Add(1)
						failLookup.Store(true)
						return ackFault
					})
				})
			}
			return next.Mutate(ctx, m)
		})
	})
	leaseBefore := f.lease.usage
	out, err := f.g.ProcessResponse(f.ctx, f.in)
	out.Release()
	if !errors.Is(err, ackFault) || f.g.database.pendingCache == nil {
		t.Fatalf("unknown ACK did not park its real cache: %v", err)
	}
	if len(f.g.entries) != 1 || f.lease.usage != leaseBefore || f.env.client.GlossaryEntry.Query().CountX(f.ctx) != 2 {
		t.Fatal("parked cache visibility/lease differs from committed SQL")
	}
	first := f.env.client.WorkRequest.Query().OnlyX(f.ctx)
	if first.PromotionState != "business_ready" || len(first.GlossaryReceipt) == 0 {
		t.Fatal("missing committed business receipt")
	}
	out, err = f.g.ProcessResponse(f.ctx, f.in)
	if err != nil {
		t.Fatal(err)
	}
	out.Release()
	if f.g.database.pendingCache != nil || len(f.g.entries) != 2 || f.lease.usage.PayloadBytes <= leaseBefore.PayloadBytes || commits.Load() != 1 {
		t.Fatal("reconfirmation did not install exactly one committed cache")
	}
	adopted, paid := f.lease.usage, backend.RejectionEvidenceFromContext(f.ctx)
	out, err = f.g.ProcessResponse(f.ctx, f.in)
	if err != nil {
		t.Fatal(err)
	}
	out.Release()
	last := f.env.client.WorkRequest.Query().OnlyX(f.ctx)
	if f.lease.usage != adopted || backend.RejectionEvidenceFromContext(f.ctx).MeterPaid != paid.MeterPaid || commits.Load() != 1 || !reflect.DeepEqual(first.GlossaryReceipt, last.GlossaryReceipt) || first.InputTokens != last.InputTokens || first.OutputTokens != last.OutputTokens || first.UsageRecordID == nil || !reflect.DeepEqual(first.UsageRecordID, last.UsageRecordID) {
		t.Fatal("repeat ACK moved ownership or usage twice")
	}
}

func TestMemberGlossaryCacheCloseDuringCommit(t *testing.T) {
	f := newMemberGlossaryCacheFixture(t)
	committed, proceed := make(chan struct{}), make(chan struct{})
	var resume sync.Once
	t.Cleanup(func() { resume.Do(func() { close(proceed) }) })
	f.env.client.WorkRequest.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if _, ok := m.Field("glossary_receipt"); ok {
				tx, err := m.(*ent.WorkRequestMutation).Tx()
				if err != nil {
					return nil, err
				}
				tx.OnCommit(func(next ent.Committer) ent.Committer {
					return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
						if err := next.Commit(ctx, tx); err != nil {
							return err
						}
						close(committed)
						select {
						case <-proceed:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					})
				})
			}
			return next.Mutate(ctx, m)
		})
	})
	result := make(chan error, 1)
	go func() { out, err := f.g.ProcessResponse(f.ctx, f.in); out.Release(); result <- err }()
	select {
	case <-committed:
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not reach actual SQL commit")
	}
	closing, closed := make(chan struct{}), make(chan struct{})
	go func() { close(closing); f.lease.Close(); close(closed) }()
	<-closing
	select {
	case <-closed:
		t.Fatal("Close retired a cache whose publication was still in flight")
	default:
	}
	resume.Do(func() { close(proceed) })
	select {
	case err := <-result:
		if err != nil && !errors.Is(err, resourcebudget.ErrIdentity) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessResponse deadlocked with Close")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not join dictionary mutation")
	}
	if len(f.g.entries) != 0 || f.g.head != nil || f.g.database.pendingCache != nil || f.lease.usage != (resourcebudget.Vector{}) {
		t.Fatal("closed dictionary retained runtime backing")
	}
	owners := f.admission.BuildAllocationSnapshot()
	out, err := f.g.ProcessResponse(f.ctx, f.in)
	out.Release()
	if err == nil || !reflect.DeepEqual(owners, f.admission.BuildAllocationSnapshot()) || len(f.g.entries) != 0 {
		t.Fatal("late processing revived closed cache")
	}
	req := f.env.client.WorkRequest.Query().OnlyX(f.ctx)
	if req.PromotionState != "business_ready" || len(req.GlossaryReceipt) == 0 || f.env.client.GlossaryEntry.Query().CountX(f.ctx) != 2 {
		t.Fatal("Close lost committed durable dictionary or receipt")
	}
}

func TestMemberGlossaryCacheReceiptGuard(t *testing.T) {
	for _, oversized := range []bool{true, false} {
		name := "unpublished"
		if oversized {
			name = "actual_length"
		}
		t.Run(name, func(t *testing.T) {
			f := newMemberGlossaryCacheFixture(t)
			var payload []byte
			if oversized {
				payload = []byte(`{"oversized":"` + strings.Repeat("x", glossary.ResponseReceiptLimit) + `"}`)
			} else {
				var err error
				payload, err = json.Marshal(glossary.ResponseReceipt{Version: glossary.ResponseReceiptVersion, RequestID: f.in.RequestID, InputDigest: f.in.InputDigest, OutcomeDigest: strings.Repeat("a", 64), Added: 1})
				if err != nil {
					t.Fatal(err)
				}
			}
			req := f.env.client.WorkRequest.Query().OnlyX(f.ctx)
			if err := f.env.client.WorkRequest.UpdateOneID(req.ID).SetGlossaryReceipt(payload).Exec(f.ctx); err != nil {
				t.Fatal(err)
			}
			var oversizedRead atomic.Bool
			f.env.client.WorkRequest.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
				return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
					value, err := next.Query(ctx, q)
					if rows, ok := value.([]*ent.WorkRequest); ok {
						for _, row := range rows {
							if len(row.GlossaryReceipt) > glossary.ResponseReceiptLimit {
								oversizedRead.Store(true)
							}
						}
					}
					return value, err
				})
			}))
			owners, before := f.admission.BuildAllocationSnapshot(), f.lease.usage
			out, err := f.g.ProcessResponse(f.ctx, f.in)
			out.Release()
			if !errors.Is(err, workstate.ErrManifest) || oversizedRead.Load() {
				t.Fatalf("receipt guard read or accepted corrupt/unpublished receipt: %v read=%v", err, oversizedRead.Load())
			}
			if !reflect.DeepEqual(owners, f.admission.BuildAllocationSnapshot()) || f.lease.usage != before || len(f.g.entries) != 1 || f.env.client.GlossaryEntry.Query().CountX(f.ctx) != 1 {
				t.Fatal("receipt rejection changed dictionary or owners")
			}
		})
	}
}

func TestMemberGlossaryCacheReceiptIntegrityAfterCommit(t *testing.T) {
	for _, kind := range []string{"length", "same_length"} {
		t.Run(kind, func(t *testing.T) {
			f := newMemberGlossaryCacheFixture(t)
			out, err := f.g.ProcessResponse(f.ctx, f.in)
			if err != nil {
				t.Fatal(err)
			}
			out.Release()
			row := f.env.client.WorkRequest.Query().OnlyX(f.ctx)
			bad := append([]byte(nil), row.GlossaryReceipt...)
			if kind == "length" {
				bad = append(bad, ' ')
			} else {
				bad = []byte(strings.Replace(string(bad), `"Added":1`, `"Added":2`, 1))
				if reflect.DeepEqual(bad, row.GlossaryReceipt) {
					t.Fatal("receipt mutation did not change actual bytes")
				}
			}
			if _, err := f.env.client.ExecContext(f.ctx, "UPDATE work_requests SET glossary_receipt=? WHERE id=?", string(bad), row.ID); err != nil {
				t.Fatal(err)
			}
			owners, before := f.admission.BuildAllocationSnapshot(), f.lease.usage
			out, err = f.g.ProcessResponse(f.ctx, f.in)
			out.Release()
			if !errors.Is(err, workstate.ErrManifest) {
				t.Fatalf("committed damaged receipt was accepted: %v", err)
			}
			if !reflect.DeepEqual(owners, f.admission.BuildAllocationSnapshot()) || f.lease.usage != before || len(f.g.entries) != 2 || f.env.client.GlossaryEntry.Query().CountX(f.ctx) != 2 {
				t.Fatal("receipt rejection changed cache, dictionary or owners")
			}
		})
	}
}
