package service

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
)

func TestStorageRetirementDependencyProjectionKeepsEveryAuthoritativeDependency(t *testing.T) {
	for _, objects := range []bool{false, true} {
		t.Run(map[bool]string{false: "ledger_only", true: "objects"}[objects], func(t *testing.T) {
			f := newRetirementPreviewFixture(t)
			if objects {
				object := f.client.Blob.Create().SetIdentity("projection-object").SetProjectID(f.project.ID).SetPurpose(blob.PurposeSource).SaveX(f.ctx)
				for i, status := range []bloblocation.Status{bloblocation.StatusLive, bloblocation.StatusRetired, bloblocation.StatusCandidate, bloblocation.StatusDeleting} {
					f.client.BlobLocation.Create().SetBlobID(object.ID).SetSpaceID(f.space).SetObjectKey(string(status)).SetStatus(status).SetSize(int64(i + 1)).SaveX(f.ctx)
				}
				// A deleted location and one without a blob must not add projects.
				f.client.BlobLocation.Create().SetSpaceID(f.space).SetObjectKey("orphan").SetStatus(bloblocation.StatusDeleted).SaveX(f.ctx)
			} else {
				f.client.StorageSpace.UpdateOneID(f.space).SetReservedBytes(1).ExecX(f.ctx)
			}
			target, err := f.service.normalizeTarget(f.ctx, f.client, f.owner.ID, "space", f.space)
			if err != nil {
				t.Fatal(err)
			}
			authority, err := f.service.inventory(f.ctx, f.client, target)
			if err != nil {
				t.Fatal(err)
			}
			view, err := f.service.scanInventory(f.ctx, f.client, target, false)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := f.service.dependencyProjection(f.ctx, f.client, target)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(authority.Dependencies, view.Dependencies) || !reflect.DeepEqual(authority.Dependencies, projection) {
				a, _ := json.Marshal(authority.Dependencies)
				v, _ := json.Marshal(view.Dependencies)
				t.Fatalf("browsing omitted authoritative dependencies: %s / %s", a, v)
			}
			if authority.Fingerprint == "" || len(authority.identities) == 0 || view.Fingerprint != "" || len(view.identities) != 0 {
				t.Fatal("browsing constructed confirmation identities or authority omitted them")
			}
			if objects {
				projectIDs, err := f.service.retirementKeyedProjects(f.ctx, f.client, []int{f.space})
				if err != nil || len(projectIDs) != 1 || !projectIDs[f.project.ID] {
					t.Fatalf("distinct project scope: %v %v", projectIDs, err)
				}
			} else if !authority.Blocking {
				t.Fatal("ledger mismatch was omitted by the authority scan")
			}
		})
	}
}
