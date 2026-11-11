package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

type releaseOnBudgetAbortStore struct {
	*MemoryRoundStore
	once    sync.Once
	release func()
}

func (s *releaseOnBudgetAbortStore) AbortRequest(ctx context.Context, id string) error {
	if err := s.MemoryRoundStore.AbortRequest(ctx, id); err != nil {
		return err
	}
	s.once.Do(s.release)
	return nil
}

// A completed worker event carries a capacity decision made before an external
// owner releases. Processing that event must retry, never reclassify it using a
// later empty consumer snapshot.
func TestBudgetReleasedExternalOwnerRetriesStaleDeferredProbe(t *testing.T) {
	l := resourcebudget.Limits{MaxUnconfirmedMembers: 256, MaxPayloadBytes: 8 << 20, MaxPrepareOwners: 8, MaxMainResponseOwners: 8, MaxAuxResponseOwners: 8, MaxBuildWorkers: 8, MaxQueuedDescriptors: 32, MaxLogicalStorageBytes: 64 << 20}
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: l, Instance: l})
	if err != nil {
		t.Fatal(err)
	}
	other := resourcebudget.Scope{ActorID: 1, CallID: "other"}
	pressure := resourcebudget.Vector{PayloadBytes: 7 << 20, BuildWorkers: 1}
	if err = root.Claim(other, "other-runtime", pressure); err != nil {
		t.Fatal(err)
	}
	scope := resourcebudget.Scope{ActorID: 1, CallID: "request"}
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	if err = r.Admission.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Admission.PrepareMembers("request", 1, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Admission.PrepareMain("request", 0, 0, 1024, 1024, 0, false); err != nil {
		t.Fatal(err)
	}
	p, wait, err := r.Admission.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "request", RoundIndex: 0, Stage: backend.RequestStageMain}, nil)
	if p != nil || err != nil || wait.Reason != "aggregate_capacity" {
		t.Fatalf("not a temporary joint failure: %+v %v", wait, err)
	}
	if err = root.Release(other, "other-runtime", pressure); err != nil {
		t.Fatal(err)
	}
	if root.CanDrainOutside(scope) {
		t.Fatal("expected released worker")
	}
	p, _, err = r.Admission.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "request", RoundIndex: 0, Stage: backend.RequestStageMain}, nil)
	if err != nil || p == nil {
		t.Fatalf("fresh grant misclassified released owner: %v", err)
	}
	p.Release()
	r.Window.Release("request")
	if err = root.Claim(other, "other-runtime", pressure); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	remote := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"translations":{"1":"translated"}}`}, nil
	}}
	released := false
	store := &releaseOnBudgetAbortStore{MemoryRoundStore: NewMemoryRoundStore(nil), release: func() {
		released = true
		if err := root.Release(other, "other-runtime", pressure); err != nil {
			t.Error(err)
		}
	}}
	result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, r, remote)}, newTestDoc(1), store, r, quietLogger(), nil)
	if errors.Is(err, backend.ErrNoCapacityDrain) || err != nil || len(result.Resolved) != 1 {
		t.Fatalf("released root lost fresh dispatch: %+v %v", result, err)
	}
	if !released || remote.calls.Load() != 1 {
		t.Fatalf("fixture did not release after failed actual probe: release=%v HTTP=%d", released, remote.calls.Load())
	}
}
