package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/migrate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func bootstrapForTest(name string, registration bool) config.BootstrapInput {
	return config.BootstrapInput{RegistrationEnabled: registration, Admin: &config.BootstrapAdmin{Username: name, Email: name + "@test.invalid", Password: "password-123"}}
}

func TestInitializationInventory(t *testing.T) {
	checks := initializationBusinessChecks(testClient(t))
	for _, table := range migrate.Tables {
		if table.Name == "instance_initializations" {
			continue
		}
		if _, ok := checks[table.Name]; !ok {
			t.Errorf("business table %s missing from initialization guard", table.Name)
		}
		delete(checks, table.Name)
	}
	if len(checks) != 0 {
		t.Fatalf("unknown tables in initialization inventory: %v", checks)
	}
}

func TestInitializationIsEmpty(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	svc := NewInitializationService(c)
	if empty, err := svc.IsEmpty(ctx); err != nil || !empty {
		t.Fatalf("new schema: empty=%v err=%v", empty, err)
	}
	if c.InstanceInitialization.Query().CountX(ctx) != 0 {
		t.Fatal("empty check wrote initialization marker")
	}
	c.SystemSetting.Create().SetKey(SettingRegistrationEnabled).SetValue("false").SaveX(ctx)
	if empty, err := svc.IsEmpty(ctx); err != nil || empty {
		t.Fatalf("orphan settings: empty=%v err=%v", empty, err)
	}
	c.SystemSetting.Delete().ExecX(ctx)
	if _, err := svc.Initialize(ctx, config.ModeLocal, config.BootstrapInput{}); err != nil {
		t.Fatal(err)
	}
	if empty, err := svc.IsEmpty(ctx); err != nil || empty {
		t.Fatalf("initialized instance: empty=%v err=%v", empty, err)
	}
}

func TestInitializationAtomicAndPreservesDatabaseAuthority(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	svc := NewInitializationService(c)
	if _, err := svc.Initialize(ctx, config.ModeServer, bootstrapForTest("first", true)); err != nil {
		t.Fatal(err)
	}
	account := c.User.Query().OnlyX(ctx)
	if _, err := NewSettingsService(c).Update(ctx, account.ID, SystemSettings{}); err != nil {
		t.Fatal(err)
	}
	c.User.UpdateOneID(account.ID).SetRole(SystemRoleUser).ExecX(ctx)
	if _, err := svc.Initialize(ctx, config.ModeServer, bootstrapForTest("second", true)); err != nil {
		t.Fatal(err)
	}
	if c.User.Query().CountX(ctx) != 1 || c.User.GetX(ctx, account.ID).Role != SystemRoleUser {
		t.Fatal("restart changed administrator identity")
	}
	settings, err := NewSettingsService(c).Get(ctx)
	if err != nil || settings.RegistrationEnabled {
		t.Fatalf("policy overwritten: %+v %v", settings, err)
	}
	if _, err := svc.Initialize(ctx, config.ModeLocal, config.BootstrapInput{}); !errors.Is(err, ErrInstanceIncomplete) {
		t.Fatalf("mode switch error: %v", err)
	}
	// Even removal of all users must not reopen first-run ownership.
	c.ActivityLog.Delete().ExecX(ctx)
	c.User.Delete().ExecX(ctx)
	if _, err := svc.Initialize(ctx, config.ModeServer, config.BootstrapInput{}); err != nil {
		t.Fatal(err)
	}
	if c.User.Query().CountX(ctx) != 0 {
		t.Fatal("restart recreated administrator")
	}
}

