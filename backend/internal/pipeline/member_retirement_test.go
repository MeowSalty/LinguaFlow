package pipeline

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

type memberRetirementPublicationStore struct {
	*MemoryRoundStore
	installs int
}

func (s *memberRetirementPublicationStore) PublishBusiness(ctx context.Context, id string, out glossary.ResponseBusiness, install func()) (MainResponseHeader, error) {
	if install == nil {
		return s.MemoryRoundStore.PublishBusiness(ctx, id, out, nil)
	}
	return s.MemoryRoundStore.PublishBusiness(ctx, id, out, func() { s.installs++; install() })
}

func TestMemberMemoryStoreRetirementFailuresRemainRetryable(t *testing.T) {
	for _, mode := range []string{"committed_close", "published_retire", "prepared_rollback"} {
		t.Run(mode, func(t *testing.T) {
			r := stagedTestRuntime(t, DefaultCandidateLimits())
			root, err := resourcebudget.New(resourcebudget.Policy{})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "retire:" + mode}); err != nil {
				t.Fatal(err)
			}
			s := NewMemoryRoundStore(nil)
			t.Cleanup(func() {
				if e := s.Close(); e != nil {
					t.Error(e)
				}
			})
			ctx := r.BuildContext(WithExecutionRuntime(t.Context(), r), "retire-seed")
			h := memberFixtureReserve(t, ctx, s, "retire", 4096, memberFixtureInputs(1))
			fault := errors.New("injected actual member refund failure")
			var retirementProbe func() error
			failOnce := func(b *memberBacking) {
				t.Helper()
				release := b.release
				failed := false
				b.release = func() error {
					if b.payload != nil {
						return errors.New("refund ran before physical backing retired")
					}
					// A header read here proves retirement runs outside state mu.
					if _, e := s.ResponseHeader(t.Context(), h.ID); e != nil {
						return e
					}
					if retirementProbe != nil {
						if e := retirementProbe(); e != nil {
							return e
						}
					}
					if !failed {
						failed = true
						return fault
					}
					return release()
				}
			}
			if mode == "committed_close" {
				p, a := memberFixturePayload(t, ctx, map[string]string{"capture": "paid"})
				h, err = s.CaptureResponse(ctx, CapturedResponse{MainResponseHeader: h, Payload: p, Allocation: a})
				if err != nil {
					t.Fatal(err)
				}
				backing := s.requests[h.ID].capture
				owner := "capture:" + h.ID
				paid := int64(cap(backing.payload))
				p, a = nil, nil
				failOnce(backing)
				if e := s.Close(); !errors.Is(e, fault) {
					t.Fatalf("Close swallowed member refund: %v", e)
				}
				if s.requests[h.ID] == nil || s.requests[h.ID].retiredBackings != backing || backing.refs != 1 || backing.payload != nil || r.Admission.BuildBytes(owner) != paid {
					t.Fatal("failed Close lost the actual owner/backing retry reference")
				}
				before := root.Snapshot(1).Instance
				if _, e := s.LoadCapture(ctx, h.ID, 4096); !errors.Is(e, backend.ErrAdmissionClosed) {
					t.Fatalf("closed capture borrower was accepted: %v", e)
				}
				if _, e := s.LoadInputMember(ctx, archiveMemberKey(h, 0), 4096); !errors.Is(e, backend.ErrAdmissionClosed) {
					t.Fatalf("closed input borrower was accepted: %v", e)
				}
				if _, e := s.LoadOutputMember(ctx, archiveMemberKey(h, 0), false, 4096); !errors.Is(e, backend.ErrAdmissionClosed) {
					t.Fatalf("closed output borrower was accepted: %v", e)
				}
				if e := s.StageCorrectedMember(ctx, OutputMemberFrame{Key: archiveMemberKey(h, 0), Payload: []byte("x"), Bytes: 1, Digest: responseDigest([]byte("x"))}); !errors.Is(e, backend.ErrAdmissionClosed) {
					t.Fatalf("closed correction transferred ownership: %v", e)
				}
				if e := s.Save(ctx, nil); !errors.Is(e, backend.ErrAdmissionClosed) {
					t.Fatalf("closed save built a candidate: %v", e)
				}
				if _, e := s.Seal(ctx, []int{0}); !errors.Is(e, backend.ErrAdmissionClosed) {
					t.Fatalf("closed seal mutated the manifest: %v", e)
				}
				if e := s.Cursors(ctx, []CursorTransition{{Index: 0}}); !errors.Is(e, backend.ErrAdmissionClosed) {
					t.Fatalf("closed cursor mutated the manifest: %v", e)
				}
				if root.Snapshot(1).Instance != before || r.Admission.BuildBytes(owner) != paid {
					t.Fatal("closed calls changed conservative retirement fees")
				}
			} else {
				h = memberFixtureOutputs(t, ctx, s, h, []archivedOutput{{Target: "legal"}}, []glossary.Entry{{Source: "term", Target: "译文"}})
				if mode == "published_retire" {
					old := s.requests[h.ID].publicBacking
					failOnce(old)
					dictionary := glossary.NewMemory()
					t.Cleanup(func() {
						if e := dictionary.Close(); e != nil {
							t.Error(e)
						}
					})
					if e := r.Admission.RestoreResponseOwnership(h.ID, 0, 0, h.Facts(), h.Phase, false); e != nil {
						t.Fatal(e)
					}
					probes := 0
					retirementProbe = func() error {
						// Reenter the actual dictionary writer and the store publisher.
						// Both publication mutexes must already be free at refund time.
						if _, e := dictionary.Add(ctx, glossary.Entry{Source: "probe", Target: "证明"}); e != nil {
							return e
						}
						if _, e := s.PublishBusiness(ctx, h.ID, glossary.ResponseBusiness{}, nil); e != nil {
							return e
						}
						probes++
						return nil
					}
					handler := &TranslateHandler{Glossary: dictionary, InlineBootstrap: true, Repair: defaultRepairOpts()}
					publishing := &memberRetirementPublicationStore{MemoryRoundStore: s}
					confirmed, e := processArchivedGlossary(ctx, handler, h, publishing, r, quietLogger())
					p := bytes.Clone(s.requests[h.ID].response.Receipt)
					if !errors.Is(e, fault) || confirmed.Phase != responsecontract.BusinessReady || len(p) == 0 || s.requests[h.ID].retiredBackings != old || probes != 1 || publishing.installs != 1 || old.payload != nil {
						t.Fatalf("retirement failure: phase=%s fault=%t receipt_bytes=%d retired_same=%t probes=%d installs=%d old_live=%t err=%v", confirmed.Phase, errors.Is(e, fault), len(p), s.requests[h.ID].retiredBackings == old, probes, publishing.installs, old.payload != nil, e)
					}
					if e = s.RetireResponse(context.Background(), h.ID); e != nil || probes != 2 || publishing.installs != 1 || s.requests[h.ID].retiredBackings != nil || !bytes.Equal(s.requests[h.ID].response.Receipt, p) {
						t.Fatalf("refund retry lost the accepted receipt: probes=%d err=%v", probes, e)
					}
					retirementProbe = nil
					if e := dictionary.Close(); e != nil {
						t.Fatal(e)
					}
				} else {
					frame := memberFixtureCorrection(t, ctx, s, h, 0, "corrected")
					backing := s.requests[h.ID].stagedCorrectionBackings[0]
					owner := "corrected:" + h.ID + ":0"
					paid := r.Admission.BuildBytes(owner)
					if paid != int64(cap(frame.Payload)) || backing.prepared == nil {
						t.Fatal("correction did not retain actual prepared backing")
					}
					frame.Payload = nil
					// Disturb the real transfer destination, making Rollback reject.
					// Its backing remains paid by the same Root until repaired.
					if e := r.Admission.MoveBuildBytes(owner, "retire-drift", paid); e != nil {
						t.Fatal(e)
					}
					before := root.Snapshot(1).Instance
					if e := s.AbortMemberBusiness(ctx, h.ID); !errors.Is(e, backend.ErrBuildCapacity) {
						t.Fatalf("actual prepared rollback failure was swallowed: %v", e)
					}
					if s.requests[h.ID].retiredBackings != backing || backing.prepared == nil || backing.refs != 1 || backing.payload != nil || root.Snapshot(1).Instance != before {
						t.Fatal("failed Rollback dropped prepared owner or charged a replacement")
					}
					if e := r.Admission.MoveBuildBytes("retire-drift", owner, paid); e != nil {
						t.Fatal(e)
					}
				}
			}
			if err = s.Close(); err != nil {
				t.Fatal("retry did not retire actual store backing", err)
			}
			if err = r.Close(); err != nil {
				t.Fatal(err)
			}
			if got := root.Snapshot(1); got.Instance != (resourcebudget.Vector{}) || got.ActiveIdentities != 0 || s.retiredResponses != nil || len(s.requests) != 0 {
				t.Fatalf("last retry retained owner or reference: %+v", got)
			}
		})
	}
	t.Run("borrowed_refund_inflight", func(t *testing.T) {
		r := stagedTestRuntime(t, DefaultCandidateLimits())
		root, err := resourcebudget.New(resourcebudget.Policy{})
		if err != nil {
			t.Fatal(err)
		}
		if err = r.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "retire:borrowed"}); err != nil {
			t.Fatal(err)
		}
		s := NewMemoryRoundStore(nil)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		entered, resume, joined := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var resumeOnce sync.Once
		started := false
		t.Cleanup(func() {
			resumeOnce.Do(func() { close(resume) })
			cancel()
			if started {
				<-joined
			}
			if e := s.Close(); e != nil {
				t.Error(e)
			}
			if e := r.Close(); e != nil {
				t.Error(e)
			}
		})
		ctx = r.BuildContext(WithExecutionRuntime(ctx, r), "borrowed-seed")
		h := memberFixtureReserve(t, ctx, s, "borrowed", 4096, memberFixtureInputs(2))
		h = memberFixtureOutputs(t, ctx, s, h, []archivedOutput{{Target: "model first"}, {Target: "model sibling"}}, nil)
		first := memberFixtureCorrection(t, ctx, s, h, 0, "first")
		sibling := memberFixtureCorrection(t, ctx, s, h, 1, "sibling")
		firstBytes, siblingBytes := int64(cap(first.Payload)), int64(cap(sibling.Payload))
		receipt, receiptAllocation := memberFixturePayload(t, ctx, glossary.ResponseReceipt{Version: glossary.ResponseReceiptVersion, RequestID: h.ID})
		if _, err = s.PublishBusiness(ctx, h.ID, glossary.ResponseBusiness{Receipt: receipt, ReceiptAllocation: receiptAllocation}, nil); err != nil {
			t.Fatal(err)
		}
		first = OutputMemberFrame{}
		sibling = OutputMemberFrame{}
		receipt, receiptAllocation = nil, nil
		borrowed, err := s.LoadOutputMember(ctx, archiveMemberKey(h, 0), true, 4096)
		if err != nil {
			t.Fatal(err)
		}
		defer borrowed.Release()
		kept, err := s.LoadOutputMember(ctx, archiveMemberKey(h, 1), true, 4096)
		if err != nil {
			t.Fatal(err)
		}
		defer kept.Release()
		backing := s.requests[h.ID].correctionBackings[0]
		owner := "corrected:" + h.ID + ":0"
		paid := r.Admission.BuildBytes(owner)
		if paid != firstBytes || int64(cap(kept.Payload)) != siblingBytes {
			t.Fatal("fixture did not retain both actual corrected member backings")
		}
		fault := errors.New("injected last borrower refund failure")
		var attempts atomic.Int32
		release := backing.release
		backing.release = func() error {
			if backing.payload != nil {
				return errors.New("last borrower refunded live physical backing")
			}
			if _, e := s.ResponseHeader(t.Context(), h.ID); e != nil {
				return e
			}
			if attempts.Add(1) == 1 {
				// A last-reader callback can reenter retirement while its own
				// refund is in flight; that must retain, not retry, this node.
				if e := s.RetireResponse(t.Context(), h.ID); e != nil {
					return e
				}
				close(entered)
				select {
				case <-resume:
				case <-ctx.Done():
				}
				return fault
			}
			return release()
		}
		if _, err = s.PromoteMember(ctx, archiveMemberKey(h, 0), nil, WorkCursor{}, MemberOutcome{Key: archiveMemberKey(h, 0), Reason: responsecontract.MemberInvalid}); err != nil {
			t.Fatal(err)
		}
		drop := borrowed.Release
		borrowed = OutputMemberFrame{}
		started = true
		go func() { drop(); drop(); close(joined) }()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("last-reader refund did not enter", ctx.Err())
		}
		if err = s.RetireResponse(ctx, h.ID); err != nil || attempts.Load() != 1 || r.Admission.BuildBytes(owner) != paid || s.requests[h.ID].retiredBackings != backing || backing.payload != nil {
			t.Fatal("in-flight refund lost or retried its charged retirement node", err)
		}
		resumeOnce.Do(func() { close(resume) })
		<-joined
		if attempts.Load() != 1 || r.Admission.BuildBytes(owner) != paid || s.requests[h.ID].retiredBackings != backing || memberFixtureDecode(t, kept).Target != "sibling" || len(s.requests[h.ID].response.Receipt) == 0 {
			t.Fatal("failed last-reader refund lost fees, immutable sibling, receipt, or retry rights")
		}
		kept.Payload = nil
		kept.Release()
		kept.Release()
		if err = s.Close(); err != nil || attempts.Load() != 2 || r.Admission.BuildBytes(owner) != 0 {
			t.Fatal("Close did not retry the same failed last-reader refund", err)
		}
		if err = r.Close(); err != nil {
			t.Fatal(err)
		}
		if got := root.Snapshot(1); got.Instance != (resourcebudget.Vector{}) || got.ActiveIdentities != 0 || len(s.requests) != 0 {
			t.Fatal("joined last-reader retry retained Root ownership")
		}
	})
}
