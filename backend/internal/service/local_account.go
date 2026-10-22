package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/accountidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/localaccountpolicy"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/reauthenticationproof"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshsession"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

type LocalSignupPolicy struct {
	Enabled          bool              `json:"enabled" auth:"required"`
	EmailRequirement string            `json:"email_requirement" auth:"required"`
	Rules            authrules.RuleSet `json:"rules" auth:"required"`
}

type LocalAccountPolicyInput struct {
	PasswordLoginEnabled      bool              `json:"password_login_enabled" auth:"required"`
	UsernameLoginEnabled      bool              `json:"username_login_enabled" auth:"required"`
	EmailLoginEnabled         bool              `json:"email_login_enabled" auth:"required"`
	PasswordEnrollmentEnabled bool              `json:"password_enrollment_enabled" auth:"required"`
	Signup                    LocalSignupPolicy `json:"signup" auth:"required"`
}

type LocalAccountPatchInput struct {
	ExpectedRevision int64                   `json:"expected_revision" auth:"required"`
	Policy           LocalAccountPolicyInput `json:"policy" auth:"required"`
}

type LocalAccountCandidateInput struct {
	ExpectedRevision int64                    `json:"expected_revision" auth:"required"`
	Policy           *LocalAccountPolicyInput `json:"policy,omitempty"`
}

type RegistrationAttributes struct {
	Email *string `json:"email,omitempty"`
}

type LocalAccountPreviewInput struct {
	LocalAccountCandidateInput
	Registration RegistrationAttributes `json:"registration"`
}

type LocalAccountRecord struct {
	Policy       LocalAccountPolicyInput  `json:"policy"`
	Revision     int64                    `json:"revision"`
	Capabilities LocalAccountCapabilities `json:"capabilities"`
	Issues       []AuthConfigurationIssue `json:"issues"`
}

type LocalAccountPreview struct {
	Revision   int64                          `json:"revision"`
	Simulated  bool                           `json:"simulated"`
	Attributes map[string]authrules.Attribute `json:"attributes"`
	Signup     authrules.Result               `json:"signup"`
}

func localAccountInput(p *ent.LocalAccountPolicy) LocalAccountPolicyInput {
	return LocalAccountPolicyInput{PasswordLoginEnabled: p.PasswordLoginEnabled, UsernameLoginEnabled: p.UsernameLoginEnabled,
		EmailLoginEnabled: p.EmailLoginEnabled, PasswordEnrollmentEnabled: p.PasswordEnrollmentEnabled,
		Signup: LocalSignupPolicy{Enabled: p.SignupEnabled, EmailRequirement: string(p.SignupEmailRequirement), Rules: p.SignupRules}}
}

func validLocalAccountPolicy(in LocalAccountPolicyInput) bool {
	if in.EmailLoginEnabled || (in.Signup.EmailRequirement != "optional" && in.Signup.EmailRequirement != "required") {
		return false
	}
	if in.PasswordLoginEnabled {
		if !in.UsernameLoginEnabled {
			return false
		}
	} else if in.Signup.Enabled || in.PasswordEnrollmentEnabled {
		return false
	}
	if in.Signup.Enabled && !in.UsernameLoginEnabled {
		return false
	}
	if in.PasswordEnrollmentEnabled && !in.UsernameLoginEnabled {
		return false
	}
	if referencesRegistrationDomain(in.Signup.Rules) && in.Signup.EmailRequirement != "required" {
		return false
	}
	return authrules.ValidateForPurpose("password", nil, "signup", in.Signup.Rules) == nil
}

func referencesRegistrationDomain(rule authrules.RuleSet) bool {
	for _, condition := range rule.Conditions {
		if condition.FieldID == "registration.email_domain" {
			return true
		}
	}
	return false
}

