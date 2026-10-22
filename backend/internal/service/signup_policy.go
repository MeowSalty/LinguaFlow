package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
)

func externalSignupPolicy(p *ent.AuthChannelPolicy) SignupPolicyRequirements {
	return SignupPolicyRequirements{EmailRequirement: string(p.SignupEmailRequirement), PasswordRequirement: string(p.SignupPasswordRequirement)}
}

func validSignupRequirements(p SignupPolicyRequirements) bool {
	return (p.EmailRequirement == "optional" || p.EmailRequirement == "required") &&
		(p.PasswordRequirement == "none" || p.PasswordRequirement == "optional" || p.PasswordRequirement == "required")
}

func validateConnectionPolicyRules(cfg *ent.AuthConnectionRevision, p AuthConnectionPolicyInput) error {
	if !validConnectionPolicy(p) {
		return ErrInvalidInput
	}
	scopes, userinfo := cfg.OidcScopes, cfg.OidcUserinfoEnabled
	if configProtocol(cfg) == "oauth2" {
		scopes, userinfo = cfg.Oauth2Scopes, false
	}
	for _, entry := range []struct {
		purpose string
		rule    authrules.RuleSet
	}{{"login", p.LoginRules}, {"signup", p.SignupRules}, {"link", p.LinkRules}} {
		if err := authrules.ValidateScopesForPurpose(configProtocol(cfg), cfg.AttributeMappings, scopes, userinfo, entry.purpose, entry.rule); err != nil {
			return err
		}
	}
	return nil
}

func purposePolicyChanged(a, b externalConnection, purpose string) bool {
	var first, second authrules.RuleSet
	var enabledChanged bool
	switch purpose {
	case "signup":
		first, second = a.Policy.SignupRules, b.Policy.SignupRules
		enabledChanged = a.Policy.SignupEnabled != b.Policy.SignupEnabled || externalSignupPolicy(a.Policy) != externalSignupPolicy(b.Policy) || a.Config.FreshSupported != b.Config.FreshSupported
	case "link":
		first, second = a.Policy.LinkRules, b.Policy.LinkRules
		enabledChanged = a.Policy.LinkEnabled != b.Policy.LinkEnabled
	default:
		first, second = a.Policy.LoginRules, b.Policy.LoginRules
		enabledChanged = a.Policy.LoginEnabled != b.Policy.LoginEnabled
	}
	return enabledChanged || sourceTrustFingerprint(a.Config) != sourceTrustFingerprint(b.Config) ||
		authrules.RulesFingerprintForPurpose(configProtocol(a.Config), purpose, first, a.Config.AttributeMappings) != authrules.RulesFingerprintForPurpose(configProtocol(b.Config), purpose, second, b.Config.AttributeMappings) ||
		((first.Mode == "conditions" || second.Mode == "conditions") && a.Config.EvidenceMaxAgeSeconds != b.Config.EvidenceMaxAgeSeconds)
}

func externalSignupIssues(p externalConnection, local LocalAccountPolicyInput) []AuthConfigurationIssue {
	issues := []AuthConfigurationIssue{}
	requirements := externalSignupPolicy(p.Policy)
	if requirements.PasswordRequirement == "optional" || requirements.PasswordRequirement == "required" {
		if !local.PasswordLoginEnabled || !local.UsernameLoginEnabled || !local.PasswordEnrollmentEnabled {
			issues = append(issues, AuthConfigurationIssue{Code: "local-password-unavailable", Field: "policy.signup.password_requirement", Message: "Local password login, username login and password enrollment must be enabled"})
		}
	}
	if requirements.PasswordRequirement != "required" && (p.Connection.Kind != authconnection.KindOidc || !p.Config.FreshSupported) {
		issues = append(issues, AuthConfigurationIssue{Code: "maintenance-path-required", Field: "policy.signup.password_requirement", Message: "Registration without a required local password needs a verified external login and maintenance capability"})
	}
	return issues
}

func signupRequirements(ctx context.Context, c *ent.Client, p externalConnection) ([]string, bool, error) {
	local, err := readPasswordPolicy(ctx, c)
	if err != nil {
		return nil, false, err
	}
	return signupSteps(externalSignupPolicy(p.Policy)), len(externalSignupIssues(p, localAccountInput(local.Local))) == 0, nil
}

func ensureExternalSignupReady(ctx context.Context, c *ent.Client, p externalConnection) error {
	if !p.Policy.SignupEnabled || !p.Policy.Enabled {
		return nil
	}
	local, err := readPasswordPolicy(ctx, c)
	if err != nil {
		return err
	}
	issues := externalSignupIssues(p, localAccountInput(local.Local))
	if len(issues) > 0 {
		return &AuthConnectionError{Code: "configuration-incomplete", Issues: issues}
	}
	return nil
}

func ensureExternalSignupDependencies(ctx context.Context, c *ent.Client, in LocalAccountPolicyInput) error {
	connections, err := c.AuthConnection.Query().Where(authconnection.KindIn(authconnection.KindOidc, authconnection.KindGenericOauth2), authconnection.StateEQ(authconnection.StateEnabled), authconnection.ArchivedAtIsNil()).All(ctx)
	if err != nil {
		return err
	}
	for _, connection := range connections {
		p, err := readExternalConnection(ctx, c, connection.ID, 0)
		if err != nil {
			return err
		}
		if p.Policy.SignupEnabled {
			if issues := externalSignupIssues(p, in); len(issues) != 0 {
				return &AuthConnectionError{Code: "signup-dependency-conflict", Issues: issues}
			}
		}
	}
	return nil
}

func signupDependencyFingerprint(ctx context.Context, c *ent.Client, p externalConnection) (string, error) {
	dependencies := []any{"signup-v1", p.Connection.ID, p.Connection.AuthGeneration, p.Policy.SignupPolicyRevision,
		sourceTrustFingerprint(p.Config), authrules.RulesFingerprintForPurpose(configProtocol(p.Config), "signup", p.Policy.SignupRules, p.Config.AttributeMappings), externalSignupPolicy(p.Policy)}
	if p.Policy.SignupPasswordRequirement != "none" {
		local, err := readPasswordPolicy(ctx, c)
		if err != nil {
			return "", err
		}
		dependencies = append(dependencies, local.Local.LoginPolicyRevision, local.Local.EnrollmentPolicyRevision, local.canEnrollPassword())
	}
	if p.Policy.SignupPasswordRequirement != "required" {
		dependencies = append(dependencies, p.Config.FreshSupported, connectionFingerprint(p.Config))
	}
	data, err := json.Marshal(dependencies)
	if err != nil {
		return "", fmt.Errorf("signup dependencies: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
