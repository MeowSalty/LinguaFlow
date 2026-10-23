package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"encoding/json"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageauthversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretirement"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretiremententry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
)

func TestRetirementRecoverySingleSpaceKeepsSiblingUnaffected(t *testing.T) {
	base := &connectionTestDriver{objects: map[string][]byte{}}
	f := newFinalizeFixture(t, base, true)
	ctx := f.ctx
	r := f.beginFinalizing(t, "space")
	sibling, err := f.client.StorageSpace.Create().
		SetConnectionID(f.connection.ID).SetName("sibling").
		SetOwnerKind(storagespace.OwnerKindUser).SetOwnerID(f.owner.ID).
		SetIdentity("sibling-identity").SetMarkerNonce("sibling-nonce").
		SetBucket("sibling-bucket").SetPrefix("sibling").
		SetRemoteUseState(storagespace.RemoteUseStateAttempted).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn := f.client.StorageConnection.GetX(ctx, f.connection.ID)
	manager := f.client.StorageSpace.GetX(ctx, f.space.ID)
	connectionExecutionGeneration := conn.ExecutionGeneration
	business := f.client.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(conn.ID), storageauthversion.PurposeEQ(storageauthversion.PurposeBusiness)).OnlyX(ctx)
	payload := storageauth.S3Payload{Version: storageauth.PayloadVersion, AccessKeyID: "scoped", SecretAccessKey: "scoped-secret"}
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "recover-single", r.Generation, conn.ManagementGeneration, payload); err != nil {
		t.Fatal(err)
	}
	if n := f.client.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(conn.ID), storageauthversion.PurposeEQ(storageauthversion.PurposeRetirement)).CountX(ctx); n != 1 {
		t.Fatalf("single-space recovery persisted %d credentials", n)
	}
	if got := f.client.StorageSpace.GetX(ctx, sibling.ID); got.RemoteUseState != storagespace.RemoteUseStateAttempted || got.ExecutionGeneration != sibling.ExecutionGeneration {
		t.Fatalf("sibling space changed: %+v", got)
	}
	if got := f.client.StorageSpace.GetX(ctx, f.space.ID); got.ExecutionGeneration <= manager.ExecutionGeneration {
		t.Fatal("scoped space was not fenced")
	}
	if got := f.client.StorageConnection.GetX(ctx, conn.ID); got.ExecutionGeneration != connectionExecutionGeneration {
		t.Fatalf("single-space recovery invalided sibling drivers by bumping parent connection generation: %d -> %d", connectionExecutionGeneration, got.ExecutionGeneration)
	}
	if b := f.client.StorageAuthVersion.GetX(ctx, business.ID); b.PurgedAt != nil || len(b.Ciphertext) == 0 {
		t.Fatal("sibling/shared business credential was reclaimed")
	}
}

func TestRetirementRecoverySameKeySameLengthSecretConflicts(t *testing.T) {
	base := &connectionTestDriver{objects: map[string][]byte{}}
	f := newFinalizeFixture(t, base, true)
	ctx := f.ctx
	r := f.beginFinalizing(t, "space")
	conn := f.client.StorageConnection.GetX(ctx, f.connection.ID)
	payload := storageauth.S3Payload{Version: storageauth.PayloadVersion, AccessKeyID: "same-access", SecretAccessKey: "aaaaaaaaaaaaaaaa"}
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "same-key", r.Generation, conn.ManagementGeneration, payload); err != nil {
		t.Fatal(err)
	}
	changed := payload
	changed.SecretAccessKey = "bbbbbbbbbbbbbbbb"
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "same-key", r.Generation, conn.ManagementGeneration, changed); !errors.Is(err, ErrStorageIdempotency) {
		t.Fatalf("same-length secret change did not conflict: %v", err)
	}
}