func sameLocalPolicy(a, b LocalAccountPolicyInput) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// Every cross-source security write acquires the same policy rows before users.
func lockAuthenticationConfiguration(ctx context.Context, c *ent.Client) error {
	if err := lockPasswordConnection(ctx, c); err != nil {
		return err
	}
	ids, err := c.AuthConnection.Query().Where(authconnection.IDNEQ(PasswordConnectionID)).Order(ent.Asc(authconnection.FieldID)).IDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := lockExternalSource(ctx, c, id); err != nil {
			return err
		}
	}
	return nil
}

func normalizeLocalPolicy(in LocalAccountPolicyInput) (LocalAccountPolicyInput, error) {
	rule, err := authrules.NormalizeRuleForPurpose("password", nil, "signup", in.Signup.Rules)
	if err != nil {
		return in, ErrInvalidInput
	}
	in.Signup.Rules = rule
	if !validLocalAccountPolicy(in) {
		return in, ErrInvalidInput
	}
	return in, nil
}

func localSignupChanged(a, b LocalAccountPolicyInput) bool {
	return a.Signup.Enabled != b.Signup.Enabled || a.Signup.EmailRequirement != b.Signup.EmailRequirement ||
		authrules.RulesFingerprintForPurpose("password", "signup", a.Signup.Rules, nil) != authrules.RulesFingerprintForPurpose("password", "signup", b.Signup.Rules, nil)
}

// Caller owns all policy rows. This primitive is also used by deployment recovery.
func writeLocalAccountPolicy(ctx context.Context, c *ent.Client, p passwordPolicy, in LocalAccountPolicyInput, now time.Time) error {
	in, err := normalizeLocalPolicy(in)
	if err != nil {
		return err
	}
	before := localAccountInput(p.Local)
	if sameLocalPolicy(before, in) {
		return nil
	}
	if p.Local.Revision >= maxAuthVersion {
		return ErrAuthConnectionConflict
	}
	loginChanged := before.PasswordLoginEnabled != in.PasswordLoginEnabled || before.UsernameLoginEnabled != in.UsernameLoginEnabled || before.EmailLoginEnabled != in.EmailLoginEnabled
	enrollmentChanged := loginChanged || before.PasswordEnrollmentEnabled != in.PasswordEnrollmentEnabled
	signupChanged := localSignupChanged(before, in)
	q := c.LocalAccountPolicy.UpdateOneID(1).SetRevision(p.Local.Revision + 1).
		SetPasswordLoginEnabled(in.PasswordLoginEnabled).SetUsernameLoginEnabled(in.UsernameLoginEnabled).
		SetEmailLoginEnabled(in.EmailLoginEnabled).SetPasswordEnrollmentEnabled(in.PasswordEnrollmentEnabled).
		SetSignupEnabled(in.Signup.Enabled).SetSignupEmailRequirement(localaccountpolicy.SignupEmailRequirement(in.Signup.EmailRequirement)).SetSignupRules(in.Signup.Rules)
	if loginChanged {
		if p.Local.LoginPolicyRevision >= maxAuthVersion || p.Connection.AuthGeneration >= maxAuthVersion {
			return ErrAuthConnectionConflict
		}
		q.SetLoginPolicyRevision(p.Local.LoginPolicyRevision + 1)
		if err := c.AuthConnection.UpdateOneID(PasswordConnectionID).AddAuthGeneration(1).Exec(ctx); err != nil {
			return err
		}
		if err := revokeConnectionSessions(ctx, c, PasswordConnectionID, now); err != nil {
			return err
		}
		if _, err := c.ReauthenticationProof.Update().Where(reauthenticationproof.ConnectionIDEQ(PasswordConnectionID), reauthenticationproof.ConsumedAtIsNil()).SetConsumedAt(now).Save(ctx); err != nil {
			return err
		}
		if _, err := c.AuthTransaction.Update().Where(authtransaction.AuthorizationConnectionIDEQ(PasswordConnectionID), authtransaction.StatusIn(authtransaction.StatusCreated, authtransaction.StatusCallbackProcessing, authtransaction.StatusCallbackVerified, authtransaction.StatusAwaitingConfirmation)).SetStatus(authtransaction.StatusCancelled).SetFinishedAt(now).ClearExchangeTicketHash().ClearConfirmationTokenHash().Save(ctx); err != nil {
			return err
		}
	}
	if signupChanged {
		if p.Local.SignupPolicyRevision >= maxAuthVersion {
			return ErrAuthConnectionConflict
		}
		q.SetSignupPolicyRevision(p.Local.SignupPolicyRevision + 1)
	}
	if enrollmentChanged {
		if p.Local.EnrollmentPolicyRevision >= maxAuthVersion {
			return ErrAuthConnectionConflict
		}
		q.SetEnrollmentPolicyRevision(p.Local.EnrollmentPolicyRevision + 1)
	}
	return q.Exec(ctx)
}

