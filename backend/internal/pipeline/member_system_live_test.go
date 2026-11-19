package pipeline

import (
	"context"
	"errors"
	goruntime "runtime"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

type memberLiveCapacityStore struct {
	*MemoryRoundStore
	probe         func(context.Context) error
	before, after backend.RejectionEvidence
	probeErr      error
	reached       bool
}

func (s *memberLiveCapacityStore) LoadCapture(ctx context.Context, id string, limit int64) (CapturedResponse, error) {
	if !s.reached {
		s.reached = true
		s.before = backend.RejectionEvidenceFromContext(ctx)
		s.probeErr = s.probe(ctx)
		s.after = backend.RejectionEvidenceFromContext(ctx)
		if s.probeErr != nil {
			return CapturedResponse{}, s.probeErr
		}
	}
	return s.MemoryRoundStore.LoadCapture(ctx, id, limit)
}

func TestMemberLivePaidRootProbeKeepsOriginalMeter(t *testing.T) {
	for _, mode := range []string{"no_drain", "other_main"} {
		t.Run(mode, func(t *testing.T) {
			r := stagedTestRuntime(t, DefaultCandidateLimits())
			const maximum = int64(16 << 20)
			root, err := resourcebudget.New(resourcebudget.Policy{Actor: resourcebudget.Limits{MaxPayloadBytes: maximum}, Instance: resourcebudget.Limits{MaxPayloadBytes: maximum}})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "live-main"}); err != nil {
				t.Fatal(err)
			}
			consumer := resourcebudget.Scope{ActorID: 1, CallID: "live-other"}
			var backing []byte
			var charged resourcebudget.Vector
			releaseConsumer := func() error {
				goruntime.KeepAlive(backing)
				backing = nil
				if charged == (resourcebudget.Vector{}) {
					return nil
				}
				err := root.Release(consumer, "live-other", charged)
				if err == nil {
					charged = resourcebudget.Vector{}
				}
				return err
			}
			s := &memberLiveCapacityStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
				if err := releaseConsumer(); err != nil {
					t.Error(err)
				}
			})
			s.probe = func(ctx context.Context) error {
				charged.PayloadBytes = maximum - root.Snapshot(1).Instance.PayloadBytes
				if mode == "other_main" {
					charged.MainResponseOwners = 1
				}
				if err := root.Claim(consumer, "live-other", charged); err != nil {
					return err
				}
				backing = make([]byte, charged.PayloadBytes, charged.PayloadBytes)
				b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
				if err != nil {
					return err
				}
				defer b.CloseRetired()
				// The original paid R has already been partly consumed by capture.
				// An actual allocation above the remaining credit must probe Root.
				n := s.before.MeterPaid + s.before.MeterWire + 1
				allocation, err := b.OwnAllocation(n, 1)
				if err == nil {
					data := make([]byte, n, n)
					goruntime.KeepAlive(data)
					data = nil
					allocation.Release()
					return errors.New("live over-R allocation unexpectedly succeeded")
				}
				if mode == "other_main" {
					// The independent response consumer retires its actual backing.
					// Its already-captured Root wake must allow the same paid response.
					if err := releaseConsumer(); err != nil {
						return err
					}
				}
				return err
			}
			doc := newTestDoc(1)
			defer r.CloseDocument(doc)
			remote := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
				return &backend.Response{Text: `{"translations":{"1":"legal"}}`, Usage: backend.Usage{PromptTokens: 3, CompletionTokens: 1}}, nil
			}}
			result, err := RunStagedRound(t.Context(), Round{Concurrency: 1, Handler: stagedTestHandler(t, r, remote)}, doc, s, r, quietLogger(), progress.Nop{})
			if !s.reached || remote.calls.Load() != 1 || s.before.MeterID == "" || s.before.MeterID != s.after.MeterID || s.before.MeterPaid <= 0 || s.before.MeterPaid >= r.responseLimit() || s.before.MeterPaid != s.after.MeterPaid || s.before.MeterWire != s.after.MeterWire || s.before.SourceLedger != s.after.SourceLedger {
				t.Fatalf("live probe replaced meter or restored R: reached=%t HTTP=%d paid_before=%d paid_after=%d same_meter=%t err=%v", s.reached, remote.calls.Load(), s.before.MeterPaid, s.after.MeterPaid, s.before.MeterID == s.after.MeterID, err)
			}
			var capacity *resourcebudget.CapacityError
			if !errors.As(s.probeErr, &capacity) || capacity.Stage != "root_admission" || capacity.Dimension != "payload_bytes" || capacity.Requested <= 0 {
				t.Fatalf("live actual allocator lost Root evidence: %v", s.probeErr)
			}
			if mode == "no_drain" {
				var deferred *WorkDeferred
				if !errors.Is(err, backend.ErrNoCapacityDrain) || workDeferred(err) != nil || errors.As(err, &deferred) || len(result.Resolved) != 0 {
					t.Fatalf("live self invented capacity drain: resolved=%d err=%v", len(result.Resolved), err)
				}
				for _, request := range s.requests {
					if request.response.Phase != responsecontract.CapturePending || request.response.Cursor != 0 || len(request.memberOutcomes) != 0 || !request.record.UsageKnown {
						t.Fatal("live system rejection changed paid response")
					}
				}
			} else {
				if err != nil || !capacity.DrainOutside || workDeferred(s.probeErr) == nil || len(result.Resolved) != 1 || doc.Segments[0].Target != "legal" {
					t.Fatalf("other main did not resume original response: resolved=%d drain=%t err=%v", len(result.Resolved), capacity.DrainOutside, err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if err := releaseConsumer(); err != nil {
				t.Fatal(err)
			}
			backing = nil
			r.CloseDocument(doc)
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
				t.Fatal("live probe retained actual backing")
			}
		})
	}
}
