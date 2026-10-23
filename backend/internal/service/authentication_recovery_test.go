package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authenticationrecovery"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/instanceinitialization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

func newAuthenticationRecoveryConcurrencyFixture(t *testing.T, driver string) ([2]*ent.Client, [2]*DeploymentRecoveryService, *ent.User) {
	t.Helper()
	_, clients := concurrentAuthClients(t, driver)
	ctx := context.Background()
	admin := clients[0].User.Create().SetUsername("recovery-admin").SetPasswordHash("unused").SetRole(SystemRoleAdmin).SetActive(true).SaveX(ctx)
	closeRecoveryLocalLogin(t, clients[0])
	var services [2]*DeploymentRecoveryService
	for i := range clients {
		services[i] = NewDeploymentRecoveryService(clients[i])
	}
	return clients, services, admin
}

func TestAuthenticationRecoverySingleUseRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			clients, services, admin := newAuthenticationRecoveryConcurrencyFixture(t, driver)
			ctx := context.Background()
			issued, err := services[0].Issue(ctx, DeploymentRecoveryIssueInput{Username: admin.Username})
			if err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				err error
			}
			results := make(chan outcome, 2)
			var wg sync.WaitGroup
			for _, svc := range services {
				wg.Add(1)
				go func(svc *DeploymentRecoveryService) {
					defer wg.Done()
					err := svc.RestoreLocalLogin(ctx, DeploymentRecoveryRestoreInput{Token: issued.Token, Password: "new-password-123"})
					results <- outcome{err: err}
				}(svc)
			}
			wg.Wait()
			close(results)
			successes, invalid := 0, 0
			for result := range results {
				if result.err == nil {
					successes++
				} else if errors.Is(result.err, ErrAuthenticationRecoveryInvalid) {
					invalid++
				} else {
					t.Fatalf("unexpected restore error: %v", result.err)
				}
			}
			if successes != 1 || invalid != 1 {
				t.Fatalf("single-use race produced successes=%d invalid=%d", successes, invalid)
			}
			p, err := readPasswordPolicy(ctx, clients[0])
			if err != nil {
				t.Fatal(err)
			}
			if !p.Local.PasswordLoginEnabled || !p.Local.UsernameLoginEnabled || p.Local.SignupEnabled {
				t.Fatalf("race left wrong policy: %+v", p.Local)
			}
			target := clients[0].User.GetX(ctx, admin.ID)
			if target.AuthVersion != admin.AuthVersion+1 || target.PasswordVerifiedHash != "" || target.PasswordVerifiedAt != nil {
				t.Fatalf("race left wrong target state: %+v", target)
			}
			if got := clients[0].AuthenticationRecovery.Query().Where(
				authenticationrecovery.UserIDEQ(admin.ID),
				authenticationrecovery.ConsumedAtNotNil(),
			).CountX(ctx); got != 1 {
				t.Fatalf("token consumed %d times", got)
			}
			if err := services[0].Revoke(ctx, issued.Token); !errors.Is(err, ErrAuthenticationRecoveryInvalid) {
				t.Fatalf("consumed token revoke was accepted: %v", err)
			}
		})
	}
}

func TestAuthenticationRecoveryRejectsStaleAuthorization(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			clients, services, admin := newAuthenticationRecoveryConcurrencyFixture(t, driver)
			ctx := context.Background()
			issued, err := services[0].Issue(ctx, DeploymentRecoveryIssueInput{Username: admin.Username})
			if err != nil {
				t.Fatal(err)
			}
			p, err := readPasswordPolicy(ctx, clients[0])
			if err != nil {
				t.Fatal(err)
			}
			changed := localAccountInput(p.Local)
			changed.PasswordLoginEnabled = true
			changed.UsernameLoginEnabled = true
			changed.Signup.Enabled = false
			if err := writeLocalAccountPolicy(ctx, clients[0], p, changed, timeutil.Normalize(timeutil.NowUTC())); err != nil {
				t.Fatal(err)
			}
			if err := services[1].RestoreLocalLogin(ctx, DeploymentRecoveryRestoreInput{Token: issued.Token, Password: "new-password-123"}); !errors.Is(err, ErrAuthenticationRecoveryConflict) {
				t.Fatalf("changed revision was accepted: %v", err)
			}
			if err := services[1].Revoke(ctx, issued.Token); !errors.Is(err, ErrAuthenticationRecoveryConflict) {
				t.Fatalf("changed revision revoke was accepted: %v", err)
			}
		})
	}
}

