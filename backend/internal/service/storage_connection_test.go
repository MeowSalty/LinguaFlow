package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageauthversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagereservation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
)

type connectionTestDriver struct {
	mu                 sync.Mutex
	objects            map[string][]byte
	beforePut          func(string)
	capabilities       func(context.Context) error
	failPutResponse    bool
	failDeleteResponse bool
	puts               int
	deletes            int
}

func (d *connectionTestDriver) PutNew(ctx context.Context, key string, r io.Reader, n int64) (storage.Object, error) {
	if d.beforePut != nil {
		d.beforePut(key)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return storage.Object{}, err
	}
	if int64(len(b)) != n {
		return storage.Object{}, storage.ErrCorrupt
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.puts++
	if _, ok := d.objects[key]; ok {
		return storage.Object{}, storage.ErrExists
	}
	d.objects[key] = b
	if d.failPutResponse {
		d.failPutResponse = false
		return storage.Object{}, storage.ErrUnavailable
	}
	return storage.Object{Key: key, Size: n}, nil
}
func (d *connectionTestDriver) Open(_ context.Context, o storage.Object) (io.ReadCloser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.objects[o.Key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(b))), nil
}
func (d *connectionTestDriver) Stat(_ context.Context, o storage.Object) (storage.Object, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.objects[o.Key]
	if !ok {
		return storage.Object{}, storage.ErrNotFound
	}
	return storage.Object{Key: o.Key, Size: int64(len(b))}, nil
}
func (d *connectionTestDriver) Delete(_ context.Context, o storage.Object) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deletes++
	delete(d.objects, o.Key)
	if d.failDeleteResponse {
		d.failDeleteResponse = false
		return storage.ErrUnavailable
	}
	return nil
}
func (d *connectionTestDriver) Capabilities(ctx context.Context) (storage.Capabilities, error) {
	if d.capabilities != nil {
		if err := d.capabilities(ctx); err != nil {
			return storage.Capabilities{}, err
		}
	}
	return storage.Capabilities{ConditionalCreate: true}, nil
}

