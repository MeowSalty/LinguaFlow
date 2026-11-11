package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func TestBudgetIntegrationJobDefinitionRejectsBeforeAllocation(t *testing.T) {
	for _, kind := range []string{"metadata_bytes", "metadata_nodes", "descriptors"} {
		t.Run(kind, func(t *testing.T) {
			f := newRubyPipelineFixture(t, 1)
			limits := resourcebudget.Limits{MaxUnconfirmedMembers: 4096, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 16, MaxMainResponseOwners: 16, MaxAuxResponseOwners: 16, MaxBuildWorkers: 16, MaxQueuedDescriptors: 256, MaxLogicalStorageBytes: 1 << 30}
			if kind == "descriptors" {
				limits.MaxQueuedDescriptors = 3
			}
			root, err := resourcebudget.New(resourcebudget.Policy{Actor: limits, Instance: limits})
			if err != nil {
				t.Fatal(err)
			}
			source := workstate.InventorySource{DB: f.client, Driver: "sqlite", ItemBytes: f.runner.pipeCfg.Candidates.ItemBytes}
			if err = root.Rebuild(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			f.runner.SetRootBudget(root, func(ctx context.Context, actor int) error {
				a := source
				a.ActorID = actor
				return root.RebuildActor(ctx, actor, a)
			})
			f.runner.SetBudgetSource(source, nil)
			if kind == "metadata_bytes" {
				f.runner.pipeCfg.ResponseHandoffBytes = 1024
			}
			if kind == "metadata_nodes" {
				ids := make([]int, 70000)
				for i := range ids {
					ids[i] = i + 1
				}
				f.client.JobResource.UpdateOneID(f.jobResources[0]).SetSegmentIds(ids).ExecX(t.Context())
			}
			_, _, _, _, close, err := f.runner.loadJobDefinition(t.Context(), f.jobID)
			close()
			if !errors.Is(err, backend.ErrBuildCapacity) && !errors.Is(err, resourcebudget.ErrCapacity) {
				t.Fatalf("definition bound did not reject before entry: %v", err)
			}
			main, aux, _, _ := f.calls()
			if main != 0 || aux != 0 {
				t.Fatal("oversized definition reached HTTP")
			}
			if got := root.Snapshot(0).Instance; got != (resourcebudget.Vector{}) {
				t.Fatalf("failed definition retained root: %+v", got)
			}
			t.Logf("%s rejected before ORM definition/maps/HTTP, no root residue", kind)
		})
	}
}
