package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshsession"
)

// TestAuthConnectionManagementLifecycle keeps the storage and immediate-effect
// assertions that complement TestAuthenticationB4: encrypted secret material at
// rest, display saves taking effect without restarts, logo availability, and
// the disable/archive/restore state boundaries.
func TestAuthConnectionManagementLifecycle(t *testing.T) {
	f := newB4Fixture(t, dialect.SQLite)
	record := f.enabled(t)
	if record.State != "enabled" || !record.ConfigurationComplete || !record.Configuration.HasClientSecret || !record.Capabilities.CanLogin || !record.Capabilities.CanTest || !record.Validation.FreshSupported {
		t.Fatalf("enabled record=%+v", record)
	}
	secret := f.clients[0].AuthConnectionSecret.Query().OnlyX(f.ctx)
	if bytes.Contains(secret.Ciphertext, []byte("secret-v1")) {
		t.Fatal("client secret stored in plaintext")
	}

	renamed := "Identity v2"
	record, err := f.services[0].UpdateConnection(f.ctx, f.admin.ID, record.ID, UpdateAuthConnectionInput{ExpectedRevision: record.AggregateRevision, AuthConnectionChanges: AuthConnectionChanges{DisplayName: &renamed}})
	if err != nil {
		t.Fatal(err)
	}
	if record.Configuration.DisplayName != renamed || record.State != "enabled" {
		t.Fatal("display save did not take effect immediately")
	}

	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, img); err != nil {
		t.Fatal(err)
	}
	record, err = f.services[0].UploadConnectionLogo(f.ctx, f.admin.ID, record.ID, record.AggregateRevision, pngData.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	logo, err := f.services[0].PublicConnectionLogo(f.ctx, record.ID)
	if err != nil || !bytes.HasPrefix(logo, []byte("\x89PNG")) {
		t.Fatalf("public logo err=%v", err)
	}

	// A secret rotation on an enabled connection is a protocol change: it must
	// carry the proof taken for the unsaved rotated candidate in one save.
	changes := AuthConnectionChanges{ClientSecret: json.RawMessage(`"secret-rotated"`)}
	test := f.test(t, record, &changes)
	rotated := "secret-rotated"
	record, err = f.services[0].UpdateConnection(f.ctx, f.admin.ID, record.ID, UpdateAuthConnectionInput{ExpectedRevision: record.AggregateRevision, TestID: test.TestID, Verifier: b4Verifier, AuthConnectionChanges: changes})
	if err != nil {
		t.Fatal(err)
	}
	if !record.Validation.FreshSupported || record.Validation.AppliedTestID != test.TestID || !record.Configuration.HasClientSecret || record.Configuration.HasClientSecret == (rotated == "") {
		t.Fatalf("proofed rotation record=%+v", record)
	}

	policy := b4Policy()
	policy.LoginEnabled = false
	policy.SignupEnabled = false
	record, err = f.services[0].UpdateConnection(f.ctx, f.admin.ID, record.ID, UpdateAuthConnectionInput{ExpectedRevision: record.AggregateRevision, AuthConnectionChanges: AuthConnectionChanges{Policy: &policy}})
	if err != nil {
		t.Fatal(err)
	}
	if record.Capabilities.CanLogin || record.State != "enabled" {
		t.Fatalf("channel-off record=%+v", record)
	}
	record, err = f.services[0].DisableConnection(f.ctx, f.admin.ID, record.ID, AuthConnectionActionInput{ExpectedRevision: record.AggregateRevision})
	if err != nil {
		t.Fatal(err)
	}
	if record.State != "disabled" || record.Capabilities.CanTest {
		t.Fatalf("disable record=%+v", record)
	}
	record, err = f.services[0].ArchiveConnection(f.ctx, f.admin.ID, record.ID, AuthConnectionActionInput{ExpectedRevision: record.AggregateRevision})
	if err != nil {
		t.Fatal(err)
	}
	if !record.Archived || record.State != "disabled" {
		t.Fatal("connection was not archived")
	}
	record, err = f.services[0].RestoreConnection(f.ctx, f.admin.ID, record.ID, AuthConnectionActionInput{ExpectedRevision: record.AggregateRevision})
	if err != nil {
		t.Fatal(err)
	}
	if record.Archived || record.State != "disabled" || record.Capabilities.CanLogin {
		t.Fatalf("unsafe restore=%+v", record)
	}
}

