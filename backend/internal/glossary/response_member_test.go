package glossary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestMemberResponseGlossarySourceOrderAndBoundaries(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		t.Run(lang, func(t *testing.T) {
			g := NewMemory()
			for _, entry := range []Entry{{Source: "A", Target: "old"}, {Source: "B", Target: "final"}} {
				if _, err := g.Add(context.Background(), entry); err != nil {
					t.Fatal(err)
				}
			}
			in := ResponseProcess{RequestID: "r", InputDigest: "digest", TargetLang: lang, Rewrite: true, ItemLimit: 1024, BusinessLimit: 1 << 20, EntriesPayload: []byte(`[{"Source":"A","Target":"new"},{"Source":"B","Target":"old"},{"Source":"C","Target":"added"},{"Source":"C","Target":"new"}]`)}
			var actual []string
			visits, writes, publishes := 0, 0, 0
			in.VisitTargets = func(ctx context.Context, visit func(ResponseTarget) error) error {
				for i := 0; i < 128; i++ {
					visits++
					if err := visit(ResponseTarget{Ordinal: i, ID: fmt.Sprint(i), Text: "new renewal"}); err != nil {
						return err
					}
				}
				return nil
			}
			in.WriteTarget = func(_ context.Context, target ResponseTarget) error {
				if target.Ordinal != writes {
					t.Fatalf("ordinal %d/%d", target.Ordinal, writes)
				}
				writes++
				actual = append(actual, target.Text)
				return nil
			}
			in.Publish = func(_ context.Context, out ResponseBusiness, install *responsecontract.PreparedPublication) error {
				publishes++
				if len(g.entries) != 2 {
					t.Fatal("dictionary installed before receipt publication")
				}
				var receipt ResponseReceipt
				if err := json.Unmarshal(out.Receipt, &receipt); err != nil {
					return err
				}
				if receipt.Added != 1 || receipt.Skipped != 3 {
					t.Fatalf("receipt %+v", receipt)
				}
				install.InstallOnce()
				return nil
			}
			out, err := g.ProcessResponse(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			defer out.Release()
			if visits != 128 || writes != 128 || publishes != 1 || len(g.entries) != 3 {
				t.Fatalf("visits=%d writes=%d publishes=%d dictionary=%d", visits, writes, publishes, len(g.entries))
			}
			want := "final renewal"
			if lang == "zh" {
				want = "final refinalal"
			}
			for _, text := range actual {
				if text != want {
					t.Fatalf("text=%q want=%q", text, want)
				}
			}
		})
	}
}

func TestMemberResponseGlossaryPrepareFailureIsAtomic(t *testing.T) {
	for _, stage := range []string{"write", "publish"} {
		t.Run(stage, func(t *testing.T) {
			g := NewMemory()
			fault := errors.New("sink fault")
			in := ResponseProcess{RequestID: "r", InputDigest: "digest", ItemLimit: 1024, BusinessLimit: 1024, Entries: []Entry{{Source: "A", Target: "a"}}}
			in.VisitTargets = func(ctx context.Context, visit func(ResponseTarget) error) error {
				return visit(ResponseTarget{ID: "1", Text: "body"})
			}
			in.WriteTarget = func(context.Context, ResponseTarget) error {
				if stage == "write" {
					return fault
				}
				return nil
			}
			in.Publish = func(context.Context, ResponseBusiness, *responsecontract.PreparedPublication) error { return fault }
			out, err := g.ProcessResponse(context.Background(), in)
			out.Release()
			if !errors.Is(err, fault) || len(g.entries) != 0 || g.entrySnapshot.Load() != nil || g.dirty {
				t.Fatalf("failed publication changed dictionary: err=%v entries=%d dirty=%v", err, len(g.entries), g.dirty)
			}
		})
	}
}

func TestMemberResponseFileGlossaryPublicationRollback(t *testing.T) {
	for _, stage := range []string{"write", "publish"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "glossary.csv")
			g, err := LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			fault := errors.New("file response publication fault")
			in := ResponseProcess{RequestID: "file-response", InputDigest: "digest", ItemLimit: 1024, BusinessLimit: 1024, Entries: []Entry{{Source: "A", Target: "a"}}}
			in.VisitTargets = func(ctx context.Context, visit func(ResponseTarget) error) error {
				return visit(ResponseTarget{ID: "1", Text: "body"})
			}
			in.WriteTarget = func(context.Context, ResponseTarget) error {
				if stage == "write" {
					return fault
				}
				return nil
			}
			in.Publish = func(context.Context, ResponseBusiness, *responsecontract.PreparedPublication) error { return fault }
			out, err := g.ProcessResponse(t.Context(), in)
			out.Release()
			if !errors.Is(err, fault) || len(g.entries) != 0 || g.entrySnapshot.Load() != nil || g.dirty {
				t.Fatalf("rollback: %v entries=%d dirty=%t", err, len(g.entries), g.dirty)
			}
			// The failure releases the mutation lock and leaves a writable file
			// adapter. Only the publisher's atomic install makes entries visible.
			in.WriteTarget = func(context.Context, ResponseTarget) error { return nil }
			in.Publish = func(_ context.Context, _ ResponseBusiness, install *responsecontract.PreparedPublication) error {
				install.InstallOnce()
				return nil
			}
			out, err = g.ProcessResponse(t.Context(), in)
			out.Release()
			if err != nil {
				t.Fatal(err)
			}
			if err = g.Save(t.Context()); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadFile(path)
			if err != nil || len(loaded.entries) != 1 || loaded.entries[0].Target != "a" {
				t.Fatalf("file install: %v", err)
			}
		})
	}
}

