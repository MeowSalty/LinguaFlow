package storageguard_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageguard"
)

func TestGateManyDeduplicatesAndReloadsAcrossPagesWithoutChangingGenerations(t *testing.T) {
	client, conn, original := fixture(t)
	ctx := t.Context()
	ids := []int{original.ID}
	for i := 0; i < 256; i++ {
		space := client.StorageSpace.Create().SetConnectionID(conn.ID).SetName(fmt.Sprint("space-", i)).SetIdentity(fmt.Sprint("identity-", i)).SetMarkerNonce("nonce").SaveX(ctx)
		ids = append(ids, space.ID)
	}
	// An arbitrary caller order and repeated IDs must acquire only unique locks.
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
	ids = append(ids, original.ID, original.ID)
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rows, err := storageguard.GateMany(ctx, tx.Client(), ids, storageguard.NewUse)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 257 {
		t.Fatalf("unique spaces = %d", len(rows))
	}
	for id, facts := range rows {
		if facts.Space.ID != id || facts.Connection.ID != conn.ID {
			t.Fatal("gate returned another identity")
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	after, parent := client.StorageSpace.GetX(ctx, original.ID), client.StorageConnection.GetX(ctx, conn.ID)
	if after.ManagementGeneration != original.ManagementGeneration || after.ExecutionGeneration != original.ExecutionGeneration || !after.UpdatedAt.Equal(original.UpdatedAt) || parent.ManagementGeneration != conn.ManagementGeneration || !parent.UpdatedAt.Equal(conn.UpdatedAt) {
		t.Fatal("gate changed timestamps or business generations")
	}
}

func TestGateManyKeepsLifecycleAdmissionAndRejectsMissingIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		parent bool
		value  string
	}{
		{"retiring_space", false, "retiring"}, {"removed_space", false, "removed"},
		{"retiring_connection", true, "retiring"}, {"removed_connection", true, "removed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, conn, space := fixture(t)
			ctx := t.Context()
			if test.parent {
				client.StorageConnection.UpdateOneID(conn.ID).SetLifecycle(storageconnection.Lifecycle(test.value)).ExecX(ctx)
			} else {
				client.StorageSpace.UpdateOneID(space.ID).SetLifecycle(storagespace.Lifecycle(test.value)).ExecX(ctx)
			}
			_, _, expected := storageguard.Check(ctx, client, space.ID, storageguard.NewUse, 0)
			tx, err := client.Tx(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, actual := storageguard.GateMany(ctx, tx.Client(), []int{space.ID}, storageguard.NewUse)
			if expected == nil || !errors.Is(actual, expected) {
				t.Fatalf("gate=%v check=%v", actual, expected)
			}
		})
	}
	client, _, space := fixture(t)
	tx, err := client.Tx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := storageguard.GateMany(t.Context(), tx.Client(), []int{space.ID + 1000}, storageguard.NewUse); !errors.Is(err, storageguard.ErrConflict) {
		t.Fatalf("missing identity = %v", err)
	}
}
