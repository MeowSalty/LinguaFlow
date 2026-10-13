package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionvalidation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/loginbinding"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshsession"
)

type connectionTestProtocol struct{}

func (connectionTestProtocol) Check(context.Context, authoidc.Config) (authoidc.ProviderMetadata, error) {
	return authoidc.ProviderMetadata{Issuer: "https://idp.example.test", SupportsPKCES256: true}, nil
}
func (connectionTestProtocol) BuildAuthorization(context.Context, authoidc.Config, authoidc.AuthorizationRequest) (authoidc.Authorization, error) {
	return authoidc.Authorization{}, errors.New("not used")
}
func (connectionTestProtocol) ExchangeAndVerify(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
	return authoidc.Identity{}, errors.New("not used")
}

func connectionTestServices(t *testing.T, driver string) ([2]*ExternalAuthService, [2]*ent.Client, *ent.User) {
	t.Helper()
	auth, clients := concurrentAuthClients(t, driver)
	admin := createPasswordTestUser(t, clients[0], "connection-admin", "password-123")
	admin = clients[0].User.UpdateOne(admin).SetRole(SystemRoleAdmin).SaveX(context.Background())
	var services [2]*ExternalAuthService
	for i := range services {
		var err error
		services[i], err = NewExternalAuthService(clients[i], auth[i], credentialTestKeyring(t, "connection", "connection"), config.AuthDeploymentConfig{})
		if err != nil {
			t.Fatal(err)
		}
		services[i].SetOIDCProtocol(connectionTestProtocol{})
	}
	return services, clients, admin
}

func validateConnectionCandidate(t *testing.T, svc *ExternalAuthService, client *ent.Client, adminID int, connectionID string, revision int64) {
	t.Helper()
	row := client.AuthConnectionRevision.Query().Where(authconnectionrevision.ConnectionIDEQ(connectionID), authconnectionrevision.RevisionEQ(revision)).OnlyX(context.Background())
	if valid, err := hasFreshValidation(context.Background(), client, connectionID, connectionFingerprint(row)); err != nil {
		t.Fatal(err)
	} else if valid {
		return
	}
	client.AuthConnectionValidation.Create().SetConnectionID(connectionID).SetFingerprint(connectionFingerprint(row)).SetRevision(revision).SetFreshSupported(true).SetTestedAt(time.Now().UTC()).SetActorUserID(adminID).ExecX(context.Background())
}