func (s *ExternalAuthService) GetLocalAccount(ctx context.Context, actorID int) (LocalAccountRecord, error) {
	if err := checkConnectionAdmin(ctx, s.client, actorID); err != nil {
		return LocalAccountRecord{}, err
	}
	return localAccountRecord(ctx, s.client)
}

func localAccountRecord(ctx context.Context, c *ent.Client) (LocalAccountRecord, error) {
	p, err := readPasswordPolicy(ctx, c)
	if err != nil {
		return LocalAccountRecord{}, err
	}
	registration, err := readRegistrationPolicy(ctx, c)
	if err != nil {
		return LocalAccountRecord{}, err
	}
	return LocalAccountRecord{Policy: localAccountInput(p.Local), Revision: p.Local.Revision, Capabilities: localAccountCapabilities(p, registration), Issues: []AuthConfigurationIssue{}}, nil
}

func (s *ExternalAuthService) UpdateLocalAccount(ctx context.Context, actorID int, input LocalAccountPatchInput) (LocalAccountRecord, error) {
	in, err := normalizeLocalPolicy(input.Policy)
	if err != nil || input.ExpectedRevision < 1 {
		return LocalAccountRecord{}, ErrInvalidInput
	}
	var out LocalAccountRecord
	err = withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
		if err := lockExternalConfiguration(ctx, c, actorID); err != nil {
			return err
		}
		p, err := readPasswordPolicy(ctx, c)
		if err != nil {
			return err
		}
		if p.Local.Revision != input.ExpectedRevision {
			return ErrAuthConnectionConflict
		}
		if !sameLocalPolicy(localAccountInput(p.Local), in) {
			if err := ensureLocalPolicyDependencies(ctx, c, p, in, s.now()); err != nil {
				return err
			}
			if err := writeLocalAccountPolicy(ctx, c, p, in, s.now()); err != nil {
				return err
			}
			if err := recordAuditEvent(ctx, c, AuditEvent{ActorUserID: actorID, Action: "admin.local_account.saved", ResourceType: "local_account", ResourceID: 1, Metadata: map[string]any{"revision": p.Local.Revision + 1, "affects_signup": localSignupChanged(localAccountInput(p.Local), in)}}); err != nil {
				return err
			}
		}
		out, err = localAccountRecord(ctx, c)
		return err
	})
	return out, err
}

func registrationEvidence(protocol string, mappings []authrules.Field, external authrules.Evidence, email string, now time.Time) (authrules.Evidence, error) {
	data, err := json.Marshal(map[string]any{"email": email})
	if err != nil {
		return external, err
	}
	attrs, err := authrules.ExtractForPurpose(protocol, mappings, "signup", map[string]json.RawMessage{"registration": data}, nil, now)
	if err != nil {
		return external, err
	}
	merged := authrules.SelectAttributes(external.Attributes, mapKeys(external.Attributes))
	for id, value := range attrs {
		if id == "registration.email" || id == "registration.email_domain" {
			merged[id] = value
		}
	}
	external.Attributes = merged
	if external.ObservedAt.IsZero() {
		external.ObservedAt = now
	}
	if external.ExpiresAt.IsZero() {
		external.ExpiresAt = now.Add(externalConfirmationTTL)
	}
	return external, nil
}

