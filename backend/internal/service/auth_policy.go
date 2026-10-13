package service

import (
	"context"
	"fmt"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authchannelpolicy"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
)

const PasswordConnectionID = "password"
const maxAuthVersion int64 = 1<<53 - 1

type passwordPolicy struct {
	Connection *ent.AuthConnection
	Config     *ent.AuthConnectionRevision
	Policy     *ent.AuthChannelPolicy
}

func (p passwordPolicy) canLogin() bool {
	return p.Connection.ArchivedAt == nil && p.Policy.Enabled && p.Policy.LoginEnabled && p.Config.UsernameLoginEnabled
}

// initializePasswordConnection runs only inside first-instance initialization.
// Reads never recreate missing policy or overwrite an administrator's version.
func initializePasswordConnection(ctx context.Context, client *ent.Client) error {
	if err := client.AuthConnection.Create().SetID(PasswordConnectionID).SetKind(authconnection.KindPassword).SetPublishedRevision(1).Exec(ctx); err != nil {
		return err
	}
	if err := client.AuthConnectionRevision.Create().SetConnectionID(PasswordConnectionID).SetRevision(1).
		SetDisplayName("Password").SetUsernameLoginEnabled(true).Exec(ctx); err != nil {
		return err
	}
	return client.AuthChannelPolicy.Create().SetConnectionID(PasswordConnectionID).SetRevision(1).SetLoginPolicyRevision(1).
		SetEnabled(true).SetLoginEnabled(true).SetSignupEnabled(true).SetLinkEnabled(true).
		SetLoginRules(authrules.Unrestricted()).SetSignupRules(authrules.Unrestricted()).
		SetLinkRules(authrules.Unrestricted()).Exec(ctx)
}

func readPasswordPolicy(ctx context.Context, client *ent.Client) (passwordPolicy, error) {
	var p passwordPolicy
	connection, err := client.AuthConnection.Get(ctx, PasswordConnectionID)
	if err != nil {
		return p, fmt.Errorf("%w: password connection: %w", ErrSettingsUnavailable, err)
	}
	if connection.Kind != authconnection.KindPassword || connection.PublishedRevision == nil || *connection.PublishedRevision < 1 || *connection.PublishedRevision > maxAuthVersion || connection.AuthGeneration < 1 || connection.AuthGeneration > maxAuthVersion {
		return p, fmt.Errorf("%w: invalid password connection version", ErrSettingsUnavailable)
	}
	revision := *connection.PublishedRevision
	cfg, err := client.AuthConnectionRevision.Query().Where(authconnectionrevision.ConnectionIDEQ(PasswordConnectionID), authconnectionrevision.RevisionEQ(revision)).Only(ctx)
	if err != nil {
		return p, fmt.Errorf("%w: password configuration: %w", ErrSettingsUnavailable, err)
	}
	policy, err := client.AuthChannelPolicy.Query().Where(authchannelpolicy.ConnectionIDEQ(PasswordConnectionID), authchannelpolicy.RevisionEQ(revision)).Only(ctx)
	if err != nil {
		return p, fmt.Errorf("%w: password policy: %w", ErrSettingsUnavailable, err)
	}
	if cfg.ConfigVersion != 1 || cfg.EmailLoginEnabled || policy.LoginPolicyRevision < 1 || policy.LoginPolicyRevision > maxAuthVersion ||
		policy.LoginRules.Mode != "unrestricted" || policy.SignupRules.Mode != "unrestricted" || policy.LinkRules.Mode != "unrestricted" ||
		authrules.Validate("oidc", nil, policy.LoginRules, policy.SignupRules, policy.LinkRules) != nil ||
		(policy.SignupEnabled && !policy.LoginEnabled) || (policy.LoginEnabled && !cfg.UsernameLoginEnabled) {
		return p, fmt.Errorf("%w: unsupported password policy", ErrSettingsUnavailable)
	}
	return passwordPolicy{Connection: connection, Config: cfg, Policy: policy}, nil
}

type PasswordCapabilities struct {
	CanLogin             bool `json:"can_login"`
	CanSignup            bool `json:"can_signup"`
	UsernameLoginEnabled bool `json:"username_login_enabled"`
	EmailLoginEnabled    bool `json:"email_login_enabled"`
}

type AuthCapabilities struct {
	Mode                     string                           `json:"mode"`
	Password                 PasswordCapabilities             `json:"password"`
	Connections              []PublicAuthConnectionCapability `json:"connections"`
	EmailVerificationEnabled bool                             `json:"email_verification_enabled"`
	PasswordResetEnabled     bool                             `json:"password_reset_enabled"`
}

type PublicAuthConnectionCapability struct {
	ConnectionID       string   `json:"connection_id"`
	DisplayName        string   `json:"display_name"`
	Description        string   `json:"description"`
	DisplayOrder       int      `json:"display_order"`
	LogoURL            *string  `json:"logo_url,omitempty"`
	CanLogin           bool     `json:"can_login"`
	CanSignup          bool     `json:"can_signup"`
	SignupRequirements []string `json:"signup_requirements"`
}

func (s *AuthService) Capabilities(ctx context.Context) (AuthCapabilities, error) {
	result := AuthCapabilities{Mode: "server", Connections: []PublicAuthConnectionCapability{}}
	p, err := readPasswordPolicy(ctx, s.client)
	if err != nil {
		return result, err
	}
	registration, err := readRegistrationPolicy(ctx, s.client)
	if err != nil {
		return result, fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
	}
	result.Password = PasswordCapabilities{CanLogin: p.canLogin(), CanSignup: registration && p.canLogin() && p.Policy.SignupEnabled, UsernameLoginEnabled: p.canLogin()}
	connections, err := s.client.AuthConnection.Query().All(ctx)
	if err != nil {
		return result, err
	}
	for _, connection := range connections {
		if connection.ID == PasswordConnectionID || connection.PublishedRevision == nil || connection.ArchivedAt != nil {
			continue
		}
		x, err := readExternalConnection(ctx, s.client, connection.ID, 0)
		if err != nil {
			continue
		}
		if !x.Policy.Enabled {
			continue
		}
		requirements, initializable, err := signupRequirements(ctx, s.client, x)
		if err != nil {
			return result, err
		}
		result.Connections = append(result.Connections, PublicAuthConnectionCapability{ConnectionID: connection.ID, DisplayName: x.Config.DisplayName, Description: x.Config.Description, DisplayOrder: x.Config.DisplayOrder, CanLogin: x.Policy.LoginEnabled, CanSignup: registration && x.Policy.LoginEnabled && x.Policy.SignupEnabled && initializable, SignupRequirements: requirements})
	}
	sort.Slice(result.Connections, func(i, j int) bool { return result.Connections[i].DisplayOrder < result.Connections[j].DisplayOrder })
	return result, nil
}

// Connection locks precede user/session locks. A no-op SQL update takes the
// same database row lock on PostgreSQL and write reservation on SQLite without
// changing a display timestamp or relying on an in-process mutex.
func lockPasswordConnection(ctx context.Context, client *ent.Client) error {
	result, err := client.ExecContext(ctx, "UPDATE auth_connections SET auth_generation = auth_generation WHERE id = 'password'")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrSettingsUnavailable
	}
	return nil
}
