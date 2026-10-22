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
)

// Hold the first completed read of the account, after SQL has released its
// connection but before the service receives its credential snapshot. The
// independent client can then commit a security change before verification.
func pauseCredentialSnapshot(t *testing.T, client *ent.Client, userID int) (<-chan ent.User, func()) {
	t.Helper()
	observed, resume := make(chan ent.User, 1), make(chan struct{})
	var pauseOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(resume) }) }
	client.User.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			value, err := next.Query(ctx, query)
			accounts, ok := value.([]*ent.User)
			if err != nil || !ok || len(accounts) != 1 || accounts[0].ID != userID {
				return value, err
			}
			pauseOnce.Do(func() {
				observed <- *accounts[0]
				select {
				case <-resume:
				case <-ctx.Done():
					err = ctx.Err()
				}
			})
			return value, err
		})
	}))
	return observed, release
}

func TestAuthenticationPasswordSnapshotRaces(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			for _, operation := range []string{"login", "password change"} {
				for _, intervention := range []string{"administrator reset", "disable and re-enable"} {
					t.Run(operation+" after "+intervention, func(t *testing.T) {
						services, clients := concurrentAuthClients(t, driver)
						ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
						defer cancel()
						const originalPassword = "original-password"
						const resetPassword = "administrator-password"
						const staleNewPassword = "stale-new-password"
						account := createPasswordTestUser(t, clients[0], "snapshot-user", originalPassword)
						admin := clients[1].User.Create().SetUsername("snapshot-admin").SetRole(SystemRoleAdmin).SaveX(ctx)
						observed, release := pauseCredentialSnapshot(t, clients[0], account.ID)
						type outcome struct {
							session *Session
							err     error
						}
						results, finished := make(chan outcome, 1), make(chan struct{})
						go func() {
							defer close(finished)
							if operation == "login" {
								session, err := services[0].Login(ctx, LoginInput{Identifier: account.Username, Password: originalPassword})
								results <- outcome{session: session, err: err}
								return
							}
							results <- outcome{err: services[0].ChangePassword(ctx, account.ID, originalPassword, staleNewPassword)}
						}()
						// Also unblock and join the request if an assertion fails before
						// release, before the database clients are cleaned up.
						t.Cleanup(func() {
							release()
							cancel()
							<-finished
						})
						select {
						case snapshot := <-observed:
							if snapshot.ID != account.ID || snapshot.AuthVersion != account.AuthVersion || snapshot.PasswordHash != account.PasswordHash || !snapshot.Active {
								t.Fatal("request did not capture the original active credential snapshot")
							}
						case result := <-results:
							t.Fatalf("request completed before the credential barrier: %v", result.err)
						case <-ctx.Done():
							t.Fatal("request never reached the credential barrier")
						}

						adminService := NewAdminService(clients[1])
						currentPassword := originalPassword
						if intervention == "administrator reset" {
							if err := adminService.ResetPassword(ctx, admin.ID, account.ID, resetPassword); err != nil {
								t.Fatal(err)
							}
							currentPassword = resetPassword
						} else {
							if err := adminService.DisableUser(ctx, admin.ID, account.ID); err != nil {
								t.Fatal(err)
							}
							active := true
							if _, err := adminService.UpdateUser(ctx, admin.ID, account.ID, AdminUpdateUserInput{Active: &active}); err != nil {
								t.Fatal(err)
							}
						}
						committed := clients[1].User.GetX(ctx, account.ID)
						if !committed.Active || committed.AuthVersion != account.AuthVersion+1 {
							t.Fatal("security intervention did not commit exactly one version change")
						}
						if intervention == "disable and re-enable" && committed.PasswordHash != account.PasswordHash {
							t.Fatal("disable/re-enable must isolate version rejection from password or active-state rejection")
						}
						release()

						var result outcome
						select {
						case result = <-results:
						case <-ctx.Done():
							t.Fatal("request did not finish after releasing the credential barrier")
						}
						want := ErrInvalidCredentials
						if operation == "password change" {
							want = ErrAccountConflict
						}
						if !errors.Is(result.err, want) || result.session != nil {
							t.Fatalf("stale snapshot result = %v, session=%t; want %v without a session", result.err, result.session != nil, want)
						}
						stored := clients[1].User.GetX(ctx, account.ID)
						if stored.PasswordHash != committed.PasswordHash || stored.AuthVersion != committed.AuthVersion || !stored.Active || !stored.UpdatedAt.Equal(committed.UpdatedAt) {
							t.Fatal("rejected stale request changed the committed account security state")
						}
						if n := clients[1].RefreshSession.Query().CountX(ctx); n != 0 {
							t.Fatalf("stale credentials created %d refresh chains", n)
						}
						if n := clients[1].RefreshToken.Query().CountX(ctx); n != 0 {
							t.Fatalf("stale credentials created %d refresh tokens", n)
						}
						if n := clients[1].ActivityLog.Query().Where(activitylog.ActionEQ("account.password.changed")).CountX(ctx); n != 0 {
							t.Fatal("rejected password change committed a success audit")
						}
						fresh, err := services[0].Login(ctx, LoginInput{Identifier: account.Username, Password: currentPassword})
						if err != nil || fresh == nil || fresh.User.AuthVersion != committed.AuthVersion {
							t.Fatalf("fresh credentials could not issue the current session: %v", err)
						}
					})
				}
			}
		})
	}
}
