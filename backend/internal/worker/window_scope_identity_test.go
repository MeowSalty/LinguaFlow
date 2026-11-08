package worker

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func TestRoundStoreWindowHeaderUsesDurableScopeIdentity(t *testing.T) {
	// Scope IDs are database identities, not a round's ordinal or job_resource ID.
	store := &roundStore{scope: workstate.Scope{JobID: 11, ResourceID: 23, RoundID: 37}, round: 0}
	if store.ResourceIdentity() != 23 || store.RoundIdentity() != 37 {
		t.Fatal("round store lost its durable scope")
	}
	w := pipeline.NewCandidateWindow(pipeline.DefaultCandidateLimits())
	header := workstate.CandidateHeader{ID: "durable-future", Scope: workstate.Scope{JobID: 11, ResourceID: 23, RoundID: 41}, PayloadBytes: 1024, DTOVersion: pipeline.CandidateDTOVersion}
	if err := restoreCandidateHeader(w, header); err != nil {
		t.Fatal(err)
	}
	consumer := w.EnterScope(store.ResourceIdentity(), store.RoundIdentity())
	defer consumer.Leave()
	consumer.RecoveryComplete()
	if result, _ := w.TryReserveOwned("current", 16, store.ResourceIdentity(), consumer); result != pipeline.WindowPinnedLimit {
		t.Fatalf("real header/store identities did not establish fixed pin: %v", result)
	}
	if err := w.ClaimRecovered(header.ID, header.PayloadBytes, consumer); err == nil {
		t.Fatal("current scope claimed the future header")
	}
	future := w.EnterScope(23, 41)
	defer future.Leave()
	if err := w.ClaimRecovered(header.ID, header.PayloadBytes, future); err != nil {
		t.Fatal(err)
	}
	if result, _ := w.TryReserveOwned("while_active", 16, store.ResourceIdentity(), consumer); result != pipeline.WindowBusy {
		t.Fatalf("active future consumer treated as fixed occupancy: %v", result)
	}
	header.DTOVersion++
	if err := restoreCandidateHeader(w, header); err == nil {
		t.Fatal("unsupported DTO header accepted")
	}
}