// TestAuthConnectionArchiveRequiresVerifiedFallback keeps the B4 meaning: an
// archive that would strand an external-only account requires a verified,
// enabled login capable of acting as the maintenance fallback.
func TestAuthConnectionArchiveRequiresVerifiedFallback(t *testing.T) {
	f := newB4Fixture(t, dialect.SQLite)
	ctx := f.ctx
	seed := func(id string, fresh bool) {
		f.clients[0].AuthConnection.Create().SetID(id).SetKind(authconnection.KindOidc).SetState(authconnection.StateEnabled).SetCurrentRevision(1).ExecX(ctx)
		create := f.clients[0].AuthConnectionRevision.Create().SetConnectionID(id).SetRevision(1).SetDisplayName(id).SetOidcIssuer("https://" + id + ".example.test").SetOidcClientID(id).SetOidcClientAuthMethod(authconnectionrevision.OidcClientAuthMethodClientSecretBasic).SetOidcScopes([]string{"openid"})
		if fresh {
			create = create.SetFreshSupported(true).SetTestedAt(time.Now())
		}
		create.ExecX(ctx)
		f.clients[0].AuthChannelPolicy.Create().SetConnectionID(id).SetRevision(1).SetLoginPolicyRevision(1).SetEnabled(true).SetLoginEnabled(true).SetLoginRules(authrules.Unrestricted()).SetSignupRules(authrules.Unrestricted()).SetLinkRules(authrules.Unrestricted()).ExecX(ctx)
	}
	seed("primary", true)
	seed("backup", false)
	account := f.clients[0].User.Create().SetUsername("external-only").SaveX(ctx)
	bind := func(connectionID, subject string) {
		identity := f.clients[0].ExternalIdentity.Create().SetUserID(account.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://" + connectionID + ".example.test").SetSubject(subject).SaveX(ctx)
		f.clients[0].LoginBinding.Create().SetUserID(account.ID).SetConnectionID(connectionID).SetExternalIdentityID(identity.ID).SetVerifiedAuthGeneration(1).SetVerifiedLoginPolicyRevision(1).SetLastVerifiedAt(time.Now()).ExecX(ctx)
	}
	bind("primary", "primary-sub")
	bind("backup", "backup-sub")
	action := func() error {
		_, err := f.services[0].ArchiveConnection(f.ctx, f.admin.ID, "primary", AuthConnectionActionInput{ExpectedRevision: 1})
		return err
	}
	if err := action(); !errors.Is(err, ErrAuthConnectionConflict) {
		t.Fatalf("stale backup validation allowed archive: %v", err)
	}
	// A disabled connection never counts as a maintenance fallback.
	f.clients[0].AuthConnectionRevision.Create().SetConnectionID("backup").SetRevision(2).SetDisplayName("backup").SetOidcIssuer("https://backup.example.test").SetOidcClientID("backup").SetOidcClientAuthMethod(authconnectionrevision.OidcClientAuthMethodClientSecretBasic).SetOidcScopes([]string{"openid"}).SetFreshSupported(true).SetTestedAt(time.Now()).ExecX(ctx)
	f.clients[0].AuthChannelPolicy.Create().SetConnectionID("backup").SetRevision(2).SetLoginPolicyRevision(1).SetEnabled(true).SetLoginEnabled(true).SetLoginRules(authrules.Unrestricted()).SetSignupRules(authrules.Unrestricted()).SetLinkRules(authrules.Unrestricted()).ExecX(ctx)
	f.clients[0].AuthConnection.UpdateOneID("backup").SetCurrentRevision(2).SetState(authconnection.StateDisabled).ExecX(ctx)
	if err := action(); !errors.Is(err, ErrAuthConnectionConflict) {
		t.Fatalf("disabled fallback allowed archive: %v", err)
	}
	f.clients[0].AuthConnection.UpdateOneID("backup").SetState(authconnection.StateEnabled).ExecX(ctx)
	if err := action(); err != nil {
		t.Fatalf("verified login and maintenance fallback rejected: %v", err)
	}
	if n := f.clients[0].RefreshSession.Query().Where(refreshsession.ConnectionIDEQ("primary"), refreshsession.RevokedAtIsNil()).CountX(ctx); n != 0 {
		t.Fatalf("active primary sessions=%d", n)
	}
}

// TestAuthConnectionCloseRevokesRefreshRace keeps the B4 race meaning: while a
// refresh chain is being rotated against the database, closing the connection
// must still leave no live chains behind.
func TestAuthConnectionCloseRevokesRefreshRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			f := newB4Fixture(t, driver)
			record := f.enabled(t)
			identity := f.clients[0].ExternalIdentity.Create().SetUserID(f.admin.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://idp.example.test").SetSubject("race-admin").SaveX(f.ctx)
			binding := f.clients[0].LoginBinding.Create().SetUserID(f.admin.ID).SetConnectionID(record.ID).SetExternalIdentityID(identity.ID).SetVerifiedAuthGeneration(1).SetVerifiedLoginPolicyRevision(1).SetLastVerifiedAt(time.Now()).SaveX(f.ctx)
			var initial *Session
			err := withOrganizationTransaction(f.ctx, f.clients[0], func(tx *ent.Client) error {
				current, err := readExternalConnection(f.ctx, tx, record.ID, 0)
				if err != nil {
					return err
				}
				initial, err = f.services[0].auth.createSessionForConnection(f.ctx, tx, f.admin, current.Connection, current.Policy.LoginPolicyRevision, binding, nil)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			refreshResult := make(chan error, 1)
			closeResult := make(chan error, 1)
			go func() {
				<-start
				_, err := f.services[1].auth.Refresh(context.Background(), initial.RefreshToken)
				refreshResult <- err
			}()
			go func() {
				<-start
				_, err := f.services[0].DisableConnection(f.ctx, f.admin.ID, record.ID, AuthConnectionActionInput{ExpectedRevision: record.AggregateRevision})
				closeResult <- err
			}()
			close(start)
			if err := <-closeResult; err != nil {
				t.Fatalf("connection close failed: %v", err)
			}
			if err := <-refreshResult; err != nil && !errors.Is(err, ErrRefreshTokenRevoked) {
				t.Fatalf("refresh race failed unexpectedly: %v", err)
			}
			if n := f.clients[0].RefreshSession.Query().Where(refreshsession.ConnectionIDEQ(record.ID), refreshsession.RevokedAtIsNil()).CountX(f.ctx); n != 0 {
				t.Fatalf("connection close left %d live refresh chains", n)
			}
		})
	}
}
