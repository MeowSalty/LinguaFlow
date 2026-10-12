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
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionvalidation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshsession"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/google/uuid"
	_ "golang.org/x/image/webp"
)

var (
	ErrAuthConnectionNotFound = errors.New("authentication connection not found")
	ErrAuthConnectionConflict = errors.New("authentication connection revision or safety conflict")
)

type OIDCConnectionInput struct {
	Issuer           string   `json:"issuer"`
	ClientID         string   `json:"client_id"`
	ClientAuthMethod string   `json:"client_auth_method"`
	Scopes           []string `json:"scopes"`
	UserinfoEnabled  *bool    `json:"userinfo_enabled,omitempty"`
}
type AuthConnectionPolicyInput struct {
	Enabled       bool              `json:"enabled"`
	LoginEnabled  bool              `json:"login_enabled"`
	SignupEnabled bool              `json:"signup_enabled"`
	LinkEnabled   bool              `json:"link_enabled"`
	LoginRules    authrules.RuleSet `json:"login_rules"`
	SignupRules   authrules.RuleSet `json:"signup_rules"`
	LinkRules     authrules.RuleSet `json:"link_rules"`
}
type CreateAuthConnectionInput struct {
	Kind         string              `json:"kind"`
	DisplayName  string              `json:"display_name"`
	Description  string              `json:"description"`
	DisplayOrder int                 `json:"display_order"`
	OIDC         OIDCConnectionInput `json:"oidc"`
	ClientSecret string              `json:"client_secret,omitempty"`
}
type UpdateAuthConnectionInput struct {
	AttributeMappings     *[]authrules.Field         `json:"attribute_mappings,omitempty"`
	EvidenceMaxAgeSeconds *int                       `json:"evidence_max_age_seconds,omitempty"`
	ExpectedRevision      int64                      `json:"expected_revision"`
	DisplayName           *string                    `json:"display_name,omitempty"`
	Description           *string                    `json:"description,omitempty"`
	DisplayOrder          *int                       `json:"display_order,omitempty"`
	OIDC                  *OIDCConnectionInput       `json:"oidc,omitempty"`
	ClientSecret          json.RawMessage            `json:"client_secret,omitempty"`
	Policy                *AuthConnectionPolicyInput `json:"policy,omitempty"`
}
type AuthConnectionSnapshot struct {
	AttributeMappings     []authrules.Field         `json:"attribute_mappings"`
	EvidenceMaxAgeSeconds int                       `json:"evidence_max_age_seconds"`
	LoginPolicyRevision   int64                     `json:"login_policy_revision"`
	Revision              int64                     `json:"revision"`
	DisplayName           string                    `json:"display_name"`
	Description           string                    `json:"description"`
	DisplayOrder          int                       `json:"display_order"`
	OIDC                  *OIDCConnectionInput      `json:"oidc,omitempty"`
	Policy                AuthConnectionPolicyInput `json:"policy"`
	HasClientSecret       bool                      `json:"has_client_secret"`
	LogoURL               *string                   `json:"logo_url"`
}
type AuthConnectionValidationResult struct {
	FreshSupported     bool       `json:"fresh_supported"`
	FingerprintMatches bool       `json:"fingerprint_matches"`
	TestedAt           *time.Time `json:"tested_at"`
}
type AuthConnectionRecord struct {
	ID                string                         `json:"id"`
	Kind              string                         `json:"kind"`
	AggregateRevision int64                          `json:"aggregate_revision"`
	DraftRevision     *int64                         `json:"draft_revision"`
	PublishedRevision *int64                         `json:"published_revision"`
	Archived          bool                           `json:"archived"`
	HasClientSecret   bool                           `json:"has_client_secret"`
	Draft             *AuthConnectionSnapshot        `json:"draft,omitempty"`
	Published         *AuthConnectionSnapshot        `json:"published,omitempty"`
	Validation        AuthConnectionValidationResult `json:"validation"`
}
type AuthConnectionCheckResult struct {
	Revision       int64                `json:"revision"`
	Rules          AuthRulesDiagnostics `json:"rules"`
	Valid          bool                 `json:"valid"`
	FreshSupported bool                 `json:"fresh_supported"`
	Issues         []string             `json:"issues"`
}
type AuthConnectionImpactResult struct {
	Revision           int64     `json:"revision"`
	EvaluatedAt        time.Time `json:"evaluated_at"`
	PassUsers          []int     `json:"pass_users"`
	FailUsers          []int     `json:"fail_users"`
	UnknownUsers       []int     `json:"unknown_users"`
	AdministratorUsers []int     `json:"administrator_users"`
	Remediation        string    `json:"remediation,omitempty"`
	AffectedUsers      []int     `json:"affected_users"`
	BlockedUsers       []int     `json:"blocked_users"`
	AffectedSessions   int       `json:"affected_sessions"`
	CanPublish         bool      `json:"can_publish"`
}
type externalConnection struct {
	Connection *ent.AuthConnection
	Config     *ent.AuthConnectionRevision
	Policy     *ent.AuthChannelPolicy
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
		if row.PublishedRevision == nil {
			return out, ErrAuthConnectionConflict
		}
		revision = *row.PublishedRevision
	}
	cfg, err := c.AuthConnectionRevision.Query().Where(authconnectionrevision.ConnectionIDEQ(id), authconnectionrevision.RevisionEQ(revision)).Only(ctx)
	if err != nil {
		return out, ErrSettingsUnavailable
	}
	p, err := c.AuthChannelPolicy.Query().Where(authchannelpolicy.ConnectionIDEQ(id), authchannelpolicy.RevisionEQ(revision)).Only(ctx)
	if err != nil {
		return out, ErrSettingsUnavailable
	}
	if err := authrules.Validate(cfg.AttributeMappings, p.LoginRules, p.SignupRules, p.LinkRules); err != nil {
		return out, ErrSettingsUnavailable
	}
	if cfg.EvidenceMaxAgeSeconds < 1 || cfg.EvidenceMaxAgeSeconds > 31536000 {
		return out, ErrSettingsUnavailable
	}
	return externalConnection{row, cfg, p}, nil
}
func connectionFingerprint(c *ent.AuthConnectionRevision) string {
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
	return c.AuthConnectionValidation.Query().Where(authconnectionvalidation.ConnectionIDEQ(id), authconnectionvalidation.FingerprintEQ(fingerprint), authconnectionvalidation.FreshSupportedEQ(true)).Exist(ctx)
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
	if connectionSecretVersion(cfg) > 0 {
		v, err := c.AuthConnectionSecret.Query().Where(authconnectionsecret.ConnectionIDEQ(cfg.ConnectionID), authconnectionsecret.VersionEQ(connectionSecretVersion(cfg))).Only(ctx)
		if err != nil {
			return result, ErrSettingsUnavailable
		}
		plain, err := s.keys.Open(credential.Ciphertext{Version: v.CipherVersion, KeyID: v.KeyID, Nonce: v.Nonce, Data: v.Ciphertext}, authConnectionSecretAAD(cfg.ConnectionID, connectionSecretVersion(cfg)))
		if err != nil {
			return result, ErrSettingsUnavailable
		}
		result.ClientSecret = string(plain)
	}
	return result, nil
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
	return AuthConnectionPolicyInput{p.Enabled, p.LoginEnabled, p.SignupEnabled, p.LinkEnabled, p.LoginRules, p.SignupRules, p.LinkRules}
}
func validConnectionPolicy(p AuthConnectionPolicyInput) bool {
	return (!p.SignupEnabled || p.LoginEnabled)
}
func defaultExternalPolicy() AuthConnectionPolicyInput {
	return AuthConnectionPolicyInput{LoginRules: authrules.Unrestricted(), SignupRules: authrules.Unrestricted(), LinkRules: authrules.Unrestricted()}
}
func normalizeOIDCInput(in OIDCConnectionInput) (OIDCConnectionInput, error) {
	if in.Issuer == "" || in.ClientID == "" || len(in.ClientID) > 1024 || len(in.Issuer) > 2048 {
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
func (s *ExternalAuthService) connectionRecord(ctx context.Context, c *ent.Client, row *ent.AuthConnection) (AuthConnectionRecord, error) {
	out := AuthConnectionRecord{ID: row.ID, Kind: string(row.Kind), AggregateRevision: row.AggregateRevision, DraftRevision: row.DraftRevision, PublishedRevision: row.PublishedRevision, Archived: row.ArchivedAt != nil}
	makeSnapshot := func(rev int64, draft bool) (*AuthConnectionSnapshot, *ent.AuthConnectionRevision, error) {
		p, err := readExternalConnection(ctx, c, row.ID, rev)
		if err != nil {
			return nil, nil, err
		}
		snapshot := &AuthConnectionSnapshot{AttributeMappings: append([]authrules.Field{}, p.Config.AttributeMappings...), EvidenceMaxAgeSeconds: p.Config.EvidenceMaxAgeSeconds, LoginPolicyRevision: p.Policy.LoginPolicyRevision, Revision: rev, DisplayName: p.Config.DisplayName, Description: p.Config.Description, DisplayOrder: p.Config.DisplayOrder, Policy: policyInput(p.Policy), HasClientSecret: connectionSecretVersion(p.Config) > 0}
		if row.Kind == authconnection.KindOidc {
			v := p.Config.OidcUserinfoEnabled
			snapshot.OIDC = &OIDCConnectionInput{p.Config.OidcIssuer, p.Config.OidcClientID, string(p.Config.OidcClientAuthMethod), p.Config.OidcScopes, &v}
		}
		if len(p.Config.LogoData) > 0 {
			u := "/api/v1/auth/connections/" + row.ID + "/logo"
			if draft {
				u = fmt.Sprintf("/api/v1/admin/auth-connections/%s/logo?expected_revision=%d", row.ID, row.AggregateRevision)
			}
			snapshot.LogoURL = &u
		}
		return snapshot, p.Config, nil
	}
	var cfg *ent.AuthConnectionRevision
	var err error
	if row.PublishedRevision != nil {
		out.Published, cfg, err = makeSnapshot(*row.PublishedRevision, false)
		if err != nil {
			return out, err
		}
	}
	if row.DraftRevision != nil {
		out.Draft, cfg, err = makeSnapshot(*row.DraftRevision, true)
		if err != nil {
			return out, err
		}
	}
	if cfg != nil {
		out.HasClientSecret = connectionSecretVersion(cfg) > 0
		v, e := c.AuthConnectionValidation.Query().Where(authconnectionvalidation.ConnectionIDEQ(row.ID), authconnectionvalidation.FingerprintEQ(connectionFingerprint(cfg))).Only(ctx)
		if e == nil {
			out.Validation = AuthConnectionValidationResult{v.FreshSupported, true, &v.TestedAt}
		} else if !ent.IsNotFound(e) {
			return out, e
		}
	}
	return out, nil
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
	if err := authrules.Validate(cfg.AttributeMappings, p.LoginRules, p.SignupRules, p.LinkRules); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}
	if err := authrules.ValidateScopes(cfg.AttributeMappings, cfg.OidcScopes, cfg.OidcUserinfoEnabled, p.LoginRules, p.SignupRules, p.LinkRules); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}
	if !validConnectionPolicy(p) || strings.TrimSpace(cfg.DisplayName) == "" || len(cfg.DisplayName) > 120 || len(cfg.Description) > 2000 {
		return ErrInvalidInput
	}
	q := c.AuthConnectionRevision.Create().SetConnectionID(id).SetRevision(revision).SetDisplayName(strings.TrimSpace(cfg.DisplayName)).SetDescription(cfg.Description).SetDisplayOrder(cfg.DisplayOrder).SetConfigVersion(1).SetOidcIssuer(cfg.OidcIssuer).SetOidcClientID(cfg.OidcClientID).SetOidcClientAuthMethod(cfg.OidcClientAuthMethod).SetOidcScopes(cfg.OidcScopes).SetOidcUserinfoEnabled(cfg.OidcUserinfoEnabled).SetNillableSecretVersion(cfg.SecretVersion).SetLogoMediaType(cfg.LogoMediaType)
	if len(cfg.LogoData) > 0 {
		q.SetLogoData(cfg.LogoData)
	}
	q.SetAttributeMappings(cfg.AttributeMappings).SetEvidenceMaxAgeSeconds(cfg.EvidenceMaxAgeSeconds)
	if err := q.Exec(ctx); err != nil {
		return err
	}
	return c.AuthChannelPolicy.Create().SetConnectionID(id).SetRevision(revision).SetLoginPolicyRevision(loginRevision).SetEnabled(p.Enabled).SetLoginEnabled(p.LoginEnabled).SetSignupEnabled(p.SignupEnabled).SetLinkEnabled(p.LinkEnabled).SetLoginRules(p.LoginRules).SetSignupRules(p.SignupRules).SetLinkRules(p.LinkRules).Exec(ctx)
}
func (s *ExternalAuthService) CreateConnection(ctx context.Context, actorID int, in CreateAuthConnectionInput) (AuthConnectionRecord, error) {
	if in.Kind != "oidc" {
		return AuthConnectionRecord{}, ErrInvalidInput
	}
	oidc, err := normalizeOIDCInput(in.OIDC)
	if err != nil {
		return AuthConnectionRecord{}, err
	}
	id := uuid.NewString()
	var result AuthConnectionRecord
	err = withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
		if err := lockExternalConfiguration(ctx, c, actorID); err != nil {
			return err
		}
		row, err := c.AuthConnection.Create().SetID(id).SetKind(authconnection.KindOidc).SetAggregateRevision(1).SetDraftRevision(1).Save(ctx)
		if err != nil {
			return err
		}
		cfg := &ent.AuthConnectionRevision{EvidenceMaxAgeSeconds: 3600, ConnectionID: id, DisplayName: in.DisplayName, Description: in.Description, DisplayOrder: in.DisplayOrder, OidcIssuer: oidc.Issuer, OidcClientID: oidc.ClientID, OidcClientAuthMethod: authconnectionrevision.OidcClientAuthMethod(oidc.ClientAuthMethod), OidcScopes: oidc.Scopes, OidcUserinfoEnabled: *oidc.UserinfoEnabled}
		if in.ClientSecret != "" {
			if err = s.saveConnectionSecret(ctx, c, id, 1, in.ClientSecret, actorID); err != nil {
				return err
			}
			initialSecretVersion := int64(1)
			cfg.SecretVersion = &initialSecretVersion
		}
		if err = saveExternalSnapshot(ctx, c, id, 1, 1, cfg, defaultExternalPolicy()); err != nil {
			return err
		}
		result, err = s.connectionRecord(ctx, c, row)
		if err != nil {
			return err
		}
		return recordAuditEvent(ctx, c, AuditEvent{ActorUserID: actorID, Action: "admin.auth_connection.created", ResourceType: "auth_connection", Metadata: map[string]any{"connection_id": id, "revision": int64(1)}})
	})
	return result, err
}
func (s *ExternalAuthService) mutateConnection(ctx context.Context, actorID int, id string, expected int64, mutate func(*ent.Client, *ent.AuthConnection, externalConnection) error) (AuthConnectionRecord, error) {
	var out AuthConnectionRecord
	if expected < 1 {
		return out, ErrInvalidInput
	}
	err := withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
		if err := lockExternalConfiguration(ctx, c, actorID); err != nil {
			return err
		}
		row, err := c.AuthConnection.Get(ctx, id)
		if ent.IsNotFound(err) {
			return ErrAuthConnectionNotFound
		}
		if err != nil {
			return err
		}
		if row.Kind != authconnection.KindOidc || row.AggregateRevision != expected || row.AggregateRevision >= maxAuthVersion {
			return ErrAuthConnectionConflict
		}
		var rev int64
		if row.DraftRevision != nil {
			rev = *row.DraftRevision
		} else if row.PublishedRevision != nil {
			rev = *row.PublishedRevision
		} else {
			return ErrSettingsUnavailable
		}
		p, err := readExternalConnection(ctx, c, id, rev)
		if err != nil {
			return err
		}
		if err = mutate(c, row, p); err != nil {
			return err
		}
		row, err = c.AuthConnection.Get(ctx, id)
		if err != nil {
			return err
		}
		out, err = s.connectionRecord(ctx, c, row)
		return err
	})
	return out, err
}
func (s *ExternalAuthService) UpdateConnection(ctx context.Context, actorID int, id string, in UpdateAuthConnectionInput) (AuthConnectionRecord, error) {
	return s.mutateConnection(ctx, actorID, id, in.ExpectedRevision, func(c *ent.Client, row *ent.AuthConnection, p externalConnection) error {
		cfg := *p.Config
		policy := policyInput(p.Policy)
		if in.AttributeMappings != nil {
			cfg.AttributeMappings = *in.AttributeMappings
		}
		if in.EvidenceMaxAgeSeconds != nil {
			cfg.EvidenceMaxAgeSeconds = *in.EvidenceMaxAgeSeconds
		}
		if in.DisplayName != nil {
			cfg.DisplayName = *in.DisplayName
		}
		if in.Description != nil {
			cfg.Description = *in.Description
		}
		if in.DisplayOrder != nil {
			cfg.DisplayOrder = *in.DisplayOrder
		}
		if in.Policy != nil {
			policy = *in.Policy
		}
		if in.OIDC != nil {
			o, err := normalizeOIDCInput(*in.OIDC)
			if err != nil {
				return err
			}
			if row.PublishedRevision != nil {
				pub, err := readExternalConnection(ctx, c, id, 0)
				if err != nil {
					return err
				}
				if o.Issuer != pub.Config.OidcIssuer || o.ClientID != pub.Config.OidcClientID {
					return ErrAuthConnectionConflict
				}
			}
			cfg.OidcIssuer = o.Issuer
			cfg.OidcClientID = o.ClientID
			cfg.OidcClientAuthMethod = authconnectionrevision.OidcClientAuthMethod(o.ClientAuthMethod)
			cfg.OidcScopes = o.Scopes
			cfg.OidcUserinfoEnabled = *o.UserinfoEnabled
		}
		next := row.AggregateRevision + 1
		if len(in.ClientSecret) > 0 {
			if string(in.ClientSecret) == "null" {
				cfg.SecretVersion = nil
			} else {
				var secret string
				if err := json.Unmarshal(in.ClientSecret, &secret); err != nil || secret == "" {
					return ErrInvalidInput
				}
				if err := s.saveConnectionSecret(ctx, c, id, next, secret, actorID); err != nil {
					return err
				}
				cfg.SecretVersion = &next
			}
		}
		if err := saveExternalSnapshot(ctx, c, id, next, p.Policy.LoginPolicyRevision, &cfg, policy); err != nil {
			return err
		}
		if err := c.AuthConnection.UpdateOneID(id).SetAggregateRevision(next).SetDraftRevision(next).Exec(ctx); err != nil {
			return err
		}
		return recordAuditEvent(ctx, c, AuditEvent{ActorUserID: actorID, Action: "admin.auth_connection.draft_saved", ResourceType: "auth_connection", Metadata: map[string]any{"connection_id": id, "revision": next}})
	})
}