func storageConnectionFixture(t *testing.T) (context.Context, *ent.Client, *StorageConnectionService, *ent.User, *StorageConnectionRecord, *StorageSpaceRecord, *connectionTestDriver) {
	t.Helper()
	ctx := context.Background()
	client := testClient(t)
	u := client.User.Create().SetUsername("storage-owner").SetEmail("storage-owner@example.test").SetPasswordHash("unused").SaveX(ctx)
	d := &connectionTestDriver{objects: map[string][]byte{}}
	cfg := config.DefaultStorageConfig()
	cfg.Enabled = true
	s := NewStorageConnectionService(client, credentialTestKeyring(t, "one", "one"), cfg, func(_ context.Context, c *ent.StorageConnection, sp *ent.StorageSpace, p storageauth.S3Payload) (storage.Driver, error) {
		if err := p.Validate(); err != nil {
			t.Error("factory received empty credentials")
		}
		return d, nil
	})
	c, err := s.Create(ctx, u.ID, CreateStorageConnectionInput{Name: "cloud", Scope: "user", OwnerID: u.ID, Endpoint: "https://s3.example", Region: "test", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	sp, err := s.CreateSpace(ctx, u.ID, c.ID, CreateStorageSpaceInput{Name: "files", Bucket: "bucket", Prefix: "owned", CapacityBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.Get(ctx, u.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, client, s, u, c, sp, d
}
func storageAuthorizeInput(generation int64) AuthorizeStorageInput {
	return AuthorizeStorageInput{Payload: storageauth.S3Payload{Version: 1, AccessKeyID: "access", SecretAccessKey: "secret"}, WriteCheck: true, ExpectedManagementGeneration: generation}
}

func TestStorageConnectionAuthorizationHasDurableProbeAndExactAccounting(t *testing.T) {
	ctx, client, s, u, c, sp, d := storageConnectionFixture(t)
	d.beforePut = func(key string) {
		w, err := client.StorageWrite.Query().Where(storagewrite.SpaceIDEQ(sp.ID), storagewrite.ObjectKeyEQ(key)).Only(ctx)
		if err != nil {
			t.Fatal("write sent before durable registration", err)
		}
		if !w.OutcomeUnknown || w.LocationID == nil {
			t.Fatal("write registration incomplete")
		}
		if !client.DeletionEntry.Query().Where(deletionentry.LocationIDEQ(*w.LocationID)).ExistX(ctx) {
			t.Fatal("missing cleanup evidence")
		}
		r := client.StorageReservation.Query().Where(storagereservation.WriteIDEQ(w.ID)).OnlyX(ctx)
		if r.State != storagereservation.StateReserved {
			t.Fatal("write sent without reserved capacity")
		}
	}
	got, err := s.Authorize(ctx, u.ID, c.ID, storageAuthorizeInput(c.ManagementGeneration))
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasAuth || got.AuthGeneration != 1 || got.ManagementGeneration != c.ManagementGeneration+1 {
		t.Fatal("authorization not activated")
	}
	space := client.StorageSpace.GetX(ctx, sp.ID)
	if !space.Verified || space.ReservedBytes != 0 || space.CandidateBytes != 0 || space.PendingDeleteBytes != 0 || space.LiveBytes != int64(len(storageMarkerBytes(space))) {
		t.Fatalf("incorrect space accounting %+v", space)
	}
	if len(d.objects) != 1 {
		t.Fatalf("probes not deleted: %d", len(d.objects))
	}
	writes := client.StorageWrite.Query().AllX(ctx)
	if len(writes) != 2 {
		t.Fatalf("expected marker and probe logs, got %d", len(writes))
	}
	for _, w := range writes {
		if w.Phase != "committed" && w.Phase != "cleaned" {
			t.Fatalf("unfinished probe: %s", w.Phase)
		}
	}
	a := client.StorageAuthVersion.Query().OnlyX(ctx)
	if bytes.Contains(a.Ciphertext, []byte("secret")) {
		t.Fatal("plaintext persisted")
	}
	other := client.User.Create().SetUsername("other-storage-user").SetEmail("other-storage@example.test").SetPasswordHash("unused").SaveX(ctx)
	if _, err = s.Get(ctx, other.ID, c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("cross-owner access allowed")
	}
}

func TestStorageConnectionRevocationWinsSlowCandidate(t *testing.T) {
	ctx, client, s, u, c, _, d := storageConnectionFixture(t)
	if _, err := s.Authorize(ctx, u.ID, c.ID, storageAuthorizeInput(c.ManagementGeneration)); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Get(ctx, u.ID, c.ID)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	d.capabilities = func(ctx context.Context) error {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	finished := make(chan error, 1)
	go func() {
		_, err := s.Authorize(ctx, u.ID, c.ID, storageAuthorizeInput(c.ManagementGeneration))
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("candidate did not start")
	}
	if _, err := s.Revoke(ctx, u.ID, c.ID, c.ManagementGeneration); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-finished; !errors.Is(err, ErrStorageConflict) {
		t.Fatalf("slow candidate restored revoked access: %v", err)
	}
	row := client.StorageConnection.GetX(ctx, c.ID)
	if row.ActiveAuthGeneration != 0 {
		t.Fatal("revocation undone")
	}
	if client.StorageAuthVersion.Query().Where(storageauthversion.StatusEQ(storageauthversion.StatusActive)).ExistX(ctx) {
		t.Fatal("active auth survived revocation")
	}
}

func TestStorageConnectionUnknownPutReconcilesBeforeRetry(t *testing.T) {
	ctx, client, s, u, c, sp, d := storageConnectionFixture(t)
	d.failPutResponse = true
	if _, err := s.Authorize(ctx, u.ID, c.ID, storageAuthorizeInput(c.ManagementGeneration)); !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("response-loss result: %v", err)
	}
	before := client.StorageSpace.GetX(ctx, sp.ID)
	if before.ReservedBytes == 0 || before.Verified {
		t.Fatal("unknown result lost reservation or published")
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	after := client.StorageSpace.GetX(ctx, sp.ID)
	if after.ReservedBytes != 0 || after.LiveBytes == 0 {
		t.Fatal("marker recovery failed")
	}
	if _, err := s.Authorize(ctx, u.ID, c.ID, storageAuthorizeInput(c.ManagementGeneration)); err != nil {
		t.Fatal(err)
	}
	if got := client.StorageConnection.GetX(ctx, c.ID); got.ActiveAuthGeneration != 2 {
		t.Fatal("retry did not create a new authorization")
	}
}

func TestStorageConnectionDeleteResponseLossAndReadonlyCleanup(t *testing.T) {
	ctx, client, s, u, c, sp, d := storageConnectionFixture(t)
	d.failDeleteResponse = true
	if _, err := s.Authorize(ctx, u.ID, c.ID, storageAuthorizeInput(c.ManagementGeneration)); !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("delete response loss: %v", err)
	}
	if client.StorageSpace.GetX(ctx, sp.ID).ReservedBytes == 0 {
		t.Fatal("unknown deletion freed capacity")
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if client.StorageSpace.GetX(ctx, sp.ID).ReservedBytes != 0 {
		t.Fatal("confirmed absence did not settle reservation")
	}
	if _, err := s.Authorize(ctx, u.ID, c.ID, storageAuthorizeInput(c.ManagementGeneration)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSpaceStatus(ctx, u.ID, sp.ID, "read_only", 0); err != nil {
		t.Fatal(err)
	}
	driver, err := s.ResolveDriver(ctx, sp.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = driver.PutNew(ctx, "blocked", strings.NewReader("x"), 1); !errors.Is(err, ErrStorageMaintenance) {
		t.Fatalf("read-only write allowed: %v", err)
	}
	if err = driver.Delete(ctx, storage.Object{Key: "obsolete"}); err != nil {
		t.Fatalf("read-only cleanup blocked: %v", err)
	}
	puts := d.puts
	input := storageAuthorizeInput(client.StorageConnection.GetX(ctx, c.ID).ManagementGeneration)
	input.WriteCheck = false
	if _, err = s.Authorize(ctx, u.ID, c.ID, input); err != nil {
		t.Fatal(err)
	}
	if d.puts != puts {
		t.Fatal("readonly reconnect wrote remote objects")
	}
	if _, err = s.Revoke(ctx, u.ID, c.ID, client.StorageConnection.GetX(ctx, c.ID).ManagementGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err = driver.Stat(ctx, storage.Object{Key: storageMarkerKey}); !errors.Is(err, storage.ErrAuthRequired) {
		t.Fatal("existing driver bypassed revocation")
	}
}

func TestStorageConnectionSpaceOverlapAndFeatureGate(t *testing.T) {
	ctx, _, s, u, c, _, _ := storageConnectionFixture(t)
	if _, err := s.CreateSpace(ctx, u.ID, c.ID, CreateStorageSpaceInput{Name: "overlap", Bucket: "bucket", Prefix: "owned/child"}); !errors.Is(err, ErrStorageConflict) {
		t.Fatalf("overlap allowed: %v", err)
	}
	if _, err := s.CreateSpace(ctx, u.ID, c.ID, CreateStorageSpaceInput{Name: "other", Bucket: "bucket", Prefix: "other"}); err != nil {
		t.Fatal(err)
	}
	alias, err := s.Create(ctx, u.ID, CreateStorageConnectionInput{Name: "alias", Scope: "user", OwnerID: u.ID, Endpoint: "https://alias.example", Region: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateSpace(ctx, u.ID, alias.ID, CreateStorageSpaceInput{Name: "alias-root", Bucket: "bucket", Prefix: "owned/child"}); !errors.Is(err, ErrStorageConflict) {
		t.Fatalf("endpoint alias bypassed root overlap: %v", err)
	}
	s.cfg.Enabled = false
	if _, err := s.Create(ctx, u.ID, CreateStorageConnectionInput{Name: "blocked", Scope: "user", OwnerID: u.ID, Endpoint: "https://other.example", Region: "test"}); !errors.Is(err, ErrStorageDeploymentDisabled) {
		t.Fatal("disabled remote feature accepted new connection")
	}
}

func TestStorageSiteSetupIsOfflineAndRejectsLocationChanges(t *testing.T) {
	ctx := context.Background()
	client := testClient(t)
	cfg := config.DefaultStorageConfig()
	cfg.Enabled = true
	cfg.DefaultSiteSpace = "remote"
	cfg.Backends = []config.StorageBackendConfig{{ID: "remote", Driver: "s3", Endpoint: "https://site.example", Region: "test", Bucket: "site-files", Prefix: "managed", AccessKeyID: "site-key", SecretAccessKey: "site-secret"}}
	var calls atomic.Int32
	s := NewStorageConnectionService(client, credentialTestKeyring(t, "one", "one"), cfg, func(context.Context, *ent.StorageConnection, *ent.StorageSpace, storageauth.S3Payload) (storage.Driver, error) {
		calls.Add(1)
		return nil, storage.ErrUnavailable
	})
	id, err := s.SetupSiteBackends(ctx)
	if err != nil || id == 0 {
		t.Fatalf("setup: %d %v", id, err)
	}
	if calls.Load() != 0 {
		t.Fatal("startup probed remote storage")
	}
	s.cfg.Enabled = false
	if got, err := s.SetupSiteBackends(ctx); err != nil || got != id {
		t.Fatal("feature gate hid existing site binding")
	}
	s.cfg.Backends[0].Bucket = "replacement"
	if _, err := s.SetupSiteBackends(ctx); !errors.Is(err, ErrStorageConflict) {
		t.Fatalf("stable backend silently moved: %v", err)
	}
	if client.StorageConnection.Query().Where(storageconnection.AuthSourceEQ(storageconnection.AuthSourceDeployment)).CountX(ctx) != 1 {
		t.Fatal("setup duplicated site connection")
	}
}

func TestStorageConnectionCryptoIsolationAndReauthorization(t *testing.T) {
	ctx, client, s, u, c, sp, _ := storageConnectionFixture(t)
	if _, err := s.Authorize(ctx, u.ID, c.ID, storageAuthorizeInput(c.ManagementGeneration)); err != nil {
		t.Fatal(err)
	}
	s.keys = credentialTestKeyring(t, "two", "two")
	if unavailable, err := s.ValidateCurrentKeys(ctx); err != nil || unavailable != 1 {
		t.Fatalf("storage key isolation: %d %v", unavailable, err)
	}
	if got := client.StorageConnection.GetX(ctx, c.ID); got.Health != "crypto_unavailable" {
		t.Fatal("missing-key health not isolated")
	}
	if _, err := s.ResolveDriver(ctx, sp.ID, false); !errors.Is(err, ErrStorageCrypto) {
		t.Fatalf("missing key did not stop connection: %v", err)
	}
	in := storageAuthorizeInput(client.StorageConnection.GetX(ctx, c.ID).ManagementGeneration)
	in.WriteCheck = false
	if _, err := s.Authorize(ctx, u.ID, c.ID, in); err != nil {
		t.Fatalf("new authorization could not recover access: %v", err)
	}
	if unavailable, err := s.ValidateCurrentKeys(ctx); err != nil || unavailable != 0 {
		t.Fatalf("old retired ciphertext blocked new credentials: %d %v", unavailable, err)
	}
	if _, err := s.ResolveDriver(ctx, sp.ID, false); err != nil {
		t.Fatal(err)
	}
}

func TestStorageConnectionReencryptPreservesAuthorizationFacts(t *testing.T) {
	ctx, client, s, u, c, _, _ := storageConnectionFixture(t)
	if _, err := s.Authorize(ctx, u.ID, c.ID, storageAuthorizeInput(c.ManagementGeneration)); err != nil {
		t.Fatal(err)
	}
	before := client.StorageAuthVersion.Query().OnlyX(ctx)
	s.keys = credentialTestKeyring(t, "two", "one", "two")
	changed, failed, err := s.Reencrypt(ctx)
	if err != nil || changed != 1 || failed != 0 {
		t.Fatalf("reencrypt: %d %d %v", changed, failed, err)
	}
	after := client.StorageAuthVersion.GetX(ctx, before.ID)
	if after.Generation != before.Generation || after.Status != before.Status || after.KeyID != "two" || bytes.Equal(before.Nonce, after.Nonce) {
		t.Fatal("reencryption changed authorization identity or reused nonce")
	}
	if changed, failed, err := s.Reencrypt(ctx); err != nil || changed != 0 || failed != 0 {
		t.Fatalf("repeated reencryption changed active ciphertext: %d %d %v", changed, failed, err)
	}
	client.StorageAuthVersion.UpdateOneID(after.ID).SetCiphertext([]byte("corrupt-active-key-ciphertext")).ExecX(ctx)
	if changed, failed, err := s.Reencrypt(ctx); err != nil || changed != 0 || failed != 1 {
		t.Fatalf("matching key ID hid unreadable ciphertext: %d %d %v", changed, failed, err)
	}
}

func TestStorageSiteExplicitCheckVerifiesWithoutPersistingCredentials(t *testing.T) {
	ctx := context.Background()
	client := testClient(t)
	admin := client.User.Create().SetUsername("site-admin").SetEmail("site-admin@example.test").SetPasswordHash("unused").SetRole(SystemRoleAdmin).SaveX(ctx)
	cfg := config.DefaultStorageConfig()
	cfg.Enabled = true
	cfg.DefaultSiteSpace = "remote"
	cfg.Backends = []config.StorageBackendConfig{{ID: "remote", Driver: "s3", Endpoint: "https://site.example", Region: "test", Bucket: "site-files", AccessKeyID: "deployment-access", SecretAccessKey: "deployment-secret"}}
	d := &connectionTestDriver{objects: map[string][]byte{}}
	s := NewStorageConnectionService(client, nil, cfg, func(_ context.Context, _ *ent.StorageConnection, _ *ent.StorageSpace, p storageauth.S3Payload) (storage.Driver, error) {
		if p.AccessKeyID != "deployment-access" {
			t.Error("wrong deployment credential source")
		}
		return d, nil
	})
	spaceID, err := s.SetupSiteBackends(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sp := client.StorageSpace.GetX(ctx, spaceID)
	if sp.Verified {
		t.Fatal("offline setup claimed verified")
	}
	if _, err = s.Check(ctx, admin.ID, sp.ConnectionID, true, 0); err != nil {
		t.Fatal(err)
	}
	if !client.StorageSpace.GetX(ctx, spaceID).Verified {
		t.Fatal("explicit check did not verify")
	}
	if client.StorageAuthVersion.Query().CountX(ctx) != 0 {
		t.Fatal("deployment secrets copied into user auth store")
	}
	s.cfg.Enabled = false
	if _, err = s.ResolveDriver(ctx, spaceID, false); err != nil {
		t.Fatal("disabled feature blocked historical read")
	}
	if _, err = s.ResolveDriver(ctx, spaceID, true); !errors.Is(err, ErrStorageDeploymentDisabled) {
		t.Fatalf("disabled feature admitted new write: %v", err)
	}
}