func TestAuthConnectionManagementLifecycle(t *testing.T) {
	services, clients, admin := connectionTestServices(t, dialect.SQLite)
	ctx := WithExternalActor(context.Background(), admin.ID, admin.AuthVersion)
	record, err := services[0].CreateConnection(ctx, admin.ID, CreateAuthConnectionInput{Kind: "oidc", DisplayName: "Identity", OIDC: &OIDCConnectionInput{Issuer: "https://idp.example.test", ClientID: "client-1"}, ClientSecret: "initial-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if !record.HasClientSecret || record.Draft == nil || record.Draft.Policy.Enabled || record.Draft.Policy.LoginEnabled {
		t.Fatalf("new connection defaults=%+v", record)
	}
	secret := clients[0].AuthConnectionSecret.Query().OnlyX(ctx)
	if bytes.Contains(secret.Ciphertext, []byte("initial-secret")) {
		t.Fatal("client secret stored in plaintext")
	}
	check, err := services[0].CheckConnection(ctx, admin.ID, record.ID, 1)
	if err != nil || !check.Valid || check.FreshSupported {
		t.Fatalf("check=%+v err=%v", check, err)
	}
	if _, err := services[0].PublishConnection(ctx, admin.ID, record.ID, 1); !errors.Is(err, ErrAuthConnectionConflict) {
		t.Fatalf("untested publish error=%v", err)
	}
	validateConnectionCandidate(t, services[0], clients[0], admin.ID, record.ID, 1)
	policy := AuthConnectionPolicyInput{Enabled: true, LoginEnabled: true, SignupEnabled: true, LinkEnabled: true, LoginRules: authrules.Unrestricted(), SignupRules: authrules.Unrestricted(), LinkRules: authrules.Unrestricted()}
	record, err = services[0].UpdateConnection(ctx, admin.ID, record.ID, UpdateAuthConnectionInput{ExpectedRevision: 1, Policy: &policy})
	if err != nil {
		t.Fatal(err)
	}
	record, err = services[0].PublishConnection(ctx, admin.ID, record.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if record.Published == nil || !record.Published.Policy.Enabled || !record.Published.Policy.LoginEnabled {
		t.Fatalf("explicit first-publish policy lost: %+v", record.Published)
	}

	chain := clients[0].RefreshSession.Create().SetUserID(admin.ID).SetAuthVersion(admin.AuthVersion).SetConnectionID(record.ID).SetConnectionGeneration(1).SetLoginPolicyRevision(1).SetExpiresAt(time.Now().Add(time.Hour)).SaveX(ctx)
	record, err = services[0].UpdateConnection(ctx, admin.ID, record.ID, UpdateAuthConnectionInput{ExpectedRevision: 3, ClientSecret: json.RawMessage(`"rotated-secret"`)})
	if err != nil {
		t.Fatal(err)
	}
	validateConnectionCandidate(t, services[0], clients[0], admin.ID, record.ID, 4)
	record, err = services[0].PublishConnection(ctx, admin.ID, record.ID, 4)
	if err != nil {
		t.Fatal(err)
	}
	connection := clients[0].AuthConnection.GetX(ctx, record.ID)
	if connection.AuthGeneration != 1 || clients[0].RefreshSession.GetX(ctx, chain.ID).RevokedAt != nil {
		t.Fatal("tested secret rotation revoked valid sessions")
	}

	userinfo := false
	changedOIDC := *record.Published.OIDC
	changedOIDC.Scopes = []string{"openid", "profile", "groups"}
	changedOIDC.UserinfoEnabled = &userinfo
	record, err = services[0].UpdateConnection(ctx, admin.ID, record.ID, UpdateAuthConnectionInput{ExpectedRevision: 5, OIDC: &changedOIDC})
	if err != nil {
		t.Fatal(err)
	}
	validateConnectionCandidate(t, services[0], clients[0], admin.ID, record.ID, 6)
	record, err = services[0].PublishConnection(ctx, admin.ID, record.ID, 6)
	if err != nil {
		t.Fatal(err)
	}
	connection = clients[0].AuthConnection.GetX(ctx, record.ID)
	if connection.AuthGeneration != 2 || clients[0].RefreshSession.GetX(ctx, chain.ID).RevokedAt == nil || record.Published.Policy.LoginEnabled == false {
		t.Fatal("OIDC semantic change did not revoke the old login generation")
	}

	policy.LoginEnabled = false
	policy.SignupEnabled = false
	record, err = services[0].UpdateConnection(ctx, admin.ID, record.ID, UpdateAuthConnectionInput{ExpectedRevision: 7, Policy: &policy})
	if err != nil {
		t.Fatal(err)
	}
	record, err = services[0].PublishConnection(ctx, admin.ID, record.ID, 8)
	if err != nil {
		t.Fatal(err)
	}
	firstDisabledGeneration := clients[0].AuthConnection.GetX(ctx, record.ID).AuthGeneration
	policy.Enabled = false
	record, err = services[0].UpdateConnection(ctx, admin.ID, record.ID, UpdateAuthConnectionInput{ExpectedRevision: 9, Policy: &policy})
	if err != nil {
		t.Fatal(err)
	}
	record, err = services[0].PublishConnection(ctx, admin.ID, record.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := clients[0].AuthConnection.GetX(ctx, record.ID).AuthGeneration; got != firstDisabledGeneration+1 {
		t.Fatalf("enabled close generation=%d want=%d", got, firstDisabledGeneration+1)
	}

	record, err = services[0].ArchiveConnection(ctx, admin.ID, record.ID, 11)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Archived {
		t.Fatal("connection was not archived")
	}
	record, err = services[0].RestoreConnection(ctx, admin.ID, record.ID, 12)
	if err != nil {
		t.Fatal(err)
	}
	if record.Archived || record.Published.Policy.Enabled || record.Published.Policy.LoginEnabled {
		t.Fatalf("unsafe restore=%+v", record)
	}

	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, img); err != nil {
		t.Fatal(err)
	}
	record, err = services[0].UploadConnectionLogo(ctx, admin.ID, record.ID, 13, pngData.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = services[0].PublishedConnectionLogo(ctx, record.ID); !errors.Is(err, ErrAuthConnectionNotFound) {
		t.Fatalf("draft logo leaked publicly: %v", err)
	}
	draftLogo, err := services[0].DraftConnectionLogo(ctx, admin.ID, record.ID, 14)
	if err != nil || !bytes.HasPrefix(draftLogo, []byte("\x89PNG")) {
		t.Fatalf("draft logo err=%v", err)
	}
	record, err = services[0].PublishConnection(ctx, admin.ID, record.ID, 14)
	if err != nil {
		t.Fatal(err)
	}
	if publicLogo, err := services[0].PublishedConnectionLogo(ctx, record.ID); err != nil || !bytes.Equal(publicLogo, draftLogo) {
		t.Fatalf("published logo err=%v", err)
	}

	staleCtx := WithExternalActor(context.Background(), admin.ID, admin.AuthVersion)
	admin = clients[0].User.UpdateOneID(admin.ID).AddAuthVersion(1).SaveX(ctx)
	name := "stale write"
	if _, err = services[0].UpdateConnection(staleCtx, admin.ID, record.ID, UpdateAuthConnectionInput{ExpectedRevision: 15, DisplayName: &name}); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("stale actor error=%v", err)
	}
	if got := clients[0].AuthConnection.GetX(ctx, record.ID).AggregateRevision; got != 15 {
		t.Fatalf("stale actor wrote revision=%d", got)
	}

	ctx = WithExternalActor(context.Background(), admin.ID, admin.AuthVersion)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, svc := range services {
		wg.Add(1)
		go func(i int, svc *ExternalAuthService) {
			defer wg.Done()
			<-start
			v := []string{"winner-a", "winner-b"}[i]
			_, e := svc.UpdateConnection(ctx, admin.ID, record.ID, UpdateAuthConnectionInput{ExpectedRevision: 15, DisplayName: &v})
			results <- e
		}(i, svc)
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, ErrAuthConnectionConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent results success=%d conflict=%d", success, conflict)
	}
}

func TestAuthConnectionArchiveRequiresVerifiedFallback(t *testing.T) {
	services, clients, admin := connectionTestServices(t, dialect.SQLite)
	ctx := WithExternalActor(context.Background(), admin.ID, admin.AuthVersion)
	createPublished := func(id string) {
		clients[0].AuthConnection.Create().SetID(id).SetKind(authconnection.KindOidc).SetPublishedRevision(1).ExecX(ctx)
		clients[0].AuthConnectionRevision.Create().SetConnectionID(id).SetRevision(1).SetDisplayName(id).SetOidcIssuer("https://" + id + ".example.test").SetOidcClientID(id).SetOidcClientAuthMethod(authconnectionrevision.OidcClientAuthMethodClientSecretBasic).SetOidcScopes([]string{"openid"}).ExecX(ctx)
		clients[0].AuthChannelPolicy.Create().SetConnectionID(id).SetRevision(1).SetLoginPolicyRevision(1).SetEnabled(true).SetLoginEnabled(true).SetLoginRules(authrules.Unrestricted()).SetSignupRules(authrules.Unrestricted()).SetLinkRules(authrules.Unrestricted()).ExecX(ctx)
		cfg := clients[0].AuthConnectionRevision.Query().Where(authconnectionrevision.ConnectionIDEQ(id)).OnlyX(ctx)
		clients[0].AuthConnectionValidation.Create().SetConnectionID(id).SetFingerprint(connectionFingerprint(cfg)).SetRevision(1).SetFreshSupported(true).SetTestedAt(time.Now()).SetActorUserID(admin.ID).ExecX(ctx)
	}
	createPublished("primary")
	createPublished("backup")
	account := clients[0].User.Create().SetUsername("external-only").SaveX(ctx)
	bind := func(connectionID, subject string, lastVerified bool) {
		identity := clients[0].ExternalIdentity.Create().SetUserID(account.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://issuer.example.test").SetSubject(subject).SaveX(ctx)
		q := clients[0].LoginBinding.Create().SetUserID(account.ID).SetConnectionID(connectionID).SetExternalIdentityID(identity.ID).SetVerifiedAuthGeneration(1).SetVerifiedLoginPolicyRevision(1)
		if lastVerified {
			q.SetLastVerifiedAt(time.Now())
		}
		q.ExecX(ctx)
	}
	bind("primary", "primary-sub", true)
	bind("backup", "backup-sub", false)
	if _, err := services[0].ArchiveConnection(ctx, admin.ID, "primary", 1); !errors.Is(err, ErrAuthConnectionConflict) {
		t.Fatalf("mere backup binding allowed archive: %v", err)
	}
	backup := clients[0].LoginBinding.Query().Where(loginbinding.ConnectionIDEQ("backup")).OnlyX(ctx)
	clients[0].AuthConnectionValidation.Delete().Where(authconnectionvalidation.ConnectionIDEQ("backup")).ExecX(ctx)
	clients[0].LoginBinding.UpdateOne(backup).SetLastVerifiedAt(time.Now()).ExecX(ctx)
	if _, err := services[0].ArchiveConnection(ctx, admin.ID, "primary", 1); !errors.Is(err, ErrAuthConnectionConflict) {
		t.Fatalf("unknown maintenance capability allowed archive: %v", err)
	}
	cfg := clients[0].AuthConnectionRevision.Query().Where(authconnectionrevision.ConnectionIDEQ("backup")).OnlyX(ctx)
	clients[0].AuthConnectionValidation.Create().SetConnectionID("backup").SetFingerprint(connectionFingerprint(cfg)).SetRevision(1).SetFreshSupported(true).SetTestedAt(time.Now()).SetActorUserID(admin.ID).ExecX(ctx)
	if _, err := services[0].ArchiveConnection(ctx, admin.ID, "primary", 1); err != nil {
		t.Fatalf("verified login and maintenance fallback rejected: %v", err)
	}
	if n := clients[0].RefreshSession.Query().Where(refreshsession.ConnectionIDEQ("primary"), refreshsession.RevokedAtIsNil()).CountX(ctx); n != 0 {
		t.Fatalf("active primary sessions=%d", n)
	}
}

func TestAuthConnectionExpectedRevisionConcurrency(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			services, _, admin := connectionTestServices(t, driver)
			ctx := WithExternalActor(context.Background(), admin.ID, admin.AuthVersion)
			record, err := services[0].CreateConnection(ctx, admin.ID, CreateAuthConnectionInput{Kind: "oidc", DisplayName: "Concurrent", OIDC: &OIDCConnectionInput{Issuer: "https://idp.example.test", ClientID: "concurrent-client"}, ClientSecret: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			start, results := make(chan struct{}), make(chan error, 2)
			for i, svc := range services {
				go func(i int, svc *ExternalAuthService) {
					<-start
					name := []string{"first", "second"}[i]
					_, err := svc.UpdateConnection(ctx, admin.ID, record.ID, UpdateAuthConnectionInput{ExpectedRevision: 1, DisplayName: &name})
					results <- err
				}(i, svc)
			}
			close(start)
			success, conflict := 0, 0
			for range 2 {
				err := <-results
				switch {
				case err == nil:
					success++
				case errors.Is(err, ErrAuthConnectionConflict):
					conflict++
				default:
					t.Fatal(err)
				}
			}
			if success != 1 || conflict != 1 {
				t.Fatalf("success=%d conflict=%d", success, conflict)
			}
		})
	}
}

func TestAuthConnectionCloseRevokesRefreshRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			services, clients, admin := connectionTestServices(t, driver)
			ctx := WithExternalActor(context.Background(), admin.ID, admin.AuthVersion)
			record, err := services[0].CreateConnection(ctx, admin.ID, CreateAuthConnectionInput{Kind: "oidc", DisplayName: "Race", OIDC: &OIDCConnectionInput{Issuer: "https://idp.example.test", ClientID: "race-client"}, ClientSecret: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			policy := AuthConnectionPolicyInput{Enabled: true, LoginEnabled: true, SignupEnabled: true, LinkEnabled: true, LoginRules: authrules.Unrestricted(), SignupRules: authrules.Unrestricted(), LinkRules: authrules.Unrestricted()}
			record, err = services[0].UpdateConnection(ctx, admin.ID, record.ID, UpdateAuthConnectionInput{ExpectedRevision: 1, Policy: &policy})
			if err != nil {
				t.Fatal(err)
			}
			validateConnectionCandidate(t, services[0], clients[0], admin.ID, record.ID, 2)
			record, err = services[0].PublishConnection(ctx, admin.ID, record.ID, 2)
			if err != nil {
				t.Fatal(err)
			}
			identity := clients[0].ExternalIdentity.Create().SetUserID(admin.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://idp.example.test").SetSubject("race-admin").SaveX(ctx)
			binding := clients[0].LoginBinding.Create().SetUserID(admin.ID).SetConnectionID(record.ID).SetExternalIdentityID(identity.ID).SetVerifiedAuthGeneration(1).SetVerifiedLoginPolicyRevision(1).SetLastVerifiedAt(time.Now()).SaveX(ctx)
			var initial *Session
			err = withOrganizationTransaction(ctx, clients[0], func(tx *ent.Client) error {
				published, err := readExternalConnection(ctx, tx, record.ID, 0)
				if err != nil {
					return err
				}
				initial, err = services[0].auth.createSessionForConnection(ctx, tx, admin, published.Connection, published.Policy.LoginPolicyRevision, binding, nil)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			policy.LoginEnabled = false
			policy.SignupEnabled = false
			record, err = services[0].UpdateConnection(ctx, admin.ID, record.ID, UpdateAuthConnectionInput{ExpectedRevision: 3, Policy: &policy})
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			refreshResult := make(chan error, 1)
			publishResult := make(chan error, 1)
			go func() {
				<-start
				_, err := services[1].auth.Refresh(context.Background(), initial.RefreshToken)
				refreshResult <- err
			}()
			go func() {
				<-start
				_, err := services[0].PublishConnection(ctx, admin.ID, record.ID, 4)
				publishResult <- err
			}()
			close(start)
			if err := <-publishResult; err != nil {
				t.Fatalf("close publish failed: %v", err)
			}
			if err := <-refreshResult; err != nil && !errors.Is(err, ErrRefreshTokenRevoked) {
				t.Fatalf("refresh race failed unexpectedly: %v", err)
			}
			if n := clients[0].RefreshSession.Query().Where(refreshsession.ConnectionIDEQ(record.ID), refreshsession.RevokedAtIsNil()).CountX(ctx); n != 0 {
				t.Fatalf("connection close left %d live refresh chains", n)
			}
		})
	}
}
