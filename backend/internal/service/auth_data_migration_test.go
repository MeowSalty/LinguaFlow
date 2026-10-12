package service

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
)

func TestAuthenticationB1MigrationPreservesStageA(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			_, clients := concurrentAuthClients(t, driver)
			c, ctx := clients[0], context.Background()
			account := createPasswordTestUser(t, c, "existing-owner", "password-123")
			project := c.Project.Create().SetName("Existing project").SetOwnerUserID(account.ID).SaveX(ctx)
			before, err := readPasswordPolicy(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			c.InstanceInitialization.UpdateOneID(1).SetDataVersion(2).ExecX(ctx)
			for range 2 {
				if err := MigrateData(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			if err := ValidateDataVersion(ctx, c); err != nil {
				t.Fatal(err)
			}
			after, err := readPasswordPolicy(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			current, owned := c.User.GetX(ctx, account.ID), c.Project.GetX(ctx, project.ID)
			if current.Username != account.Username || current.PasswordHash != account.PasswordHash || current.AuthVersion != account.AuthVersion || owned.OwnerUserID == nil || *owned.OwnerUserID != account.ID {
				t.Fatal("upgrade changed existing identity or business ownership")
			}
			if before.Connection.AuthGeneration != after.Connection.AuthGeneration || before.Policy.Revision != after.Policy.Revision {
				t.Fatal("upgrade reset password policy")
			}
			if c.AuthConnection.Query().CountX(ctx) != 1 || c.ExternalIdentity.Query().CountX(ctx) != 0 {
				t.Fatal("upgrade invented third-party identities")
			}
		})
	}
}