func mapKeys(values map[string]authrules.Attribute) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	return ids
}

func validateRegistrationEmail(raw string, requirement string) (string, error) {
	email, err := accountidentity.NormalizeEmail(raw)
	if err != nil {
		return "", invalidAccountField("email", err)
	}
	if requirement == "required" && email == "" {
		return "", invalidAccountField("email", errors.New("email is required"))
	}
	return email, nil
}

func validateRegistrationForRule(raw, requirement string, rule authrules.RuleSet) (string, error) {
	email, err := validateRegistrationEmail(raw, requirement)
	if err != nil {
		return "", err
	}
	if referencesRegistrationDomain(rule) {
		if _, err := accountidentity.NormalizeEmailDomain(email); err != nil {
			return "", invalidAccountField("email", err)
		}
	}
	return email, nil
}

func requireLocalSignup(p passwordPolicy, email string, now time.Time) error {
	if _, err := validateRegistrationForRule(email, string(p.Local.SignupEmailRequirement), p.Local.SignupRules); err != nil {
		return err
	}
	evidence, err := registrationEvidence("password", nil, authrules.Evidence{}, email, now)
	if err != nil {
		return ErrInvalidInput
	}
	if authrules.EvaluateForPurpose("password", "signup", p.Local.SignupRules, nil, evidence, now).Status != "pass" {
		return ErrAuthenticationRuleDenied
	}
	return nil
}

func (s *ExternalAuthService) LocalAccountAttributes(ctx context.Context, actorID int, expected int64) (AuthAttributeCatalog, error) {
	if _, err := s.localCandidate(ctx, actorID, LocalAccountCandidateInput{ExpectedRevision: expected}); err != nil {
		return AuthAttributeCatalog{}, err
	}
	fields, err := authrules.CatalogForPurpose("password", nil, "signup")
	return AuthAttributeCatalog{Revision: expected, Purpose: "signup", Fields: fields, Operators: authOperatorCatalog()}, err
}

func (s *ExternalAuthService) localCandidate(ctx context.Context, actorID int, in LocalAccountCandidateInput) (passwordPolicy, error) {
	if err := checkConnectionAdmin(ctx, s.client, actorID); err != nil {
		return passwordPolicy{}, err
	}
	p, err := readPasswordPolicy(ctx, s.client)
	if err != nil {
		return p, err
	}
	if in.ExpectedRevision < 1 {
		return p, ErrInvalidInput
	}
	if in.ExpectedRevision != p.Local.Revision {
		return p, ErrAuthConnectionConflict
	}
	if in.Policy != nil {
		policy, err := normalizeLocalPolicy(*in.Policy)
		if err != nil {
			return p, err
		}
		local := *p.Local
		local.PasswordLoginEnabled, local.UsernameLoginEnabled = policy.PasswordLoginEnabled, policy.UsernameLoginEnabled
		local.EmailLoginEnabled, local.PasswordEnrollmentEnabled = policy.EmailLoginEnabled, policy.PasswordEnrollmentEnabled
		local.SignupEnabled, local.SignupEmailRequirement, local.SignupRules = policy.Signup.Enabled, localaccountpolicy.SignupEmailRequirement(policy.Signup.EmailRequirement), policy.Signup.Rules
		p.Local = &local
	}
	return p, nil
}