func TestInitializationRollbackAtEveryWrite(t *testing.T) {
	for _, test := range []struct {
		mode, entity string
		op           ent.Op
	}{
		{config.ModeServer, "InstanceInitialization", ent.OpCreate},
		{config.ModeServer, "SystemSetting", ent.OpCreate},
		{config.ModeServer, "User", ent.OpCreate},
		{config.ModeServer, "ActivityLog", ent.OpCreate},
		{config.ModeLocal, "InstanceInitialization", ent.OpCreate},
		{config.ModeLocal, "SystemSetting", ent.OpCreate},
		{config.ModeLocal, "User", ent.OpCreate},
		{config.ModeLocal, "InstanceInitialization", ent.OpUpdateOne},
		{config.ModeLocal, "ActivityLog", ent.OpCreate},
	} {
		t.Run(test.mode+"/"+test.entity+"/"+test.op.String(), func(t *testing.T) {
			c := testClient(t)
			failure := errors.New("injected failure")
			c.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
					if m.Type() == test.entity && m.Op().Is(test.op) {
						return nil, failure
					}
					return next.Mutate(ctx, m)
				})
			})
			input := bootstrapForTest("admin", false)
			if test.mode == config.ModeLocal {
				input.Admin = nil
			}
			_, err := NewInitializationService(c).Initialize(context.Background(), test.mode, input)
			if !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
			if c.InstanceInitialization.Query().CountX(context.Background()) != 0 {
				t.Fatal("partial marker")
			}
			for table, exists := range initializationBusinessChecks(c) {
				populated, err := exists(context.Background())
				if err != nil || populated {
					t.Fatalf("partial %s: %v %v", table, populated, err)
				}
			}
		})
	}
}

func TestInitializationCommitFailureRollsBack(t *testing.T) {
	for _, mode := range []string{config.ModeServer, config.ModeLocal} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			client := testClient(t)
			failure := errors.New("commit rejected")
			client.ActivityLog.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					tx, err := mutation.(*ent.ActivityLogMutation).Tx()
					if err != nil {
						return nil, err
					}
					tx.OnCommit(func(ent.Committer) ent.Committer {
						return ent.CommitFunc(func(context.Context, *ent.Tx) error { return failure })
					})
					return next.Mutate(ctx, mutation)
				})
			})
			input := bootstrapForTest("admin", true)
			if mode == config.ModeLocal {
				input.Admin = nil
			}
			if _, err := NewInitializationService(client).Initialize(ctx, mode, input); !errors.Is(err, failure) {
				t.Fatalf("commit error=%v", err)
			}
			if empty, err := NewInitializationService(client).IsEmpty(ctx); err != nil || !empty {
				t.Fatalf("commit failure persisted initialization: empty=%v err=%v", empty, err)
			}
		})
	}
}

