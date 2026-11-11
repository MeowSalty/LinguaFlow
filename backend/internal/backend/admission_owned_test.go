package backend

import "testing"

func TestOwnedPayloadObservationTracksBackingWithoutGrant(t *testing.T) {
	a, _ := NewRequestAdmission(nil, RequestAdmissionConfig{})
	if err := a.ConfigurePipelineBudget(PipelineBudgetLimits{Segments: 256, Candidates: 16 << 20, Item: 1 << 20, Responses: 1 << 20, Handoff: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	a.ObserveOwned("raw", "one", 17)
	a.ObserveOwned("raw", "two", 31)
	a.ObserveOwned("codec", "decode", 64)
	a.ObserveOwned("raw", "one", 0)
	a.ObserveOwned("codec", "decode", 0)
	s := a.OwnedPayloadSnapshot()
	if s.Peak["raw"] != 48 || s.TotalPeak != 112 || s.Current["raw"] != 31 || s.Total != 31 {
		t.Fatalf("incorrect actual backing lifetime %+v", s)
	}
	budget := a.PipelineSnapshot()
	if budget.HandoffBytes != 0 || len(budget.MainOwners) > 0 || budget.Segments != 0 {
		t.Fatalf("observation changed resource grants %+v", budget)
	}
	a.ObserveOwned("raw", "two", 0)
	if a.OwnedPayloadSnapshot().Total != 0 {
		t.Fatal("terminal diagnostic body retained")
	}
}
