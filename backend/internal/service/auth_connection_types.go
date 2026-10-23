package service

import (
	"encoding/json"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
)

type OIDCConnectionInput struct {
	Issuer           string   `json:"issuer"`
	ClientID         string   `json:"client_id"`
	ClientAuthMethod string   `json:"client_auth_method"`
	Scopes           []string `json:"scopes"`
	UserinfoEnabled  *bool    `json:"userinfo_enabled,omitempty"`
}

type AuthConnectionPolicyInput struct {
	LoginEnabled  bool                     `json:"login_enabled" auth:"required"`
	SignupEnabled bool                     `json:"signup_enabled" auth:"required"`
	LinkEnabled   bool                     `json:"link_enabled" auth:"required"`
	LoginRules    authrules.RuleSet        `json:"login_rules" auth:"required"`
	SignupRules   authrules.RuleSet        `json:"signup_rules" auth:"required"`
	LinkRules     authrules.RuleSet        `json:"link_rules" auth:"required"`
	Signup        SignupPolicyRequirements `json:"signup" auth:"required"`
}

type CreateAuthConnectionInput struct {
	Kind         string                 `json:"kind"`
	DisplayName  string                 `json:"display_name"`
	Description  string                 `json:"description"`
	DisplayOrder int                    `json:"display_order"`
	OIDC         *OIDCConnectionInput   `json:"oidc,omitempty"`
	OAuth2       *OAuth2ConnectionInput `json:"oauth2,omitempty"`
	ClientSecret string                 `json:"client_secret,omitempty"`
}

type AuthConnectionChanges struct {
	AttributeMappings     *[]authrules.Field         `json:"attribute_mappings,omitempty"`
	EvidenceMaxAgeSeconds *int                       `json:"evidence_max_age_seconds,omitempty"`
	DisplayName           *string                    `json:"display_name,omitempty"`
	Description           *string                    `json:"description,omitempty"`
	DisplayOrder          *int                       `json:"display_order,omitempty"`
	OAuth2                *OAuth2ConnectionInput     `json:"oauth2,omitempty"`
	OIDC                  *OIDCConnectionInput       `json:"oidc,omitempty"`
	ClientSecret          json.RawMessage            `json:"client_secret,omitempty"`
	Policy                *AuthConnectionPolicyInput `json:"policy,omitempty"`
}

type UpdateAuthConnectionInput struct {
	AuthConnectionChanges
	ExpectedRevision int64  `json:"expected_revision"`
	TestID           string `json:"test_id,omitempty"`
	Verifier         string `json:"verifier,omitempty"`
}
type AuthConnectionCandidateInput struct {
	ExpectedRevision int64                  `json:"expected_revision"`
	Changes          *AuthConnectionChanges `json:"changes,omitempty"`
}
type AuthConnectionActionInput struct {
	ExpectedRevision int64  `json:"expected_revision"`
	TestID           string `json:"test_id,omitempty"`
	Verifier         string `json:"verifier,omitempty"`
}
type AuthConnectionTestInput struct {
	AuthConnectionCandidateInput
	ExternalBrowserInput
}

type AuthConnectionSnapshot struct {
	AttributeMappings     []authrules.Field         `json:"attribute_mappings"`
	EvidenceMaxAgeSeconds int                       `json:"evidence_max_age_seconds"`
	LoginPolicyRevision   int64                     `json:"login_policy_revision"`
	SignupPolicyRevision  int64                     `json:"signup_policy_revision"`
	LinkPolicyRevision    int64                     `json:"link_policy_revision"`
	DisplayName           string                    `json:"display_name"`
	Description           string                    `json:"description"`
	DisplayOrder          int                       `json:"display_order"`
	OAuth2                *OAuth2ConnectionInput    `json:"oauth2,omitempty"`
	OIDC                  *OIDCConnectionInput      `json:"oidc,omitempty"`
	Policy                AuthConnectionPolicyInput `json:"policy"`
	HasClientSecret       bool                      `json:"has_client_secret"`
	LogoURL               *string                   `json:"logo_url"`
}
type AuthConnectionValidationResult struct {
	FreshSupported bool       `json:"fresh_supported"`
	TestedAt       *time.Time `json:"tested_at"`
	AppliedTestID  string     `json:"applied_test_id,omitempty"`
}
type AuthConfigurationIssue struct {
	Code    string `json:"code"`
	Field   string `json:"field"`
	Message string `json:"message"`
}
type AuthConnectionProtocolSetup struct {
	RequiredFields    []string `json:"required_fields"`
	ClientAuthMethods []string `json:"client_auth_methods"`
	ScopeGuidance     string   `json:"scope_guidance"`
}
type AuthConnectionSetup struct {
	CallbackURL           *string                     `json:"callback_url"`
	CompletionURLs        []string                    `json:"completion_urls"`
	ConfigurationComplete bool                        `json:"configuration_complete"`
	Issues                []AuthConfigurationIssue    `json:"issues"`
	Protocol              AuthConnectionProtocolSetup `json:"protocol"`
}
type AuthConnectionCapabilities struct {
	CanLogin  bool `json:"can_login"`
	CanSignup bool `json:"can_signup"`
	CanLink   bool `json:"can_link"`
	CanTest   bool `json:"can_test"`
}
type AuthConnectionRecord struct {
	IdentityAuthorityID   *string                        `json:"identity_authority_id,omitempty"`
	ID                    string                         `json:"id"`
	Kind                  string                         `json:"kind"`
	AggregateRevision     int64                          `json:"aggregate_revision"`
	State                 string                         `json:"state,omitempty"`
	Archived              bool                           `json:"archived"`
	Configuration         *AuthConnectionSnapshot        `json:"configuration"`
	ConfigurationComplete bool                           `json:"configuration_complete"`
	Capabilities          AuthConnectionCapabilities     `json:"capabilities"`
	Validation            AuthConnectionValidationResult `json:"validation"`
	Setup                 AuthConnectionSetup            `json:"setup"`
}
type AuthConnectionCheckResult struct {
	Revision       int64                    `json:"revision"`
	Rules          AuthRulesDiagnostics     `json:"rules"`
	Valid          bool                     `json:"valid"`
	FreshSupported bool                     `json:"fresh_supported"`
	Issues         []AuthConfigurationIssue `json:"issues"`
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
	CanSave            bool      `json:"can_save"`
	AffectsSignup      bool      `json:"affects_signup"`
}

// AuthConnectionError distinguishes an actionable configuration problem from
// transport failure, authorization failure and a stale management revision.
type AuthConnectionError struct {
	Code   string
	Issues []AuthConfigurationIssue
	Impact *AuthConnectionImpactResult
}

func (e *AuthConnectionError) Error() string { return "authentication connection: " + e.Code }
func (e *AuthConnectionError) Unwrap() error { return ErrAuthConnectionConflict }
func connectionError(code string) error      { return &AuthConnectionError{Code: code} }
