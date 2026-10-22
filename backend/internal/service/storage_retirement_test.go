package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageauthversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagepathclaim"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func TestStorageRetirementCapabilitiesRemainClosed(t *testing.T) {
	ctx, client, svc, owner, conn, space, driver := storageConnectionFixture(t)
	beforeConn := client.StorageConnection.GetX(ctx, conn.ID)
	beforeSpace := client.StorageSpace.GetX(ctx, space.ID)
	for _, enabled := range []bool{false, true} {
		for _, maintenance := range []bool{false, true} {
			svc.cfg.Enabled, svc.cfg.Maintenance = enabled, maintenance
			got, err := svc.Capabilities(ctx, owner.ID, ScopeUser, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.Runtime.Retirement.ContractVersion != 1 {
				t.Fatal("retirement must have its own contract version")
			}
			assertEAvailability(t, got.Runtime.Retirement.UnusedRemoval, false, "storage_capability_unsupported")
			assertEAvailability(t, got.Runtime.Retirement.InUseRetirement, false, "storage_capability_unsupported")
			connections, err := svc.List(ctx, owner.ID, ScopeUser, owner.ID)
			if err != nil || len(connections) != 1 {
				t.Fatalf("connections: %v %v", connections, err)
			}
			spaces, err := svc.Spaces(ctx, owner.ID, conn.ID)
			if err != nil || len(spaces) != 1 {
				t.Fatalf("spaces: %v %v", spaces, err)
			}
			assertEAvailability(t, connections[0].ManagementActions.Retire, false, "storage_capability_unsupported")
			assertEAvailability(t, spaces[0].ManagementActions.Retire, false, "storage_capability_unsupported")
			if connections[0].Lifecycle != "available" || connections[0].RetirementID != nil || connections[0].ConfigurationSource != "stored" || spaces[0].Lifecycle != "available" || spaces[0].RetirementID != nil || spaces[0].ConfigurationSource != "stored" {
				t.Fatal("management projection does not reflect persistent lifecycle/source")
			}
		}
	}
	if client.StorageRetirement.Query().CountX(ctx) != 0 || client.StorageWrite.Query().CountX(ctx) != 0 || client.StorageCheck.Query().CountX(ctx) != 0 || driver.puts != 0 || driver.deletes != 0 {
		t.Fatal("capability discovery performed retirement or remote work")
	}
	afterConn := client.StorageConnection.GetX(ctx, conn.ID)
	afterSpace := client.StorageSpace.GetX(ctx, space.ID)
	if beforeConn.ManagementGeneration != afterConn.ManagementGeneration || beforeSpace.ManagementGeneration != afterSpace.ManagementGeneration || !beforeConn.UpdatedAt.Equal(afterConn.UpdatedAt) || !beforeSpace.UpdatedAt.Equal(afterSpace.UpdatedAt) {
		t.Fatal("discovery updated managed identities")
	}
}

func TestStorageRetirementAdmissionAndSanitizationShareDomainCodes(t *testing.T) {
	svc := &StorageConnectionService{}
	for _, code := range []string{"storage_retiring", "storage_removed", "storage_retirement_conflict", "storage_retirement_preview_stale", "storage_retirement_dependencies_remaining", "storage_retirement_configuration_pending"} {
		err := storageAdmissionError([]string{code})
		if got := StorageErrorCode(svc.sanitize(fmt.Errorf("private provider text: %w", err))); got != code {
			t.Fatalf("lost retirement recovery reason: %s => %s", code, got)
		}
	}
}

func storageRetirementFixture(t *testing.T, client *ent.Client, owner, spaceID int, identity, key string) *ent.StorageRetirement {
	t.Helper()
	scope, err := json.Marshal(map[string]any{"space_ids": []int{spaceID}})
	if err != nil {
		t.Fatal(err)
	}
	return client.StorageRetirement.Create().SetTargetKind("space").SetTargetID(spaceID).
		SetTargetIdentity(identity).SetOwnerKind("user").SetOwnerID(owner).SetActorID(owner).
		SetConfirmedScope(scope).SetIdempotencyKey(key).
		SetRequestHash("retirement-test-request").SaveX(t.Context())
}