func TestMemberResponseGlossaryIndependentLimitsAndFailure(t *testing.T) {
	in := ResponseProcess{RequestID: "r", InputDigest: "digest", TargetLang: "en", Rewrite: true, ItemLimit: 4, BusinessLimit: 4}
	in.VisitTargets = func(ctx context.Context, visit func(ResponseTarget) error) error {
		return visit(ResponseTarget{Text: "new"})
	}
	in.WriteTarget = func(_ context.Context, target ResponseTarget) error {
		key := responsecontract.MemberKey{RequestID: in.RequestID, Ordinal: 0, CaptureEpoch: in.CaptureEpoch}
		if target.Text != "" || target.Failure == "" || target.Outcome.Key != key || target.Outcome.Reason != responsecontract.MemberTooLarge || target.Outcome.Limit != in.ItemLimit || target.Outcome.Observed <= in.ItemLimit {
			t.Fatalf("oversize member: text_bytes=%d failure_present=%t key_matches=%t reason=%s limit=%d observed=%d", len(target.Text), target.Failure != "", target.Outcome.Key == key, target.Outcome.Reason, target.Outcome.Limit, target.Outcome.Observed)
		}
		return nil
	}
	result := AddResult{Skipped: []SkippedEntry{{Proposed: Entry{Target: "new"}, Existing: Entry{Target: "long target"}, Reason: SkipReasonExists}}}
	out, err := BuildResponseBusiness(context.Background(), in, result)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Release()
	if len(out.Receipt) <= int(in.BusinessLimit) || len(out.Receipt) > ResponseReceiptLimit {
		t.Fatalf("receipt incorrectly uses body limit: %d", len(out.Receipt))
	}
	in.ItemLimit = 1024
	in.BusinessLimit = 3
	in.WriteTarget = func(context.Context, ResponseTarget) error { return nil }
	in.VisitTargets = func(ctx context.Context, visit func(ResponseTarget) error) error {
		return visit(ResponseTarget{Text: "four"})
	}
	_, err = BuildResponseBusiness(context.Background(), in, AddResult{})
	if !errors.Is(err, backend.ErrBuildCapacity) {
		t.Fatalf("business limit err=%v", err)
	}
	in.BusinessLimit = 1024
	written := false
	in.WriteTarget = func(context.Context, ResponseTarget) error { written = true; return nil }
	in.Validate = func(context.Context, ResponseTarget) error { return context.Canceled }
	_, err = BuildResponseBusiness(context.Background(), in, AddResult{})
	if !errors.Is(err, context.Canceled) || written {
		t.Fatalf("cancelled validation was published: err=%v written=%v", err, written)
	}
}

func TestMemberResponseGlossaryMalformedPublicEntries(t *testing.T) {
	for _, payload := range []string{`[{"Source":"A","Target":"a"},]`, `[{"Source":"A","Target":"a"}] trailing`} {
		g := NewMemory()
		out, err := g.ProcessResponse(context.Background(), ResponseProcess{RequestID: "r", InputDigest: "d", ItemLimit: 1024, BusinessLimit: 1024, EntriesPayload: []byte(payload)})
		out.Release()
		if err == nil || len(g.entries) != 0 {
			t.Fatalf("accepted malformed public entries %q", payload)
		}
	}
}

