package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Each service has an independent connection pool, exercising database locks
// rather than an in-memory mutex shared by requests in one service instance.
func concurrentAuthClients(t *testing.T, driver string) ([2]*AuthService, [2]*ent.Client) {
	t.Helper()
	ctx := context.Background()
	var open func() *sql.DB
	if driver == dialect.Postgres {
		dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("PostgreSQL authentication concurrency unverified: LINGUAFLOW_TEST_POSTGRES_DSN is not set")
		}
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal("invalid PostgreSQL test DSN")
		}
		admin := stdlib.OpenDB(*cfg)
		name := fmt.Sprintf("auth_test_%d", time.Now().UnixNano())
		if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+name); err != nil {
			admin.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+name+" CASCADE")
			_ = admin.Close()
		})
		cfg.RuntimeParams["search_path"], cfg.RuntimeParams["timezone"] = name, "UTC"
		open = func() *sql.DB { return stdlib.OpenDB(*cfg) }
	} else {
		dsn := filepath.ToSlash(filepath.Join(t.TempDir(), "auth.db")) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
		open = func() *sql.DB {
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			return db
		}
	}
	var clients [2]*ent.Client
	var services [2]*AuthService
	for i := range clients {
		db := open()
		db.SetMaxOpenConns(1)
		client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(driver, db))))
		t.Cleanup(func() { _ = client.Close(); _ = db.Close() })
		clients[i] = client
		services[i] = NewAuthService(client, AuthConfig{Secret: []byte("authentication-concurrency-test-key"), Issuer: "auth-concurrency", AccessTokenTTL: time.Hour, RefreshTokenTTL: 24 * time.Hour}, NewSettingsService(client))
	}
	if err := clients[0].Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	if err := initializePasswordConnection(ctx, clients[0]); err != nil {
		t.Fatal(err)
	}
	clients[0].SystemSetting.Create().SetKey(SettingRegistrationEnabled).SetValue("true").SaveX(ctx)
	clients[0].InstanceInitialization.Create().SetID(1).SetVersion(InitializationVersion).SetDataVersion(CurrentDataVersion).SetMode("serve").SaveX(ctx)
	return services, clients
}

func TestAuthenticationConcurrency(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			t.Run("refresh replay revokes winning successor", func(t *testing.T) {
				services, clients := concurrentAuthClients(t, driver)
				ctx := context.Background()
				account := createPasswordTestUser(t, clients[0], "refresh-owner", "password-123")
				initial, err := services[0].Login(ctx, LoginInput{Identifier: account.Username, Password: "password-123"})
				if err != nil {
					t.Fatal(err)
				}
				type outcome struct {
					session *Session
					err     error
				}
				start, results := make(chan struct{}), make(chan outcome, 2)
				for _, svc := range services {
					go func(svc *AuthService) {
						<-start
						s, err := svc.Refresh(ctx, initial.RefreshToken)
						results <- outcome{s, err}
					}(svc)
				}
				close(start)
				var winner *Session
				for range 2 {
					r := <-results
					if r.err == nil {
						if winner != nil {
							t.Fatal("duplicate issuance")
						}
						winner = r.session
					} else if !errors.Is(r.err, ErrRefreshTokenRevoked) {
						t.Fatal(r.err)
					}
				}
				if winner == nil {
					t.Fatal("no refresh succeeded")
				}
				if _, err := services[0].Refresh(ctx, winner.RefreshToken); !errors.Is(err, ErrRefreshTokenRevoked) {
					t.Fatalf("successor remains usable: %v", err)
				}
				if n := clients[0].RefreshToken.Query().CountX(ctx); n != 2 {
					t.Fatalf("token history=%d", n)
				}
				if _, _, err := services[0].ResolveUserFromAccessToken(ctx, winner.AccessToken); err != nil {
					t.Fatalf("chain revocation changed access lifetime: %v", err)
				}
			})
			t.Run("logout covers concurrent rotation but isolates other logins", func(t *testing.T) {
				services, clients := concurrentAuthClients(t, driver)
				ctx := context.Background()
				account := createPasswordTestUser(t, clients[0], "logout-owner", "password-123")
				initial, err := services[0].issueSession(ctx, account)
				if err != nil {
					t.Fatal(err)
				}
				sibling, err := services[0].issueSession(ctx, account)
				if err != nil {
					t.Fatal(err)
				}
				start := make(chan struct{})
				done := make(chan error, 1)
				go func() { <-start; done <- services[1].Logout(ctx, account.ID, initial.RefreshToken) }()
				close(start)
				next, refreshErr := services[0].Refresh(ctx, initial.RefreshToken)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if refreshErr != nil && !errors.Is(refreshErr, ErrRefreshTokenRevoked) {
					t.Fatal(refreshErr)
				}
				if next != nil {
					if _, err := services[0].Refresh(ctx, next.RefreshToken); !errors.Is(err, ErrRefreshTokenRevoked) {
						t.Fatalf("successor usable after logout: %v", err)
					}
				}
				if err := services[0].Logout(ctx, account.ID, initial.RefreshToken); err != nil {
					t.Fatal(err)
				}
				if _, err := services[1].Refresh(ctx, sibling.RefreshToken); err != nil {
					t.Fatalf("independent chain revoked: %v", err)
				}
			})
			t.Run("administrator set changes serialize", func(t *testing.T) {
				_, clients := concurrentAuthClients(t, driver)
				ctx := context.Background()
				a := createTestUser(t, clients[0], "admin-one")
				b := createTestUser(t, clients[0], "admin-two")
				clients[0].User.UpdateOne(a).SetRole(SystemRoleAdmin).SaveX(ctx)
				clients[0].User.UpdateOne(b).SetRole(SystemRoleAdmin).SaveX(ctx)
				start, done := make(chan struct{}), make(chan error, 2)
				go func() { <-start; done <- NewAdminService(clients[0]).DisableUser(ctx, a.ID, b.ID) }()
				go func() { <-start; done <- NewAdminService(clients[1]).DisableUser(ctx, b.ID, a.ID) }()
				close(start)
				success := 0
				for range 2 {
					err := <-done
					if err == nil {
						success++
					} else if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrLastAdmin) {
						t.Fatal(err)
					}
				}
				if n := clients[0].User.Query().Where(user.ActiveEQ(true), user.RoleEQ(SystemRoleAdmin)).CountX(ctx); n != 1 || success != 1 {
					t.Fatalf("active admins=%d successful disables=%d", n, success)
				}
			})
		})
	}
}

