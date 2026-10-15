package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authchannelpolicy"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionsecret"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshsession"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	_ "golang.org/x/image/webp"
)

var (
	ErrAuthConnectionNotFound = errors.New("authentication connection not found")
	ErrAuthConnectionConflict = errors.New("authentication connection revision or safety conflict")
)

type externalConnection struct {
	Connection     *ent.AuthConnection
	Config         *ent.AuthConnectionRevision
	Policy         *ent.AuthChannelPolicy
	SecretOverride *string
}

func readExternalConnection(ctx context.Context, c *ent.Client, id string, revision int64) (externalConnection, error) {
	var out externalConnection
	row, err := c.AuthConnection.Get(ctx, id)
	if ent.IsNotFound(err) {
		return out, ErrAuthConnectionNotFound
	}
	if err != nil {
		return out, err
	}
	if revision == 0 {
		revision = row.CurrentRevision
	}
	cfg, err := c.AuthConnectionRevision.Query().Where(authconnectionrevision.ConnectionIDEQ(id), authconnectionrevision.RevisionEQ(revision)).Only(ctx)
	if err != nil {
		return out, ErrSettingsUnavailable
	}
	p, err := c.AuthChannelPolicy.Query().Where(authchannelpolicy.ConnectionIDEQ(id), authchannelpolicy.RevisionEQ(revision)).Only(ctx)
	if err != nil {
		return out, ErrSettingsUnavailable
	}
	if cfg.ConfigVersion != 1 || (!supportedExternalKind(row.Kind) && row.Kind != authconnection.KindPassword) {
		return out, ErrSettingsUnavailable
	}
	if row.Kind == authconnection.KindGenericOauth2 {
		if row.IdentityAuthorityID == nil || *row.IdentityAuthorityID == "" || cfg.OidcIssuer != "" {
			return out, ErrSettingsUnavailable
		}
	} else if cfg.Oauth2ClientID != "" {
		return out, ErrSettingsUnavailable
	}
	if err := authrules.Validate(configProtocol(cfg), cfg.AttributeMappings, p.LoginRules, p.SignupRules, p.LinkRules); err != nil {
		return out, ErrSettingsUnavailable
	}
	if cfg.EvidenceMaxAgeSeconds < 1 || cfg.EvidenceMaxAgeSeconds > 31536000 {
		return out, ErrSettingsUnavailable
	}
	if row.Kind != authconnection.KindPassword {
		p.Enabled = row.State == authconnection.StateEnabled
	}
	return externalConnection{Connection: row, Config: cfg, Policy: p}, nil
}
func connectionFingerprint(c *ent.AuthConnectionRevision) string {
	if configProtocol(c) == "oauth2" {
		data, _ := json.Marshal([]any{"oauth2", c.ConnectionID, oauthInput(c), connectionSecretVersion(c)})
		h := sha256.Sum256(data)
		return hex.EncodeToString(h[:])
	}
	data, _ := json.Marshal(struct {
		Issuer, ClientID, Method string
		Scopes                   []string
		Userinfo                 bool
		Secret                   int64
	}{c.OidcIssuer, c.OidcClientID, string(c.OidcClientAuthMethod), c.OidcScopes, c.OidcUserinfoEnabled, connectionSecretVersion(c)})
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
func hasFreshValidation(ctx context.Context, c *ent.Client, id, fingerprint string) (bool, error) {
	p, err := readExternalConnection(ctx, c, id, 0)
	if err != nil {
		return false, err
	}
	return p.Config.FreshSupported && connectionFingerprint(p.Config) == fingerprint, nil
}
func authConnectionSecretAAD(id string, version int64) []byte {
	data, _ := json.Marshal([]any{"linguaflow.authentication.client-secret", 1, id, version})
	return data
}
func (s *ExternalAuthService) externalOIDCConfig(ctx context.Context, c *ent.Client, cfg *ent.AuthConnectionRevision) (authoidc.Config, error) {
	method := "basic"
	if cfg.OidcClientAuthMethod == authconnectionrevision.OidcClientAuthMethodClientSecretPost {
		method = "post"
	}
	result := authoidc.Config{Issuer: cfg.OidcIssuer, ClientID: cfg.OidcClientID, ClientAuthMethod: method, Scopes: cfg.OidcScopes, UserInfoEnabled: cfg.OidcUserinfoEnabled}
	var err error
	result.ClientSecret, err = s.connectionClientSecret(ctx, c, cfg)
	return result, err
}
func (s *ExternalAuthService) connectionClientSecret(ctx context.Context, c *ent.Client, cfg *ent.AuthConnectionRevision) (string, error) {
	if connectionSecretVersion(cfg) == 0 {
		return "", nil
	}
	v, err := c.AuthConnectionSecret.Query().Where(authconnectionsecret.ConnectionIDEQ(cfg.ConnectionID), authconnectionsecret.VersionEQ(connectionSecretVersion(cfg))).Only(ctx)
	if err != nil {
		return "", ErrSettingsUnavailable
	}
	plain, err := s.keys.Open(credential.Ciphertext{Version: v.CipherVersion, KeyID: v.KeyID, Nonce: v.Nonce, Data: v.Ciphertext}, authConnectionSecretAAD(cfg.ConnectionID, connectionSecretVersion(cfg)))
	if err != nil {
		return "", ErrSettingsUnavailable
	}
	return string(plain), nil
}

func (s *ExternalAuthService) saveConnectionSecret(ctx context.Context, c *ent.Client, id string, version int64, secret string, actorID int) error {
	if secret == "" || len(secret) > 16384 {
		return ErrInvalidInput
	}
	sealed, err := s.keys.Seal([]byte(secret), authConnectionSecretAAD(id, version))
	if err != nil {
		return ErrSettingsUnavailable
	}
	return c.AuthConnectionSecret.Create().SetConnectionID(id).SetVersion(version).SetCipherVersion(sealed.Version).SetActorUserID(actorID).SetKeyID(sealed.KeyID).SetNonce(sealed.Nonce).SetCiphertext(sealed.Data).Exec(ctx)
}
func checkConnectionAdmin(ctx context.Context, c *ent.Client, actorID int) error {
	ok, err := c.User.Query().Where(user.IDEQ(actorID), user.ActiveEQ(true), user.RoleEQ(SystemRoleAdmin)).Exist(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}
func lockExternalConfiguration(ctx context.Context, c *ent.Client, actorID int) error {
	r, err := c.ExecContext(ctx, "UPDATE instance_initializations SET version=version WHERE id=1")
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInstanceUninitialized
	}
	if err = lockSetting(ctx, c, SettingRegistrationEnabled); err != nil {
		return err
	}
	ids, err := c.AuthConnection.Query().Order(ent.Asc(authconnection.FieldID)).IDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = c.ExecContext(ctx, "UPDATE auth_connections SET auth_generation=auth_generation WHERE id='"+strings.ReplaceAll(id, "'", "''")+"'"); err != nil {
			return err
		}
	}
	// Management can inspect all users' remaining methods. Lock in stable order
	// before policy changes so unlink and administrator maintenance cannot race.
	users, err := c.User.Query().Order(ent.Asc(user.FieldID)).IDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range users {
		if err = lockAuthUser(ctx, c, id); err != nil {
			return err
		}
	}
	// Recheck the administrator's bearer generation only after all shared
	// security rows have been locked. A token revoked while waiting for the
	// management lock must not be able to commit a configuration change.
	actor, err := c.User.Get(ctx, actorID)
	if err != nil {
		return err
	}
	if err = checkExternalActor(ctx, actor); err != nil {
		return err
	}
	return checkConnectionAdmin(ctx, c, actorID)
}
func policyInput(p *ent.AuthChannelPolicy) AuthConnectionPolicyInput {
	return AuthConnectionPolicyInput{p.LoginEnabled, p.SignupEnabled, p.LinkEnabled, p.LoginRules, p.SignupRules, p.LinkRules}
}
func validConnectionPolicy(p AuthConnectionPolicyInput) bool {
	return (!p.SignupEnabled || p.LoginEnabled)
}
func defaultExternalPolicy() AuthConnectionPolicyInput {
	return AuthConnectionPolicyInput{LoginRules: authrules.Unrestricted(), SignupRules: authrules.Unrestricted(), LinkRules: authrules.Unrestricted()}
}
func normalizeOIDCInput(in OIDCConnectionInput) (OIDCConnectionInput, error) {
	if len(in.ClientID) > 1024 || len(in.Issuer) > 2048 {
		return in, ErrInvalidInput
	}
	if in.ClientAuthMethod == "" {
		in.ClientAuthMethod = "client_secret_basic"
	}
	if in.ClientAuthMethod != "client_secret_basic" && in.ClientAuthMethod != "client_secret_post" {
		return in, ErrInvalidInput
	}
	if len(in.Scopes) == 0 {
		in.Scopes = []string{"openid", "profile"}
	}
	if len(in.Scopes) > 32 {
		return in, ErrInvalidInput
	}
	seen := map[string]bool{}
	for _, scope := range in.Scopes {
		if scope == "" || len(scope) > 128 || strings.ContainsAny(scope, " \t\r\n") || seen[scope] {
			return in, ErrInvalidInput
		}
		seen[scope] = true
	}
	if !seen["openid"] {
		return in, ErrInvalidInput
	}
	if in.UserinfoEnabled == nil {
		v := true
		in.UserinfoEnabled = &v
	}
	return in, nil
}
func (s *ExternalAuthService) ListConnections(ctx context.Context, actorID int) ([]AuthConnectionRecord, error) {
	if err := checkConnectionAdmin(ctx, s.client, actorID); err != nil {
		return nil, err
	}
	rows, err := s.client.AuthConnection.Query().Order(ent.Asc(authconnection.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AuthConnectionRecord, 0, len(rows))
	for _, row := range rows {
		v, err := s.connectionRecord(ctx, s.client, row)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *ExternalAuthService) GetConnection(ctx context.Context, actorID int, id string) (AuthConnectionRecord, error) {
	if err := checkConnectionAdmin(ctx, s.client, actorID); err != nil {
		return AuthConnectionRecord{}, err
	}
	row, err := s.client.AuthConnection.Get(ctx, id)
	if ent.IsNotFound(err) {
		return AuthConnectionRecord{}, ErrAuthConnectionNotFound
	}
	if err != nil {
		return AuthConnectionRecord{}, err
	}
	return s.connectionRecord(ctx, s.client, row)
}

func saveExternalSnapshot(ctx context.Context, c *ent.Client, id string, revision, loginRevision int64, cfg *ent.AuthConnectionRevision, p AuthConnectionPolicyInput) error {
	if cfg.EvidenceMaxAgeSeconds < 1 || cfg.EvidenceMaxAgeSeconds > 31536000 {
		return ErrInvalidInput
	}
	if err := authrules.Validate(configProtocol(cfg), cfg.AttributeMappings, p.LoginRules, p.SignupRules, p.LinkRules); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}
	if err := validateConnectionRules(cfg, p.LoginRules, p.SignupRules, p.LinkRules); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}
	if !validConnectionPolicy(p) || strings.TrimSpace(cfg.DisplayName) == "" || len(cfg.DisplayName) > 120 || len(cfg.Description) > 2000 {
		return ErrInvalidInput
	}
	q := c.AuthConnectionRevision.Create().SetConnectionID(id).SetRevision(revision).SetDisplayName(strings.TrimSpace(cfg.DisplayName)).SetDescription(cfg.Description).SetDisplayOrder(cfg.DisplayOrder).SetConfigVersion(1).SetOidcIssuer(cfg.OidcIssuer).SetOidcClientID(cfg.OidcClientID).SetOidcScopes(cfg.OidcScopes).SetOidcUserinfoEnabled(cfg.OidcUserinfoEnabled).SetNillableSecretVersion(cfg.SecretVersion).SetLogoMediaType(cfg.LogoMediaType)
	if configProtocol(cfg) == "oauth2" {
		q.SetOauth2AuthorizationEndpoint(cfg.Oauth2AuthorizationEndpoint).SetOauth2TokenEndpoint(cfg.Oauth2TokenEndpoint).SetOauth2ProfileEndpoint(cfg.Oauth2ProfileEndpoint).SetOauth2ClientID(cfg.Oauth2ClientID).SetOauth2ClientAuthMethod(cfg.Oauth2ClientAuthMethod).SetOauth2Scopes(cfg.Oauth2Scopes).SetOauth2SubjectPointer(cfg.Oauth2SubjectPointer).SetOauth2SubjectType(cfg.Oauth2SubjectType)
	} else {
		q.SetOidcClientAuthMethod(cfg.OidcClientAuthMethod)
	}
	if len(cfg.LogoData) > 0 {
		q.SetLogoData(cfg.LogoData)
	}
	q.SetFreshSupported(cfg.FreshSupported).SetNillableTestedAt(cfg.TestedAt).SetAppliedTestID(cfg.AppliedTestID)
	q.SetAttributeMappings(cfg.AttributeMappings).SetEvidenceMaxAgeSeconds(cfg.EvidenceMaxAgeSeconds)
	if err := q.Exec(ctx); err != nil {
		return err
	}
	return c.AuthChannelPolicy.Create().SetConnectionID(id).SetRevision(revision).SetLoginPolicyRevision(loginRevision).SetEnabled(false).SetLoginEnabled(p.LoginEnabled).SetSignupEnabled(p.SignupEnabled).SetLinkEnabled(p.LinkEnabled).SetLoginRules(p.LoginRules).SetSignupRules(p.SignupRules).SetLinkRules(p.LinkRules).Exec(ctx)
}
func revokeConnectionSessions(ctx context.Context, c *ent.Client, id string, now time.Time) error {
	sessions, err := c.RefreshSession.Query().Where(refreshsession.ConnectionIDEQ(id), refreshsession.RevokedAtIsNil()).IDs(ctx)
	if err != nil {
		return err
	}
	for _, sid := range sessions {
		if err = revokeRefreshSession(ctx, c, sid, now); err != nil {
			return err
		}
	}
	return nil
}
func ensurePasswordAdministrator(ctx context.Context, c *ent.Client) error {
	p, err := readPasswordPolicy(ctx, c)
	if err != nil {
		return err
	}
	if !p.canLogin() {
		return ErrAuthConnectionConflict
	}
	ok, err := c.User.Query().Where(user.ActiveEQ(true), user.RoleEQ(SystemRoleAdmin), user.PasswordHashNotNil(), user.PasswordHashNEQ("")).Exist(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAuthConnectionConflict
	}
	return nil
}
func (s *ExternalAuthService) UploadConnectionLogo(ctx context.Context, actorID int, id string, expected int64, data []byte) (AuthConnectionRecord, error) {
	if len(data) == 0 || len(data) > 256<<10 {
		return AuthConnectionRecord{}, ErrInvalidInput
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "webp") || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 1024 || cfg.Height > 1024 {
		return AuthConnectionRecord{}, ErrInvalidInput
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return AuthConnectionRecord{}, ErrInvalidInput
	}
	var encoded bytes.Buffer
	if err = png.Encode(&encoded, img); err != nil {
		return AuthConnectionRecord{}, ErrInvalidInput
	}
	if encoded.Len() > 1<<20 {
		return AuthConnectionRecord{}, ErrInvalidInput
	}
	return s.mutateConnection(ctx, actorID, id, expected, func(c *ent.Client, row *ent.AuthConnection, p externalConnection) error {
		if bytes.Equal(p.Config.LogoData, encoded.Bytes()) && p.Config.LogoMediaType == "image/png" {
			return nil
		}
		cfg := *p.Config
		cfg.LogoData = encoded.Bytes()
		cfg.LogoMediaType = "image/png"
		next := row.AggregateRevision + 1
		if err := saveExternalSnapshot(ctx, c, id, next, p.Policy.LoginPolicyRevision, &cfg, policyInput(p.Policy)); err != nil {
			return err
		}
		if err := c.AuthConnection.UpdateOneID(id).SetAggregateRevision(next).SetCurrentRevision(next).Exec(ctx); err != nil {
			return err
		}
		return recordAuditEvent(ctx, c, AuditEvent{ActorUserID: actorID, Action: "admin.auth_connection.logo_saved", ResourceType: "auth_connection", Metadata: map[string]any{"connection_id": id, "revision": next}})
	})
}
func (s *ExternalAuthService) PublicConnectionLogo(ctx context.Context, id string) ([]byte, error) {
	p, err := readExternalConnection(ctx, s.client, id, 0)
	if err != nil {
		return nil, err
	}
	if p.Connection.ArchivedAt != nil || !p.Policy.Enabled || len(p.Config.LogoData) == 0 {
		return nil, ErrAuthConnectionNotFound
	}
	return p.Config.LogoData, nil
}
func (s *ExternalAuthService) CurrentConnectionLogo(ctx context.Context, actorID int, id string) ([]byte, error) {
	if err := checkConnectionAdmin(ctx, s.client, actorID); err != nil {
		return nil, err
	}
	p, err := readExternalConnection(ctx, s.client, id, 0)
	if err != nil {
		return nil, err
	}
	if len(p.Config.LogoData) == 0 {
		return nil, ErrAuthConnectionNotFound
	}
	return p.Config.LogoData, nil
}

func connectionSecretVersion(c *ent.AuthConnectionRevision) int64 {
	if c.SecretVersion == nil {
		return 0
	}
	return *c.SecretVersion
}
