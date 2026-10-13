package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authoauth2"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authprotocol"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionvalidation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/loginbinding"
)

type OAuth2ConnectionInput struct {
	AuthorizationEndpoint string                    `json:"authorization_endpoint"`
	TokenEndpoint         string                    `json:"token_endpoint"`
	ProfileEndpoint       string                    `json:"profile_endpoint"`
	ClientID              string                    `json:"client_id"`
	ClientAuthMethod      string                    `json:"client_auth_method"`
	Scopes                []string                  `json:"scopes"`
	SubjectMapping        authoauth2.SubjectMapping `json:"subject_mapping"`
}
type OAuth2Protocol interface {
	Check(context.Context, authoauth2.Config) (authprotocol.ProviderMetadata, error)
	BuildAuthorization(context.Context, authoauth2.Config, authprotocol.AuthorizationRequest) (authprotocol.Authorization, error)
	ExchangeAndVerify(context.Context, authoauth2.Config, authprotocol.ExchangeRequest) (authprotocol.Identity, error)
}

func supportedExternalKind(k authconnection.Kind) bool {
	return k == authconnection.KindOidc || k == authconnection.KindGenericOauth2
}
func configProtocol(c *ent.AuthConnectionRevision) string {
	if c.Oauth2ClientID != "" {
		return "oauth2"
	}
	return "oidc"
}
func connectionAuthority(p externalConnection) string {
	if p.Connection.Kind == authconnection.KindGenericOauth2 {
		if p.Connection.IdentityAuthorityID != nil {
			return *p.Connection.IdentityAuthorityID
		}
		return ""
	}
	return p.Config.OidcIssuer
}
func oauthInput(c *ent.AuthConnectionRevision) OAuth2ConnectionInput {
	return OAuth2ConnectionInput{AuthorizationEndpoint: c.Oauth2AuthorizationEndpoint, TokenEndpoint: c.Oauth2TokenEndpoint, ProfileEndpoint: c.Oauth2ProfileEndpoint, ClientID: c.Oauth2ClientID, ClientAuthMethod: string(c.Oauth2ClientAuthMethod), Scopes: append([]string{}, c.Oauth2Scopes...), SubjectMapping: authoauth2.SubjectMapping{Pointer: c.Oauth2SubjectPointer, Type: string(c.Oauth2SubjectType)}}
}
func setOAuthInput(c *ent.AuthConnectionRevision, in OAuth2ConnectionInput) {
	c.Oauth2AuthorizationEndpoint = in.AuthorizationEndpoint
	c.Oauth2TokenEndpoint = in.TokenEndpoint
	c.Oauth2ProfileEndpoint = in.ProfileEndpoint
	c.Oauth2ClientID = in.ClientID
	c.Oauth2ClientAuthMethod = authconnectionrevision.Oauth2ClientAuthMethod(in.ClientAuthMethod)
	c.Oauth2Scopes = in.Scopes
	c.Oauth2SubjectPointer = in.SubjectMapping.Pointer
	c.Oauth2SubjectType = authconnectionrevision.Oauth2SubjectType(in.SubjectMapping.Type)
}
func oauthConfig(in OAuth2ConnectionInput, authority string) authoauth2.Config {
	return authoauth2.Config{Authority: authority, AuthorizationEndpoint: in.AuthorizationEndpoint, TokenEndpoint: in.TokenEndpoint, ProfileEndpoint: in.ProfileEndpoint, ClientID: in.ClientID, ClientAuthMethod: in.ClientAuthMethod, Scopes: in.Scopes, SubjectMapping: in.SubjectMapping}
}
func (s *ExternalAuthService) normalizeOAuthInput(ctx context.Context, in OAuth2ConnectionInput) (OAuth2ConnectionInput, error) {
	if in.ClientAuthMethod == "" {
		in.ClientAuthMethod = "client_secret_basic"
	}
	if in.Scopes == nil {
		in.Scopes = []string{}
	}
	// This validates the actual adapter configuration, without making a request.
	c, err := authoauth2.New(s.cfg)
	if err != nil {
		return in, err
	}
	if _, err = c.Check(ctx, oauthConfig(in, "validation-only")); err != nil {
		return in, ErrInvalidInput
	}
	return in, nil
}
func oauthIdentityContract(in OAuth2ConnectionInput) string {
	b, _ := json.Marshal([]any{in.AuthorizationEndpoint, in.TokenEndpoint, in.ProfileEndpoint, in.ClientID, in.SubjectMapping})
	return string(b)
}
func validateConnectionRules(c *ent.AuthConnectionRevision, rules ...authrules.RuleSet) error {
	scopes, userinfo := c.OidcScopes, c.OidcUserinfoEnabled
	if configProtocol(c) == "oauth2" {
		scopes = c.Oauth2Scopes
		userinfo = false
	}
	return authrules.ValidateScopes(configProtocol(c), c.AttributeMappings, scopes, userinfo, rules...)
}
func (s *ExternalAuthService) externalOAuthConfig(ctx context.Context, p externalConnection) (authoauth2.Config, error) {
	cfg := oauthConfig(oauthInput(p.Config), connectionAuthority(p))
	// Credential encryption is independent of the selected protocol.
	secret, err := s.connectionClientSecret(ctx, s.client, p.Config)
	if err != nil {
		return cfg, err
	}
	cfg.ClientSecret = secret
	return cfg, nil
}
func (s *ExternalAuthService) buildAuthorization(ctx context.Context, p externalConnection, r authprotocol.AuthorizationRequest) (authprotocol.Authorization, error) {
	if p.Connection.Kind == authconnection.KindGenericOauth2 {
		cfg, err := s.externalOAuthConfig(ctx, p)
		if err != nil {
			return authprotocol.Authorization{}, err
		}
		return s.oauth2Protocol.BuildAuthorization(ctx, cfg, r)
	}
	cfg, err := s.externalOIDCConfig(ctx, s.client, p.Config)
	if err != nil {
		return authprotocol.Authorization{}, err
	}
	return s.protocol.BuildAuthorization(ctx, cfg, r)
}
func (s *ExternalAuthService) verifyExternal(ctx context.Context, p externalConnection, r authprotocol.ExchangeRequest) (authprotocol.Identity, error) {
	if p.Connection.Kind == authconnection.KindGenericOauth2 {
		cfg, err := s.externalOAuthConfig(ctx, p)
		if err != nil {
			return authprotocol.Identity{}, err
		}
		return s.oauth2Protocol.ExchangeAndVerify(ctx, cfg, r)
	}
	cfg, err := s.externalOIDCConfig(ctx, s.client, p.Config)
	if err != nil {
		return authprotocol.Identity{}, err
	}
	identity, err := s.protocol.ExchangeAndVerify(ctx, cfg, r)
	// OIDC's verified issuer is the authority, never a profile mapping.
	identity.Protocol = "oidc"
	identity.Authority = identity.Issuer
	return identity, err
}
func hasAuthenticationValidation(ctx context.Context, c *ent.Client, id, fingerprint string) (bool, error) {
	return c.AuthConnectionValidation.Query().Where(authconnectionvalidation.ConnectionIDEQ(id), authconnectionvalidation.FingerprintEQ(fingerprint)).Exist(ctx)
}
func signupRequirements(ctx context.Context, c *ent.Client, p externalConnection) ([]string, bool, error) {
	fresh, err := hasFreshValidation(ctx, c, p.Connection.ID, connectionFingerprint(p.Config))
	if err != nil {
		return nil, false, err
	}
	if fresh && p.Connection.Kind == authconnection.KindOidc {
		return []string{}, true, nil
	}
	password, err := readPasswordPolicy(ctx, c)
	if err != nil {
		return nil, false, err
	}
	return []string{"local_password"}, password.canLogin() && password.Policy.LinkEnabled, nil
}
func validateIdentityResult(p externalConnection, i authprotocol.Identity) error {
	if i.Protocol != configProtocol(p.Config) || i.Authority != connectionAuthority(p) || i.Subject == "" {
		return fmt.Errorf("%w: identity source mismatch", ErrExternalFlowInvalid)
	}
	return nil
}