func (s *ExternalAuthService) candidateConnection(ctx context.Context, actorID int, id string, expected int64) (externalConnection, error) {
	if err := checkConnectionAdmin(ctx, s.client, actorID); err != nil {
		return externalConnection{}, err
	}
	row, err := s.client.AuthConnection.Get(ctx, id)
	if ent.IsNotFound(err) {
		return externalConnection{}, ErrAuthConnectionNotFound
	}
	if err != nil {
		return externalConnection{}, err
	}
	if row.Kind != authconnection.KindOidc || row.AggregateRevision != expected || row.DraftRevision == nil {
		return externalConnection{}, ErrAuthConnectionConflict
	}
	return readExternalConnection(ctx, s.client, id, *row.DraftRevision)
}
func (s *ExternalAuthService) CheckConnection(ctx context.Context, actorID int, id string, expected int64) (AuthConnectionCheckResult, error) {
	out := AuthConnectionCheckResult{Issues: []string{}}
	p, err := s.candidateConnection(ctx, actorID, id, expected)
	if err != nil {
		return out, err
	}
	out.Revision = p.Config.Revision
	out.Rules = diagnostics(p, authrules.Evidence{}, s.now())
	if err := authrules.ValidateScopes(p.Config.AttributeMappings, p.Config.OidcScopes, p.Config.OidcUserinfoEnabled, p.Policy.LoginRules, p.Policy.SignupRules, p.Policy.LinkRules); err != nil {
		out.Issues = append(out.Issues, err.Error())
		return out, nil
	}
	cfg, err := s.externalOIDCConfig(ctx, s.client, p.Config)
	if err != nil {
		return out, err
	}
	if cfg.ClientSecret == "" {
		out.Issues = append(out.Issues, "client_secret is required")
		return out, nil
	}
	if _, err = s.protocol.Check(ctx, cfg); err != nil {
		out.Issues = append(out.Issues, "OIDC discovery or network validation failed")
		return out, nil
	}
	out.Valid = true
	out.FreshSupported, err = hasFreshValidation(ctx, s.client, id, connectionFingerprint(p.Config))
	return out, err
}