func TestInitializationRejectsIncompleteAndMissingInputs(t *testing.T) {
	ctx := context.Background()
	t.Run("missing administrator", func(t *testing.T) {
		c := testClient(t)
		if _, err := NewInitializationService(c).Initialize(ctx, config.ModeServer, config.BootstrapInput{}); err == nil {
			t.Fatal("missing admin accepted")
		}
		if c.InstanceInitialization.Query().CountX(ctx) != 0 {
			t.Fatal("partial marker")
		}
	})
	t.Run("orphan data", func(t *testing.T) {
		c := testClient(t)
		c.SystemSetting.Create().SetKey(SettingRegistrationEnabled).SetValue("true").SaveX(ctx)
		if _, err := NewInitializationService(c).Initialize(ctx, config.ModeServer, bootstrapForTest("admin", false)); !errors.Is(err, ErrInstanceIncomplete) {
			t.Fatalf("error=%v", err)
		}
		if c.InstanceInitialization.Query().CountX(ctx) != 0 || c.User.Query().CountX(ctx) != 0 {
			t.Fatal("claimed incomplete database")
		}
	})
	t.Run("damaged policy", func(t *testing.T) {
		c := testClient(t)
		svc := NewInitializationService(c)
		if _, err := svc.Initialize(ctx, config.ModeServer, bootstrapForTest("admin", false)); err != nil {
			t.Fatal(err)
		}
		c.SystemSetting.Delete().ExecX(ctx)
		if _, err := svc.Initialize(ctx, config.ModeServer, bootstrapForTest("new", true)); !errors.Is(err, ErrSettingsUnavailable) {
			t.Fatalf("error=%v", err)
		}
	})
	for _, state := range []string{"inactive", "demoted", "deleted"} {
		t.Run("local identity/"+state, func(t *testing.T) {
			c := testClient(t)
			svc := NewInitializationService(c)
			local, err := svc.Initialize(ctx, config.ModeLocal, config.BootstrapInput{})
			if err != nil {
				t.Fatal(err)
			}
			if local == nil || local.Role != SystemRoleAdmin {
				t.Fatal("missing local admin")
			}
			switch state {
			case "inactive":
				c.User.UpdateOneID(local.ID).SetActive(false).ExecX(ctx)
			case "demoted":
				c.User.UpdateOneID(local.ID).SetRole(SystemRoleUser).ExecX(ctx)
			case "deleted":
				if err := c.User.DeleteOneID(local.ID).Exec(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := svc.Initialize(ctx, config.ModeLocal, config.BootstrapInput{}); !errors.Is(err, ErrInstanceIncomplete) {
				t.Fatalf("error=%v", err)
			}
			marker := c.InstanceInitialization.Query().OnlyX(ctx)
			if marker.LocalUserID == nil || *marker.LocalUserID != local.ID {
				t.Fatal("restart reassigned local identity")
			}
		})
	}
}

func TestInitializationIndependentSQLiteConnections(t *testing.T) {
	for _, mode := range []string{config.ModeServer, config.ModeLocal} {
		t.Run(mode, func(t *testing.T) {
			dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "initialization.db")) + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)"
			open := func() *ent.Client {
				db, err := sql.Open("sqlite", dsn)
				if err != nil {
					t.Fatal(err)
				}
				db.SetMaxOpenConns(1)
				c := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
				t.Cleanup(func() { _ = c.Close() })
				return c
			}
			first, second := open(), open()
			if err := first.Schema.Create(context.Background()); err != nil {
				t.Fatal(err)
			}
			runInitializationCompetition(t, first, second, mode)
		})
	}
}

func TestInitializationIndependentPostgresConnections(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test DSN")
	}
	admin := stdlib.OpenDB(*base)
	defer admin.Close()
	schema := fmt.Sprintf("initialization_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	open := func() *ent.Client {
		cfg := base.Copy()
		cfg.RuntimeParams["search_path"] = schema
		cfg.RuntimeParams["timezone"] = "UTC"
		db := stdlib.OpenDB(*cfg)
		db.SetMaxOpenConns(1)
		return ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.Postgres, db))))
	}
	first, second := open(), open()
	defer first.Close()
	defer second.Close()
	if err := first.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	runInitializationCompetition(t, first, second, config.ModeServer)
}