func TestRetirementRecoveryValidatesNewCandidate(t *testing.T) {
	base := &connectionTestDriver{objects: map[string][]byte{}}
	f := newFinalizeFixture(t, base, true)
	ctx := f.ctx
	r := f.beginFinalizing(t, "space")
	conn := f.client.StorageConnection.GetX(ctx, f.connection.ID)
	payload := storageauth.S3Payload{Version: storageauth.PayloadVersion, AccessKeyID: "new-access", SecretAccessKey: "new-secret"}
	f.connections.factory = func(_ context.Context, _ *ent.StorageConnection, _ *ent.StorageSpace, p storageauth.S3Payload) (storage.Driver, error) {
		if p != payload {
			t.Error("recovery verified a different credential")
			return nil, storage.ErrAuthRequired
		}
		return base, nil
	}
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "recover-candidate", r.Generation, conn.ManagementGeneration, payload); err != nil {
		t.Fatal(err)
	}
	entry := f.client.StorageRetirementEntry.Query().Where(storageretiremententry.RetirementIDEQ(r.ID), storageretiremententry.KindEQ(retirementRecoveryKind)).OnlyX(ctx)
	if entry.Status != storageretiremententry.StatusCompleted {
		t.Fatalf("new candidate validation failed: %+v status=%s", entry.ErrorCode, entry.Status)
	}
	a := f.client.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(conn.ID), storageauthversion.PurposeEQ(storageauthversion.PurposeRetirement)).OnlyX(ctx)
	decrypted, err := storageauth.DecryptS3(f.connections.keys, storageAuthIdentity(conn, a.Generation), storageCiphertext(a))
	if err != nil || decrypted != payload {
		t.Fatalf("stored candidate was not the submitted payload: %v", err)
	}
}