func (s *ExternalAuthService) ImpactConnection(ctx context.Context, actorID int, id string, expected int64) (AuthConnectionImpactResult, error) {
	p, err := s.candidateConnection(ctx, actorID, id, expected)
	if err != nil {
		return AuthConnectionImpactResult{}, err
	}
	if p.Connection.PublishedRevision != nil {
		published, err := readExternalConnection(ctx, s.client, id, 0)
		if err != nil {
			return AuthConnectionImpactResult{}, err
		}
		turnsOff := (published.Policy.Enabled && !p.Policy.Enabled) || (published.Policy.Enabled && published.Policy.LoginEnabled && !p.Policy.LoginEnabled)
		if !loginRulesChanged(published, p) && !turnsOff {
			return emptyAuthImpact(p.Config.Revision, s.now()), nil
		}
	}
	return candidateImpact(ctx, s.client, p, s.now(), !p.Policy.Enabled || !p.Policy.LoginEnabled)
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
func (s *ExternalAuthService) PublishConnection(ctx context.Context, actorID int, id string, expected int64) (AuthConnectionRecord, error) {
	return s.mutateConnection(ctx, actorID, id, expected, func(c *ent.Client, row *ent.AuthConnection, p externalConnection) error {
		if row.ArchivedAt != nil || row.DraftRevision == nil || connectionSecretVersion(p.Config) < 1 {
			return ErrAuthConnectionConflict
		}
		valid, err := hasFreshValidation(ctx, c, id, connectionFingerprint(p.Config))
		if err != nil {
			return err
		}
		if !valid {
			return ErrAuthConnectionConflict
		}
		policy := policyInput(p.Policy)
		next := row.AggregateRevision + 1
		generation := row.AuthGeneration
		loginRevision := p.Policy.LoginPolicyRevision
		if row.PublishedRevision != nil {
			pub, err := readExternalConnection(ctx, c, id, 0)
			if err != nil {
				return err
			}
			invalidates := loginRulesChanged(pub, p)
			// Disabling the connection itself revokes its generation even when
			// login was already off: pending transactions and stale refresh
			// chains must not become valid after a close/reopen cycle.
			turnsOff := (pub.Policy.Enabled && !policy.Enabled) || (pub.Policy.Enabled && pub.Policy.LoginEnabled && !policy.LoginEnabled)
			if invalidates || turnsOff {
				impact, err := candidateImpact(ctx, c, p, s.now(), !policy.Enabled || !policy.LoginEnabled)
				if err != nil {
					return err
				}
				if !impact.CanPublish {
					return &AuthenticationPublishConflict{Impact: impact}
				}
				if generation >= maxAuthVersion || loginRevision >= maxAuthVersion {
					return ErrAuthConnectionConflict
				}
				generation++
				if invalidates {
					loginRevision++
				}
				if err = revokeConnectionSessions(ctx, c, id, s.now()); err != nil {
					return err
				}
			}
		}
		if err = ensurePasswordAdministrator(ctx, c); err != nil {
			return err
		}
		if err = saveExternalSnapshot(ctx, c, id, next, loginRevision, p.Config, policy); err != nil {
			return err
		}
		if err = c.AuthConnection.UpdateOneID(id).SetAggregateRevision(next).SetPublishedRevision(next).ClearDraftRevision().SetAuthGeneration(generation).Exec(ctx); err != nil {
			return err
		}
		return recordAuditEvent(ctx, c, AuditEvent{ActorUserID: actorID, Action: "admin.auth_connection.published", ResourceType: "auth_connection", Metadata: map[string]any{"connection_id": id, "revision": next, "auth_generation": generation}})
	})
}
func (s *ExternalAuthService) ArchiveConnection(ctx context.Context, actorID int, id string, expected int64) (AuthConnectionRecord, error) {
	return s.changeConnectionArchive(ctx, actorID, id, expected, true)
}
func (s *ExternalAuthService) RestoreConnection(ctx context.Context, actorID int, id string, expected int64) (AuthConnectionRecord, error) {
	return s.changeConnectionArchive(ctx, actorID, id, expected, false)
}
func (s *ExternalAuthService) changeConnectionArchive(ctx context.Context, actorID int, id string, expected int64, archive bool) (AuthConnectionRecord, error) {
	return s.mutateConnection(ctx, actorID, id, expected, func(c *ent.Client, row *ent.AuthConnection, p externalConnection) error {
		if (row.ArchivedAt != nil) == archive || row.AuthGeneration >= maxAuthVersion {
			return ErrAuthConnectionConflict
		}
		if archive {
			impact, err := candidateImpact(ctx, c, p, s.now(), true)
			if err != nil {
				return err
			}
			if !impact.CanPublish {
				return &AuthenticationPublishConflict{Impact: impact}
			}
		}
		if err := ensurePasswordAdministrator(ctx, c); err != nil {
			return err
		}
		next := row.AggregateRevision + 1
		if err := saveExternalSnapshot(ctx, c, id, next, p.Policy.LoginPolicyRevision, p.Config, defaultExternalPolicy()); err != nil {
			return err
		}
		q := c.AuthConnection.UpdateOneID(id).SetAggregateRevision(next).AddAuthGeneration(1).SetDraftRevision(next)
		if row.PublishedRevision != nil {
			q.SetPublishedRevision(next)
		}
		if archive {
			q.SetArchivedAt(s.now())
		} else {
			q.ClearArchivedAt()
		}
		if err := q.Exec(ctx); err != nil {
			return err
		}
		if err := revokeConnectionSessions(ctx, c, id, s.now()); err != nil {
			return err
		}
		action := "admin.auth_connection.restored"
		if archive {
			action = "admin.auth_connection.archived"
		}
		return recordAuditEvent(ctx, c, AuditEvent{ActorUserID: actorID, Action: action, ResourceType: "auth_connection", Metadata: map[string]any{"connection_id": id, "revision": next}})
	})
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
		cfg := *p.Config
		cfg.LogoData = encoded.Bytes()
		cfg.LogoMediaType = "image/png"
		next := row.AggregateRevision + 1
		if err := saveExternalSnapshot(ctx, c, id, next, p.Policy.LoginPolicyRevision, &cfg, policyInput(p.Policy)); err != nil {
			return err
		}
		return c.AuthConnection.UpdateOneID(id).SetAggregateRevision(next).SetDraftRevision(next).Exec(ctx)
	})
}
func (s *ExternalAuthService) PublishedConnectionLogo(ctx context.Context, id string) ([]byte, error) {
	p, err := readExternalConnection(ctx, s.client, id, 0)
	if err != nil {
		return nil, err
	}
	if p.Connection.ArchivedAt != nil || len(p.Config.LogoData) == 0 {
		return nil, ErrAuthConnectionNotFound
	}
	return p.Config.LogoData, nil
}
func (s *ExternalAuthService) DraftConnectionLogo(ctx context.Context, actorID int, id string, expected int64) ([]byte, error) {
	p, err := s.candidateConnection(ctx, actorID, id, expected)
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
