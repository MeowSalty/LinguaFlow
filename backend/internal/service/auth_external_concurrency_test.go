package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

const flowTestVerifier = "test-browser-verifier-abcdefghijklmnopqrstuvwxyz"

func flowTestStart(t *testing.T, svc *ExternalAuthService, id string) ExternalStartResult {
	t.Helper()
	r, err := svc.StartLogin(context.Background(), id, ExternalBrowserInput{BrowserChallenge: browserChallenge(flowTestVerifier), CompletionURL: "https://app.example.test/auth/complete"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func flowTestConfirm(t *testing.T, svc *ExternalAuthService, protocol *externalFlowTestProtocol, id string) (ExternalStartResult, ExternalCompleteInput) {
	t.Helper()
	start := flowTestStart(t, svc, id)
	cb, err := svc.HandleCallback(context.Background(), id, protocol.state, "authorization-code-secret", "")
	if err != nil {
		t.Fatal(err)
	}
	x, err := svc.Exchange(context.Background(), nil, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: flowTestVerifier})
	if err != nil {
		t.Fatal(err)
	}
	if x.Kind != "signup_confirmation" || x.Confirmation == nil {
		t.Fatalf("unexpected result kind: %s", x.Kind)
	}
	return start, ExternalCompleteInput{ConfirmationToken: x.Confirmation.ConfirmationToken, Verifier: flowTestVerifier, Username: "external-owner"}
}

func raceExternalCalls(calls ...func() error) []error {
	start, results := make(chan struct{}), make(chan error, len(calls))
	for _, call := range calls {
		go func(f func() error) { <-start; results <- f() }(call)
	}
	close(start)
	out := make([]error, len(calls))
	for i := range out {
		out[i] = <-results
	}
	return out
}

func expectOneExternalWinner(t *testing.T, results []error, allowed ...error) {
	t.Helper()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
			continue
		}
		matched := false
		for _, want := range allowed {
			matched = matched || errors.Is(err, want)
		}
		if !matched {
			t.Fatalf("unexpected race error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("race winners=%d, want exactly one", winners)
	}
}

func TestExternalAuthStateMachineConcurrency(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			t.Run("callback cancellation releases network lock and never retries code", func(t *testing.T) {
				svcs, clients, protocol := externalFlowServices(t, driver, time.Now().UTC())
				started := flowTestStart(t, svcs[0], "flow-oidc")
				state := protocol.state
				entered, release := make(chan struct{}, 2), make(chan struct{})
				var calls atomic.Int32
				protocol.exchange = func(ctx context.Context, _ authoidc.Config, _ authoidc.ExchangeRequest) (authoidc.Identity, error) {
					calls.Add(1)
					entered <- struct{}{}
					select {
					case <-release:
					case <-ctx.Done():
						return authoidc.Identity{}, ctx.Err()
					}
					return authoidc.Identity{Issuer: "https://issuer.example.test", Subject: "external-subject"}, nil
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				finished := make(chan error, 1)
				go func() {
					_, err := svcs[0].HandleCallback(ctx, "flow-oidc", state, "authorization-code-secret", "")
					finished <- err
				}()
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("callback did not reach provider")
				}
				if _, err := svcs[1].HandleCallback(ctx, "flow-oidc", state, "authorization-code-secret", ""); !errors.Is(err, ErrExternalFlowInvalid) {
					t.Fatalf("concurrent callback: %v", err)
				}
				err := svcs[1].Cancel(ctx, nil, ExternalCancelInput{TransactionID: started.TransactionID, Verifier: flowTestVerifier})
				close(release)
				if err != nil {
					t.Fatalf("cancel blocked by provider call: %v", err)
				}
				if err := <-finished; !errors.Is(err, ErrExternalFlowInvalid) {
					t.Fatalf("cancelled callback published ticket: %v", err)
				}
				if calls.Load() != 1 || clients[0].AuthTransaction.GetX(ctx, started.TransactionID).Status != authtransaction.StatusCancelled {
					t.Fatal("callback retried or cancellation lost")
				}
			})
			t.Run("complete replay and username retry", func(t *testing.T) {
				svcs, clients, protocol := externalFlowServices(t, driver, time.Now().UTC())
				ctx := context.Background()
				createPasswordTestUser(t, clients[0], "taken-name", "password-123")
				_, input := flowTestConfirm(t, svcs[0], protocol, "flow-oidc")
				if clients[0].ExternalIdentity.Query().CountX(ctx) != 0 {
					t.Fatal("exchange created an identity")
				}
				input.Username = "taken-name"
				if _, err := svcs[0].Complete(ctx, nil, input); !errors.Is(err, ErrUserExists) {
					t.Fatalf("duplicate username: %v", err)
				}
				input.Username = "external-owner"
				expectOneExternalWinner(t, raceExternalCalls(func() error { _, err := svcs[0].Complete(ctx, nil, input); return err }, func() error { _, err := svcs[1].Complete(ctx, nil, input); return err }), ErrExternalFlowInvalid)
				if _, err := svcs[1].Complete(ctx, nil, input); !errors.Is(err, ErrExternalFlowInvalid) {
					t.Fatalf("lost response retry returned secrets: %v", err)
				}
				if clients[0].User.Query().Where(user.UsernameEQ(input.Username)).CountX(ctx) != 1 || clients[0].LoginBinding.Query().CountX(ctx) != 1 || clients[0].RefreshSession.Query().CountX(ctx) != 1 {
					t.Fatal("complete duplicated account, binding or session")
				}
			})
			t.Run("complete versus cancel rolls back loser", func(t *testing.T) {
				svcs, clients, protocol := externalFlowServices(t, driver, time.Now().UTC())
				ctx := context.Background()
				start, input := flowTestConfirm(t, svcs[0], protocol, "flow-oidc")
				expectOneExternalWinner(t, raceExternalCalls(func() error { _, err := svcs[0].Complete(ctx, nil, input); return err }, func() error {
					return svcs[1].Cancel(ctx, nil, ExternalCancelInput{TransactionID: start.TransactionID, Verifier: input.Verifier})
				}), ErrExternalFlowInvalid)
				row := clients[0].AuthTransaction.GetX(ctx, start.TransactionID)
				want := 0
				if row.Status == authtransaction.StatusCompleted {
					want = 1
				}
				if clients[0].User.Query().CountX(ctx) != want || clients[0].ExternalIdentity.Query().CountX(ctx) != want || clients[0].RefreshSession.Query().CountX(ctx) != want {
					t.Fatal("cancel/complete left partial account state")
				}
			})
			t.Run("same subject across connections never creates two owners", func(t *testing.T) {
				svcs, clients, protocol := externalFlowServices(t, driver, time.Now().UTC())
				ctx := context.Background()
				seedExternalFlowConnection(t, clients[0], "second-oidc")
				_, first := flowTestConfirm(t, svcs[0], protocol, "flow-oidc")
				_, second := flowTestConfirm(t, svcs[1], protocol, "second-oidc")
				second.Username = "second-owner"
				expectOneExternalWinner(t, raceExternalCalls(func() error { _, err := svcs[0].Complete(ctx, nil, first); return err }, func() error { _, err := svcs[1].Complete(ctx, nil, second); return err }), ErrAuthConnectionConflict)
				if clients[0].User.Query().CountX(ctx) != 1 || clients[0].ExternalIdentity.Query().CountX(ctx) != 1 || clients[0].LoginBinding.Query().CountX(ctx) != 1 {
					t.Fatal("subject has duplicate ownership")
				}
			})
			t.Run("two unlinks retain a maintenance method", func(t *testing.T) {
				svcs, clients, _ := externalFlowServices(t, driver, time.Now().UTC())
				ctx := context.Background()
				seedExternalFlowConnection(t, clients[0], "second-oidc")
				account := clients[0].User.Create().SetUsername("unlink-owner").SaveX(ctx)
				identity := clients[0].ExternalIdentity.Create().SetUserID(account.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://issuer.example.test").SetSubject("external-subject").SaveX(ctx)
				var bindings [2]*ent.LoginBinding
				var proofs [2]ReauthenticationResult
				for i, id := range []string{"flow-oidc", "second-oidc"} {
					bindings[i] = clients[0].LoginBinding.Create().SetConnectionID(id).SetUserID(account.ID).SetExternalIdentityID(identity.ID).SetVerifiedAuthGeneration(1).SetVerifiedLoginPolicyRevision(1).SetLastVerifiedAt(time.Now()).SaveX(ctx)
					var err error
					proofs[i], err = svcs[0].issueReauthenticationProof(ctx, clients[0], account, "oidc", []string{"second-oidc", "flow-oidc"}[i], "unlink", strconv.Itoa(bindings[i].ID), time.Now())
					if err != nil {
						t.Fatal(err)
					}
				}
				expectOneExternalWinner(t, raceExternalCalls(func() error { return svcs[0].Unlink(ctx, account.ID, bindings[0].ID, proofs[0].Proof) }, func() error { return svcs[1].Unlink(ctx, account.ID, bindings[1].ID, proofs[1].Proof) }), ErrReauthenticationRequired, ErrLoginMethodConflict)
				if clients[0].LoginBinding.Query().CountX(ctx) != 1 || clients[0].ExternalIdentity.Query().CountX(ctx) != 1 {
					t.Fatal("last usable login method removed")
				}
			})
			t.Run("first password set has one winner", func(t *testing.T) {
				svcs, clients, _ := externalFlowServices(t, driver, time.Now().UTC())
				ctx := context.Background()
				account := clients[0].User.Create().SetUsername("set-password-owner").SaveX(ctx)
				proof, err := svcs[0].issueReauthenticationProof(ctx, clients[0], account, "oidc", "flow-oidc", "password_set", PasswordConnectionID, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				expectOneExternalWinner(t, raceExternalCalls(func() error { return svcs[0].SetInitialPassword(ctx, account.ID, "first-password-123", proof.Proof) }, func() error { return svcs[1].SetInitialPassword(ctx, account.ID, "second-password-123", proof.Proof) }), ErrReauthenticationRequired, ErrLoginMethodConflict)
				current := clients[0].User.GetX(ctx, account.ID)
				if current.PasswordHash == "" || current.AuthVersion != account.AuthVersion+1 || strings.Contains(current.PasswordHash, "password-123") {
					t.Fatal("password race failed to atomically update credentials and generation")
				}
			})
		})
	}
}
