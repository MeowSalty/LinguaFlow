package service

import (
	"context"
	"fmt"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
)

const PasswordConnectionID = "password"
const maxAuthVersion int64 = 1<<53 - 1

type passwordPolicy struct {
	Connection *ent.AuthConnection
	Local      *ent.LocalAccountPolicy
}

func (p passwordPolicy) canLogin() bool {
	return p.Local.PasswordLoginEnabled && p.Local.UsernameLoginEnabled && !p.Local.EmailLoginEnabled
}

func (p passwordPolicy) canEnrollPassword() bool {
	return p.canLogin() && p.Local.PasswordEnrollmentEnabled && p.Local.UsernameLoginEnabled
}

type SignupPolicyRequirements struct {
	EmailRequirement    string `json:"email_requirement" auth:"required"`
	PasswordRequirement string `json:"password_requirement" auth:"required"`
}

func signupSteps(signup SignupPolicyRequirements) []string {
	steps := []string{}
	if signup.EmailRequirement == "required" {
		steps = append(steps, "email")
	}
	if signup.PasswordRequirement == "required" {
		steps = append(steps, "local_password")
	}
	return steps
}

// initializePasswordConnection runs only inside first-instance initialization.
// Reads never recreate missing policy or overwrite an administrator's version.
func initializePasswordConnection(ctx context.Context, client *ent.Client) error {
	if err := client.AuthConnection.Create().SetID(PasswordConnectionID).SetKind(authconnection.KindPassword).SetCurrentRevision(1).Exec(ctx); err != nil {
		return err
	}
	return client.LocalAccountPolicy.Create().SetID(1).SetSignupRules(authrules.Unrestricted()).Exec(ctx)
}

func readPasswordPolicy(ctx context.Context, client *ent.Client) (passwordPolicy, error) {
	var p passwordPolicy
	connection, err := client.AuthConnection.Get(ctx, PasswordConnectionID)
	if err != nil {
		return p, fmt.Errorf("%w: password connection: %w", ErrSettingsUnavailable, err)
	}
	if connection.Kind != authconnection.KindPassword || connection.AuthGeneration < 1 || connection.AuthGeneration > maxAuthVersion {
		return p, fmt.Errorf("%w: invalid password connection version", ErrSettingsUnavailable)
	}
	policy, err := client.LocalAccountPolicy.Get(ctx, 1)
	if err != nil {
		return p, fmt.Errorf("%w: password policy: %w", ErrSettingsUnavailable, err)
	}
	if !validLocalAccountPolicy(localAccountInput(policy)) || policy.Revision < 1 || policy.Revision > maxAuthVersion ||
		policy.LoginPolicyRevision < 1 || policy.LoginPolicyRevision > maxAuthVersion || policy.SignupPolicyRevision < 1 || policy.SignupPolicyRevision > maxAuthVersion ||
		policy.EnrollmentPolicyRevision < 1 || policy.EnrollmentPolicyRevision > maxAuthVersion {
		return p, fmt.Errorf("%w: unsupported password policy", ErrSettingsUnavailable)
	}
	return passwordPolicy{Connection: connection, Local: policy}, nil
}

type LocalAccountCapabilities struct {
	CanLogin                  bool                     `json:"can_login"`
	CanSignup                 bool                     `json:"can_signup"`
	CanEnrollPassword         bool                     `json:"can_enroll_password"`
	PasswordLoginEnabled      bool                     `json:"password_login_enabled"`
	PasswordEnrollmentEnabled bool                     `json:"password_enrollment_enabled"`
	UsernameLoginEnabled      bool                     `json:"username_login_enabled"`
	EmailLoginEnabled         bool                     `json:"email_login_enabled"`
	Signup                    SignupPolicyRequirements `json:"signup"`
	SignupRequirements        []string                 `json:"signup_requirements"`
}

func localAccountCapabilities(p passwordPolicy, registration bool) LocalAccountCapabilities {
	signup := SignupPolicyRequirements{EmailRequirement: string(p.Local.SignupEmailRequirement), PasswordRequirement: "required"}
	return LocalAccountCapabilities{CanLogin: p.canLogin(), CanSignup: registration && p.canLogin() && p.Local.SignupEnabled && p.Local.UsernameLoginEnabled,
		CanEnrollPassword: p.canEnrollPassword(), PasswordLoginEnabled: p.Local.PasswordLoginEnabled, PasswordEnrollmentEnabled: p.Local.PasswordEnrollmentEnabled, UsernameLoginEnabled: p.canLogin() && p.Local.UsernameLoginEnabled,
		EmailLoginEnabled: p.canLogin() && p.Local.EmailLoginEnabled, Signup: signup, SignupRequirements: signupSteps(signup)}
}

type AuthCapabilities struct {
	Mode                     string                           `json:"mode"`
	LocalAccount             LocalAccountCapabilities         `json:"local_account"`
	Connections              []PublicAuthConnectionCapability `json:"connections"`
	EmailVerificationEnabled bool                             `json:"email_verification_enabled"`
	PasswordResetEnabled     bool                             `json:"password_reset_enabled"`
}

type PublicAuthConnectionCapability struct {
	ConnectionID       string                   `json:"connection_id"`
	DisplayName        string                   `json:"display_name"`
	Description        string                   `json:"description"`
	DisplayOrder       int                      `json:"display_order"`
	LogoURL            *string                  `json:"logo_url,omitempty"`
	CanLogin           bool                     `json:"can_login"`
	CanSignup          bool                     `json:"can_signup"`
	Signup             SignupPolicyRequirements `json:"signup"`
	SignupRequirements []string                 `json:"signup_requirements"`
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
	result.LocalAccount = localAccountCapabilities(p, registration)
	connections, err := s.client.AuthConnection.Query().All(ctx)
	if err != nil {
		return result, err
	}
	for _, connection := range connections {
		if connection.ID == PasswordConnectionID || connection.State != authconnection.StateEnabled || connection.ArchivedAt != nil {
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
		result.Connections = append(result.Connections, PublicAuthConnectionCapability{ConnectionID: connection.ID, DisplayName: x.Config.DisplayName, Description: x.Config.Description, DisplayOrder: x.Config.DisplayOrder, CanLogin: x.Policy.LoginEnabled, CanSignup: registration && x.Policy.LoginEnabled && x.Policy.SignupEnabled && initializable, Signup: externalSignupPolicy(x.Policy), SignupRequirements: requirements})
	}
	sort.Slice(result.Connections, func(i, j int) bool { return result.Connections[i].DisplayOrder < result.Connections[j].DisplayOrder })
	return result, nil
}

// Connection locks precede user/session locks. A no-op SQL update takes the
// same database row lock on PostgreSQL and write reservation on SQLite without
// changing a display timestamp or relying on an in-process mutex.
func lockPasswordConnection(ctx context.Context, client *ent.Client) error {
	if err := lockSetting(ctx, client, SettingRegistrationEnabled); err != nil {
		return err
	}
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
	result, err = client.ExecContext(ctx, "UPDATE local_account_policies SET revision = revision WHERE id = 1")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
	}
	n, err = result.RowsAffected()
	if err != nil || n != 1 {
		return ErrSettingsUnavailable
	}
	return nil
}