func freshCapabilityChanged(ctx context.Context, c *ent.Client, a, b externalConnection) (bool, error) {
	old, err := hasFreshValidation(ctx, c, a.Connection.ID, connectionFingerprint(a.Config))
	if err != nil {
		return false, err
	}
	next, err := hasFreshValidation(ctx, c, b.Connection.ID, connectionFingerprint(b.Config))
	return old != next, err
}

// A draft may predate the first binding. Recheck at every publication boundary
// under the same connection lock used by registration and explicit linking.
func ensureIdentityContract(ctx context.Context, c *ent.Client, p externalConnection) error {
	if p.Connection.Kind != authconnection.KindGenericOauth2 || p.Connection.PublishedRevision == nil {
		return nil
	}
	bound, err := c.LoginBinding.Query().Where(loginbinding.ConnectionIDEQ(p.Connection.ID)).Exist(ctx)
	if err != nil || !bound {
		return err
	}
	published, err := readExternalConnection(ctx, c, p.Connection.ID, 0)
	if err != nil {
		return err
	}
	if oauthIdentityContract(oauthInput(p.Config)) != oauthIdentityContract(oauthInput(published.Config)) {
		return ErrAuthConnectionConflict
	}
	return nil
}

// Match configuration publication's stable row order before locking a user.
func lockSignupConnections(ctx context.Context, c *ent.Client, id string) error {
	ids := []string{id, PasswordConnectionID}
	sort.Strings(ids)
	for _, id := range ids {
		if err := lockAuthConnection(ctx, c, id); err != nil {
			return err
		}
	}
	return nil
}