func TestAuthenticationRecoveryRejectsExpiredRevokedAndTargetChanges(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			t.Run("expired-restore", func(t *testing.T) {
				clients, services, admin := newAuthenticationRecoveryConcurrencyFixture(t, driver)
				services[0].now = func() time.Time {
					return timeutil.Normalize(timeutil.NowUTC().Add(-6 * time.Minute))
				}
				issued, err := services[0].Issue(context.Background(), DeploymentRecoveryIssueInput{Username: admin.Username})
				if err != nil {
					t.Fatal(err)
				}
				services[0].now = timeutil.NowUTC
				if err := services[1].RestoreLocalLogin(context.Background(), DeploymentRecoveryRestoreInput{Token: issued.Token, Password: "new-password-123"}); !errors.Is(err, ErrAuthenticationRecoveryInvalid) {
					t.Fatalf("expired token was accepted: %v", err)
				}
				_ = clients
			})
			t.Run("expired-revoke", func(t *testing.T) {
				_, services, admin := newAuthenticationRecoveryConcurrencyFixture(t, driver)
				services[0].now = func() time.Time {
					return timeutil.Normalize(timeutil.NowUTC().Add(-6 * time.Minute))
				}
				issued, err := services[0].Issue(context.Background(), DeploymentRecoveryIssueInput{Username: admin.Username})
				if err != nil {
					t.Fatal(err)
				}
				services[0].now = timeutil.NowUTC
				if err := services[1].Revoke(context.Background(), issued.Token); !errors.Is(err, ErrAuthenticationRecoveryInvalid) {
					t.Fatalf("expired revoke was accepted: %v", err)
				}
			})
			t.Run("role-change", func(t *testing.T) {
				clients, services, admin := newAuthenticationRecoveryConcurrencyFixture(t, driver)
				issued, err := services[0].Issue(context.Background(), DeploymentRecoveryIssueInput{Username: admin.Username})
				if err != nil {
					t.Fatal(err)
				}
				clients[0].User.UpdateOneID(admin.ID).SetRole(SystemRoleUser).SaveX(context.Background())
				if err := services[1].Revoke(context.Background(), issued.Token); !errors.Is(err, ErrMissingAdministrator) {
					t.Fatalf("non-admin revoke was accepted: %v", err)
				}
			})
			t.Run("inactive", func(t *testing.T) {
				clients, services, admin := newAuthenticationRecoveryConcurrencyFixture(t, driver)
				issued, err := services[0].Issue(context.Background(), DeploymentRecoveryIssueInput{Username: admin.Username})
				if err != nil {
					t.Fatal(err)
				}
				clients[0].User.UpdateOneID(admin.ID).SetActive(false).SaveX(context.Background())
				if err := services[1].Revoke(context.Background(), issued.Token); !errors.Is(err, ErrMissingAdministrator) {
					t.Fatalf("inactive revoke was accepted: %v", err)
				}
			})
		})
	}
}

