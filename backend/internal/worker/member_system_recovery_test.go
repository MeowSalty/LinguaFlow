package worker

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

type memberPrivateCopyStore struct {
	*roundStore
	beforeLoad  func() error
	privateCopy bool
	loadReached bool
}

func (s *memberPrivateCopyStore) LoadCapture(ctx context.Context, id string, limit int64) (pipeline.CapturedResponse, error) {
	s.loadReached = true
	if s.beforeLoad != nil {
		if err := s.beforeLoad(); err != nil {
			return pipeline.CapturedResponse{}, err
		}
	}
	frame, err := s.roundStore.LoadCapture(ctx, id, limit)
	if err != nil {
		return frame, err
	}
	if !s.privateCopy {
		return frame, nil
	}
	defer frame.Release()
	// This is an actual private-copy allocator rejection after the paid frame
	// was read. It is independent of the member's generous, valid Q limit.
	b, err := backend.NewBuildBounds(limit, 0, 128)
	if err != nil {
		return pipeline.CapturedResponse{}, err
	}
	defer b.CloseRetired()
	var copy []byte
	if err = b.CloneInto(frame.Payload, &copy); err != nil {
		return pipeline.CapturedResponse{}, err
	}
	return pipeline.CapturedResponse{}, errors.New("private-copy fixture unexpectedly allocated")
}

