package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

func TestAuthenticationIdentityModel(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			t.Run("external principal belongs to one global owner", func(t *testing.T) {
				_, clients := concurrentAuthClients(t, driver)
				client, ctx := clients[0], t.Context()
				owner := client.User.Create().SetUsername("identity-owner").SaveX(ctx)
				other := client.User.Create().SetUsername("identity-other").SaveX(ctx)
				original := client.ExternalIdentity.Create().SetUserID(owner.ID).
					SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://issuer.example").SetSubject("subject-one").SaveX(ctx)

				for _, ownerID := range []int{owner.ID, other.ID} {
					_, err := clients[1].ExternalIdentity.Create().SetUserID(ownerID).
						SetProtocol(original.Protocol).SetAuthority(original.Authority).SetSubject(original.Subject).Save(ctx)
					if !ent.IsConstraintError(err) {
						t.Fatalf("duplicate principal with owner %d: %v, want constraint error", ownerID, err)
					}
				}
				if n := client.ExternalIdentity.Query().CountX(ctx); n != 1 {
					t.Fatalf("rejected ownership claim left %d identities, want 1", n)
				}
				if stored := client.ExternalIdentity.GetX(ctx, original.ID); stored.UserID != owner.ID {
					t.Fatalf("duplicate principal changed its owner to %d", stored.UserID)
				}

				// All three namespace components participate in the unique key.
				for _, principal := range []struct {
					protocol           externalidentity.Protocol
					authority, subject string
				}{
					{externalidentity.ProtocolOauth2, original.Authority, original.Subject},
					{original.Protocol, "https://another-issuer.example", original.Subject},
					{original.Protocol, original.Authority, "subject-two"},
				} {
					if _, err := clients[1].ExternalIdentity.Create().SetUserID(other.ID).
						SetProtocol(principal.protocol).SetAuthority(principal.authority).SetSubject(principal.subject).Save(ctx); err != nil {
						t.Fatalf("independent principal was merged or rejected: %v", err)
					}
				}
				if n := client.ExternalIdentity.Query().CountX(ctx); n != 4 {
					t.Fatalf("independent principal count = %d, want 4", n)
				}
			})

			t.Run("bindings preserve ownership and explicit connection scope", func(t *testing.T) {
				_, clients := concurrentAuthClients(t, driver)
				client, ctx := clients[0], t.Context()
				owner := client.User.Create().SetUsername("binding-owner").SaveX(ctx)
				other := client.User.Create().SetUsername("binding-other").SaveX(ctx)
				first := client.AuthConnection.Create().SetID("identity-first").SetKind(authconnection.KindOidc).SaveX(ctx)
				second := client.AuthConnection.Create().SetID("identity-second").SetKind(authconnection.KindOidc).SaveX(ctx)
				identity := client.ExternalIdentity.Create().SetUserID(owner.ID).
					SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://issuer.example").SetSubject("subject-one").SaveX(ctx)
				alternative := client.ExternalIdentity.Create().SetUserID(owner.ID).
					SetProtocol(identity.Protocol).SetAuthority(identity.Authority).SetSubject("subject-two").SaveX(ctx)

				if _, err := clients[1].LoginBinding.Create().SetUserID(other.ID).
					SetConnectionID(first.ID).SetExternalIdentityID(identity.ID).Save(ctx); err == nil {
					t.Fatal("another user claimed the external identity through a binding")
				}
				if n := client.LoginBinding.Query().CountX(ctx); n != 0 {
					t.Fatalf("rejected owner mismatch persisted %d bindings", n)
				}

				binding := client.LoginBinding.Create().SetUserID(owner.ID).
					SetConnectionID(first.ID).SetExternalIdentityID(identity.ID).SaveX(ctx)
				if _, err := clients[1].LoginBinding.Create().SetUserID(owner.ID).
					SetConnectionID(first.ID).SetExternalIdentityID(alternative.ID).Save(ctx); !ent.IsConstraintError(err) {
					t.Fatalf("second subject for the same user and connection: %v, want constraint error", err)
				}
				if n := client.LoginBinding.Query().CountX(ctx); n != 1 {
					t.Fatalf("duplicate connection binding left %d bindings, want 1", n)
				}
				if stored := client.LoginBinding.GetX(ctx, binding.ID); stored.ExternalIdentityID != identity.ID {
					t.Fatal("duplicate connection binding replaced the original subject")
				}

				shared, err := clients[1].LoginBinding.Create().SetUserID(owner.ID).
					SetConnectionID(second.ID).SetExternalIdentityID(identity.ID).Save(ctx)
				if err != nil {
					t.Fatalf("explicit binding of the same principal to another connection: %v", err)
				}
				if shared.ID == binding.ID || shared.ConnectionID == binding.ConnectionID || shared.UserID != binding.UserID || shared.ExternalIdentityID != binding.ExternalIdentityID {
					t.Fatal("separate connection did not retain a distinct binding to the same owned identity")
				}
				if n := client.LoginBinding.Query().CountX(ctx); n != 2 {
					t.Fatalf("explicit connections have %d bindings, want 2", n)
				}
			})

			t.Run("ORM accepts only immutable canonical usernames", func(t *testing.T) {
				_, clients := concurrentAuthClients(t, driver)
				client, ctx := clients[0], t.Context()
				for _, name := range []string{"Alice", " alice ", "alice@example.com", "ab", "alice--one", "alice_", "\talice", "爱丽丝"} {
					if _, err := client.User.Create().SetUsername(name).Save(ctx); !ent.IsValidationError(err) {
						t.Fatalf("noncanonical username %q: %v, want validation error", name, err)
					}
				}
				if n := client.User.Query().CountX(ctx); n != 0 {
					t.Fatalf("invalid ORM names persisted %d users", n)
				}
				account := client.User.Create().SetUsername("alice").SaveX(ctx)

				// Generic mutations can carry fields absent from the typed update
				// builder. Neither update path may persist an immutable rename.
				one := client.User.UpdateOneID(account.ID).SetDisplayName("single update")
				one.Mutation().SetUsername("renamed-one")
				if _, err := one.Save(ctx); err != nil {
					t.Fatal(err)
				}
				if stored := clients[1].User.GetX(ctx, account.ID); stored.Username != "alice" || stored.DisplayName != "single update" {
					t.Fatalf("single ORM update changed immutable username: username=%q display_name=%q", stored.Username, stored.DisplayName)
				}
				many := client.User.Update().Where(user.IDEQ(account.ID)).SetDisplayName("bulk update")
				many.Mutation().SetUsername("renamed-many")
				if n, err := many.Save(ctx); err != nil || n != 1 {
					t.Fatalf("bulk update = %d, %v", n, err)
				}
				if stored := clients[1].User.GetX(ctx, account.ID); stored.Username != "alice" || stored.DisplayName != "bulk update" {
					t.Fatalf("bulk ORM update changed immutable username: username=%q display_name=%q", stored.Username, stored.DisplayName)
				}
			})
		})
	}
}

