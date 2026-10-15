package service

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authhttp"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
)

func (s *ExternalAuthService) connectionSetup(p externalConnection) AuthConnectionSetup {
	out := AuthConnectionSetup{CompletionURLs: append([]string{}, s.cfg.CompletionURLs...), Issues: []AuthConfigurationIssue{},
		Protocol: AuthConnectionProtocolSetup{ClientAuthMethods: []string{"client_secret_basic", "client_secret_post"}}}
	missing := func(field string) {
		out.Issues = append(out.Issues, AuthConfigurationIssue{Code: "required", Field: field, Message: "A value is required"})
	}
	if p.Connection.Kind == authconnection.KindPassword {
		out.ConfigurationComplete = true
		out.Protocol.RequiredFields = []string{}
		out.Protocol.ClientAuthMethods = []string{}
		return out
	}
	if p.Connection.Kind == authconnection.KindOidc {
		out.Protocol.RequiredFields = []string{"oidc.issuer", "oidc.client_id", "client_secret"}
		out.Protocol.ScopeGuidance = "openid is required; request only the scopes needed by the configured attributes"
		if p.Config.OidcIssuer == "" {
			missing("oidc.issuer")
		}
		if p.Config.OidcClientID == "" {
			missing("oidc.client_id")
		}
	} else {
		out.Protocol.RequiredFields = []string{"oauth2.authorization_endpoint", "oauth2.token_endpoint", "oauth2.profile_endpoint", "oauth2.client_id", "oauth2.subject_mapping.pointer", "client_secret"}
		out.Protocol.ScopeGuidance = "Configure the provider's documented scopes for its current-user endpoint and selected attributes"
		for _, v := range []struct{ field, value string }{{"authorization_endpoint", p.Config.Oauth2AuthorizationEndpoint}, {"token_endpoint", p.Config.Oauth2TokenEndpoint}, {"profile_endpoint", p.Config.Oauth2ProfileEndpoint}, {"client_id", p.Config.Oauth2ClientID}, {"subject_mapping.pointer", p.Config.Oauth2SubjectPointer}} {
			if v.value == "" {
				missing("oauth2." + v.field)
			}
		}
	}
	if (p.SecretOverride == nil && connectionSecretVersion(p.Config) == 0) || (p.SecretOverride != nil && *p.SecretOverride == "") {
		missing("client_secret")
	}
	out.ConfigurationComplete = len(out.Issues) == 0
	if u, err := s.callbackURL(p.Connection.ID); err == nil {
		out.CallbackURL = &u
	} else {
		missing("server.authentication.public_base_url")
	}
	if len(out.CompletionURLs) == 0 {
		missing("server.authentication.completion_urls")
	}
	if base, err := url.Parse(s.cfg.PublicBaseURL); err == nil && base.Host != "" {
		origin := func(u *url.URL) string { return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) }
		allowed := map[string]bool{}
		for _, v := range s.corsOrigins {
			allowed[strings.ToLower(v)] = true
		}
		for _, v := range out.CompletionURLs {
			u, err := url.Parse(v)
			if err == nil && origin(u) != origin(base) && !allowed[origin(u)] {
				out.Issues = append(out.Issues, AuthConfigurationIssue{Code: "cors-origin-required", Field: "server.cors.allowed_origins", Message: "Allow the exact origin of the selected cross-origin completion page"})
				break
			}
		}
	}
	if s.keys == nil || !s.keys.HasKey(s.keys.ActiveKeyID()) {
		missing("credentials.keyring")
	}
	return out
}

func (s *ExternalAuthService) requireConnectionReady(p externalConnection) error {
	setup := s.connectionSetup(p)
	if !setup.ConfigurationComplete {
		return &AuthConnectionError{Code: "configuration-incomplete", Issues: setup.Issues}
	}
	if len(setup.Issues) > 0 {
		return &AuthConnectionError{Code: "deployment-incomplete", Issues: setup.Issues}
	}
	return nil
}

