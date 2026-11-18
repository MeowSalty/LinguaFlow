package workstate

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestMemberSQLHandoffProofReadsOnlyIdentity(t *testing.T) {
	ctx, client, store, scope, first, recorder := memberDeltaFixture(t)
	r, capture, candidates := integrityMemberPair(t, ctx, client, store, scope, first)
	integrityPublish(t, ctx, store, r, capture)
	recorder.recordPayloadQueries.Store(true)
	if err := Transaction(ctx, client, func(tx *ent.Client) error {
		ok, err := guardPromotedResponse(ctx, tx, candidates[0])
		if err != nil {
			return err
		}
		if !ok {
			t.Fatal("valid member handoff proof was absent")
		}
		bad := candidates[0]
		bad.BaselineVersion++
		if _, err := guardPromotedResponse(ctx, tx, bad); !errors.Is(err, ErrManifest) {
			t.Fatalf("identity projection lost baseline rejection: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if recorder.identityQueries.Load() != 2 || recorder.payloadQueries.Load() != 0 {
		t.Fatalf("handoff proof read payload: identity=%d payload=%d", recorder.identityQueries.Load(), recorder.payloadQueries.Load())
	}
}

func TestMemberSQLPayloadAdmissionPrecedesSelect(t *testing.T) {
	for _, kind := range []string{"input", "output", "capture", "public"} {
		t.Run(kind, func(t *testing.T) {
			ctx, client, store, scope, first, recorder := memberDeltaFixture(t)
			r, capture, _ := integrityMemberPair(t, ctx, client, store, scope, first)
			if err := store.ReserveRequest(ctx, r); err != nil {
				t.Fatal(err)
			}
			if err := store.CaptureResponse(ctx, capture); err != nil {
				t.Fatal(err)
			}
			if kind == "output" || kind == "public" {
				publication := integrityPublication(r, 0, 2, true)
				publication.Glossary = []byte(`[{"source":"aa","target":"bb"}]`)
				if err := store.PublishOutputs(ctx, publication); err != nil {
					t.Fatal(err)
				}
			}
			limits := resourcebudget.Limits{MaxPayloadBytes: 1 << 20}
			root, err := resourcebudget.New(resourcebudget.Policy{Actor: limits, Instance: limits})
			if err != nil {
				t.Fatal(err)
			}
			owner := resourcebudget.Scope{ActorID: 1, CallID: "member-read"}
			filler := make([]byte, 1<<20)
			if err := root.Claim(owner, "filler", resourcebudget.Vector{PayloadBytes: int64(cap(filler))}); err != nil {
				t.Fatal(err)
			}
			owned := backend.WithBuildAllocation(ctx, 0, func(n int64) error {
				if prior, ok := root.Usage(owner, "reader"); ok {
					prior.PayloadBytes += n
					return root.Transfer(owner, "reader", prior)
				}
				return root.Claim(owner, "reader", resourcebudget.Vector{PayloadBytes: n})
			}, func(n int64) {
				if err := root.Release(owner, "reader", resourcebudget.Vector{PayloadBytes: n}); err != nil {
					t.Error(err)
				}
			})
			load := func(ctx context.Context) ([]byte, func(), error) {
				switch kind {
				case "input":
					m, e := store.LoadInputMember(ctx, requestMemberKey(r, 0), 1<<20)
					return m.Payload, m.Release, e
				case "output":
					m, e := store.LoadOutputMember(ctx, requestMemberKey(r, 0), false, 1<<20)
					return m.Payload, m.Release, e
				case "capture":
					m, e := store.LoadCapture(ctx, r.ID, 1<<20)
					return m.Payload, m.Release, e
				default:
					m, e := store.LoadResponsePublic(ctx, r.ID, 1<<20)
					return m.Payload, m.Release, e
				}
			}
			recorder.recordPayloadQueries.Store(true)
			if p, release, err := load(owned); !errors.Is(err, resourcebudget.ErrCapacity) || len(p) != 0 || release != nil {
				t.Fatalf("full Root read was admitted: %v", err)
			}
			if recorder.payloadQueries.Load() != 0 || root.Snapshot(1).Instance.PayloadBytes != int64(cap(filler)) {
				t.Fatal("payload SELECT ran before admission or failure changed Root")
			}
			filler = nil
			if err := root.Release(owner, "filler", resourcebudget.Vector{PayloadBytes: 1 << 20}); err != nil {
				t.Fatal(err)
			}
			p, release, err := load(owned)
			if err != nil || len(p) == 0 || release == nil || recorder.payloadQueries.Load() != 1 || root.Snapshot(1).Instance.PayloadBytes != int64(cap(p)) {
				t.Fatalf("read/actual backing mismatch: err=%v queries=%d actual=%d root=%+v", err, recorder.payloadQueries.Load(), cap(p), root.Snapshot(1).Instance)
			}
			p = nil
			release()
			release()
			if root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
				t.Fatal("last frame release leaked or duplicated charge")
			}
			_ = client
		})
	}
}

func TestMemberSQLPublicIntegrityRejectsLengthAndSameLengthDamage(t *testing.T) {
	for _, kind := range []string{"length", "same_length"} {
		t.Run(kind, func(t *testing.T) {
			ctx, client, store, scope, first, _ := memberDeltaFixture(t)
			r, capture, _ := integrityMemberPair(t, ctx, client, store, scope, first)
			if err := store.ReserveRequest(ctx, r); err != nil {
				t.Fatal(err)
			}
			if err := store.CaptureResponse(ctx, capture); err != nil {
				t.Fatal(err)
			}
			publication := integrityPublication(r, 0, 2, true)
			publication.Glossary = []byte(`[{"source":"aa","target":"bb"}]`)
			if err := store.PublishOutputs(ctx, publication); err != nil {
				t.Fatal(err)
			}
			bad := []byte(`[{"source":"aa","target":"cc"}]`)
			if kind == "length" {
				bad = append(bad, ' ')
			}
			row := client.WorkRequest.Query().OnlyX(ctx)
			client.WorkRequest.UpdateOneID(row.ID).SetResponseGlossary(bad).ExecX(ctx)
			var live int64
			owned := backend.WithBuildAllocation(ctx, 0, func(n int64) error { live += n; return nil }, func(n int64) { live -= n })
			m, err := store.LoadResponsePublic(owned, r.ID, 1<<20)
			if err == nil || len(m.Payload) != 0 || m.Release != nil || live != 0 {
				t.Fatalf("damaged public facts became visible: err=%v live=%d", err, live)
			}
			if err := store.PublishMemberBusiness(owned, r.ID, nil); err == nil {
				t.Fatal("business publication accepted damaged public facts")
			}
			if client.WorkRequest.GetX(ctx, row.ID).PromotionState != "outputs_ready" || client.WorkResponseMember.Query().CountX(ctx) != 2 || client.UsageRecord.Query().CountX(ctx) != 1 {
				t.Fatal("damage rejection changed siblings, publication or usage")
			}
		})
	}
}