func TestMemberResponseGlossaryPreparedOwnerRollback(t *testing.T) {
	a, err := backend.NewRequestAdmission(backend.NewLimiterPool(), backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	g := NewMemory()
	ctx := backend.WithBuildReservation(context.Background(), func(n int64) error { return a.ClaimBuild("member-temporary", n) }, func(n int64) {
		if e := a.ReleaseBuildBytes("member-temporary", n); e != nil {
			t.Error(e)
		}
	})
	if err = g.BindAllocationOwner(ctx, a); err != nil {
		t.Fatal(err)
	}
	before := a.BuildBytes(g.allocationOwner)
	fault := errors.New("prepublication failure")
	in := ResponseProcess{RequestID: "r", InputDigest: "digest", ItemLimit: 4096, BusinessLimit: 4096, EntriesPayload: []byte(`[{"Source":"A","Target":"added","Notes":"private"}]`), Publish: func(context.Context, ResponseBusiness, *responsecontract.PreparedPublication) error { return fault }}
	out, err := g.ProcessResponse(ctx, in)
	out.Release()
	if !errors.Is(err, fault) {
		t.Fatal(err)
	}
	if got := a.BuildBytes(g.allocationOwner); got != before {
		t.Fatalf("prepared dictionary leaked owner: %d/%d", got, before)
	}
	if got := a.BuildBytes("member-temporary"); got != 0 {
		t.Fatalf("attempt backing leaked: %d", got)
	}
	if err = g.Close(); err != nil {
		t.Fatal(err)
	}
	if got := a.BuildBytes(g.allocationOwner); got != 0 {
		t.Fatalf("closed dictionary owner=%d", got)
	}
}

func TestMemberResponseGlossaryFullPaidRoot(t *testing.T) {
	const maximum = int64(1 << 20)
	const response = int64(16 << 10)
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: resourcebudget.Limits{MaxPayloadBytes: maximum}, Instance: resourcebudget.Limits{MaxPayloadBytes: maximum}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = a.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "glossary-paid"}); err != nil {
		t.Fatal(err)
	}
	if err = a.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: 1, Candidates: 4096, Item: 4096, Responses: response, Handoff: maximum}); err != nil {
		t.Fatal(err)
	}
	g := NewMemory()
	if err = g.BindAllocationOwner(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if ok, e := a.PrepareMain("paid", 0, 1, 0, 0, 0, false, 1); e != nil || !ok {
		t.Fatalf("prepare %v/%v", ok, e)
	}
	permit, wait, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "paid", RoundIndex: 0, ResourceID: 1, Stage: backend.RequestStageMain}, nil)
	if err != nil || permit == nil {
		t.Fatalf("admit %+v/%v", wait, err)
	}
	defer permit.Release()
	ctx := backend.WithBuildOwnership(context.Background(), a, "response:paid")
	ctx = backend.WithBuildReservation(ctx, func(n int64) error { return a.ClaimBuild("response:paid", n) }, func(n int64) {
		if e := a.ReleaseBuildBytes("response:paid", n); e != nil {
			t.Error(e)
		}
	})
	ctx = backend.WithProviderResponseOwnership(ctx, a, "paid", backend.RequestStageMain)
	ctx, err = backend.WithProviderResponseAllocation(ctx, response)
	if err != nil {
		t.Fatal(err)
	}
	remaining := maximum - root.Snapshot(1).Actor.PayloadBytes
	if err = a.ClaimBuild("filler", remaining); err != nil {
		t.Fatal(err)
	}
	filler := make([]byte, int(remaining))
	filler[0] = 1
	before := root.Snapshot(1).Actor.PayloadBytes
	if before != maximum {
		t.Fatalf("root not full %d", before)
	}
	dictionaryBefore := a.BuildBytes(g.allocationOwner)
	sourceBefore := backend.ProviderResponseSnapshot(ctx)
	fault := errors.New("publish cancelled")
	in := ResponseProcess{RequestID: "paid", InputDigest: "digest", ItemLimit: 4096, BusinessLimit: 4096, Entries: []Entry{{Source: "thread pool", Target: "private", Notes: "a real retained dictionary entry"}}}
	publicationCtx, cancelPublication := context.WithCancel(ctx)
	in.Publish = func(context.Context, ResponseBusiness, *responsecontract.PreparedPublication) error {
		cancelPublication()
		return fault
	}
	out, err := g.ProcessResponse(publicationCtx, in)
	out.Release()
	if !errors.Is(err, fault) {
		t.Fatalf("full paid rollback failed %v", err)
	}
	if root.Snapshot(1).Actor.PayloadBytes != before || a.BuildBytes(g.allocationOwner) != dictionaryBefore || backend.ProviderResponseSnapshot(ctx).LiveBytes != sourceBefore.LiveBytes {
		t.Fatalf("rollback changed real ledger: root=%+v source=%+v", root.Snapshot(1), backend.ProviderResponseSnapshot(ctx))
	}
	in.Publish = func(_ context.Context, _ ResponseBusiness, install *responsecontract.PreparedPublication) error {
		install.InstallOnce()
		return nil
	}
	out, err = g.ProcessResponse(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if root.Snapshot(1).Actor.PayloadBytes != before || len(g.entries) != 1 || a.BuildBytes(g.allocationOwner) <= dictionaryBefore {
		t.Fatalf("paid transfer changed full root or lost dictionary %+v", root.Snapshot(1))
	}
	out.Release()
	out.Release()
	if err = backend.CloseProviderResponseAllocation(ctx); err != nil {
		t.Fatal(err)
	}
	permit.Release()
	if len(g.entries) != 1 || g.entries[0].Target != "private" {
		t.Fatal("response release retired live dictionary")
	}
	if err = g.Close(); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(filler)
	filler = nil
	if err = a.ReleaseBuild("filler"); err != nil {
		t.Fatal(err)
	}
	if got := root.Snapshot(1).Instance; got != (resourcebudget.Vector{}) {
		t.Fatalf("final root=%+v", got)
	}
}
