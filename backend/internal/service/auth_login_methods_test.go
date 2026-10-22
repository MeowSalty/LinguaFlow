package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/reauthenticationproof"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
)

func TestLoginMethodRemovalRequiresAlternativeProof(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			now := time.Now().UTC()
			svcs, clients, _ := externalFlowServices(t, driver, now)
			ctx, c := context.Background(), clients[0]
			account := createPasswordTestUser(t, c, "removal-owner", "password-123")
			identity := c.ExternalIdentity.Create().SetUserID(account.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://issuer.example.test").SetSubject("external-subject").SaveX(ctx)
			binding := c.LoginBinding.Create().SetUserID(account.ID).SetConnectionID("flow-oidc").SetExternalIdentityID(identity.ID).SetLastVerifiedAt(now).SetVerifiedAuthGeneration(1).SetVerifiedLoginPolicyRevision(1).SaveX(ctx)
			for _, tc := range []struct {
				method, connection, action, target string
				remove                             func(string) error
			}{
				{"password", PasswordConnectionID, "password_delete", PasswordConnectionID, func(proof string) error { return svcs[0].DeletePassword(ctx, account.ID, proof) }},
				{"oidc", binding.ConnectionID, "unlink", strconv.Itoa(binding.ID), func(proof string) error { return svcs[0].Unlink(ctx, account.ID, binding.ID, proof) }},
			} {
				proof, err := svcs[0].issueReauthenticationProof(ctx, c, account, tc.method, tc.connection, tc.action, tc.target, now)
				if err != nil {
					t.Fatal(err)
				}
				if err = tc.remove(proof.Proof); !errors.Is(err, ErrReauthenticationRequired) {
					t.Fatalf("%s accepted proof from the removed method: %v", tc.action, err)
				}
				current := c.User.GetX(ctx, account.ID)
				if current.AuthVersion != account.AuthVersion || current.PasswordHash != account.PasswordHash || c.LoginBinding.Query().CountX(ctx) != 1 {
					t.Fatal("rejected removal changed credentials or session version")
				}
				if c.ReauthenticationProof.Query().Where(reauthenticationproof.TokenHashEQ(hash.Full(proof.Proof))).OnlyX(ctx).ConsumedAt != nil {
					t.Fatal("rejected removal consumed its proof outside the rolled-back transaction")
				}
			}
		})
	}
}

func TestLoginBindingReleaseAndOldTicketInvalidation(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			now := time.Now().UTC()
			svcs, clients, protocol := externalFlowServices(t, driver, now)
			ctx, c := context.Background(), clients[0]
			seedExternalFlowConnection(t, c, "second-oidc")
			account := createPasswordTestUser(t, c, "former-owner", "password-123")
			identity := c.ExternalIdentity.Create().SetUserID(account.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://issuer.example.test").SetSubject("external-subject").SaveX(ctx)
			var bindingIDs []int
			for _, id := range []string{"flow-oidc", "second-oidc"} {
				bindingIDs = append(bindingIDs, c.LoginBinding.Create().SetUserID(account.ID).SetConnectionID(id).SetExternalIdentityID(identity.ID).SaveX(ctx).ID)
			}
			for i, id := range bindingIDs {
				flowTestStart(t, svcs[0], "second-oidc")
				callback, err := svcs[0].HandleCallback(ctx, "second-oidc", protocol.state, "authorization-code-secret", "")
				if err != nil {
					t.Fatal(err)
				}
				account = c.User.GetX(ctx, account.ID)
				proof, _, err := svcs[0].Reauthenticate(ctx, account.ID, ReauthenticationInput{Method: "password", Password: "password-123", Action: "unlink", TargetID: strconv.Itoa(id)})
				if err != nil {
					t.Fatal(err)
				}
				if err = svcs[0].Unlink(ctx, account.ID, id, proof.Proof); err != nil {
					t.Fatal(err)
				}
				if _, err = svcs[1].Exchange(ctx, nil, ExternalExchangeInput{Ticket: callback.Ticket, Verifier: flowTestVerifier}); !errors.Is(err, ErrExternalFlowInvalid) {
					t.Fatalf("pre-unlink ticket remained usable: %v", err)
				}
				if count := c.ExternalIdentity.Query().CountX(ctx); count != 1-i {
					t.Fatalf("identity count after unlink %d = %d", i, count)
				}
			}
			// A fresh OIDC authorization can assign the now-unbound subject again.
			// The former owner's account and password remain intact.
			_, input := flowTestConfirm(t, svcs[0], protocol, "flow-oidc")
			input.Username = "new-owner"
			result, err := svcs[0].Complete(ctx, nil, input)
			if err != nil {
				t.Fatal(err)
			}
			if result.Session.User.ID == account.ID || c.ExternalIdentity.Query().OnlyX(ctx).UserID != result.Session.User.ID {
				t.Fatal("released external subject retained the former owner")
			}
			if c.User.GetX(ctx, account.ID).PasswordHash != account.PasswordHash {
				t.Fatal("unlink changed the former owner's password")
			}
		})
	}
}
