package pipeline

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

type memoryConfirmationBoundaryStore struct {
	*MemoryRoundStore
	unknown bool
	cancel  context.CancelFunc
}

func (s *memoryConfirmationBoundaryStore) PrepareCandidateCommit(_ context.Context, b *backend.BuildBounds, _ *Candidate, result TranslatedSegment) (TranslatedSegment, error) {
	if err := b.CloneInto([]qa.QualityIssue{{Code: "fixture", Message: "confirmed warning", Disposition: qa.DispositionPending}}, &result.Issues); err != nil {
		return result, err
	}
	if err := b.CloneInto(map[string]string{"[P1]": "protected"}, &result.Protected); err != nil {
		return result, err
	}
	return result, nil
}

func (s *memoryConfirmationBoundaryStore) Commit(ctx context.Context, c *Candidate, result TranslatedSegment) (CommitOutcome, error) {
	outcome, err := s.MemoryRoundStore.Commit(ctx, c, result)
	if err != nil {
		return outcome, err
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.unknown {
		return "", permanentCommitFailure{errors.New("confirmation ACK unknown")}
	}
	return outcome, nil
}

func TestMemoryConfirmationRestoresFreshDocumentAndOwnsEncodedInventory(t *testing.T) {
	for _, mode := range []string{"accepted", "unknown_ack", "caller_cancel", "callback_corrected"} {
		t.Run(mode, func(t *testing.T) {
			root, err := resourcebudget.New(resourcebudget.Policy{})
			if err != nil {
				t.Fatal(err)
			}
			first := testExecutionRuntime(t, 1)
			defer first.Close()
			if err := first.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "memory-first"}); err != nil {
				t.Fatal(err)
			}
			baseline := root.Snapshot(1).Instance
			newDoc := func() *Document {
				doc := newTestDoc(1)
				doc.Segments[0].ContentVersion, doc.Segments[0].TargetIsNull = 7, true
				doc.Segments[0].Meta = map[string]any{"pos_lines": []int{2, 3}, "line": 9, "extension": map[string]any{"opaque": true}}
				return doc
			}
			doc := newDoc()
			defer first.CloseDocument(doc)
			callbackCalls := 0
			var callback func(context.Context, BatchResult) error
			if mode == "callback_corrected" {
				callback = func(ctx context.Context, _ BatchResult) error {
					callbackCalls++
					b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
					if err != nil {
						return err
					}
					if err := b.ReserveAllocation(9, 1); err != nil {
						return err
					}
					doc.Segments[0].Target, doc.Segments[0].Status = strings.Clone("corrected"), "reviewed"
					return nil
				}
			}
			store := &memoryConfirmationBoundaryStore{MemoryRoundStore: NewMemoryRoundStore(callback), unknown: mode == "unknown_ack"}
			defer store.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "caller_cancel" {
				store.cancel = cancel
			}
			main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
				return &backend.Response{Text: `{"translations":{"1":"confirmed"}}`}, nil
			}}
			_, err = RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, first, main)}, doc, store, first, quietLogger(), nil)
			if mode == "unknown_ack" && !storageFailure(err) || mode != "unknown_ack" && err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			record := store.confirmations[0]
			if record == nil || len(record.payload) == 0 || !record.stored {
				t.Fatal("confirmed fact has no encoded inventory")
			}
			if mode == "unknown_ack" {
				for _, candidate := range store.promotionCandidates {
					if candidate.Segment.Target != "" || candidate.Segment.Issues != nil {
						t.Fatal("unknown ACK retained refunded transient decoded candidate body")
					}
				}
			}
			inventory := first.Admission.BuildBytes(record.owner)
			if inventory != int64(cap(record.payload))+int64(unsafe.Sizeof(memoryConfirmation{})) {
				t.Fatalf("inventory=%d cap=%d", inventory, cap(record.payload))
			}
			if err := first.CloseDocument(doc); err != nil {
				t.Fatal(err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			if first.Admission.BuildBytes(record.owner) != inventory {
				t.Fatal("old document or runtime refunded store inventory")
			}
			second := testExecutionRuntime(t, 1)
			defer second.Close()
			if err := second.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "memory-second"}); err != nil {
				t.Fatal(err)
			}
			fresh := newDoc()
			defer second.CloseDocument(fresh)
			store.unknown, store.cancel = false, nil
			out, err := RunStagedRound(t.Context(), Round{Concurrency: 1, Handler: stagedTestHandler(t, second, main)}, fresh, store, second, quietLogger(), nil)
			if err != nil || len(out.Resolved) != 1 || main.calls.Load() != 1 {
				t.Fatalf("fresh recovery=%+v calls=%d err=%v", out, main.calls.Load(), err)
			}
			seg := fresh.Segments[0]
			wantTarget, wantStatus := "confirmed", "translated"
			if mode == "callback_corrected" {
				wantTarget, wantStatus = "corrected", "reviewed"
			}
			if seg.Target != wantTarget || seg.Status != wantStatus || !seg.RoundWork.Completed || seg.ContentVersion != 7 || seg.TargetIsNull || !reflect.DeepEqual(seg.Meta["pos_lines"], []int{2, 3}) || seg.Meta["line"] != 9 || !reflect.DeepEqual(seg.Meta["extension"], map[string]any{"opaque": true}) || len(seg.Issues) != 1 || seg.Issues[0].Message != "confirmed warning" || seg.Protected["[P1]"] != "protected" {
				t.Fatalf("recovered fields=%+v", seg)
			}
			if mode == "callback_corrected" && callbackCalls != 1 {
				t.Fatalf("callback repeated after recovery: %d", callbackCalls)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if record.payload != nil || first.Admission.BuildBytes(record.owner) != 0 || fresh.Segments[0].Target != wantTarget {
				t.Fatal("store close retained inventory or broke current document")
			}
			if err := second.CloseDocument(fresh); err != nil {
				t.Fatal(err)
			}
			if got := root.Snapshot(1).Instance; got != baseline {
				t.Fatalf("last inventory/document borrower left credit: got=%+v baseline=%+v", got, baseline)
			}
		})
	}
}