func TestAuthenticationRevocationAndDeadlines(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			ctx := t.Context()
			services, clients := concurrentAuthClients(t, driver)
			svc, client := services[0], clients[0]
			admin := createPasswordTestUser(t, client, "security-admin", "password-123")
			client.User.UpdateOne(admin).SetRole(SystemRoleAdmin).SaveX(ctx)
			account := createPasswordTestUser(t, client, "security-user", "password-123")
			before, err := svc.issueSession(ctx, account)
			if err != nil {
				t.Fatal(err)
			}
			if err := NewAdminService(client).DisableUser(ctx, admin.ID, account.ID); err != nil {
				t.Fatal(err)
			}
			active := true
			if _, err := NewAdminService(client).UpdateUser(ctx, admin.ID, account.ID, AdminUpdateUserInput{Active: &active}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := services[1].ResolveUserFromAccessToken(ctx, before.AccessToken); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("access revived: %v", err)
			}
			if _, err := services[1].Refresh(ctx, before.RefreshToken); !errors.Is(err, ErrRefreshTokenRevoked) {
				t.Fatalf("refresh revived: %v", err)
			}
			if _, err := svc.issueSession(ctx, account); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("stale credential proof issued session: %v", err)
			}
			current := client.User.GetX(ctx, account.ID)
			session, err := svc.issueSession(ctx, current)
			if err != nil {
				t.Fatal(err)
			}
			deadline := session.RefreshExpiresAt
			svc.now = func() time.Time { return deadline.Add(-time.Minute) }
			next, err := svc.Refresh(ctx, session.RefreshToken)
			if err != nil {
				t.Fatal(err)
			}
			if !next.RefreshExpiresAt.Equal(deadline) {
				t.Fatal("rotation extended absolute lifetime")
			}
			svc.now = func() time.Time { return deadline }
			if _, err := svc.Refresh(ctx, next.RefreshToken); !errors.Is(err, ErrTokenExpired) {
				t.Fatalf("deadline equality accepted: %v", err)
			}
		})
	}
}

func TestAccessTokenRequiresPositiveIntegralVersion(t *testing.T) {
	svc, client := newAccountTestAuth(t)
	account := createTestUser(t, client, "version-user")
	for _, value := range []any{nil, 0, -1, 1.5, "1", 2} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			claims := jwt.MapClaims{"uid": account.ID, "sub": fmt.Sprintf("user:%d", account.ID), "iss": svc.cfg.Issuer, "exp": time.Now().Add(time.Hour).Unix()}
			if value != nil {
				claims["auth_version"] = value
			}
			raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(svc.cfg.Secret)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := svc.ResolveUserFromAccessToken(context.Background(), raw); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("version %#v accepted: %v", value, err)
			}
		})
	}
}

