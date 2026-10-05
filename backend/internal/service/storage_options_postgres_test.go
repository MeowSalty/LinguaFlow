package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestStoragePostgresDeletionWaitsForMaintenanceGate(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("PostgreSQL deletion/maintenance race unverified: LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	configuration, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test DSN")
	}
	admin := stdlib.OpenDB(*configuration)
	defer admin.Close()
	schema := fmt.Sprintf("storage_options_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("remove isolated test schema: %v", err)
		}
	}()
	configuration.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*configuration)
	db.SetMaxOpenConns(4)
	client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.Postgres, db))))
	defer client.Close()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"resource", "empty_project"} {
		t.Run(kind, func(t *testing.T) {
			owner := createTestUser(t, client, "maintenance-"+kind)
			p := client.Project.Create().SetName(kind).SetOwnerUserID(owner.ID).SaveX(ctx)
			projects := NewProjectService(client, NewUserService(client, nil))
			resources := NewResourceService(client, projects, nil)
			var resourceID int
			if kind == "resource" {
				resourceID = client.Resource.Create().SetProjectID(p.ID).SetPath("original.txt").SetFormat("txt").SetStoragePath("legacy.txt").SaveX(ctx).ID
			}
			migration, err := client.Tx(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer migration.Rollback()
			if err := migration.Project.UpdateOneID(p.ID).SetStorageState("draining").AddStorageGeneration(1).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			attempted := make(chan struct{})
			var once sync.Once
			client.Project.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					if mutation.Op().Is(ent.OpUpdate | ent.OpUpdateOne) {
						once.Do(func() { close(attempted) })
					}
					return next.Mutate(ctx, mutation)
				})
			})
			result := make(chan error, 1)
			go func() {
				if kind == "resource" {
					result <- resources.DeleteResource(ctx, owner.ID, p.ID, resourceID)
				} else {
					_, err := projects.DeleteProject(ctx, owner.ID, p.ID)
					result <- err
				}
			}()
			select {
			case <-attempted:
			case err := <-result:
				t.Fatalf("deletion bypassed the project's mutation gate: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := migration.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if !errors.Is(err, ErrStorageMaintenance) {
					t.Fatalf("deletion did not observe committed maintenance state: %v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if current := client.Project.GetX(ctx, p.ID); current.StorageState != "draining" || current.StorageGeneration != 1 {
				t.Fatalf("deletion changed migration facts: %+v", current)
			}
			if resourceID != 0 {
				client.Resource.GetX(ctx, resourceID)
			}
		})
	}
}