func TestStorageRetirementScopedAuthorizationCannotBecomeBusinessAccess(t *testing.T) {
	ctx, client, svc, owner, conn, space, driver := storageConnectionFixture(t)
	if _, err := svc.Authorize(ctx, owner.ID, conn.ID, storageAuthorizeInput(conn.ManagementGeneration)); err != nil {
		t.Fatal(err)
	}
	current := client.StorageConnection.GetX(ctx, conn.ID)
	auth := client.StorageAuthVersion.Query().OnlyX(ctx)
	retirement := storageRetirementFixture(t, client, owner.ID, space.ID, "restricted-space", "restricted")
	// Simulate a bad/stale active-generation reference to an otherwise valid,
	// decryptable restricted authorization. Neither discovery nor I/O may use it.
	client.StorageAuthVersion.UpdateOneID(auth.ID).SetPurpose("retirement").SetRetirementID(retirement.ID).ExecX(ctx)
	puts, deletes := driver.puts, driver.deletes
	for _, write := range []bool{false, true} {
		if _, err := svc.ResolveDriver(ctx, space.ID, write); !errors.Is(err, storage.ErrAuthRequired) {
			t.Fatalf("restricted credential accepted for business write=%v: %v", write, err)
		}
	}
	if _, _, err := svc.loadAuth(ctx, current, auth.Generation, storageauthversion.StatusActive); !errors.Is(err, storage.ErrAuthRequired) {
		t.Fatalf("restricted authorization decrypted by business loader: %v", err)
	}
	record, err := svc.Get(ctx, owner.ID, conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertEAvailability(t, record.ManagementActions.CheckRead, false, "storage_auth_required")
	if driver.puts != puts || driver.deletes != deletes {
		t.Fatal("restricted authorization allowed provider mutation")
	}
}

func TestStorageRetirementCredentialPurgeKeepsAuditAndRotationWorks(t *testing.T) {
	ctx, client, svc, owner, conn, space, _ := storageConnectionFixture(t)
	if _, err := svc.Authorize(ctx, owner.ID, conn.ID, storageAuthorizeInput(conn.ManagementGeneration)); err != nil {
		t.Fatal(err)
	}
	auth := client.StorageAuthVersion.Query().OnlyX(ctx)
	if err := client.StorageAuthVersion.UpdateOneID(auth.ID).SetPurgedAt(time.Now().UTC()).Exec(ctx); err == nil {
		t.Fatal("purge accepted while ciphertext remained online")
	}
	if err := client.StorageAuthVersion.UpdateOneID(auth.ID).ClearCiphertext().ClearNonce().Exec(ctx); err == nil {
		t.Fatal("ciphertext disappeared without durable purge evidence")
	}
	client.StorageAuthVersion.UpdateOneID(auth.ID).SetPurgedAt(time.Now().UTC()).ClearCiphertext().ClearNonce().SetStatus("retired").ExecX(ctx)
	if _, err := svc.ResolveDriver(ctx, space.ID, false); !errors.Is(err, storage.ErrAuthRequired) {
		t.Fatalf("purged authorization retained business access: %v", err)
	}
	changed, failed, err := svc.Reencrypt(ctx)
	if err != nil || changed != 0 || failed != 0 {
		t.Fatalf("rotation attempted to decrypt a purged audit record: %d %d %v", changed, failed, err)
	}
	saved := client.StorageAuthVersion.GetX(ctx, auth.ID)
	if saved.Generation != auth.Generation || saved.ConnectionID != conn.ID || saved.PurgedAt == nil || len(saved.Ciphertext) != 0 || len(saved.Nonce) != 0 {
		t.Fatal("purge lost non-secret audit or rotation resurrected ciphertext")
	}
}

func TestStorageRetirementDurableIdentityAndGenerationConstraints(t *testing.T) {
	ctx, client, _, owner, _, space, _ := storageConnectionFixture(t)
	first := storageRetirementFixture(t, client, owner.ID, space.ID, "identity-one", "same-key")
	client.StorageRetirement.UpdateOneID(first.ID).SetStatus("failed").ExecX(ctx)
	_, err := client.StorageRetirement.Create().SetTargetKind("space").SetTargetID(space.ID).
		SetTargetIdentity("identity-one").SetOwnerKind("user").SetOwnerID(owner.ID).SetActorID(owner.ID).
		SetConfirmedScope(json.RawMessage(`{}`)).SetIdempotencyKey("same-key").SetRequestHash("changed-input").Save(ctx)
	if !ent.IsConstraintError(err) {
		t.Fatalf("failed parent allowed duplicate immutable idempotency identity: %v", err)
	}
	// A new identity may use the same client key; a tombstone must never redirect
	// that key's old result to a replacement space.
	replacement := storageRetirementFixture(t, client, owner.ID, space.ID, "identity-two", "same-key")
	if replacement.ID == first.ID {
		t.Fatal("different immutable targets shared a retirement record")
	}
	client.StorageRetirement.UpdateOneID(first.ID).SetGeneration(MaxStorageInteger).ExecX(ctx)
	if err := client.StorageRetirement.UpdateOneID(first.ID).AddGeneration(1).Exec(ctx); err == nil {
		t.Fatal("atomic generation increment exceeded the API's exact integer range")
	}
	entry := client.StorageRetirementEntry.Create().SetRetirementID(first.ID).SetEntryKey("space").SetKind("space").SetSpaceID(space.ID).SaveX(ctx)
	if err := client.StorageRetirement.DeleteOneID(first.ID).Exec(ctx); err == nil {
		t.Fatal("deleting a parent cascaded away unresolved retirement facts")
	}
	if client.StorageRetirementEntry.GetX(ctx, entry.ID).RetirementID != first.ID {
		t.Fatal("retirement entry lost its owning task")
	}
}

func TestStorageRetirementCapabilitiesAreIndependentSnapshots(t *testing.T) {
	first := storageRuntimeState(config.DefaultStorageConfig())
	first.Retirement.UnusedRemoval.ReasonCodes[0] = "changed"
	second := storageRuntimeState(config.DefaultStorageConfig())
	if !reflect.DeepEqual(second.Retirement.UnusedRemoval.ReasonCodes, []string{"storage_capability_unsupported"}) || first.Retirement.InUseRetirement.ReasonCodes[0] != "storage_capability_unsupported" {
		t.Fatal("callers can mutate shared retirement capability state")
	}
}

func TestStorageRetirementSchemaSeparatesPathClaimsFromHistory(t *testing.T) {
	ctx, client, _, _, conn, space, _ := storageConnectionFixture(t)
	claim := client.StoragePathClaim.Query().Where(storagepathclaim.SpaceIDEQ(space.ID)).OnlyX(ctx)
	other := client.StorageSpace.Create().SetConnectionID(conn.ID).SetName("replacement").SetIdentity("replacement-identity").SetMarkerNonce("replacement-nonce").SetBucket("bucket").SetPrefix("owned").SaveX(ctx)
	if _, err := client.StoragePathClaim.Create().SetSpaceID(other.ID).SetIdentity("replacement-claim").SetBucket("bucket").SetPrefix("owned").Save(ctx); !ent.IsConstraintError(err) {
		t.Fatalf("two active claims acquired the same exact bucket/prefix: %v", err)
	}
	// This only tests the storage model: R02 must prove exact cleanup and release
	// the claim transactionally before any service can perform this operation.
	client.StoragePathClaim.DeleteOneID(claim.ID).ExecX(ctx)
	client.StoragePathClaim.Create().SetSpaceID(other.ID).SetIdentity("replacement-claim").SetBucket("bucket").SetPrefix("owned").SaveX(ctx)
	if client.StorageSpace.GetX(ctx, space.ID).Identity == other.Identity || client.StorageSpace.Query().CountX(ctx) != 2 {
		t.Fatal("claim replacement rewrote or removed the historical space")
	}
}

func TestStorageRetirementRecoveryIdempotencyIsPersistent(t *testing.T) {
	ctx, client, _, owner, _, space, _ := storageConnectionFixture(t)
	parent := storageRetirementFixture(t, client, owner.ID, space.ID, "recovery-identity", "retirement")
	client.StorageRetirementEntry.Create().SetRetirementID(parent.ID).SetEntryKey("recovery-one").SetKind("recover_authorization").SetIdempotencyKey("restore").SetRequestHash("original").SaveX(ctx)
	if _, err := client.StorageRetirementEntry.Create().SetRetirementID(parent.ID).SetEntryKey("recovery-two").SetKind("recover_authorization").SetIdempotencyKey("restore").SetRequestHash("different").Save(ctx); !ent.IsConstraintError(err) {
		t.Fatalf("recovery replay could create a second acceptance identity: %v", err)
	}
	if _, err := client.StorageRetirementEntry.Create().SetRetirementID(parent.ID).SetEntryKey("partial").SetKind("recover_authorization").SetIdempotencyKey("partial").Save(ctx); !ent.IsConstraintError(err) {
		t.Fatalf("recovery persisted a key without its confirmed request hash: %v", err)
	}
}
