package backend_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestResourceBudgetMainTurnOwnsNoCreditAndYieldsWhenCold(t *testing.T) {
	pool := backend.NewLimiterPool()
	pool.Initialize(map[int]int{1: 0, 2: 1})
	defer pool.Shutdown()
	limiter, err := pool.Lookup(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := limiter.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, err := backend.NewRequestAdmission(pool, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 4}, AlignmentConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: 17, Candidates: 16 << 20, Item: 1 << 20, Responses: 1 << 20, Handoff: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	before, changed := a.PipelineSnapshot(), a.Changed()
	a.WaitMainTurn(2, 0)
	a.WaitMainTurn(3, 0) // A later producer cannot steal the first retained turn.
	if !reflect.DeepEqual(before, a.PipelineSnapshot()) {
		t.Fatal("fair turn acquired payload/member/owner credit")
	}
	select {
	case <-changed:
		t.Fatal("waiting turn notified its own Busy probe")
	default:
	}
	warm := backend.RequestAdmissionIntent{ResourceID: 1, BackendID: 1, RoundIndex: 0, Stage: backend.RequestStageMain}
	if hint, err := a.ReadyHint(context.Background(), warm, nil); err != nil || hint.Reason != "fair_main_turn" {
		t.Fatalf("later unfrozen main bypassed retained turn: %+v %v", hint, err)
	}
	aux := warm
	aux.Stage = backend.RequestStageAlignment
	if hint, err := a.ReadyHint(context.Background(), aux, nil); err != nil || hint.Reason != "" {
		t.Fatalf("fair turn blocked legal aux consumer: %+v %v", hint, err)
	}
	cold := warm
	cold.ResourceID, cold.BackendID = 2, 2
	if hint, err := a.ReadyHint(context.Background(), cold, nil); err != nil || hint.AvailableAt.IsZero() {
		t.Fatalf("cold owner has no real cooldown: %+v %v", hint, err)
	}
	if hint, err := a.ReadyHint(context.Background(), warm, nil); err != nil || hint.Reason != "" {
		t.Fatalf("cold head locked warm producer: %+v %v", hint, err)
	}
	a.WaitMainTurn(2, 0)
	a.YieldMainPriority(3, 0)
	if hint, _ := a.ReadyHint(context.Background(), warm, nil); hint.Reason != "fair_main_turn" {
		t.Fatal("unrelated yield removed retained turn")
	}
	a.YieldMainPriority(2, 0)
	if hint, err := a.ReadyHint(context.Background(), warm, nil); err != nil || hint.Reason != "" {
		t.Fatalf("yield left producer locked: %+v %v", hint, err)
	}
}