func (s *ExternalAuthService) PreviewLocalAccount(ctx context.Context, actorID int, in LocalAccountPreviewInput) (LocalAccountPreview, error) {
	p, err := s.localCandidate(ctx, actorID, in.LocalAccountCandidateInput)
	if err != nil {
		return LocalAccountPreview{}, err
	}
	var raw string
	if in.Registration.Email != nil {
		raw = *in.Registration.Email
	}
	email, err := validateRegistrationForRule(raw, string(p.Local.SignupEmailRequirement), p.Local.SignupRules)
	if err != nil {
		return LocalAccountPreview{}, err
	}
	evidence, err := registrationEvidence("password", nil, authrules.Evidence{}, email, timeutil.Normalize(s.now()))
	if err != nil {
		return LocalAccountPreview{}, err
	}
	return LocalAccountPreview{Revision: p.Local.Revision, Simulated: true, Attributes: evidence.Attributes, Signup: authrules.EvaluateForPurpose("password", "signup", p.Local.SignupRules, nil, evidence, s.now())}, nil
}

func localPolicyImpact(ctx context.Context, c *ent.Client, current passwordPolicy, in LocalAccountPolicyInput, now time.Time) (AuthConnectionImpactResult, error) {
	out := emptyAuthImpact(current.Local.Revision, now)
	out.AffectsSignup = localSignupChanged(localAccountInput(current.Local), in) || current.Local.PasswordEnrollmentEnabled != in.PasswordEnrollmentEnabled || current.Local.PasswordLoginEnabled != in.PasswordLoginEnabled || current.Local.UsernameLoginEnabled != in.UsernameLoginEnabled
	if current.canLogin() && !in.PasswordLoginEnabled {
		users, err := c.User.Query().Where(user.ActiveEQ(true)).Order(ent.Asc(user.FieldID)).All(ctx)
		if err != nil {
			return out, err
		}
		for _, u := range users {
			out.AffectedUsers = append(out.AffectedUsers, u.ID)
			if u.Role == SystemRoleAdmin {
				out.AdministratorUsers = append(out.AdministratorUsers, u.ID)
			}
			ok, err := connectionFallbackAt(ctx, c, u, PasswordConnectionID, now)
			if err != nil {
				return out, err
			}
			if ok {
				out.PassUsers = append(out.PassUsers, u.ID)
			} else {
				out.UnknownUsers = append(out.UnknownUsers, u.ID)
				out.BlockedUsers = append(out.BlockedUsers, u.ID)
				out.CanSave = false
			}
		}
		count, err := c.RefreshSession.Query().Where(refreshsession.ConnectionIDEQ(PasswordConnectionID), refreshsession.RevokedAtIsNil(), refreshsession.ExpiresAtGT(now)).Count(ctx)
		if err != nil {
			return out, err
		}
		out.AffectedSessions = count
	}
	if !out.CanSave {
		out.Remediation = "All active accounts, including administrators, must verify an external login and maintenance path before disabling password login."
	}
	if err := ensureExternalSignupDependencies(ctx, c, in); err != nil {
		var dependency *AuthConnectionError
		if !errors.As(err, &dependency) {
			return out, err
		}
		out.CanSave = false
		out.Remediation = "Update the enabled external signup password requirements before removing their local login or password enrollment prerequisites."
	}
	return out, nil
}

func ensureLocalPolicyDependencies(ctx context.Context, c *ent.Client, current passwordPolicy, in LocalAccountPolicyInput, now time.Time) error {
	impact, err := localPolicyImpact(ctx, c, current, in, now)
	if err != nil {
		return err
	}
	if !impact.CanSave {
		return &AuthConnectionError{Code: "fallback-required", Impact: &impact}
	}
	return ensureExternalSignupDependencies(ctx, c, in)
}

func (s *ExternalAuthService) LocalAccountImpact(ctx context.Context, actorID int, in LocalAccountCandidateInput) (AuthConnectionImpactResult, error) {
	current, err := s.localCandidate(ctx, actorID, LocalAccountCandidateInput{ExpectedRevision: in.ExpectedRevision})
	if err != nil {
		return AuthConnectionImpactResult{}, err
	}
	policy := localAccountInput(current.Local)
	if in.Policy != nil {
		policy, err = normalizeLocalPolicy(*in.Policy)
		if err != nil {
			return AuthConnectionImpactResult{}, err
		}
	}
	return localPolicyImpact(ctx, s.client, current, policy, s.now())
}