func TestAuthenticationIdentityUsernameRegistrationConcurrency(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			services, clients := concurrentAuthClients(t, driver)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			type outcome struct {
				input   RegisterInput
				session *Session
				err     error
			}
			inputs := []RegisterInput{
				{Username: "Alice", Password: "first-password"},
				{Username: "alice", Password: "second-password"},
			}
			start, results := make(chan struct{}), make(chan outcome, len(inputs))
			for i, input := range inputs {
				go func(svc *AuthService, input RegisterInput) {
					<-start
					session, err := svc.Register(ctx, input)
					results <- outcome{input: input, session: session, err: err}
				}(services[i], input)
			}
			close(start)
			var winner, loser outcome
			successes, conflicts := 0, 0
			for range inputs {
				result := <-results
				if result.err == nil {
					successes++
					winner = result
				} else if errors.Is(result.err, ErrUserExists) {
					conflicts++
					loser = result
				} else {
					t.Fatalf("registration for %q failed unexpectedly: %v", result.input.Username, result.err)
				}
			}
			if successes != 1 || conflicts != 1 || winner.session == nil || loser.session != nil {
				t.Fatalf("concurrent registration: successes=%d conflicts=%d winner_session=%t loser_session=%t", successes, conflicts, winner.session != nil, loser.session != nil)
			}
			client := clients[0]
			if n := client.User.Query().CountX(ctx); n != 1 {
				t.Fatalf("case variants created %d users, want 1", n)
			}
			account := client.User.Query().OnlyX(ctx)
			if account.Username != "alice" || winner.session.User.ID != account.ID || account.AuthVersion != 1 {
				t.Fatalf("wrong canonical winner: id=%d username=%q auth_version=%d", account.ID, account.Username, account.AuthVersion)
			}
			if comparePassword(account.PasswordHash, winner.input.Password) != nil || comparePassword(account.PasswordHash, loser.input.Password) == nil {
				t.Fatal("losing registration replaced the winning account credential")
			}
			if n := client.RefreshSession.Query().CountX(ctx); n != 1 {
				t.Fatalf("registrations left %d refresh chains, want 1", n)
			}
			if n := client.RefreshToken.Query().CountX(ctx); n != 1 {
				t.Fatalf("registrations left %d refresh tokens, want 1", n)
			}
			if _, _, err := services[1].ResolveUserFromAccessToken(ctx, winner.session.AccessToken); err != nil {
				t.Fatalf("committed registration access token unusable from another connection: %v", err)
			}
		})
	}
}
