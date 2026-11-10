package backend_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func budgetFixture(t *testing.T, j int64) *backend.RequestAdmission {
	t.Helper()
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 32, 1: 4}, AlignmentConcurrency: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: 256, Candidates: 16 << 20, Item: 1 << 20, Responses: 1 << 20, Handoff: j}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRubyHandoffJointJLimits(t *testing.T) {
	for _, tc := range []struct{ n, want int }{{7, 6}, {1, 12}} {
		t.Run(fmt.Sprint(tc.n), func(t *testing.T) {
			a := budgetFixture(t, 16<<20)
			var held []*backend.RequestPermit
			defer func() {
				for _, p := range held {
					p.Release()
				}
			}()
			for i := 0; i < 32; i++ {
				id := fmt.Sprint(i)
				ok, err := a.PrepareMain(id, 0, 1, 8<<10, 0, int64(tc.n)<<20, false)
				if err != nil || !ok {
					t.Fatalf("prepare %d: %v/%v", i, ok, err)
				}
				p, w, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: id, RoundIndex: 0, Stage: backend.RequestStageMain}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if p == nil {
					a.ReleasePrepared(id)
					if w.Reason != "response_handoff" {
						t.Fatalf("reason %s", w.Reason)
					}
					break
				}
				p.RequestDone()
				held = append(held, p)
			}
			if len(held) != tc.want {
				t.Fatalf("ordinary owners=%d want %d", len(held), tc.want)
			}
			s := a.PipelineSnapshot()
			if s.MainOwners[0] != tc.want || s.HandoffBytes > 16<<20 {
				t.Fatalf("snapshot %+v", s)
			}
		})
	}
}

func TestRubyHandoffAuxEightGrowthAndInput(t *testing.T) {
	a := budgetFixture(t, 64<<20)
	var held []*backend.RequestPermit
	defer func() {
		for _, p := range held {
			p.Release()
		}
	}()
	for i := 0; i < 8; i++ {
		id := fmt.Sprint(i)
		if err := a.RestoreBudgetEntry(id, backend.PipelineBudgetEntry{Count: 1, Bytes: 1024, Durable: true}); err != nil {
			t.Fatal(err)
		}
		p, w, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{Stage: backend.RequestStageAlignment, RoundIndex: 0, CandidateIDs: []string{id}, InputBytes: 2048}, nil)
		if err != nil || p == nil {
			t.Fatalf("aux %d: %+v/%v", i, w, err)
		}
		p.RequestDone()
		held = append(held, p)
	}
	s := a.PipelineSnapshot()
	if s.AuxOwners != 8 || s.AuxInputBytes != 8*2048 || s.CandidateBytes+s.GrowthBytes != 8<<20 {
		t.Fatalf("snapshot %+v", s)
	}
	if p, w, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{Stage: backend.RequestStageAlignment, RoundIndex: 0}, nil); err != nil || p != nil || w.Reason != "alignment_concurrency" {
		t.Fatalf("ninth %+v/%v", w, err)
	}
	for _, p := range held {
		p.Release()
	}
	s = a.PipelineSnapshot()
	if s.AuxInputBytes != 0 || s.AuxOwners != 0 || s.GrowthBytes == 0 {
		t.Fatalf("growth must survive response owner: %+v", s)
	}
}

func TestRubyHandoffRoundDenominatorDeduplicates(t *testing.T) {
	a := budgetFixture(t, 64<<20)
	if n, err := a.ActiveMainCapacity(0, []int{0, 0, 1, 1}); err != nil || n != 36 {
		t.Fatalf("D=%d/%v", n, err)
	}
}
