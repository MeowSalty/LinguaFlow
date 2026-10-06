package v013

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

var testDatabases sync.Map

func testClient(t *testing.T) *ent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	testDatabases.Store(client, db)
	t.Cleanup(func() { testDatabases.Delete(client); client.Close(); db.Close() })
	return client
}

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

type testCredentials struct {
	*service.CredentialService
	keys *credential.Keyring
}

func credentialTestServices(t *testing.T) (context.Context, *ent.Client, *ent.User, *testCredentials, *service.BackendService) {
	t.Helper()
	ctx := context.Background()
	client := testClient(t)
	u := client.User.Create().SetUsername("credential-owner").SetEmail("credential@example.test").SetPasswordHash("unused").SaveX(ctx)
	users := service.NewUserService(client, nil)
	keys := credentialTestKeyring(t, "one", "one")
	c := service.NewCredentialService(client, keys, users)
	b := service.NewBackendService(client, users, nil)
	b.SetCredentials(c)
	return ctx, client, u, &testCredentials{CredentialService: c, keys: keys}, b
}

func credentialTestBackend(t *testing.T, ctx context.Context, b *service.BackendService, u *ent.User, name, secret string) *service.BackendRecord {
	t.Helper()
	r, err := b.Create(ctx, service.CreateBackendInput{Scope: "user", OwnerUserID: &u.ID, BackendInput: service.BackendInput{Name: name, Type: "openai", Options: map[string]any{"model": "test"}, Secret: &secret}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func backendBinding(ctx context.Context, client *ent.Client, id int) credential.Binding {
	row := client.Backend.GetX(ctx, id)
	if row.CredentialID == nil {
		return credential.Binding{}
	}
	c := client.Credential.GetX(ctx, *row.CredentialID)
	return credential.Binding{ID: c.ID, Version: c.CurrentVersion}
}

// Seed exact old JSON, bypassing the current generated profile JSON writer.
func legacyTestProfile(t *testing.T, ctx context.Context, client *ent.Client, ownerID int, profile sourceExecutionProfileConfigData) *ent.ExecutionProfile {
	t.Helper()
	row := client.ExecutionProfile.Create().SetName("legacy profile").SetScope("user").SetOwnerUserID(ownerID).SetConfig(execution.ProfileSpec{}).SaveX(ctx)
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := testDatabases.Load(client)
	if _, err := value.(*sql.DB).ExecContext(ctx, "UPDATE execution_profiles SET config=? WHERE id=?", string(raw), row.ID); err != nil {
		t.Fatal(err)
	}
	return row
}
