package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestConfirmedCallbackPendingPreservesBodyAndOneSuccessfulCall(t *testing.T) {
	for _, mode := range []string{"callback_failure", "receiver_failure", "receiver_failure_nonzero_index", "deadline", "lifetime"} {
		t.Run(mode, func(t *testing.T) {
			r := testExecutionRuntime(t, 1)
			defer r.Close()
			index := 0
			if mode == "receiver_failure_nonzero_index" {
				index = 1
			}
			receiverFailure := strings.HasPrefix(mode, "receiver_failure")
			doc := newTestDoc(index + 1)
			if index == 1 {
				doc.Segments[0].Translate = false
			}
			doc.Segments[index].ContentVersion = 7
			defer r.CloseDocument(doc)
			lifetime, cancelLifetime := context.WithCancel(t.Context())
			defer cancelLifetime()
			r.Lifetime = lifetime
			cause := errors.New("callback or receiver failed")
			calls, reject := 0, true
			store := NewMemoryRoundStore(func(ctx context.Context, _ BatchResult) error {
				calls++
				if !receiverFailure && reject {
					return cause
				}
				b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
				if err != nil {
					return err
				}
				if err := b.ReserveAllocation(9, 1); err != nil {
					return err
				}
				doc.Segments[index].Target = strings.Clone("corrected")
				doc.Segments[index].Status = "reviewed"
				return nil
			})
			defer store.Close()
			ctx := WithDocumentAllocationOwner(t.Context(), func(ctx context.Context, index int, owner string, plan DocumentAllocationPlan, borrowed uint8, install func()) error {
				if receiverFailure && reject && strings.Contains(owner, "callback:") {
					return cause
				}
				return r.installDocumentBacking(nil, doc, index, owner, plan, borrowed, install)
			})
			main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
				return &backend.Response{Text: `{"translations":{"1":"confirmed"}}`}, nil
			}}
			_, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, r, main)}, doc, store, r, quietLogger(), nil)
			var pending *ConfirmationProjectionError
			if !errors.As(err, &pending) || !errors.Is(err, cause) || doc.Segments[index].Target != "confirmed" || doc.Segments[index].RoundWork.Completed || len(r.pendingConfirmations) != 1 || calls != 1 || main.calls.Load() != 1 {
				t.Fatalf("pending=%v calls=%d doc=%+v", err, calls, doc.Segments[index])
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			// Engine's implicit Memory store closes at entrypoint return. The
			// frozen callback and independent paid owner must survive that close.
			reject = false
			if mode == "deadline" {
				for _, p := range r.pendingConfirmations {
					p.deadline = time.Now().Add(-time.Second)
				}
			}
			if mode == "lifetime" {
				cancelLifetime()
			}
			err = r.RetryConfirmedDocument(ctx, doc)
			if mode == "deadline" || mode == "lifetime" {
				want := context.DeadlineExceeded
				if mode == "lifetime" {
					want = context.Canceled
				}
				if !errors.Is(err, want) || calls != 1 || len(r.pendingConfirmations) != 1 {
					t.Fatalf("retry escaped original lifetime: %v calls=%d", err, calls)
				}
				return
			}
			wantCalls := 2
			if receiverFailure {
				wantCalls = 1
			}
			if err != nil || doc.Segments[index].Target != "corrected" || doc.Segments[index].Status != "reviewed" || doc.Segments[index].ContentVersion != 7 || !doc.Segments[index].RoundWork.Completed || calls != wantCalls || main.calls.Load() != 1 || len(r.pendingConfirmations) != 0 {
				t.Fatalf("retry=%v calls=%d doc=%+v", err, calls, doc.Segments[index])
			}
		})
	}
}

func TestConfirmedMemoryCloseRefundsPromotionHeaderAndPreservesPendingOwner(t *testing.T) {
	for _, failCallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "pending_callback"}[failCallback], func(t *testing.T) {
			r := testExecutionRuntime(t, 1)
			defer r.Close()
			root, err := resourcebudget.New(resourcebudget.Policy{})
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "confirmed-cleanup"}); err != nil {
				t.Fatal(err)
			}
			baseline := root.Snapshot(1).Instance
			doc := newTestDoc(1)
			defer r.CloseDocument(doc)
			cause := errors.New("callback pending")
			var callback func(context.Context, BatchResult) error
			if failCallback {
				callback = func(context.Context, BatchResult) error { return cause }
			}
			store := NewMemoryRoundStore(callback)
			defer store.Close()
			main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
				return &backend.Response{Text: `{"translations":{"1":"confirmed"}}`}, nil
			}}
			_, err = RunStagedRound(t.Context(), Round{Concurrency: 1, Handler: stagedTestHandler(t, r, main)}, doc, store, r, quietLogger(), nil)
			if failCallback && !errors.Is(err, cause) || !failCallback && err != nil {
				t.Fatal(err)
			}
			var headerOwner string
			for id := range store.confirmed {
				headerOwner = store.promotionOwner(id)
			}
			if headerOwner == "" || r.Admission.BuildBytes(headerOwner) == 0 {
				t.Fatal("fixture did not retain a real promotion header")
			}
			var pendingOwner string
			if failCallback {
				for _, pending := range r.pendingConfirmations {
					pendingOwner = pending.owner
				}
				if r.Admission.BuildBytes(pendingOwner) == 0 {
					t.Fatal("pending result has no independent live owner")
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if r.Admission.BuildBytes(headerOwner) != 0 || failCallback && r.Admission.BuildBytes(pendingOwner) == 0 {
				t.Fatal("store close leaked its header or refunded the live pending result")
			}
			if err := r.CloseDocument(doc); err != nil {
				t.Fatal(err)
			}
			if got := root.Snapshot(1).Instance; got != baseline {
				t.Fatalf("last document/pending borrower ended but credit survived before Runtime.Close: got=%+v baseline=%+v", got, baseline)
			}
		})
	}
}

func TestUnconfirmedCommitNeverCallsPublicCallback(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	calls := 0
	cause := permanentCommitFailure{errors.New("commit failed before acknowledgement")}
	store := &controlCommitStore{MemoryRoundStore: NewMemoryRoundStore(func(context.Context, BatchResult) error { calls++; return nil }), cause: cause}
	defer store.Close()
	doc := newTestDoc(1)
	defer r.CloseDocument(doc)
	main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"translations":{"1":"unconfirmed"}}`}, nil
	}}
	_, err := RunStagedRound(t.Context(), Round{Concurrency: 1, Handler: stagedTestHandler(t, r, main)}, doc, store, r, quietLogger(), nil)
	if !errors.Is(err, cause) || calls != 0 || doc.Segments[0].Target != "" {
		t.Fatalf("unconfirmed result escaped: %v callback=%d doc=%+v", err, calls, doc.Segments[0])
	}
}
