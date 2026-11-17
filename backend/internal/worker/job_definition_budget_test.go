package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func TestBudgetIntegrationJobDefinitionRejectsBeforeAllocation(t *testing.T) {
	for _, kind := range []string{"metadata_bytes", "metadata_many_values", "descriptors"} {
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
			if kind == "metadata_many_values" {
				// Dense legal metadata is limited by its finite byte allocation,
				// without a node quota derived from the raw representation.
				project := f.client.Job.GetX(t.Context(), f.jobID).QueryProject().OnlyX(t.Context())
				config := make(map[string]any, 800)
				for i := range 800 {
					config[fmt.Sprintf("k%03d", i)] = i
				}
				f.client.Project.UpdateOneID(project.ID).SetConfig(config).ExecX(t.Context())
			}
			_, loaded, _, _, close, err := f.runner.loadJobDefinition(t.Context(), f.jobID)
			if kind == "metadata_many_values" {
				if err != nil {
					close()
					t.Fatalf("legal dense definition rejected: %v", err)
				}
				if len(loaded.Project.Config) != 800 {
					close()
					t.Fatal("legal metadata was trimmed")
				}
				if held := root.Snapshot(0).Instance; held.PayloadBytes <= 0 {
					close()
					t.Fatalf("definition lost finite allocation credit: %+v", held)
				}
			}
			loaded = nil
			close()
			if kind != "metadata_many_values" && !errors.Is(err, backend.ErrBuildCapacity) && !errors.Is(err, resourcebudget.ErrCapacity) {
				t.Fatalf("definition bound did not reject before entry: %v", err)
			}
			main, aux, _, _ := f.calls()
			if main != 0 || aux != 0 {
				t.Fatal("oversized definition reached HTTP")
			}
			if got := root.Snapshot(0).Instance; got != (resourcebudget.Vector{}) {
				t.Fatalf("failed definition retained root: %+v", got)
			}
			t.Logf("%s definition admission completed without HTTP or root residue", kind)
		})
	}
}

func TestBudgetIntegrationJobDefinitionSegmentIDs(t *testing.T) {
	for _, tt := range []struct {
		name     string
		ids      int
		accepted bool
	}{
		{"segment_ids_within_cap", 70000, true},
		{"segment_ids_over_cap", int(service.MaxJobDefinitionSegmentIDs) + 1, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newRubyPipelineFixture(t, 1)
			limits := resourcebudget.Limits{MaxUnconfirmedMembers: 4096, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 16, MaxMainResponseOwners: 16, MaxAuxResponseOwners: 16, MaxBuildWorkers: 16, MaxQueuedDescriptors: 256, MaxLogicalStorageBytes: 1 << 30}
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
			ids := make([]int, tt.ids)
			for i := range ids {
				ids[i] = i + 1
			}
			f.client.JobResource.UpdateOneID(f.jobResources[0]).SetSegmentIds(ids).ExecX(t.Context())
			_, _, _, _, close, err := f.runner.loadJobDefinition(t.Context(), f.jobID)
			close()
			if tt.accepted {
				if err != nil {
					t.Fatalf("segment ids within cap rejected: %v", err)
				}
			} else if !errors.Is(err, backend.ErrBuildCapacity) {
				t.Fatalf("segment ids over cap accepted: %v", err)
			}
			main, aux, _, _ := f.calls()
			if main != 0 || aux != 0 {
				t.Fatal("definition reached HTTP")
			}
			if got := root.Snapshot(0).Instance; got != (resourcebudget.Vector{}) {
				t.Fatalf("definition retained root: %+v", got)
			}
		})
	}
}