func connectionSnapshot(p externalConnection) *AuthConnectionSnapshot {
	out := &AuthConnectionSnapshot{DisplayName: p.Config.DisplayName, Description: p.Config.Description, DisplayOrder: p.Config.DisplayOrder,
		AttributeMappings: append([]authrules.Field{}, p.Config.AttributeMappings...), EvidenceMaxAgeSeconds: p.Config.EvidenceMaxAgeSeconds,
		LoginPolicyRevision: p.Policy.LoginPolicyRevision, Policy: policyInput(p.Policy), HasClientSecret: connectionSecretVersion(p.Config) > 0}
	if p.SecretOverride != nil {
		out.HasClientSecret = *p.SecretOverride != ""
	}
	if p.Connection.Kind == authconnection.KindOidc {
		enabled := p.Config.OidcUserinfoEnabled
		out.OIDC = &OIDCConnectionInput{p.Config.OidcIssuer, p.Config.OidcClientID, string(p.Config.OidcClientAuthMethod), append([]string{}, p.Config.OidcScopes...), &enabled}
	}
	if p.Connection.Kind == authconnection.KindGenericOauth2 {
		v := oauthInput(p.Config)
		out.OAuth2 = &v
	}
	if len(p.Config.LogoData) > 0 {
		u := "/api/v1/admin/auth-connections/" + url.PathEscape(p.Connection.ID) + "/logo"
		out.LogoURL = &u
	}
	return out
}

func (s *ExternalAuthService) connectionRecord(ctx context.Context, c *ent.Client, row *ent.AuthConnection) (AuthConnectionRecord, error) {
	p, err := readExternalConnection(ctx, c, row.ID, 0)
	if err != nil {
		return AuthConnectionRecord{}, err
	}
	// A list may have fetched this connection before a concurrent save. Keep
	// its revision and state paired with the configuration actually projected.
	row = p.Connection
	setup := s.connectionSetup(p)
	out := AuthConnectionRecord{ID: row.ID, Kind: string(row.Kind), IdentityAuthorityID: row.IdentityAuthorityID, AggregateRevision: row.AggregateRevision, State: string(row.State), Archived: row.ArchivedAt != nil, Configuration: connectionSnapshot(p), ConfigurationComplete: setup.ConfigurationComplete, Setup: setup,
		Validation: AuthConnectionValidationResult{FreshSupported: p.Config.FreshSupported, TestedAt: p.Config.TestedAt, AppliedTestID: p.Config.AppliedTestID}}
	registration, err := readRegistrationPolicy(ctx, c)
	if err != nil {
		return out, err
	}
	active := row.ArchivedAt == nil && p.Policy.Enabled
	_, initializable, err := signupRequirements(ctx, c, p)
	if err != nil {
		return out, err
	}
	out.Capabilities = AuthConnectionCapabilities{CanLogin: active && p.Policy.LoginEnabled, CanLink: active && p.Policy.LinkEnabled,
		CanSignup: active && p.Policy.LoginEnabled && p.Policy.SignupEnabled && registration && initializable,
		CanTest:   supportedExternalKind(row.Kind) && row.ArchivedAt == nil && row.State != authconnection.StateDisabled && setup.ConfigurationComplete && len(setup.Issues) == 0}
	return out, nil
}

