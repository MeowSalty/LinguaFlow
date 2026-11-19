package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

type memoryPromotionBoundaryStore struct {
	*MemoryRoundStore
	boundary string
	cause    error
}

func (s *memoryPromotionBoundaryStore) PromoteMember(ctx context.Context, key MemberKey, c *Candidate, next WorkCursor, failure MemberOutcome) (MainResponseHeader, error) {
	if s.boundary == "before_write" {
		return MainResponseHeader{}, s.cause
	}
	if _, err := s.MemoryRoundStore.PromoteMember(ctx, key, c, next, failure); err != nil {
		return MainResponseHeader{}, err
	}
	if s.boundary == "after_write" {
		return MainResponseHeader{}, s.cause
	}
	return s.MemoryRoundStore.ResponseHeader(ctx, key.RequestID)
}

func (s *memoryPromotionBoundaryStore) Commit(ctx context.Context, c *Candidate, result TranslatedSegment) (CommitOutcome, error) {
	if s.boundary == "acknowledged" {
		return "", s.cause
	}
	return s.MemoryRoundStore.Commit(ctx, c, result)
}

// Exercise the actual save callback, outer ACK and coordinator confirmation.
// The returned candidate's backing must remain owned across Runtime.Close,
// including a save that entered without producing any candidate table row.
func TestAllocationMemoryPromotionOwnershipAcrossAcknowledgement(t *testing.T) {
	for _, boundary := range []string{"before_write", "after_write", "acknowledged", "completed"} {
		t.Run(boundary, func(t *testing.T) {
			r := stagedTestRuntime(t, DefaultCandidateLimits())
			root, err := resourcebudget.New(resourcebudget.Policy{})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: boundary}); err != nil {
				t.Fatal(err)
			}
			s := &memoryPromotionBoundaryStore{MemoryRoundStore: NewMemoryRoundStore(nil), boundary: boundary, cause: permanentCommitFailure{errors.New("promotion boundary failure")}}
			defer s.Close()
			probe := &windowBatchProbe{batchSize: 0}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			doc := newTestDoc(1)
			defer r.CloseDocument(doc)
			_, err = RunStagedRound(ctx, Round{Concurrency: 1, Handler: probe.handler(t, r)}, doc, s, r, quietLogger(), nil)
			if boundary == "completed" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, s.cause) {
				t.Fatalf("save/commit cause lost: %v", err)
			}
			if len(s.promotionCandidates) != 1 {
				t.Fatalf("entered save lost its retained objects: %d", len(s.promotionCandidates))
			}
			var id string
			var held *Candidate
			for id, held = range s.promotionCandidates {
			}
			if held.WorkID == "" || held.BaselineDigest == "" || len(held.PreparedHeader) == 0 {
				t.Fatal("compact header backing did not survive")
			}
			owner := s.promotionOwner(id)
			credit := r.Admission.BuildBytes(owner)
			if credit <= int64(len(held.WorkID)+len(held.BaselineDigest)+cap(held.PreparedHeader)) {
				t.Fatal("retained candidate lost its accurate owner")
			}
			if boundary == "completed" {
				if held.PreparedPayload != nil || len(s.candidates) != 0 {
					t.Fatal("confirmed payload was not destroyed")
				}
			} else if len(held.PreparedPayload) == 0 {
				t.Fatal("uncertain/unfinished candidate payload was refunded")
			}
			if boundary == "before_write" && len(s.candidates) != 0 {
				t.Fatal("before-write failure installed a candidate")
			}
			r.Close()
			before := root.Snapshot(1).Instance
			if before.PayloadBytes < credit || r.Admission.BuildBytes(owner) != credit {
				t.Fatal("runtime close refunded retained header backing")
			}
			if boundary != "completed" {
				entries := r.Admission.BudgetEntries()
				if !entries[id].MemoryOwned || entries[id].Bytes != int64(cap(held.PreparedPayload)) {
					t.Fatal("runtime close refunded retained payload inventory")
				}
			}
			// A later call on the same Root does not inherit or erase this store.
			later := stagedTestRuntime(t, DefaultCandidateLimits())
			if err = later.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "later-" + boundary}); err != nil {
				t.Fatal(err)
			}
			later.Close()
			if root.Snapshot(1).Instance != before {
				t.Fatal("later call changed the original store ownership")
			}
			held = nil
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			if boundary == "completed" && root.Snapshot(1).Instance.PayloadBytes != int64(len(doc.Segments[0].Target)) {
				t.Fatal("store close refunded the live accepted document")
			}
			if err = r.CloseDocument(doc); err != nil {
				t.Fatal(err)
			}
			if root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
				t.Fatalf("joined store leaked inventory: %+v", root.Snapshot(1).Instance)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAllocationMemoryFailedPromotionResumesWithoutReusingOldBacking(t *testing.T) {
	root, err := resourcebudget.New(resourcebudget.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	first := stagedTestRuntime(t, DefaultCandidateLimits())
	if err = first.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "failed-promotion"}); err != nil {
		t.Fatal(err)
	}
	s := &memoryPromotionBoundaryStore{MemoryRoundStore: NewMemoryRoundStore(nil), boundary: "before_write", cause: permanentCommitFailure{errors.New("before write failure")}}
	defer s.Close()
	doc := newTestDoc(1)
	defer first.CloseDocument(doc)
	probe := &windowBatchProbe{batchSize: 0}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = RunStagedRound(ctx, Round{Concurrency: 1, Handler: probe.handler(t, first)}, doc, s, first, quietLogger(), nil)
	if !errors.Is(err, s.cause) {
		t.Fatalf("original storage cause lost: %v", err)
	}
	var old *Candidate
	var id string
	for id, old = range s.promotionCandidates {
	}
	if old == nil || old.PreparedPayload == nil || s.promotionActive[id] {
		t.Fatal("failed attempt did not finish with retained backing")
	}
	oldOwner := s.promotionOwner(id)
	if first.Admission.BuildBytes(oldOwner) == 0 {
		t.Fatal("failed attempt lost its original owner")
	}
	s.boundary = "completed"
	second := stagedTestRuntime(t, DefaultCandidateLimits())
	defer second.CloseDocument(doc)
	if err = second.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "resumed-promotion"}); err != nil {
		t.Fatal(err)
	}
	result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: probe.handler(t, second)}, doc, s, second, quietLogger(), nil)
	if err != nil || len(result.Resolved) != 1 || doc.Segments[0].Target == "" {
		t.Fatalf("actual retained response did not resume: %+v %v", result, err)
	}
	if batches, _ := probe.snapshot(); len(batches) != 1 {
		t.Fatal("resume repeated the paid model request")
	}
	if s.promotionCandidates[id] == old || old.PreparedPayload != nil || old.PreparedHeader != nil || old.WorkID != "" || first.Admission.BuildBytes(oldOwner) != 0 {
		t.Fatal("replacement retained/refunded the wrong original backing")
	}
	old = nil
	first.Close()
	second.Close()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = first.CloseDocument(doc); err != nil {
		t.Fatal(err)
	}
	if err = second.CloseDocument(doc); err != nil {
		t.Fatal(err)
	}
	if root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
		t.Fatalf("joined recovery retained inventory: %+v", root.Snapshot(1).Instance)
	}
}