func TestAuthenticationRecoveryAuditRollback(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			clients, services, admin := newAuthenticationRecoveryConcurrencyFixture(t, driver)
			ctx := context.Background()
			issued, err := services[0].Issue(ctx, DeploymentRecoveryIssueInput{Username: admin.Username})
			if err != nil {
				t.Fatal(err)
			}
			before := clients[0].User.GetX(ctx, admin.ID)
			policyBefore, err := readPasswordPolicy(ctx, clients[0])
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("recovery audit failure")
			clients[0].ActivityLog.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
					return nil, failure
				})
			})
			if err := services[0].RestoreLocalLogin(ctx, DeploymentRecoveryRestoreInput{Token: issued.Token, Password: "new-password-123"}); !errors.Is(err, failure) {
				t.Fatalf("audit failure was transformed: %v", err)
			}
			after := clients[0].User.GetX(ctx, admin.ID)
			policyAfter, err := readPasswordPolicy(ctx, clients[0])
			if err != nil {
				t.Fatal(err)
			}
			if after.AuthVersion != before.AuthVersion || after.PasswordHash != before.PasswordHash || after.PasswordVerifiedHash != before.PasswordVerifiedHash {
				t.Fatalf("audit failure left target changes: before=%+v after=%+v", before, after)
			}
			if policyAfter.Local.Revision != policyBefore.Local.Revision || policyAfter.Local.PasswordLoginEnabled || policyAfter.Local.UsernameLoginEnabled || policyAfter.Local.SignupEnabled {
				t.Fatalf("audit failure left policy changes: before=%+v after=%+v", policyBefore.Local, policyAfter.Local)
			}
			row := clients[0].AuthenticationRecovery.Query().Where(
				authenticationrecovery.TokenHashEQ(hash.Full(deploymentRecoveryTokenString(issued.Token))),
			).OnlyX(ctx)
			if row.ConsumedAt != nil || row.RevokedAt != nil {
				t.Fatalf("audit failure consumed authorization: %+v", row)
			}
		})
	}
}

func newAuthenticationRecoveryFixture(t *testing.T) (*ent.Client, *DeploymentRecoveryService, *ent.User) {
	t.Helper()
	ctx := context.Background()
	client := testClient(t)
	if err := initializePasswordConnection(ctx, client); err != nil {
		t.Fatal(err)
	}
	client.SystemSetting.Create().SetKey(SettingRegistrationEnabled).SetValue("true").SaveX(ctx)
	client.InstanceInitialization.Create().SetID(1).SetVersion(InitializationVersion).SetDataVersion(CurrentDataVersion).SetMode(instanceinitialization.ModeServe).SaveX(ctx)
	admin := client.User.Create().SetUsername("recovery-admin").SetPasswordHash("unused").SetPasswordVerifiedHash(hash.Full("unused")).SetPasswordVerifiedAt(timeutil.NowUTC()).SetRole(SystemRoleAdmin).SetActive(true).SaveX(ctx)
	return client, NewDeploymentRecoveryService(client), admin
}