func TestRetirementSiteSetupRejectsBackendIDResurrection(t *testing.T) {
	base := &connectionTestDriver{objects: map[string][]byte{}}
	f := newFinalizeFixture(t, base, true)
	ctx := f.ctx
	conn := f.client.StorageConnection.GetX(ctx, f.connection.ID)
	f.client.StorageConnection.UpdateOneID(conn.ID).
		SetOwnerKind(storageconnection.OwnerKindSite).SetOwnerID(0).
		SetAuthSource(storageconnection.AuthSourceDeployment).
		SetBackendID("site-old").SetLifecycle(storageconnection.LifecycleRemoved).ExecX(ctx)
	cfg := f.connections.cfg
	cfg.Backends = []config.StorageBackendConfig{{ID: "site-old", Driver: "s3", Endpoint: "https://s3.example", Region: "test", Bucket: "old-bucket", Prefix: "old"}}
	connections := NewStorageConnectionService(f.client, credentialTestKeyring(t, "one", "one"), cfg, func(context.Context, *ent.StorageConnection, *ent.StorageSpace, storageauth.S3Payload) (storage.Driver, error) {
		return base, nil
	})
	st := f.storage
	st.cfg = cfg
	admin := f.client.User.Create().SetUsername("site-admin-replay").SetEmail("site-admin-replay@example.test").SetPasswordHash("unused").SetRole(SystemRoleAdmin).SetActive(true).SaveX(ctx)
	service, err := NewStorageRetirementService(st, connections)
	if err != nil {
		t.Fatal(err)
	}
	backend := cfg.Backends[0].ID
	scope, err := json.Marshal(RetirementTarget{TargetKind: "connection", TargetID: conn.ID, TargetIdentity: "site-old", Scope: "site", OwnerID: 0, ConfigurationSource: "deployment", BackendID: &backend, ManagementGeneration: conn.ManagementGeneration, ExecutionGeneration: conn.ExecutionGeneration, Spaces: []RetirementSpaceScope{{SpaceID: 0, SpaceIdentity: "missing", ConnectionID: conn.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	r := f.client.StorageRetirement.Create().SetTargetKind(storageretirement.TargetKindConnection).SetTargetID(conn.ID).
		SetTargetIdentity("site-old").SetOwnerKind(storageretirement.OwnerKindSite).SetOwnerID(0).SetActorID(admin.ID).
		SetConfirmedScope(scope).SetIdempotencyKey("site-old-replay").SetRequestHash("site-old").
		SetPhase(storageretirement.PhaseFinalizing).SetStatus(storageretirement.StatusNeedsAction).
		SetGeneration(1).SaveX(ctx)
	payload := storageauth.S3Payload{Version: storageauth.PayloadVersion, AccessKeyID: "access", SecretAccessKey: "secret"}
	if _, err = service.RecoverAuthorization(ctx, admin.ID, r.ID, "deploy-replay", r.Generation, conn.ManagementGeneration, payload); !errors.Is(err, ErrStorageRetirementConfigurationPending) {
		t.Fatalf("deployment same-key replay did not reject uploaded credentials: %v", err)
	}
	if _, err = connections.SetupSiteBackends(ctx); !errors.Is(err, ErrStorageRetirementConflict) {
		t.Fatalf("removed old backend id was resurrected: %v", err)
	}
	if len(cfg.Backends) != 1 || cfg.Backends[0].ID != "site-old" {
		t.Fatal("deployment config was mutated")
	}
}

func TestRetirementRecoveryReplayAfterStaleStateAndExpiry(t *testing.T) {
	base := &connectionTestDriver{objects: map[string][]byte{}}
	f := newFinalizeFixture(t, base, true)
	ctx := f.ctx
	r := f.beginFinalizing(t, "space")
	conn := f.client.StorageConnection.GetX(ctx, f.connection.ID)
	payload := storageauth.S3Payload{Version: storageauth.PayloadVersion, AccessKeyID: "replay-access", SecretAccessKey: "replay-secret"}
	expires := time.Now().UTC().Add(time.Second)
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "recover-replay", r.Generation, conn.ManagementGeneration, payload, &expires); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "recover-replay", r.Generation, conn.ManagementGeneration, payload, &expires); err != nil {
		t.Fatalf("same-key replay immediately after completion failed: %v", err)
	}
	f.client.StorageRetirement.UpdateOneID(r.ID).SetGeneration(r.Generation + 1).ExecX(ctx)
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "recover-replay", r.Generation, conn.ManagementGeneration, payload, &expires); err != nil {
		t.Fatalf("same-key replay after generation advanced failed: %v", err)
	}
	f.client.StorageRetirement.UpdateOneID(r.ID).SetPhase(storageretirement.PhaseWaitingConfiguration).ExecX(ctx)
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "recover-replay", r.Generation, conn.ManagementGeneration, payload, &expires); err != nil {
		t.Fatalf("same-key replay after phase advanced failed: %v", err)
	}
	f.client.StorageConnection.UpdateOneID(conn.ID).SetLifecycle(storageconnection.LifecycleRemoved).ExecX(ctx)
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "recover-replay", r.Generation, conn.ManagementGeneration, payload, &expires); err != nil {
		t.Fatalf("same-key replay after connection removed failed: %v", err)
	}
	f.client.StorageConnection.UpdateOneID(conn.ID).SetStatus(storageconnection.StatusDisabled).ExecX(ctx)
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "recover-replay", r.Generation, conn.ManagementGeneration, payload, &expires); err != nil {
		t.Fatalf("same-key replay after connection disabled failed: %v", err)
	}
	f.client.StorageRetirement.UpdateOneID(r.ID).SetStatus(storageretirement.StatusNeedsAction).ExecX(ctx)
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "recover-replay", r.Generation, conn.ManagementGeneration, payload, &expires); err != nil {
		t.Fatalf("same-key replay after retirement failed failed: %v", err)
	}
	time.Sleep(max(time.Until(expires)+time.Millisecond, 0))
	f.connections.keys = nil // Replaying an accepted result no longer needs decryption.
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "recover-replay", r.Generation, conn.ManagementGeneration, payload, &expires); err != nil {
		t.Fatalf("completed same-key replay after expiresAt passed failed: %v", err)
	}
}
