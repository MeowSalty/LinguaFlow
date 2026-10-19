package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authhttp"
	"net/url"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authoauth2"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authprotocol"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
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
	if c.Oauth2ClientAuthMethod != "" {
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
	if in.ClientAuthMethod != "client_secret_basic" && in.ClientAuthMethod != "client_secret_post" {
		return in, ErrInvalidInput
	}
	if len(in.ClientID) > 1024 {
		return in, ErrInvalidInput
	}
	if in.SubjectMapping.Type == "" {
		in.SubjectMapping.Type = "string"
	}
	if in.SubjectMapping.Type != "string" && in.SubjectMapping.Type != "integer" {
		return in, ErrInvalidInput
	}
	if in.SubjectMapping.Pointer != "" && authrules.ValidatePointer(in.SubjectMapping.Pointer) != nil {
		return in, ErrInvalidInput
	}
	for _, endpoint := range []string{in.AuthorizationEndpoint, in.TokenEndpoint, in.ProfileEndpoint} {
		if endpoint == "" {
			continue
		}
		if len(endpoint) > 2048 || !(&authhttp.Transport{Deployment: s.cfg}).ValidRawURL(endpoint) {
			return in, ErrInvalidInput
		}
		u, _ := url.Parse(endpoint)
		q, e := url.ParseQuery(u.RawQuery)
		if e != nil {
			return in, ErrInvalidInput
		}
		for _, name := range []string{"response_type", "client_id", "client_secret", "redirect_uri", "scope", "state", "code", "code_verifier", "code_challenge", "code_challenge_method", "grant_type", "access_token"} {
			if q.Has(name) {
				return in, ErrInvalidInput
			}
		}
	}
	seen := map[string]bool{}
	if len(in.Scopes) > 32 {
		return in, ErrInvalidInput
	}
	for _, scope := range in.Scopes {
		if scope == "" || len(scope) > 128 || strings.ContainsAny(scope, " \t\r\n") || seen[scope] {
			return in, ErrInvalidInput
		}
		seen[scope] = true
	}
	in.Scopes = append([]string{}, in.Scopes...)
	sort.Strings(in.Scopes)
	return in, nil
}

func oauthIdentityContract(in OAuth2ConnectionInput) string {
	b, _ := json.Marshal([]any{in.AuthorizationEndpoint, in.TokenEndpoint, in.ProfileEndpoint, in.ClientID, in.SubjectMapping})
	return string(b)
}
func (s *ExternalAuthService) externalOAuthConfig(ctx context.Context, p externalConnection) (authoauth2.Config, error) {
	cfg := oauthConfig(oauthInput(p.Config), connectionAuthority(p))
	// Credential encryption is independent of the selected protocol.
	secret, err := s.connectionClientSecret(ctx, s.client, p.Config)
	if err != nil {
		return cfg, err
	}
	cfg.ClientSecret = secret
	if p.SecretOverride != nil {
		cfg.ClientSecret = *p.SecretOverride
	}
	return cfg, nil
}
func (s *ExternalAuthService) buildAuthorization(ctx context.Context, p externalConnection, r authprotocol.AuthorizationRequest) (authprotocol.Authorization, error) {
	ctx, release, gateErr := s.enterProvider(ctx, p)
	if gateErr != nil {
		return authprotocol.Authorization{}, gateErr
	}
	defer release()
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
	if p.SecretOverride != nil {
		cfg.ClientSecret = *p.SecretOverride
	}
	return s.protocol.BuildAuthorization(ctx, cfg, r)
}
func (s *ExternalAuthService) verifyExternal(ctx context.Context, p externalConnection, r authprotocol.ExchangeRequest) (authprotocol.Identity, error) {
	ctx, release, gateErr := s.enterProvider(ctx, p)
	if gateErr != nil {
		return authprotocol.Identity{}, gateErr
	}
	defer release()
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
	if p.SecretOverride != nil {
		cfg.ClientSecret = *p.SecretOverride
	}
	identity, err := s.protocol.ExchangeAndVerify(ctx, cfg, r)
	// OIDC's verified issuer is the authority, never a profile mapping.
	identity.Protocol = "oidc"
	identity.Authority = identity.Issuer
	return identity, err
}
func validateIdentityResult(p externalConnection, i authprotocol.Identity) error {
	if i.Protocol != configProtocol(p.Config) || i.Authority != connectionAuthority(p) || i.Subject == "" {
		return fmt.Errorf("%w: identity source mismatch", ErrExternalFlowInvalid)
	}
	return nil
}

func freshCapabilityChanged(ctx context.Context, c *ent.Client, a, b externalConnection) (bool, error) {
	return a.Config.FreshSupported != b.Config.FreshSupported, nil
}
func identityContract(p externalConnection) string {
	if p.Connection.Kind == authconnection.KindGenericOauth2 {
		return oauthIdentityContract(oauthInput(p.Config))
	}
	data, _ := json.Marshal([]string{p.Config.OidcIssuer, p.Config.OidcClientID})
	return string(data)
}
func ensureIdentityContract(ctx context.Context, c *ent.Client, p externalConnection) error {
	if p.Connection.IdentityContract != "" && p.Connection.IdentityContract != identityContract(p) {
		return connectionError("state-conflict")
	}
	return nil
}

// Match configuration publication's stable row order before locking a user.
func lockSignupConnections(ctx context.Context, c *ent.Client, id string) error {
	return lockAuthenticationConfiguration(ctx, c)
}