func (s *ExternalAuthService) applyConnectionChanges(ctx context.Context, c *ent.Client, p externalConnection, in *AuthConnectionChanges) (externalConnection, error) {
	if in == nil {
		return p, nil
	}
	cfg, policy := *p.Config, *p.Policy
	p.Config, p.Policy = &cfg, &policy
	if in.DisplayName != nil {
		cfg.DisplayName = strings.TrimSpace(*in.DisplayName)
	}
	if in.Description != nil {
		cfg.Description = *in.Description
	}
	if in.DisplayOrder != nil {
		cfg.DisplayOrder = *in.DisplayOrder
	}
	if in.AttributeMappings != nil {
		cfg.AttributeMappings = append([]authrules.Field{}, (*in.AttributeMappings)...)
	}
	if in.EvidenceMaxAgeSeconds != nil {
		cfg.EvidenceMaxAgeSeconds = *in.EvidenceMaxAgeSeconds
	}
	if in.Policy != nil {
		v := *in.Policy
		policy.LoginEnabled = v.LoginEnabled
		policy.SignupEnabled = v.SignupEnabled
		policy.LinkEnabled = v.LinkEnabled
		policy.LoginRules = v.LoginRules
		policy.SignupRules = v.SignupRules
		policy.LinkRules = v.LinkRules
	}
	if in.OIDC != nil {
		if p.Connection.Kind != authconnection.KindOidc {
			return p, ErrInvalidInput
		}
		v, err := normalizeOIDCInput(*in.OIDC)
		if err != nil {
			return p, err
		}
		if v.Issuer != "" && !(&authhttp.Transport{Deployment: s.cfg}).ValidRawURL(v.Issuer) {
			return p, ErrInvalidInput
		}
		sort.Strings(v.Scopes)
		cfg.OidcIssuer = v.Issuer
		cfg.OidcClientID = v.ClientID
		cfg.OidcClientAuthMethod = authconnectionrevision.OidcClientAuthMethod(v.ClientAuthMethod)
		cfg.OidcScopes = v.Scopes
		cfg.OidcUserinfoEnabled = *v.UserinfoEnabled
	}
	if in.OAuth2 != nil {
		if p.Connection.Kind != authconnection.KindGenericOauth2 {
			return p, ErrInvalidInput
		}
		v, err := s.normalizeOAuthInput(ctx, *in.OAuth2)
		if err != nil {
			return p, err
		}
		setOAuthInput(&cfg, v)
	}
	if len(in.ClientSecret) > 0 {
		var value string
		if string(in.ClientSecret) != "null" {
			if err := json.Unmarshal(in.ClientSecret, &value); err != nil || value == "" || len(value) > 16384 {
				return p, ErrInvalidInput
			}
		}
		old, err := s.connectionClientSecret(ctx, c, p.Config)
		if err != nil {
			return p, err
		}
		if subtle.ConstantTimeCompare([]byte(old), []byte(value)) != 1 {
			p.SecretOverride = &value
			cfg.SecretVersion = nil
		}
	}
	if cfg.DisplayName == "" || len(cfg.DisplayName) > 120 || len(cfg.Description) > 2000 || cfg.EvidenceMaxAgeSeconds < 1 || cfg.EvidenceMaxAgeSeconds > 31536000 || !validConnectionPolicy(policyInput(&policy)) {
		return p, ErrInvalidInput
	}
	if err := authrules.Validate(configProtocol(&cfg), cfg.AttributeMappings, policy.LoginRules, policy.SignupRules, policy.LinkRules); err != nil {
		return p, ErrInvalidInput
	}
	if err := validateConnectionRules(&cfg, policy.LoginRules, policy.SignupRules, policy.LinkRules); err != nil {
		return p, ErrInvalidInput
	}
	if err := ensureIdentityContract(ctx, c, p); err != nil {
		return p, err
	}
	return p, nil
}

func (s *ExternalAuthService) candidateConnection(ctx context.Context, actorID int, id string, in AuthConnectionCandidateInput) (externalConnection, error) {
	if err := checkConnectionAdmin(ctx, s.client, actorID); err != nil {
		return externalConnection{}, err
	}
	p, err := readExternalConnection(ctx, s.client, id, 0)
	if err != nil {
		return p, err
	}
	if in.ExpectedRevision < 1 {
		return p, ErrInvalidInput
	}
	if p.Connection.AggregateRevision != in.ExpectedRevision {
		return p, connectionError("revision-conflict")
	}
	if !supportedExternalKind(p.Connection.Kind) {
		return p, ErrInvalidInput
	}
	return s.applyConnectionChanges(ctx, s.client, p, in.Changes)
}

func (s *ExternalAuthService) CheckConnection(ctx context.Context, actorID int, id string, in AuthConnectionCandidateInput) (AuthConnectionCheckResult, error) {
	p, err := s.candidateConnection(ctx, actorID, id, in)
	if err != nil {
		return AuthConnectionCheckResult{}, err
	}
	setup := s.connectionSetup(p)
	return AuthConnectionCheckResult{Revision: p.Connection.AggregateRevision, Rules: diagnostics(p, authrules.Evidence{}, s.now()), Valid: setup.ConfigurationComplete && len(setup.Issues) == 0, FreshSupported: p.Config.FreshSupported, Issues: setup.Issues}, nil
}

func (s *ExternalAuthService) ImpactConnection(ctx context.Context, actorID int, id string, in AuthConnectionCandidateInput) (AuthConnectionImpactResult, error) {
	p, err := s.candidateConnection(ctx, actorID, id, in)
	if err != nil {
		return AuthConnectionImpactResult{}, err
	}
	current, err := readExternalConnection(ctx, s.client, id, 0)
	if err != nil {
		return AuthConnectionImpactResult{}, err
	}
	if !loginRulesChanged(current, p) && current.Policy.LoginEnabled == p.Policy.LoginEnabled && current.Config.FreshSupported == p.Config.FreshSupported {
		return emptyAuthImpact(p.Connection.AggregateRevision, s.now()), nil
	}
	return candidateImpact(ctx, s.client, p, s.now(), !p.Policy.Enabled || !p.Policy.LoginEnabled)
}
