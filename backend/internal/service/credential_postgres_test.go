package service

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestCredentialLifecyclePostgres(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("PostgreSQL credential lifecycle unverified: LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test DSN")
	}
	admin := stdlib.OpenDB(*base)
	defer admin.Close()
	schema := fmt.Sprintf("credential_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	base.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*base)
	db.SetMaxOpenConns(2)
	client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.Postgres, db))))
	defer client.Close()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetUsername("pg-credential").SetEmail("pg-credential@example.test").SetPasswordHash("unused").SaveX(ctx)
	c := NewCredentialService(client, credentialTestKeyring(t, "one", "one"), nil)
	b := NewBackendService(client, NewUserService(client, nil), nil)
	b.SetCredentials(c)
	back := credentialTestBackend(t, ctx, b, u, "backend", "secret-one")
	binding, release, err := c.AcquireBackend(ctx, back.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Rotate(ctx, u.ID, binding.ID, "secret-two"); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Collect(ctx, u.ID, binding.ID); err != nil || n != 0 {
		t.Fatalf("lease lost: %d %v", n, err)
	}
	project := client.Project.Create().SetName("pg-job").SetOwnerUserID(u.ID).SaveX(ctx)
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	job, err := tx.Job.Create().SetProjectID(project.ID).SetExecutionPlanID(1).SetStatus("completed").Save(ctx)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := c.RetainJob(ctx, tx, job.ID, []credential.Binding{binding}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	release()
	if n, err := c.Collect(ctx, u.ID, binding.ID); err != nil || n != 0 {
		t.Fatalf("persisted binding lost: %d %v", n, err)
	}
	if err := client.Job.DeleteOneID(job.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Collect(ctx, u.ID, binding.ID); err != nil || n != 1 {
		t.Fatalf("FK release failed: %d %v", n, err)
	}
	next := NewCredentialService(client, credentialTestKeyring(t, "two", "one", "two"), nil)
	if n, err := next.Reencrypt(ctx); err != nil || n != 1 {
		t.Fatalf("reencrypt: %d %v", n, err)
	}
	if secret, err := next.Resolve(ctx, credential.Binding{ID: binding.ID, Version: 2}, "openai", back.Options["base_url"].(string)); err != nil || secret != "secret-two" {
		t.Fatalf("reencrypted secret: %v", err)
	}
}
