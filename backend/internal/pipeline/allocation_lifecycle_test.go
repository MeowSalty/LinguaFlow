package pipeline

import (
	"context"
	"errors"
	"fmt"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestAllocationMemoryHistorySurvivesPayloadUntilClose(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	s := NewMemoryRoundStore(nil)
	defer s.Close()
	ctx := r.BuildContext(WithExecutionRuntime(context.Background(), r), "build:request")
	header := memberFixtureReserve(t, ctx, s, "request", 1024, memberFixtureInputs(1))
	history := s.requests[header.ID].historyOwner
	before := r.Admission.BuildBytes(history)
	if before == 0 {
		t.Fatal("unfunded history")
	}
	header = memberFixtureOutputs(t, ctx, s, header, []archivedOutput{{Missing: true}}, nil)
	if _, err := s.PublishBusiness(ctx, header.ID, glossary.ResponseBusiness{}, nil); err != nil {
		t.Fatal(err)
	}
	before = r.Admission.BuildBytes(history)
	if _, err := s.PromoteMember(ctx, archiveMemberKey(header, 0), nil, WorkCursor{}, MemberOutcome{Key: archiveMemberKey(header, 0), Reason: responsecontract.MemberMissing}); err != nil {
		t.Fatal(err)
	}
	if err := r.Admission.ReleaseBuild("build:request"); err != nil {
		t.Fatal(err)
	}
	if len(s.requests) != 1 || r.Admission.BuildBytes(history) != before {
		t.Fatal("history refunded at payload completion")
	}
	if s.requests[header.ID].intent.Indices != nil || s.requests[header.ID].capture != nil || len(s.requests[header.ID].inputs) != 0 || len(s.requests[header.ID].outputs) != 0 {
		t.Fatal("payload survived promotion")
	}
	r.Close()
	if r.Admission.BuildBytes(history) != before {
		t.Fatal("runtime close refunded retained store")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if r.Admission.BuildBytes(history) != 0 || s.requests != nil {
		t.Fatal("history survived store close")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAllocationMemoryCloseRetiresDestroyedCandidateInventory(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	root, err := resourcebudget.New(resourcebudget.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "memory-close"}); err != nil {
		t.Fatal(err)
	}
	s := NewMemoryRoundStore(nil)
	defer s.Close()
	ctx := r.BuildContext(WithExecutionRuntime(context.Background(), r), "build:request")
	if err = s.Reserve(ctx, RequestIntent{ID: "request", InputDigest: "input"}); err != nil {
		t.Fatal(err)
	}
	c := &Candidate{ID: "candidate", DTOVersion: CandidateDTOVersion, Version: 1}
	if err = s.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if !r.Window.TryReserve("reservation", 1) {
		t.Fatal("candidate reservation failed")
	}
	// Transfer acknowledges a promotion whose exact serialized payload was
	// already reserved; a member reservation alone cannot create a candidate.
	if ready, err := r.Admission.BeginPromotion("reservation", c.ID, c.StoredBytes, false); err != nil || !ready {
		t.Fatalf("candidate promotion pregrant: ready=%t err=%v bytes=%d", ready, err, c.StoredBytes)
	}
	if err = r.Window.Transfer("reservation", c.ID, c.StoredBytes); err != nil {
		t.Fatal(err)
	}
	if err = r.Admission.ClaimBuild("candidate:"+c.ID, c.StoredBytes); err != nil {
		t.Fatal(err)
	}
	c = nil
	if err = r.Admission.ReleaseBuild("build:request"); err != nil {
		t.Fatal(err)
	}
	if got := root.Snapshot(1).Instance; got.UnconfirmedMembers != 1 || got.PayloadBytes == 0 {
		t.Fatalf("candidate was not live before close: %+v", got)
	}
	r.Close()
	if root.Snapshot(1).Instance.UnconfirmedMembers != 1 {
		t.Fatal("runtime close refunded the still-retained memory candidate")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := root.Snapshot(1).Instance; got != (resourcebudget.Vector{}) {
		t.Fatalf("destroyed memory candidate inventory survived close: %+v", got)
	}
}

func TestAllocationMemoryCloseRetiresEachOriginalRuntime(t *testing.T) {
	root, err := resourcebudget.New(resourcebudget.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	s := NewMemoryRoundStore(nil)
	defer s.Close()
	for i := 1; i <= 2; i++ {
		r := testExecutionRuntime(t, 1)
		defer r.Close()
		id := fmt.Sprintf("runtime-%d", i)
		if err = r.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: i, CallID: id}); err != nil {
			t.Fatal(err)
		}
		ctx := r.BuildContext(WithExecutionRuntime(context.Background(), r), "build:"+id)
		if err = s.Reserve(ctx, RequestIntent{ID: id, InputDigest: "input"}); err != nil {
			t.Fatal(err)
		}
		c := &Candidate{ID: id, DTOVersion: CandidateDTOVersion, Version: 1}
		if err = s.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		if !r.Window.TryReserve("reservation:"+id, 1) {
			t.Fatal("candidate reservation failed")
		}
		if ready, err := r.Admission.BeginPromotion("reservation:"+id, id, c.StoredBytes, false); err != nil || !ready {
			t.Fatalf("candidate promotion pregrant: ready=%t err=%v bytes=%d", ready, err, c.StoredBytes)
		}
		if err = r.Window.Transfer("reservation:"+id, id, c.StoredBytes); err != nil {
			t.Fatal(err)
		}
		if err = r.Admission.ClaimBuild("candidate:"+id, c.StoredBytes); err != nil {
			t.Fatal(err)
		}
		c = nil
		if err = r.Admission.ReleaseBuild("build:" + id); err != nil {
			t.Fatal(err)
		}
		r.Close()
	}
	if got := root.Snapshot(1).Instance; got.UnconfirmedMembers != 2 {
		t.Fatalf("runtime close retired retained source owners: %+v", got)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := root.Snapshot(1).Instance; got != (resourcebudget.Vector{}) {
		t.Fatalf("close used only the last runtime: %+v", got)
	}
}

func TestAllocationDocumentReplacementPreservesBorrowAndReleasesOldFields(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	doc := &Document{Segments: []Segment{{}}}
	var heldIssueOwner string
	for round := 0; round < 4; round++ {
		owner := fmt.Sprintf("candidate:round%d", round)
		ctx := r.BuildContext(context.Background(), owner)
		b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		previous := doc.Segments[0]
		result := TranslatedSegment{Issues: previous.Issues, Meta: previous.Meta, Protected: previous.Protected}
		if err = b.ReserveAllocation(32, 1); err != nil {
			t.Fatal(err)
		}
		result.TargetText = strings.Repeat(string(rune('a'+round)), 32)
		if round == 0 {
			if err = b.CloneInto([]qa.QualityIssue{{Code: "kept", Message: "owned message"}}, &result.Issues); err != nil {
				t.Fatal(err)
			}
		}
		old := r.documentOwners[documentSlot{doc, 0}]
		c := &Candidate{Index: 0, Segment: Segment{Status: "translated"}}
		if err = installConfirmedFields(ctx, r, doc, c, result, false, owner, previous, nil); err != nil {
			t.Fatal(err)
		}
		fields := r.documentOwners[documentSlot{doc, 0}]
		if round == 0 {
			heldIssueOwner = fields[1]
		} else if fields[1] != heldIssueOwner || r.Admission.BuildBytes(old[0]) != 0 {
			t.Fatal("replacement refunded borrow or retained old target")
		}
		if r.Admission.BuildBytes(heldIssueOwner) == 0 {
			t.Fatal("borrowed issues lost their owner")
		}
		if err = r.Admission.ReleaseBuild(owner); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.CloseDocument(doc); err != nil {
		t.Fatal(err)
	}
	if doc.Segments != nil || len(r.documentOwners) != 0 || r.Admission.BuildBytes(heldIssueOwner) != 0 || r.Admission.BuildBytes("document-owner-table") != 0 {
		t.Fatal("closed document retained backing")
	}
}

func TestAllocationDocumentFailedTransferPreservesAllOwners(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reuse-%v", reuse), func(t *testing.T) {
			r := testExecutionRuntime(t, 1)
			defer r.Close()
			doc := &Document{Segments: []Segment{{Target: "previous"}}}
			source := "candidate:failed-transfer"
			ctx := r.BuildContext(context.Background(), source)
			b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err = b.ReserveAllocation(16, 1); err != nil {
				t.Fatal(err)
			}
			key := documentSlot{doc, 0}
			if reuse {
				r.documentOwners = map[documentSlot][4]string{key: {"previous-target", "document:" + source + ":0:1"}}
			}
			previous := r.documentOwners[key]
			installed := false
			err = r.installDocumentBacking(b, doc, 0, source, DocumentAllocationPlan{TargetBytes: 8, IssuesBytes: 32}, 0, func() {
				installed = true
				doc.Segments[0].Target = "replacement"
			})
			if !errors.Is(err, backend.ErrBuildCapacity) || installed || doc.Segments[0].Target != "previous" {
				t.Fatalf("failed transfer installed fields: installed=%v doc=%+v err=%v", installed, doc.Segments[0], err)
			}
			if r.Admission.BuildBytes(source) != 16 || r.Admission.BuildBytes("document:"+source+":0:0") != 0 || r.documentOwners[key] != previous {
				t.Fatal("failed transfer changed source, destination or installed owners")
			}
			if err = r.Admission.ReleaseBuild(source); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAllocationCompletedAuxHistoryDropsBorrowedInputs(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	s := NewMemoryRoundStore(nil)
	defer s.Close()
	ctx := r.BuildContext(WithExecutionRuntime(context.Background(), r), "build:aux")
	intent := RequestIntent{ID: "aux", Stage: backend.RequestStageAlignment, InputDigest: "input", Indices: []int{0}, Members: []RequestMember{{Index: 0, CandidateID: "candidate"}}}
	if err := s.Reserve(ctx, intent); err != nil {
		t.Fatal(err)
	}
	record := s.requests[intent.ID]
	before := r.Admission.BuildBytes(record.historyOwner)
	if err := s.Record(ctx, intent.ID, RequestRecord{State: "completed"}); err != nil {
		t.Fatal(err)
	}
	if record.intent.Indices != nil || record.intent.Members != nil || record.intent.ID != intent.ID || record.intent.InputDigest != intent.InputDigest || r.Admission.BuildBytes(record.historyOwner) != before {
		t.Fatal("completed auxiliary history retained input backing or refunded audit identity")
	}
}

func TestAllocationMainProducerDropsTransferredPayloadReferences(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	var captured *requestSession
	inner := &runtimeTestBackend{call: func(ctx context.Context) (*backend.Response, error) {
		captured, _ = ctx.Value(requestSessionKey{}).(*requestSession)
		return &backend.Response{Text: `{"translations":{"1":"translated"}}`}, nil
	}}
	doc := newTestDoc(1)
	defer r.CloseDocument(doc)
	s := NewMemoryRoundStore(nil)
	defer s.Close()
	result, err := RunStagedRound(context.Background(), Round{Concurrency: 1, Handler: stagedTestHandler(t, r, inner)}, doc, s, r, quietLogger(), nil)
	if err != nil || len(result.Resolved) != 1 || captured == nil {
		t.Fatalf("main handoff failed: result=%+v captured=%v err=%v", result, captured != nil, err)
	}
	if captured.rawResponse != nil || captured.intent.InputMembers != nil || captured.intent.Indices != nil || captured.intent.Members != nil {
		t.Fatal("retained provider context still references transferred producer payload")
	}
}

type allocationOrderedDocumentStore struct {
	*MemoryRoundStore
	installed <-chan struct{}
}

func (s *allocationOrderedDocumentStore) ValidateCandidate(ctx context.Context, c *Candidate) (bool, error) {
	if c.Index == 1 {
		select {
		case <-s.installed:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return true, nil
}

func TestAllocationDocumentInstallFailureKeepsCommittedSiblingAndUsage(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	doc := newTestDoc(2)
	doc.Segments[1].Target = "previous"
	defer r.CloseDocument(doc)
	installed := make(chan struct{})
	s := &allocationOrderedDocumentStore{installed: installed}
	s.MemoryRoundStore = NewMemoryRoundStore(func(ctx context.Context, batch BatchResult) error {
		b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
		if err != nil {
			return err
		}
		if err = b.ReserveAllocation(12, 1); err != nil {
			return err
		}
		idx := batch.Segments[0].Index
		doc.Segments[idx].Target = strings.Clone("QA corrected")
		doc.Segments[idx].Status = "reviewed"
		return nil
	})
	defer s.Close()
	ctx := WithDocumentAllocationOwner(context.Background(), func(ctx context.Context, index int, owner string, plan DocumentAllocationPlan, borrowed uint8, install func()) error {
		if index == 1 {
			return backend.ErrBuildCapacity
		}
		b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
		if err != nil {
			return err
		}
		return r.installDocumentBacking(b, doc, index, owner, plan, borrowed, func() {
			install()
			close(installed)
		})
	})
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"translations":{"1":"first","2":"second"}}`, Usage: backend.Usage{PromptTokens: 3, CompletionTokens: 5}}, nil
	}}
	_, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, r, inner)}, doc, s, r, quietLogger(), nil)
	if !errors.Is(err, backend.ErrBuildCapacity) || doc.Segments[0].Target != "QA corrected" || doc.Segments[1].Target != "previous" {
		t.Fatalf("failed install changed sibling or previous document: doc=%+v err=%v", doc.Segments, err)
	}
	if len(s.confirmed) != 2 || len(s.state.Completed) != 2 || doc.InputTokens != 3 || doc.OutputTokens != 5 {
		t.Fatalf("document failure changed durable commits or usage: confirmed=%d completed=%d usage=%d/%d", len(s.confirmed), len(s.state.Completed), doc.InputTokens, doc.OutputTokens)
	}
	for id := range s.confirmed {
		if r.Admission.BuildBytes("candidate:"+id) != 0 {
			t.Fatalf("candidate owner survived local install: %s", id)
		}
	}
}

func TestAllocationDocumentPostInstallErrorKeepsInstalledFields(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	doc := newTestDoc(1)
	doc.Segments[0].Target = "previous"
	defer r.CloseDocument(doc)
	s := NewMemoryRoundStore(func(ctx context.Context, _ BatchResult) error {
		b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
		if err != nil {
			return err
		}
		if err = b.ReserveAllocation(12, 1); err != nil {
			return err
		}
		doc.Segments[0].Target = strings.Clone("QA corrected")
		return nil
	})
	defer s.Close()
	ctx := WithDocumentAllocationOwner(context.Background(), func(ctx context.Context, index int, owner string, plan DocumentAllocationPlan, borrowed uint8, install func()) error {
		b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
		if err != nil {
			return err
		}
		if err = r.installDocumentBacking(b, doc, index, owner, plan, borrowed, install); err != nil {
			return err
		}
		return backend.ErrBuildCapacity
	})
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"translations":{"1":"translated"}}`}, nil
	}}
	_, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, r, inner)}, doc, s, r, quietLogger(), nil)
	if !errors.Is(err, backend.ErrBuildCapacity) || doc.Segments[0].Target != "QA corrected" || len(s.confirmed) != 1 {
		t.Fatalf("post-install error reverted installed fields or durable commit: doc=%+v err=%v", doc.Segments[0], err)
	}
	fields := r.documentOwners[documentSlot{doc, 0}]
	if r.Admission.BuildBytes(fields[0]) != 12 {
		t.Fatal("installed target lost its document owner")
	}
	for id := range s.confirmed {
		if r.Admission.BuildBytes("candidate:"+id) != 0 {
			t.Fatal("post-install error retained candidate temporary backing")
		}
	}
}

func TestAllocationBuildAbsoluteCapIsHardAndTemporaryCauseSurvives(t *testing.T) {
	root, err := resourcebudget.New(resourcebudget.Policy{Instance: resourcebudget.Limits{MaxPayloadBytes: 128}})
	if err != nil {
		t.Fatal(err)
	}
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	if err = r.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "builder"}); err != nil {
		t.Fatal(err)
	}
	b, err := backend.NewBuildBoundsFromContext(r.BuildContext(context.Background(), "test"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = b.ReserveAllocation(129, 1)
	if !errors.Is(err, backend.ErrBuildCapacity) || workDeferred(err) != nil {
		t.Fatalf("absolute cap became wait: %v", err)
	}
	other := resourcebudget.Scope{ActorID: 2, CallID: "live"}
	if err = root.Claim(other, "pressure", resourcebudget.Vector{PayloadBytes: 100, BuildWorkers: 1}); err != nil {
		t.Fatal(err)
	}
	err = b.ReserveAllocation(64, 1)
	d := workDeferred(err)
	var detail *resourcebudget.CapacityError
	if d == nil || !errors.As(d, &detail) || !resourcebudget.CapacityHasDrain(d) || detail.Requested == 0 || detail.Limit != 128 {
		t.Fatalf("temporary probe lost coordinates/drain: %v", err)
	}
	if err = root.Release(other, "pressure", resourcebudget.Vector{PayloadBytes: 100, BuildWorkers: 1}); err != nil {
		t.Fatal(err)
	}
	if err = b.ReserveAllocation(64, 1); err != nil {
		t.Fatal(err)
	}
	if err = r.Admission.ReleaseBuild("test"); err != nil {
		t.Fatal(err)
	}
}

func TestAllocationFinalQACallbackKeepsTargetIssuesAndStatus(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	doc := newTestDoc(1)
	s := NewMemoryRoundStore(func(ctx context.Context, _ BatchResult) error {
		b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
		if err != nil {
			return err
		}
		if err = b.ReserveAllocation(12, 1); err != nil {
			return err
		}
		doc.Segments[0].Target = strings.Clone("QA corrected")
		var issues []qa.QualityIssue
		if err = b.CloneInto([]qa.QualityIssue{{Code: "qa-final", Message: "final issue"}}, &issues); err != nil {
			return err
		}
		doc.Segments[0].Issues = issues
		doc.Segments[0].Status = "reviewed"
		return nil
	})
	defer s.Close()
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"translations":{"1":"translated"}}`}, nil
	}}
	h := stagedTestHandler(t, r, inner)
	result, err := RunStagedRound(context.Background(), Round{Concurrency: 1, Handler: h}, doc, s, r, quietLogger(), nil)
	if err != nil || len(result.Resolved) != 1 || doc.Segments[0].Target != "QA corrected" || doc.Segments[0].Status != "reviewed" || len(doc.Segments[0].Issues) != 1 || doc.Segments[0].Issues[0].Code != "qa-final" {
		t.Fatalf("QA final fields lost: result=%+v segment=%+v err=%v", result, doc.Segments[0], err)
	}
	if err = r.CloseDocument(doc); err != nil {
		t.Fatal(err)
	}
}

func TestAllocationResponseBudgetDoesNotEstimateFutureWorkspace(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	// Actual one-member construction fits 64KiB. A future maximum R and an
	// unknown storage workspace must not multiply into a pre-dispatch gate.
	ctx := backend.WithBuildAllocation(context.Background(), 64<<10, nil, nil)
	calls := 0
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		calls++
		return &backend.Response{Text: `{"translations":{"1":"paid"}}`}, nil
	}}
	h := stagedTestHandler(t, r, inner)
	s := NewMemoryRoundStore(nil)
	defer s.Close()
	doc := newTestDoc(1)
	result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: h}, doc, s, r, quietLogger(), nil)
	if err != nil || calls != 1 || len(result.Resolved) != 1 || doc.Segments[0].Target != "paid" {
		t.Fatalf("unknown future workspace rejected actual small request: calls=%d result=%+v err=%v", calls, result, err)
	}
	if err = r.CloseDocument(doc); err != nil {
		t.Fatal(err)
	}
}
func TestAllocationFileDictionaryRetainsPrivateGrowthUntilClose(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	root, err := resourcebudget.New(resourcebudget.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "dictionary"}); err != nil {
		t.Fatal(err)
	}
	g := glossary.NewMemory()
	ctx := r.BuildContext(context.Background(), "promotion:dictionary")
	if err = g.BindAllocationOwner(ctx, r.Admission); err != nil {
		t.Fatal(err)
	}
	in := glossary.ResponseProcess{RequestID: "dictionary", InputDigest: "input", ItemLimit: 1 << 20, BusinessLimit: 1 << 20, Entries: []glossary.Entry{{Source: "thread pool", Target: "private"}}}
	out, err := g.ProcessResponse(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	out = glossary.ResponseBusiness{}
	if err = r.Admission.ReleaseBuild("promotion:dictionary"); err != nil {
		t.Fatal(err)
	}
	retained := root.Snapshot(1).Instance.PayloadBytes
	if retained == 0 {
		t.Fatal("dictionary retained private entries without Root owner")
	}
	b, err := backend.NewBuildBounds(1<<20, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	borrowed, err := g.LookupEntriesBounded(context.Background(), b, "thread pool", "", "")
	if err != nil || len(borrowed) != 1 {
		t.Fatalf("borrowed dictionary %v %v", borrowed, err)
	}
	in.Entries = []glossary.Entry{{Source: "longer thread pool", Target: "second"}}
	out, err = g.ProcessResponse(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if borrowed[0].Target != "private" {
		t.Fatal("publishing changed old dictionary reader")
	}
	borrowed = nil
	out = glossary.ResponseBusiness{}
	_ = out
	if err = r.Admission.ReleaseBuild("promotion:dictionary"); err != nil {
		t.Fatal(err)
	}
	if root.Snapshot(1).Instance.PayloadBytes < retained {
		t.Fatal("publishing refunded borrowed dictionary strings")
	}
	if err = g.Close(); err != nil {
		t.Fatal(err)
	}
	if root.Snapshot(1).Instance.PayloadBytes != 0 {
		t.Fatal("dictionary owner survived close")
	}
}

func TestAllocationMemoryBusinessRetirementKeepsImmutableSibling(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	s := NewMemoryRoundStore(nil)
	defer s.Close()
	ctx := r.BuildContext(WithExecutionRuntime(context.Background(), r), "promotion:business")
	header := memberFixtureReserve(t, ctx, s, "business", 1024, memberFixtureInputs(2))
	header = memberFixtureOutputs(t, ctx, s, header, []archivedOutput{{Target: "original first"}, {Target: "original sibling"}}, nil)
	first := memberFixtureCorrection(t, ctx, s, header, 0, "first")
	sibling := memberFixtureCorrection(t, ctx, s, header, 1, "sibling")
	receipt, receiptAllocation := memberFixturePayload(t, ctx, glossary.ResponseReceipt{Version: glossary.ResponseReceiptVersion, RequestID: header.ID})
	if _, err := s.PublishBusiness(ctx, header.ID, glossary.ResponseBusiness{Receipt: receipt, ReceiptAllocation: receiptAllocation}, nil); err != nil {
		t.Fatal(err)
	}
	borrowed, err := s.LoadOutputMember(ctx, archiveMemberKey(header, 0), true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	originalOwner := "corrected:" + header.ID + ":0"
	siblingOwner := "corrected:" + header.ID + ":1"
	originalBytes := int64(cap(first.Payload))
	siblingBytes := int64(cap(sibling.Payload))
	first = OutputMemberFrame{}
	sibling = OutputMemberFrame{}
	if got := r.Admission.BuildBytes(originalOwner); got != originalBytes {
		t.Fatalf("original content owner=%d want=%d", got, originalBytes)
	}
	if _, err = s.PromoteMember(ctx, archiveMemberKey(header, 0), nil, WorkCursor{}, MemberOutcome{Key: archiveMemberKey(header, 0), Reason: responsecontract.MemberInvalid}); err != nil {
		t.Fatal(err)
	}
	current, err := s.LoadOutputMember(ctx, archiveMemberKey(header, 1), true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadOutputMember(ctx, archiveMemberKey(header, 1), true, current.Bytes-1); err == nil {
		t.Fatal("member output limit was bypassed")
	}
	model, err := s.LoadOutputMember(ctx, archiveMemberKey(header, 1), false, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if memberFixtureDecode(t, model).Target != "original sibling" {
		t.Fatal("publication mutated the immutable model member")
	}
	model.Release()
	if memberFixtureDecode(t, borrowed).Target != "first" || memberFixtureDecode(t, current).Target != "sibling" {
		t.Fatal("retirement mutated borrowed response or lost sibling")
	}
	current.Release()
	current.Release()
	if got := r.Admission.BuildBytes(siblingOwner); got != siblingBytes {
		t.Fatalf("sibling backing changed during earlier retirement: %d want=%d", got, siblingBytes)
	}
	if got := r.Admission.BuildBytes(originalOwner); got != originalBytes {
		t.Fatalf("borrowed content refunded early: %d want=%d", got, originalBytes)
	}
	releaseBorrowed := borrowed.Release
	borrowed = OutputMemberFrame{}
	releaseBorrowed()
	releaseBorrowed()
	if got := r.Admission.BuildBytes(originalOwner); got != 0 {
		t.Fatalf("last reader retained content: %d", got)
	}
	if _, err = s.PromoteMember(ctx, archiveMemberKey(header, 1), nil, WorkCursor{}, MemberOutcome{Key: archiveMemberKey(header, 1), Reason: responsecontract.MemberInvalid}); err != nil {
		t.Fatal(err)
	}
	if len(s.requests[header.ID].corrections) != 0 || len(s.requests[header.ID].outputs) != 0 || r.Admission.BuildBytes(siblingOwner) != 0 || s.requests[header.ID].response.OutputMemberBytes != 0 {
		t.Fatal("final retirement retained business backing")
	}
	if got := r.Admission.BuildBytes("receipt:" + header.ID); got != int64(cap(receipt)) {
		t.Fatalf("retirement refunded receipt history: %d want=%d", got, cap(receipt))
	}
	if err = r.Admission.ReleaseBuild("promotion:business"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := r.Admission.BuildBytes("receipt:" + header.ID); got != 0 {
		t.Fatalf("receipt survived Store.Close: %d", got)
	}
}

func TestAllocationMemoryBusinessReceiptFailureDoesNotInstall(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	s := NewMemoryRoundStore(nil)
	defer s.Close()
	ctx := r.BuildContext(WithExecutionRuntime(context.Background(), r), "promotion:receipt-failure")
	header := memberFixtureReserve(t, ctx, s, "receipt-failure", 1024, memberFixtureInputs(1))
	header = memberFixtureOutputs(t, ctx, s, header, []archivedOutput{{Target: "model"}}, nil)
	correction := memberFixtureCorrection(t, ctx, s, header, 0, "corrected")
	receipt, _ := memberFixturePayload(t, ctx, glossary.ResponseReceipt{Version: glossary.ResponseReceiptVersion, RequestID: header.ID})
	installed := false
	// Withholding an actual backing receipt must reject the entire publication,
	// before its dictionary install callback or staged member becomes visible.
	_, err := s.PublishBusiness(ctx, header.ID, glossary.ResponseBusiness{Receipt: receipt}, func() { installed = true })
	if !errors.Is(err, backend.ErrBuildCapacity) || installed || s.requests[header.ID].response.Phase != "outputs_ready" || len(s.requests[header.ID].corrections) != 0 {
		t.Fatalf("failed receipt transfer published state: installed=%v phase=%s err=%v", installed, s.requests[header.ID].response.Phase, err)
	}
	owner := "corrected:" + header.ID + ":0"
	if got := r.Admission.BuildBytes(owner); got != int64(cap(correction.Payload)) {
		t.Fatalf("failed publish lost staged backing: %d want=%d", got, cap(correction.Payload))
	}
	if _, err := s.LoadOutputMember(ctx, archiveMemberKey(header, 0), true, 1024); err == nil {
		t.Fatal("unpublished corrected member became readable")
	}
	if err := s.AbortMemberBusiness(ctx, header.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.Admission.BuildBytes(owner); got != 0 {
		t.Fatalf("aborted correction retained owner: %d", got)
	}
	model, err := s.LoadOutputMember(ctx, archiveMemberKey(header, 0), false, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if memberFixtureDecode(t, model).Target != "model" {
		t.Fatal("rollback lost original model output")
	}
	model.Release()
	model.Release()
}