func TestMemberSystemRejectionPreservesPaidResponseAcrossRecovery(t *testing.T) {
	for _, mode := range []string{"root", "private_copy"} {
		t.Run(mode, func(t *testing.T) {
			f, http := newHandoffHTTPFixture(t, "translate", 1, 1, 1)
			http.open()
			http.transform = func(content any) any {
				result := content.(map[string]any)
				result["padding"] = strings.Repeat("p", 900<<10)
				return result
			}
			root, err := resourcebudget.New(resourcebudget.Policy{Actor: resourcebudget.Limits{MaxPayloadBytes: 16 << 20}, Instance: resourcebudget.Limits{MaxPayloadBytes: 16 << 20}})
			if err != nil {
				t.Fatal(err)
			}
			gate := pipeline.NewPauseGate()
			seedRuntime, err := NewExecutionRuntime(f.snapshot, f.pool, gate, pipeline.DefaultCandidateLimits())
			if err != nil {
				t.Fatal(err)
			}
			seedEngine, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(t.Context(), seedRuntime), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
			if err != nil {
				t.Fatal(err)
			}
			if err = seedRuntime.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: f.ownerID, CallID: "system-seed"}); err != nil {
				t.Fatal(err)
			}
			doc, base := f.round(t, 0)
			seedEngine.PrepareDocument(doc, nil)
			store := &handoffPauseBoundaryStore{roundStore: base, phase: "raw_after", pause: func(context.Context) error { gate.Pause(); return nil }}
			_, err = pipeline.RunStagedRound(t.Context(), seedEngine.Rounds()[0], doc, store, seedRuntime, f.logger, progress.Nop{})
			if err != nil || !gate.Paused() {
				t.Fatalf("paid near-R capture did not pause: %v", err)
			}
			captured := f.client.WorkRequest.Query().OnlyX(t.Context())
			if captured.CaptureBytes < 900<<10 || captured.PromotionState != "capture_pending" {
				t.Fatal("system fixture lost original R load shape")
			}
			seedEngine.Close()
			seedRuntime.CloseDocument(doc)
			if err = seedRuntime.Close(); err != nil {
				t.Fatal(err)
			}
			fillerScope := resourcebudget.Scope{ActorID: f.ownerID, CallID: "system-filler"}
			var filler []byte
			var fillerBytes int64
			var failedRead *memberPrivateCopyStore
			run := func(fail bool) error {
				r, e := NewExecutionRuntime(f.snapshot, f.pool, nil, pipeline.DefaultCandidateLimits())
				if e != nil {
					return e
				}
				defer r.Close()
				eng, e := f.factory.BuildEngine(pipeline.WithExecutionRuntime(t.Context(), r), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
				if e != nil {
					return e
				}
				defer eng.Close()
				if e = r.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: f.ownerID, CallID: "system-recovery"}); e != nil {
					return e
				}
				current, currentStore := f.round(t, 0)
				eng.PrepareDocument(current, nil)
				defer r.CloseDocument(current)
				var selected pipeline.RoundStore = currentStore
				if fail {
					failedRead = &memberPrivateCopyStore{roundStore: currentStore, privateCopy: mode == "private_copy"}
					if mode == "root" {
						failedRead.beforeLoad = func() error {
							// Headers and canonical ownership have already recovered.
							// Saturate Root at the actual allocator read boundary.
							fillerBytes = (16 << 20) - root.Snapshot(f.ownerID).Actor.PayloadBytes
							if e := root.Claim(fillerScope, "system-filler", resourcebudget.Vector{PayloadBytes: fillerBytes}); e != nil {
								return e
							}
							filler = make([]byte, fillerBytes, fillerBytes)
							return nil
						}
					}
					selected = failedRead
				}
				_, e = pipeline.RunStagedRound(t.Context(), eng.Rounds()[0], current, selected, r, f.logger, progress.Nop{})
				return e
			}
			err = run(true)
			var content *responsecontract.MemberContentError
			var rejection *backend.RejectionError
			if err == nil || errors.As(err, &content) || failedRead == nil || !failedRead.loadReached || !errors.As(err, &rejection) || rejection.Evidence.Branch != "response_capture_read" || rejection.Evidence.Operation != "load_capture" || rejection.Evidence.Phase != "capture_pending" {
				t.Fatalf("system rejection became content or success: %v", err)
			}
			if mode == "root" {
				var capacity *resourcebudget.CapacityError
				if !errors.As(err, &capacity) || capacity.Stage != "root_admission" || capacity.Dimension != "payload_bytes" || capacity.Requested < captured.CaptureBytes || fillerBytes <= 0 {
					t.Fatalf("Root actual LoadCapture allocator was not rejected: %v", err)
				}
			} else {
				var capacity *backend.AllocationCapacityError
				if !errors.As(err, &capacity) || capacity.Limit != 128 || capacity.Requested < captured.CaptureBytes {
					t.Fatalf("actual private-copy allocator was not rejected: %v", err)
				}
			}
			pending := f.client.WorkRequest.GetX(t.Context(), captured.ID)
			if pending.Identity != captured.Identity || pending.PromotionState != "capture_pending" || pending.PromotionCursor != 0 || pending.ArchiveCursor != 0 || pending.CaptureBytes != captured.CaptureBytes || f.client.WorkResponseMember.Query().CountX(t.Context()) != 0 || f.client.WorkCandidate.Query().CountX(t.Context()) != 0 {
				t.Fatal("system error discarded or rejected a paid member")
			}
			http.mu.Lock()
			calls := http.calls
			http.mu.Unlock()
			if calls != 1 {
				t.Fatal("system rejection entered another business pool")
			}
			f.assertUsage(t, 1)
			goruntime.KeepAlive(filler)
			filler = nil
			if mode == "root" {
				if err = root.Release(fillerScope, "system-filler", resourcebudget.Vector{PayloadBytes: fillerBytes}); err != nil {
					t.Fatal(err)
				}
			}
			if err = run(false); err != nil {
				t.Fatal("original paid response did not recover", err)
			}
			http.mu.Lock()
			calls = http.calls
			http.mu.Unlock()
			if calls != 1 || f.client.JobRoundSegment.Query().CountX(t.Context()) != 1 {
				t.Fatal("system recovery repeated paid HTTP or lost checkpoint")
			}
			f.assertUsage(t, 1)
			if got := root.Snapshot(f.ownerID).Instance; got != (resourcebudget.Vector{}) {
				t.Fatalf("system recovery retained transient backing: %+v", got)
			}
		})
	}
}