func TestRegistrationAtomicAndUnverifiedEmailDoesNotClaimAddress(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			services, clients := concurrentAuthClients(t, driver)
			svc, client := services[0], clients[0]
			ctx := t.Context()
			for _, name := range []string{"email-one", "email-two"} {
				if _, err := svc.Register(ctx, RegisterInput{Username: name, Email: "same@example.com", Password: "password-123"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := svc.Login(ctx, LoginInput{Identifier: "same@example.com", Password: "password-123"}); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatal("unverified email accepted")
			}
			if _, err := svc.Login(ctx, LoginInput{Identifier: "email-one", Password: "wrong"}); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatal(err)
			}
			before := client.User.Query().CountX(ctx)
			beforeSessions := client.RefreshSession.Query().CountX(ctx)
			beforeTokens := client.RefreshToken.Query().CountX(ctx)
			svc.rand = strings.NewReader("")
			if _, err := svc.Register(ctx, RegisterInput{Username: "atomic-signup", Password: "password-123"}); err == nil {
				t.Fatal("random failure accepted")
			}
			if client.User.Query().CountX(ctx) != before || client.RefreshSession.Query().CountX(ctx) != beforeSessions || client.RefreshToken.Query().CountX(ctx) != beforeTokens {
				t.Fatal("failed session issuance left an account, refresh chain or token")
			}
			first := client.User.Query().Where(user.UsernameEQ("email-one")).OnlyX(ctx)
			client.User.UpdateOne(first).SetEmailVerifiedAt(time.Now()).SetEmailVerificationSource("test").SetVerifiedEmailKey(first.Email).SaveX(ctx)
			second := client.User.Query().Where(user.UsernameEQ("email-two")).OnlyX(ctx)
			if _, err := client.User.UpdateOne(second).SetEmailVerifiedAt(time.Now()).SetEmailVerificationSource("test").SetVerifiedEmailKey(second.Email).Save(ctx); !ent.IsConstraintError(err) {
				t.Fatalf("duplicate verified ownership allowed: %v", err)
			}
			if _, err := client.User.Create().SetUsername("invalid-verified").SetEmail("valid@example.com").SetEmailVerifiedAt(time.Now()).Save(ctx); !ent.IsConstraintError(err) {
				t.Fatalf("inconsistent verification accepted: %v", err)
			}
		})
	}
}

func TestPasswordPublishedConfigurationIsolation(t *testing.T) {
	svc, client := newAccountTestAuth(t)
	ctx := context.Background()
	account := createTestUser(t, client, "policy-user")
	session, err := svc.issueSession(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	candidate := client.AuthConnectionRevision.Create().SetConnectionID(PasswordConnectionID).SetRevision(2).SetDisplayName("Draft").SetUsernameLoginEnabled(false).SaveX(ctx)
	if err := client.AuthConnectionRevision.UpdateOneID(candidate.ID).Exec(ctx); err == nil {
		t.Fatal("immutable snapshot updated")
	}
	caps, err := svc.Capabilities(ctx)
	if err != nil || !caps.Password.CanLogin {
		t.Fatalf("unsaved revision changed runtime: %+v %v", caps, err)
	}
	if err := client.AuthConnection.DeleteOneID(PasswordConnectionID).Exec(ctx); err == nil {
		t.Fatal("deleted built-in connection")
	}
	if _, err := client.AuthConnection.Create().SetID("another-password").SetKind(authconnection.KindPassword).Save(ctx); !ent.IsConstraintError(err) {
		t.Fatalf("duplicate password kind allowed: %v", err)
	}
	client.AuthConnection.UpdateOneID(PasswordConnectionID).SetCurrentRevision(2).SaveX(ctx)
	if _, err := svc.Capabilities(ctx); !errors.Is(err, ErrSettingsUnavailable) {
		t.Fatalf("missing current policy accepted: %v", err)
	}
	if _, err := svc.Refresh(ctx, session.RefreshToken); !errors.Is(err, ErrSettingsUnavailable) {
		t.Fatalf("refresh ignored missing policy: %v", err)
	}
	if n := client.RefreshToken.Query().Where(refreshtoken.ConsumedAtIsNil()).CountX(ctx); n != 1 {
		t.Fatal("failed refresh consumed token")
	}
}