func closeRecoveryLocalLogin(t *testing.T, client *ent.Client) {
	t.Helper()
	ctx := context.Background()
	p, err := readPasswordPolicy(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	next := localAccountInput(p.Local)
	next.PasswordLoginEnabled = false
	next.UsernameLoginEnabled = false
	next.Signup.Enabled = false
	next.PasswordEnrollmentEnabled = false
	if err := writeLocalAccountPolicy(ctx, client, p, next, timeutil.Normalize(timeutil.NowUTC())); err != nil {
		t.Fatal(err)
	}
}

func requireAuthenticationRecoveryAudit(t *testing.T, client *ent.Client, action string, targetID int, revision, version int64) {
	t.Helper()
	row := client.ActivityLog.Query().Where(
		activitylog.ActionEQ(action),
		activitylog.ResourceTypeEQ("local_account"),
	).OnlyX(context.Background())
	if row.ResourceID == nil || *row.ResourceID != 1 {
		t.Fatalf("%s audit did not target local account: %+v", action, row)
	}
	if row.Metadata["target_user_id"] != float64(targetID) ||
		row.Metadata["local_policy_revision"] != float64(revision) ||
		row.Metadata["auth_version"] != float64(version) {
		t.Fatalf("%s audit lost recovery metadata: %+v", action, row.Metadata)
	}
}

func TestAuthenticationRecoveryIssueAndRestore(t *testing.T) {
	client, svc, admin := newAuthenticationRecoveryFixture(t)
	ctx := context.Background()
	closeRecoveryLocalLogin(t, client)
	issued, err := svc.Issue(ctx, DeploymentRecoveryIssueInput{Username: admin.Username})
	if err != nil {
		t.Fatal(err)
	}
	if issued.UserID != admin.ID || len(issued.Token) != 32 {
		t.Fatalf("bad issue result: %+v", issued)
	}
	row := client.AuthenticationRecovery.Query().Where(
		authenticationrecovery.TokenHashEQ(hash.Full(deploymentRecoveryTokenString(issued.Token))),
	).OnlyX(ctx)
	if row.UserID != admin.ID || row.AuthVersion != admin.AuthVersion || row.ConsumedAt != nil || row.RevokedAt != nil {
		t.Fatalf("bad issued row: %+v", row)
	}
	current, err := readPasswordPolicy(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	if current.Local.PasswordLoginEnabled || current.Local.UsernameLoginEnabled || current.Local.SignupEnabled {
		t.Fatal("issue changed local policy")
	}
	requireAuthenticationRecoveryAudit(t, client, "admin.authentication_recovery.issued", admin.ID, row.LocalPolicyRevision, row.AuthVersion)
	if err := svc.RestoreLocalLogin(ctx, DeploymentRecoveryRestoreInput{Token: issued.Token, Password: "new-password-123"}); err != nil {
		t.Fatal(err)
	}
	restored, err := readPasswordPolicy(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.Local.PasswordLoginEnabled || !restored.Local.UsernameLoginEnabled || restored.Local.SignupEnabled {
		t.Fatalf("restore did not set exact local boolean contract: %+v", restored.Local)
	}
	target := client.User.GetX(ctx, admin.ID)
	if target.PasswordVerifiedHash != "" || target.PasswordVerifiedAt != nil {
		t.Fatal("restore retained verified password evidence for a changed password")
	}
	if target.AuthVersion != admin.AuthVersion+1 {
		t.Fatalf("restore did not revoke target sessions: %d -> %d", admin.AuthVersion, target.AuthVersion)
	}
	requireAuthenticationRecoveryAudit(t, client, "admin.authentication_recovery.restored", admin.ID, row.LocalPolicyRevision, row.AuthVersion)
	if err := svc.RestoreLocalLogin(ctx, DeploymentRecoveryRestoreInput{Token: issued.Token, Password: "new-password-123"}); !errors.Is(err, ErrAuthenticationRecoveryInvalid) {
		t.Fatalf("replay was accepted: %v", err)
	}
	if err := svc.Revoke(ctx, issued.Token); !errors.Is(err, ErrAuthenticationRecoveryInvalid) {
		t.Fatalf("consumed token revoke was accepted: %v", err)
	}
}

func TestAuthenticationRecoveryRevoke(t *testing.T) {
	client, svc, admin := newAuthenticationRecoveryFixture(t)
	ctx := context.Background()
	closeRecoveryLocalLogin(t, client)
	issued, err := svc.Issue(ctx, DeploymentRecoveryIssueInput{Username: admin.Username})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Revoke(ctx, issued.Token); err != nil {
		t.Fatal(err)
	}
	row := client.AuthenticationRecovery.Query().Where(
		authenticationrecovery.TokenHashEQ(hash.Full(deploymentRecoveryTokenString(issued.Token))),
	).OnlyX(ctx)
	if row.RevokedAt == nil {
		t.Fatal("token was not revoked")
	}
	requireAuthenticationRecoveryAudit(t, client, "admin.authentication_recovery.revoked", admin.ID, row.LocalPolicyRevision, row.AuthVersion)
	if err := svc.RestoreLocalLogin(ctx, DeploymentRecoveryRestoreInput{Token: issued.Token, Password: "new-password-123"}); !errors.Is(err, ErrAuthenticationRecoveryInvalid) {
		t.Fatalf("revoked token was accepted: %v", err)
	}
	if err := svc.Revoke(ctx, issued.Token); !errors.Is(err, ErrAuthenticationRecoveryInvalid) {
		t.Fatalf("revoked token revoke was accepted: %v", err)
	}
}

func TestAuthenticationRecoveryRejectsStaleAndInvalidTargets(t *testing.T) {
	client, svc, admin := newAuthenticationRecoveryFixture(t)
	ctx := context.Background()
	closeRecoveryLocalLogin(t, client)
	issued, err := svc.Issue(ctx, DeploymentRecoveryIssueInput{Username: admin.Username})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreLocalLogin(ctx, DeploymentRecoveryRestoreInput{Token: issued.Token[:31], Password: "new-password-123"}); !errors.Is(err, ErrAuthenticationRecoveryInvalid) {
		t.Fatalf("malformed token was accepted: %v", err)
	}
	past := timeutil.Normalize(timeutil.NowUTC().Add(-10 * time.Minute))
	client.User.Create().SetUsername("expired-admin").SetPasswordHash("unused").SetRole(SystemRoleAdmin).SetActive(true).SaveX(ctx)
	svc.now = func() time.Time { return past }
	expired, err := svc.Issue(ctx, DeploymentRecoveryIssueInput{Username: "expired-admin"})
	if err != nil {
		t.Fatal(err)
	}
	svc.now = timeutil.NowUTC
	if err := svc.RestoreLocalLogin(ctx, DeploymentRecoveryRestoreInput{Token: expired.Token, Password: "new-password-123"}); !errors.Is(err, ErrAuthenticationRecoveryInvalid) {
		t.Fatalf("expired token was accepted: %v", err)
	}
	if err := svc.Revoke(ctx, expired.Token); !errors.Is(err, ErrAuthenticationRecoveryInvalid) {
		t.Fatalf("expired token revoke was accepted: %v", err)
	}
	client.User.UpdateOneID(admin.ID).SetRole(SystemRoleUser).SaveX(ctx)
	if err := svc.Revoke(ctx, issued.Token); !errors.Is(err, ErrMissingAdministrator) {
		t.Fatalf("non-admin target was accepted: %v", err)
	}
}

func TestAuthenticationRecoveryRejectsTargetAndAuthorizationChanges(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			clients, services, admin := newAuthenticationRecoveryConcurrencyFixture(t, driver)
			ctx := context.Background()
			issued, err := services[0].Issue(ctx, DeploymentRecoveryIssueInput{Username: admin.Username})
			if err != nil {
				t.Fatal(err)
			}
			p, err := readPasswordPolicy(ctx, clients[0])
			if err != nil {
				t.Fatal(err)
			}
			changed := localAccountInput(p.Local)
			if changed.Signup.EmailRequirement == "optional" {
				changed.Signup.EmailRequirement = "required"
			} else {
				changed.Signup.EmailRequirement = "optional"
			}
			if err := writeLocalAccountPolicy(ctx, clients[0], p, changed, timeutil.Normalize(timeutil.NowUTC())); err != nil {
				t.Fatal(err)
			}
			if err := services[1].RestoreLocalLogin(ctx, DeploymentRecoveryRestoreInput{Token: issued.Token, Password: "new-password-123"}); !errors.Is(err, ErrAuthenticationRecoveryConflict) {
				t.Fatalf("changed policy revision was accepted: %v", err)
			}
			if err := services[1].Revoke(ctx, issued.Token); !errors.Is(err, ErrAuthenticationRecoveryConflict) {
				t.Fatalf("changed policy revision revoke was accepted: %v", err)
			}
		})
	}
}

func TestAuthenticationRecoveryRejectsTargetAuthorizationChanges(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			clients, services, admin := newAuthenticationRecoveryConcurrencyFixture(t, driver)
			issued, err := services[0].Issue(context.Background(), DeploymentRecoveryIssueInput{Username: admin.Username})
			if err != nil {
				t.Fatal(err)
			}
			clients[0].User.UpdateOneID(admin.ID).AddAuthVersion(1).SaveX(context.Background())
			if err := services[1].RestoreLocalLogin(context.Background(), DeploymentRecoveryRestoreInput{Token: issued.Token, Password: "new-password-123"}); !errors.Is(err, ErrAuthenticationRecoveryConflict) {
				t.Fatalf("changed auth version restore was accepted: %v", err)
			}
			if err := services[1].Revoke(context.Background(), issued.Token); !errors.Is(err, ErrAuthenticationRecoveryConflict) {
				t.Fatalf("changed auth version revoke was accepted: %v", err)
			}
		})
	}
}