func TestMemoryConfirmationDirectExistingIdentityAndReadOwnership(t *testing.T) {
	for _, paid := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "allocation_callback_without_owner"}[paid], func(t *testing.T) {
			ctx := t.Context()
			var live int64
			if paid {
				ctx = backend.WithBuildAllocation(ctx, 1<<20, func(n int64) error { live += n; return nil }, func(n int64) { live -= n })
			}
			store := NewMemoryRoundStore(nil)
			defer store.Close()
			first := &Candidate{ID: "confirmed-first", Index: 0, Mode: RoundModeTranslate, Segment: Segment{ContentVersion: 7, TargetIsNull: true}}
			body := TranslatedSegment{TargetText: "confirmed \"body\"", Issues: []qa.QualityIssue{{Code: "confirmed", Message: "saved issue", Disposition: qa.DispositionPending}}, Meta: map[string]any{"line": 9, "pos_lines": []int{2, 3}}, Protected: map[string]string{"[P1]": "saved protected"}}
			if out, err := store.Commit(ctx, first, body); err != nil || out != CommitAccepted {
				t.Fatalf("first=%s %v", out, err)
			}
			second := &Candidate{ID: "confirmed-second", Index: 1, Mode: RoundModeTranslate}
			if out, err := store.Commit(ctx, second, TranslatedSegment{TargetText: "other confirmed"}); err != nil || out != CommitAccepted {
				t.Fatalf("second=%s %v", out, err)
			}
			inventory := live
			original := store.confirmations[0]
			intruder := &Candidate{ID: "new-identity", Index: 0}
			if out, err := store.Commit(ctx, intruder, TranslatedSegment{TargetText: "unconfirmed"}); out != CommitStale || !errors.Is(err, ErrCandidateStale) || store.confirmations[0] != original || live != inventory {
				t.Fatalf("new identity overwrote confirmation: %s %v", out, err)
			}
			wrongIndex := &Candidate{ID: first.ID, Index: 1, Segment: Segment{Target: "untouched"}}
			if _, err := store.Commit(ctx, wrongIndex, TranslatedSegment{TargetText: "unconfirmed"}); !errors.Is(err, ErrCandidateStale) || wrongIndex.Segment.Target != "untouched" || live != inventory {
				t.Fatalf("confirmed ID selected another index: %v", err)
			}
			reader := &Candidate{ID: first.ID, Index: 0, Segment: Segment{Target: "unconfirmed", Meta: map[string]any{"line": 99, "pos_lines": []int{10}}, Protected: map[string]string{"unconfirmed": "wrong"}}}
			defer reader.CloseConfirmedRead()
			if out, err := store.Commit(ctx, reader, TranslatedSegment{TargetText: "unconfirmed", Issues: []qa.QualityIssue{{Code: "unconfirmed"}}}); err != nil || out != CommitExisting {
				t.Fatalf("existing=%s %v", out, err)
			}
			if reader.Segment.Target != body.TargetText || !reflect.DeepEqual(reader.Segment.Issues, body.Issues) || !reflect.DeepEqual(reader.Segment.Meta, body.Meta) || !reflect.DeepEqual(reader.Segment.Protected, body.Protected) || reader.Segment.Status != "translated" || reader.Segment.ContentVersion != 7 || reader.Segment.TargetIsNull {
				t.Fatalf("unconfirmed input escaped Existing: %+v", reader.Segment)
			}
			if paid && live <= inventory {
				t.Fatal("Existing decoded fields have no live caller receipt")
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if reader.Segment.Target != body.TargetText || paid && live <= 0 {
				t.Fatal("store close ended live caller read")
			}
			reader.CloseConfirmedRead()
			if live != 0 {
				t.Fatalf("last caller read left allocation credit: %d", live)
			}
		})
	}
}

func TestMemoryConfirmationDirectCommitKeepsRuntimeItemLimit(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	defer runtime.Close()
	runtime.Window.limits.ItemBytes = 128
	store := NewMemoryRoundStore(nil)
	defer store.Close()
	_, err := store.Commit(WithExecutionRuntime(t.Context(), runtime), &Candidate{ID: "bounded", Index: 0}, TranslatedSegment{TargetText: strings.Repeat("x", 256)})
	if err == nil || len(store.confirmations) != 0 || len(store.state.Completed) != 0 {
		t.Fatalf("direct commit bypassed current item limit: %v", err)
	}
}
