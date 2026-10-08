package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/credentialversion"
)

func credentialTestKeyring(t *testing.T, active string, ids ...string) *credential.Keyring {
	t.Helper()
	keys := map[string]string{}
	for i, id := range ids {
		keys[id] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{byte(i + 1)}, 32))
	}
	raw, err := json.Marshal(map[string]any{"version": 1, "active_key_id": active, "keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	k, err := credential.ParseKeyring(raw)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
func credentialTestServices(t *testing.T) (context.Context, *ent.Client, *ent.User, *CredentialService, *BackendService) {
	t.Helper()
	ctx := context.Background()
	client := testClient(t)
	u := client.User.Create().SetUsername("credential-owner").SetEmail("credential@example.test").SetPasswordHash("unused").SaveX(ctx)
	users := NewUserService(client, nil)
	c := NewCredentialService(client, credentialTestKeyring(t, "one", "one"), users)
	b := NewBackendService(client, users, nil)
	b.SetCredentials(c)
	return ctx, client, u, c, b
}
func credentialTestBackend(t *testing.T, ctx context.Context, b *BackendService, u *ent.User, name, secret string) *BackendRecord {
	t.Helper()
	r, err := b.Create(ctx, CreateBackendInput{Scope: ScopeUser, OwnerUserID: &u.ID, BackendInput: BackendInput{Name: name, Type: "openai", Options: map[string]any{"model": "test"}, Secret: &secret}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCredentialsEncryptRotateRevokeAndBackendDeletion(t *testing.T) {
	ctx, client, u, c, b := credentialTestServices(t)
	first := credentialTestBackend(t, ctx, b, u, "one", "old-secret")
	if !first.Credential.Valid() || !first.HasSecret {
		t.Fatal("backend missing executable binding")
	}
	if _, ok := first.Options["api_key"]; ok {
		t.Fatal("plaintext in backend options")
	}
	stored := client.Backend.GetX(ctx, first.ID)
	raw, _ := json.Marshal(stored.Options)
	if bytes.Contains(raw, []byte("old-secret")) {
		t.Fatal("plaintext persisted in options")
	}
	v := client.CredentialVersion.Query().OnlyX(ctx)
	if bytes.Contains(v.Ciphertext, []byte("old-secret")) {
		t.Fatal("unencrypted secret")
	}
	endpoint := first.Options["base_url"].(string)
	if got, err := c.Resolve(ctx, first.Credential, "openai", endpoint); err != nil || got != "old-secret" {
		t.Fatalf("resolve: %v", err)
	}
	second, err := b.Create(ctx, CreateBackendInput{Scope: ScopeUser, OwnerUserID: &u.ID, BackendInput: BackendInput{Name: "shared", Type: "openai", Options: map[string]any{"model": "test"}, CredentialID: &first.Credential.ID}})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := c.Rotate(ctx, u.ID, first.Credential.ID, "new-secret")
	if err != nil {
		t.Fatal(err)
	}
	current, release, err := c.AcquireBackend(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if current != rotated {
		t.Fatal("new execution did not bind rotated version")
	}
	if got, err := c.Resolve(ctx, first.Credential, "openai", endpoint); err != nil || got != "old-secret" {
		t.Fatalf("rotation changed history: %v", err)
	}
	if err := b.Delete(ctx, u.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Check(ctx, first.Credential, first.ID, "openai", endpoint); !errors.Is(err, credential.ErrBackendDeleted) {
		t.Fatalf("deleted backend check: %v", err)
	}
	if err := c.Check(ctx, first.Credential, second.ID, "openai", endpoint); err != nil {
		t.Fatalf("deletion revoked shared credential: %v", err)
	}
	if err := c.Revoke(ctx, u.ID, first.Credential); err != nil {
		t.Fatal(err)
	}
	if err := c.Check(ctx, first.Credential, second.ID, "openai", endpoint); !errors.Is(err, credential.ErrRevoked) {
		t.Fatalf("revocation ignored: %v", err)
	}
	if err := c.Check(ctx, rotated, second.ID, "openai", endpoint); err != nil {
		t.Fatalf("new version incorrectly revoked: %v", err)
	}
}

func TestValidateKeysRejectsCorruptMaterialAndEncryptedRows(t *testing.T) {
	for _, damage := range []string{"key_material", "ciphertext", "nonce", "format"} {
		t.Run(damage, func(t *testing.T) {
			ctx, client, u, credentials, backends := credentialTestServices(t)
			credentialTestBackend(t, ctx, backends, u, "startup-check", "never-expose-this")
			if err := credentials.ValidateKeys(ctx); err != nil {
				t.Fatal(err)
			}
			row := client.CredentialVersion.Query().OnlyX(ctx)
			switch damage {
			case "key_material":
				credentials.keys = credentialTestKeyring(t, "one", "another", "one")
			case "ciphertext":
				row.Ciphertext[0] ^= 1
				client.CredentialVersion.UpdateOne(row).SetCiphertext(row.Ciphertext).ExecX(ctx)
			case "nonce":
				client.CredentialVersion.UpdateOne(row).SetNonce([]byte{1}).ExecX(ctx)
			case "format":
				client.CredentialVersion.UpdateOne(row).SetEncryptionVersion(99).ExecX(ctx)
			}
			if err := credentials.ValidateKeys(ctx); !errors.Is(err, credential.ErrDecrypt) {
				t.Fatalf("corrupt credential state accepted at startup: %v", err)
			}
		})
	}
}

func TestCredentialGCTransfersLeaseToEveryExistingJob(t *testing.T) {
	ctx, client, u, c, b := credentialTestServices(t)
	back := credentialTestBackend(t, ctx, b, u, "one", "old")
	binding, release, err := c.AcquireBackend(ctx, back.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Rotate(ctx, u.ID, binding.ID, "new"); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Collect(ctx, u.ID, binding.ID); err != nil || n != 0 {
		t.Fatalf("leased version collected: %d %v", n, err)
	}
	project := client.Project.Create().SetName("credential-job").SetOwnerUserID(u.ID).SaveX(ctx)
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	job, err := tx.Job.Create().SetProjectID(project.ID).SetExecutionPlanID(1).SetStatus("completed").Save(ctx)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := c.RetainJob(ctx, tx, job.ID, []credential.Binding{binding, binding}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	release()
	release()
	if got := client.CredentialJobReference.Query().CountX(ctx); got != 1 {
		t.Fatalf("references=%d", got)
	}
	if n, err := c.Collect(ctx, u.ID, binding.ID); err != nil || n != 0 {
		t.Fatalf("completed job lost credential: %d %v", n, err)
	}
	if err := client.Job.DeleteOneID(job.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Collect(ctx, u.ID, binding.ID); err != nil || n != 1 {
		t.Fatalf("unreferenced old version retained: %d %v", n, err)
	}
	if _, err := c.Resolve(ctx, binding, "openai", back.Options["base_url"].(string)); !errors.Is(err, credential.ErrUnavailable) {
		t.Fatalf("collected version still resolves: %v", err)
	}
}

func TestCredentialsRejectOwnershipEndpointAndOldSecretContract(t *testing.T) {
	ctx, client, u, c, b := credentialTestServices(t)
	back := credentialTestBackend(t, ctx, b, u, "one", "secret")
	other := client.User.Create().SetUsername("other-owner").SetEmail("other@example.test").SetPasswordHash("unused").SaveX(ctx)
	if _, err := c.Rotate(ctx, other.ID, back.Credential.ID, "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other user rotated: %v", err)
	}
	if _, err := c.Resolve(ctx, back.Credential, "openai", "https://outside.example/"); !errors.Is(err, credential.ErrEndpoint) {
		t.Fatalf("endpoint mismatch accepted: %v", err)
	}
	_, err := b.Create(ctx, CreateBackendInput{Scope: ScopeUser, OwnerUserID: &other.ID, BackendInput: BackendInput{Name: "stolen", Type: "openai", Options: map[string]any{"model": "test"}, CredentialID: &back.Credential.ID}})
	if !errors.Is(err, credential.ErrOwnership) {
		t.Fatalf("cross-owner binding accepted: %v", err)
	}
	_, err = b.Create(ctx, CreateBackendInput{Scope: ScopeUser, OwnerUserID: &u.ID, BackendInput: BackendInput{Name: "legacy", Type: "openai", Options: map[string]any{"model": "test", "api_key": "secret"}}})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("old contract accepted: %v", err)
	}
	_, err = b.Update(ctx, u.ID, back.ID, BackendInput{Name: back.Name, Type: "openai", Options: map[string]any{"model": "test", "base_url": "https://elsewhere.example/"}})
	if !errors.Is(err, credential.ErrEndpoint) {
		t.Fatalf("endpoint changed without explicit credential: %v", err)
	}
}

func TestBackendAndCredentialCreationRollbackTogether(t *testing.T) {
	ctx, client, u, _, b := credentialTestServices(t)
	credentialTestBackend(t, ctx, b, u, "same", "first")
	secret := "second"
	_, err := b.Create(ctx, CreateBackendInput{Scope: ScopeUser, OwnerUserID: &u.ID, BackendInput: BackendInput{Name: "same", Type: "openai", Options: map[string]any{"model": "test"}, Secret: &secret}})
	if !errors.Is(err, ErrBackendExists) {
		t.Fatalf("duplicate create: %v", err)
	}
	if client.Credential.Query().CountX(ctx) != 1 || client.CredentialVersion.Query().CountX(ctx) != 1 {
		t.Fatal("failed backend left orphan credential")
	}
}

func TestReencryptPreservesBindingsAndRevocationAndCanResume(t *testing.T) {
	ctx, client, u, c, b := credentialTestServices(t)
	back := credentialTestBackend(t, ctx, b, u, "one", "first")
	if _, err := c.Rotate(ctx, u.ID, back.Credential.ID, "second"); err != nil {
		t.Fatal(err)
	}
	if err := c.Revoke(ctx, u.ID, back.Credential); err != nil {
		t.Fatal(err)
	}
	before := client.CredentialVersion.Query().Order(ent.Asc(credentialversion.FieldVersion)).AllX(ctx)
	next := NewCredentialService(client, credentialTestKeyring(t, "two", "one", "two"), nil)
	if changed, err := next.reencryptOne(ctx); err != nil || !changed {
		t.Fatalf("first committed row: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if n, err := next.Reencrypt(cancelled); !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("interruption: %d %v", n, err)
	}
	n, err := next.Reencrypt(ctx)
	if err != nil || n != 1 {
		t.Fatalf("reencrypt=%d %v", n, err)
	}
	if n, err := next.Reencrypt(ctx); err != nil || n != 0 {
		t.Fatalf("idempotence=%d %v", n, err)
	}
	after := client.CredentialVersion.Query().Order(ent.Asc(credentialversion.FieldVersion)).AllX(ctx)
	for i, row := range after {
		if row.KeyID != "two" || row.Version != before[i].Version || row.Revoked != before[i].Revoked || bytes.Equal(row.Nonce, before[i].Nonce) {
			t.Fatal("reencryption changed identity or reused nonce")
		}
	}
	if err := c.ValidateKeys(ctx); !errors.Is(err, credential.ErrKeyUnavailable) {
		t.Fatalf("old deployment accepted unknown key: %v", err)
	}
	if err := next.ValidateKeys(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := next.Resolve(ctx, credential.Binding{ID: back.Credential.ID, Version: 2}, "openai", back.Options["base_url"].(string)); err != nil || got != "second" {
		t.Fatalf("reencrypted secret: %v", err)
	}
}

func TestCredentialLeaseGCConcurrency(t *testing.T) {
	ctx, _, u, c, b := credentialTestServices(t)
	back := credentialTestBackend(t, ctx, b, u, "one", "first")
	if _, err := c.Rotate(ctx, u.ID, back.Credential.ID, "second"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			release, err := c.Acquire(ctx, back.Credential)
			if errors.Is(err, credential.ErrUnavailable) {
				return
			}
			if err != nil {
				errs <- err
				return
			}
			defer release()
			if _, err := c.Resolve(ctx, back.Credential, "openai", back.Options["base_url"].(string)); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := c.Collect(ctx, u.ID, back.Credential.ID); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestGuardObservesBackendDeletionAndRevocationOnLaterHTTPAttempt(t *testing.T) {
	ctx, _, u, c, b := credentialTestServices(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, "ok") }))
	defer server.Close()
	secret := "test-key"
	back, err := b.Create(ctx, CreateBackendInput{Scope: ScopeUser, OwnerUserID: &u.ID, BackendInput: BackendInput{Name: "remote", Type: "openai", Options: map[string]any{"model": "test", "base_url": server.URL}, Secret: &secret}})
	if err != nil {
		t.Fatal(err)
	}
	client, err := credential.GuardClient(nil, c, back.Credential, back.ID, "openai", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := c.Revoke(ctx, u.ID, back.Credential); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(server.URL); !errors.Is(err, credential.ErrRevoked) {
		t.Fatalf("revocation: %v", err)
	}
	if err := b.Delete(ctx, u.ID, back.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(server.URL); !errors.Is(err, credential.ErrBackendDeleted) {
		t.Fatalf("deletion reason: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("invalid execution reached provider")
	}
}