func runInitializationCompetition(t *testing.T, first, second *ent.Client, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i, c := range []*ent.Client{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			name := "first"
			if i == 1 {
				name = "second"
			}
			input := bootstrapForTest(name, i == 0)
			if mode == config.ModeLocal {
				input.Admin = nil
			}
			_, err := NewInitializationService(c).Initialize(ctx, mode, input)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if first.User.Query().CountX(ctx) != 1 || first.InstanceInitialization.Query().CountX(ctx) != 1 || first.ActivityLog.Query().CountX(ctx) != 1 {
		t.Fatal("duplicate initialization")
	}
	u := first.User.Query().OnlyX(ctx)
	setting, err := NewSettingsService(first).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mode == config.ModeServer && setting.RegistrationEnabled != (u.Username == "first") {
		t.Fatal("loser's bootstrap policy was applied")
	}
	if mode == config.ModeLocal {
		for _, client := range []*ent.Client{first, second} {
			local, err := NewInitializationService(client).Validate(ctx, mode)
			if err != nil || local == nil || local.ID != u.ID {
				t.Fatalf("local initializers did not converge on one identity: %v", err)
			}
		}
		audit := first.ActivityLog.Query().OnlyX(ctx)
		if audit.Metadata["registration_enabled"] != setting.RegistrationEnabled {
			t.Fatal("local policy differs from the committed initializer")
		}
	}
}

func TestAdministratorMaintenancePreservesPolicyAndRevokesSessions(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	svc := NewInitializationService(c)
	if _, err := svc.Initialize(ctx, config.ModeServer, bootstrapForTest("admin", false)); err != nil {
		t.Fatal(err)
	}
	account := c.User.Query().Where(user.UsernameEQ("admin")).OnlyX(ctx)
	auth := NewAuthService(c, AuthConfig{Secret: []byte("test-secret"), Issuer: "test", AccessTokenTTL: time.Hour, RefreshTokenTTL: time.Hour}, NewSettingsService(c))
	session, err := auth.Login(ctx, LoginInput{Username: "admin", Password: "password-123"})
	if err != nil {
		t.Fatal(err)
	}
	c.User.UpdateOneID(account.ID).SetRole(SystemRoleUser).SetActive(false).ExecX(ctx)
	recovered, err := svc.MaintainAdministrator(ctx, AdminCreateUserInput{Username: "admin", Password: "new-password"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID != account.ID || !recovered.Active || recovered.Role != SystemRoleAdmin {
		t.Fatal("incorrect recovery")
	}
	if _, err := auth.Refresh(ctx, session.RefreshToken); !errors.Is(err, ErrRefreshTokenRevoked) {
		t.Fatalf("old session accepted: %v", err)
	}
	if _, err := auth.Login(ctx, LoginInput{Username: "admin", Password: "new-password"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MaintainAdministrator(ctx, AdminCreateUserInput{Username: "second", Email: "second@test.invalid", Password: "new-password"}, false); err != nil {
		t.Fatal(err)
	}
	settings, err := NewSettingsService(c).Get(ctx)
	if err != nil || settings.RegistrationEnabled {
		t.Fatal("maintenance changed policy")
	}
}

func TestAdministratorMaintenanceAuditFailureRollsBackIdentity(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	svc := NewInitializationService(c)
	if _, err := svc.Initialize(ctx, config.ModeServer, bootstrapForTest("admin", false)); err != nil {
		t.Fatal(err)
	}
	account := c.User.Query().OnlyX(ctx)
	token := c.RefreshToken.Create().SetTokenHash("maintenance-token").SetUserID(account.ID).SetExpiresAt(time.Now().Add(time.Hour)).SaveX(ctx)
	c.User.UpdateOneID(account.ID).SetRole(SystemRoleUser).SetActive(false).ExecX(ctx)
	failure := errors.New("audit unavailable")
	c.ActivityLog.Use(func(ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) { return nil, failure })
	})
	if _, err := svc.MaintainAdministrator(ctx, AdminCreateUserInput{Username: "admin", Password: "new-password"}, true); !errors.Is(err, failure) {
		t.Fatalf("error=%v", err)
	}
	unchanged := c.User.GetX(ctx, account.ID)
	if unchanged.Role != SystemRoleUser || unchanged.Active || unchanged.PasswordHash != account.PasswordHash {
		t.Fatal("failed maintenance changed identity")
	}
	if c.RefreshToken.GetX(ctx, token.ID).RevokedAt != nil {
		t.Fatal("failed maintenance revoked an existing session")
	}
	if _, err := svc.MaintainAdministrator(ctx, AdminCreateUserInput{Username: "second", Email: "second@test.invalid", Password: "new-password"}, false); !errors.Is(err, failure) {
		t.Fatalf("error=%v", err)
	}
	if c.User.Query().CountX(ctx) != 1 {
		t.Fatal("failed create left an administrator")
	}
}
