package pipeline

import (
	"context"
	"math"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestRubyHandoffAcceptanceStationAndOwnerAreSparse(t *testing.T) {
	station := NewStation(math.MaxInt)
	if station.capacity != math.MaxInt || cap(station.changed) != 0 || !station.TryAcquire() || !station.TryAcquire() {
		t.Fatal("MaxInt station allocated capacity-sized storage or lost scalar capacity")
	}
	station.Release()
	station.Release()
	if station.inflight != 0 {
		t.Fatal("sparse station leaked inflight")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if station.Acquire(cancelled) {
		t.Fatal("station accepted cancelled acquisition")
	}
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: math.MaxInt}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewExecutionRuntime(a, DefaultCandidateLimits(), nil)
	defer runtime.Close()
	if ok, err := a.PrepareMain("sparse-owner", 0, 1, 128, 128, 0, false); err != nil || !ok {
		t.Fatalf("sparse prepared input: %v", err)
	}
	permit, _, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "sparse-owner", RoundIndex: 0, BackendID: 1, Stage: backend.RequestStageMain}, nil)
	if err != nil || permit == nil {
		t.Fatalf("sparse MaxInt owner failed: %v", err)
	}
	if snap := a.PipelineSnapshot(); len(snap.MainOwners) != 1 || snap.MainOwners[0] != 1 {
		permit.Release()
		t.Fatalf("owner map grew with configured capacity: %+v", snap)
	}
	permit.Release()
}

func TestRubyHandoffAcceptanceActiveRoundArithmeticAndInvalidCaps(t *testing.T) {
	for _, capacity := range []int{0, -1} {
		if a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: capacity}}); err == nil || a != nil {
			t.Fatalf("explicit invalid C%d accepted", capacity)
		}
	}
	if a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}, AlignmentConcurrency: -1}); err == nil || a != nil {
		t.Fatal("negative A accepted")
	}
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: math.MaxInt, 1: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ActiveMainCapacity(0, []int{0, 1}); err == nil {
		t.Fatal("active-round sum overflow accepted")
	}
	if n, err := a.ActiveMainCapacity(0, []int{0, 0}); err != nil || n != math.MaxInt {
		t.Fatalf("same round counted twice: %d %v", n, err)
	}
}
