package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/reauthenticationproof"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
)

type externalFlowTestProtocol struct {
	state    string
	exchange func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error)
}

func (p *externalFlowTestProtocol) Check(context.Context, authoidc.Config) (authoidc.ProviderMetadata, error) {
	return authoidc.ProviderMetadata{Issuer: "https://issuer.example.test", SupportsPKCES256: true}, nil
}
func (p *externalFlowTestProtocol) BuildAuthorization(_ context.Context, _ authoidc.Config, in authoidc.AuthorizationRequest) (authoidc.Authorization, error) {
	p.state = in.State
	return authoidc.Authorization{URL: "https://issuer.example.test/authorize"}, nil
}
func (p *externalFlowTestProtocol) ExchangeAndVerify(ctx context.Context, cfg authoidc.Config, in authoidc.ExchangeRequest) (authoidc.Identity, error) {
	if p.exchange != nil {
		return p.exchange(ctx, cfg, in)
	}
	if in.Code != "authorization-code-secret" || in.CodeVerifier == "" || in.ExpectedNonce == "" {
		return authoidc.Identity{}, errors.New("bad exchange request")
	}
	return authoidc.Identity{Issuer: "https://issuer.example.test", Subject: "external-subject", DisplayName: "External User"}, nil
}

func externalFlowServices(t *testing.T, driver string, now time.Time) ([2]*ExternalAuthService, [2]*ent.Client, *externalFlowTestProtocol) {
	t.Helper()
	authServices, clients := concurrentAuthClients(t, driver)
	protocol := &externalFlowTestProtocol{}
	cfg := config.AuthDeploymentConfig{
		PublicBaseURL:  "https://app.example.test",
		CompletionURLs: []string{"https://app.example.test/auth/complete"},
	}
	keys := credentialTestKeyring(t, "flow", "flow")
	var services [2]*ExternalAuthService
	for i := range services {
		var err error
		services[i], err = NewExternalAuthService(clients[i], authServices[i], keys, cfg)
		if err != nil {
			t.Fatal(err)
		}
		services[i].SetOIDCProtocol(protocol)
		services[i].now = func() time.Time { return now }
	}
	seedExternalFlowConnection(t, clients[0])
	return services, clients, protocol
}

func seedExternalFlowConnection(t *testing.T, c *ent.Client, ids ...string) {
	t.Helper()
	ctx := context.Background()
	id := "flow-oidc"
	if len(ids) > 0 {
		id = ids[0]
	}
	c.AuthConnection.Create().SetID(id).SetKind(authconnection.KindOidc).SetPublishedRevision(1).ExecX(ctx)
	c.AuthConnectionRevision.Create().SetConnectionID(id).SetRevision(1).SetDisplayName("Test identity").SetOidcIssuer("https://issuer.example.test").SetOidcClientID("client").SetOidcClientAuthMethod(authconnectionrevision.OidcClientAuthMethodClientSecretBasic).SetOidcScopes([]string{"openid", "profile"}).ExecX(ctx)
	c.AuthChannelPolicy.Create().SetConnectionID(id).SetRevision(1).SetLoginPolicyRevision(1).SetEnabled(true).SetLoginEnabled(true).SetSignupEnabled(true).SetLinkEnabled(true).SetLoginRule("unrestricted").SetSignupRule("unrestricted").SetLinkRule("unrestricted").ExecX(ctx)
}

func browserChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestExternalAuthSecretStorageAndCleanup(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			now := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
			services, clients, protocol := externalFlowServices(t, driver, now)
			ctx := context.Background()
			verifier := "browser-secret-verifier-with-enough-entropy"
			started, err := services[0].StartLogin(ctx, "flow-oidc", ExternalBrowserInput{
				BrowserChallenge: browserChallenge(verifier),
				CompletionURL:    "https://app.example.test/auth/complete",
				ReturnTo:         "/settings/login-methods",
			})
			if err != nil {
				t.Fatal(err)
			}
			row := clients[0].AuthTransaction.GetX(ctx, started.TransactionID)
			if row.StateHash != hash.Full(protocol.state) || row.StateHash == protocol.state {
				t.Fatal("OIDC state was not stored only as a hash")
			}
			if _, err := services[0].openTransactionField(row.ID, "nonce", row.Nonce); err != nil {
				t.Fatalf("open nonce: %v", err)
			}
			if _, err := services[0].openTransactionField(row.ID, "pkce", row.PkceVerifier); err != nil {
				t.Fatalf("open PKCE verifier: %v", err)
			}
			if _, err := services[0].openTransactionField(row.ID+"-other", "nonce", row.Nonce); !errors.Is(err, ErrExternalFlowInvalid) {
				t.Fatalf("nonce ciphertext was not bound to transaction id: %v", err)
			}
			if _, err := services[0].openTransactionField(row.ID, "pkce", row.Nonce); !errors.Is(err, ErrExternalFlowInvalid) {
				t.Fatalf("nonce ciphertext was accepted as PKCE verifier: %v", err)
			}

			callback, err := services[0].HandleCallback(ctx, "flow-oidc", protocol.state, "authorization-code-secret", "")
			if err != nil {
				t.Fatal(err)
			}
			row = clients[0].AuthTransaction.GetX(ctx, started.TransactionID)
			if row.Status != authtransaction.StatusCallbackVerified || row.ExchangeTicketHash == nil || *row.ExchangeTicketHash != hash.Full(callback.Ticket) {
				t.Fatalf("callback ticket state was not hashed: status=%s", row.Status)
			}
			if row.IdentitySubject != "external-subject" || row.IdentityDisplayName != "External User" {
				t.Fatal("verified identity profile was not retained for explicit confirmation")
			}
			if _, err := url.Parse(callback.CompletionURL); err != nil {
				t.Fatalf("completion URL: %v", err)
			}
			exchanged, err := services[0].Exchange(ctx, nil, ExternalExchangeInput{Ticket: callback.Ticket, Verifier: verifier})
			if err != nil {
				t.Fatal(err)
			}
			if exchanged.Confirmation == nil {
				t.Fatalf("exchange result=%+v, want signup confirmation", exchanged)
			}
			row = clients[0].AuthTransaction.GetX(ctx, started.TransactionID)
			if row.Status != authtransaction.StatusAwaitingConfirmation || row.ConfirmationTokenHash == nil || *row.ConfirmationTokenHash != hash.Full(exchanged.Confirmation.ConfirmationToken) {
				t.Fatalf("confirmation state does not contain only token hash: status=%s", row.Status)
			}
			if row.ExchangeTicketHash != nil || row.IdentityEmail != "" {
				t.Fatal("consumed exchange ticket or unneeded provider email remained stored")
			}

			account := clients[0].User.Create().SetUsername("proof-owner").SetPasswordHash("placeholder").SaveX(ctx)
			proofExpiry := now.Add(time.Minute)
			clients[0].ReauthenticationProof.Create().SetTokenHash(hash.Full("proof-secret")).SetUserID(account.ID).SetAuthVersion(account.AuthVersion).SetMethod(reauthenticationproof.MethodPassword).SetAction(reauthenticationproof.ActionLink).SetTargetID("flow-oidc").SetAuthenticatedAt(now).SetExpiresAt(proofExpiry).ExecX(ctx)
			if err := services[0].CleanupExpired(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := clients[0].AuthTransaction.Get(ctx, started.TransactionID); err != nil {
				t.Fatalf("cleanup removed live transaction: %v", err)
			}
			if _, err := clients[0].ReauthenticationProof.Query().Only(ctx); err != nil {
				t.Fatalf("cleanup removed live proof: %v", err)
			}
			now = now.Add(externalTransactionTTL)
			services[0].now = func() time.Time { return now }
			if err := services[0].CleanupExpired(ctx); err != nil {
				t.Fatal(err)
			}
			if count := clients[0].AuthTransaction.Query().CountX(ctx); count != 0 {
				t.Fatalf("expired transaction/profile data remains: count=%d", count)
			}
			if count := clients[0].ReauthenticationProof.Query().CountX(ctx); count != 0 {
				t.Fatalf("expired proof remains: count=%d", count)
			}
			if _, err := services[1].Exchange(ctx, nil, ExternalExchangeInput{Ticket: callback.Ticket, Verifier: verifier}); !errors.Is(err, ErrExternalFlowInvalid) {
				t.Fatalf("cross-instance old ticket accepted after cleanup: %v", err)
			}
		})
	}
}

func TestReauthenticationProofConcurrentConsumption(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			now := time.Date(2026, 10, 12, 2, 0, 0, 0, time.UTC)
			services, clients, _ := externalFlowServices(t, driver, now)
			ctx := context.Background()
			account := createPasswordTestUser(t, clients[0], "proof-race-owner", "password-123")
			proof := "one-use-proof-secret"
			clients[0].ReauthenticationProof.Create().SetTokenHash(hash.Full(proof)).SetUserID(account.ID).SetAuthVersion(account.AuthVersion).SetMethod(reauthenticationproof.MethodPassword).SetAction(reauthenticationproof.ActionUnlink).SetTargetID("19").SetAuthenticatedAt(now).SetExpiresAt(now.Add(reauthenticationTTL)).ExecX(ctx)

			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for i := range services {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					err := withOrganizationTransaction(ctx, clients[i], func(c *ent.Client) error {
						if err := lockAuthUser(ctx, c, account.ID); err != nil {
							return err
						}
						_, err := services[i].consumeReauthenticationProof(ctx, c, account.ID, proof, "unlink", "19")
						return err
					})
					results <- err
				}(i)
			}
			close(start)
			wg.Wait()
			close(results)
			success, rejected := 0, 0
			for err := range results {
				switch {
				case err == nil:
					success++
				case errors.Is(err, ErrReauthenticationRequired):
					rejected++
				default:
					t.Fatalf("proof consumption error: %v", err)
				}
			}
			if success != 1 || rejected != 1 {
				t.Fatalf("concurrent proof consumption success=%d rejected=%d, want 1/1", success, rejected)
			}
			stored := clients[0].ReauthenticationProof.Query().OnlyX(ctx)
			if stored.TokenHash != hash.Full(proof) || stored.TokenHash == proof || stored.ConsumedAt == nil {
				t.Fatal("proof token was not retained only as a consumed hash")
			}
		})
	}
}
