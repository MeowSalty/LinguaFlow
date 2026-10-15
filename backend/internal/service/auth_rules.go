package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/loginbinding"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshsession"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
)

var ErrAuthenticationRuleDenied = errors.New("authentication rule denied")

func (s *ExternalAuthService) cleanupRuleEvidence(ctx context.Context, now time.Time) error {
	ids, err := s.client.RefreshSession.Query().Where(refreshsession.EvidenceNotNil(), refreshsession.Or(refreshsession.ExpiresAtLTE(now), refreshsession.RevokedAtNotNil())).IDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = s.client.ExecContext(ctx, fmt.Sprintf("UPDATE refresh_sessions SET evidence=NULL WHERE id=%d", id)); err != nil {
			return err
		}
	}
	bindings, err := s.client.LoginBinding.Query().Where(loginbinding.EvidenceNotNil()).All(ctx)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if !binding.Evidence.ExpiresAt.IsZero() && !now.Before(binding.Evidence.ExpiresAt) {
			// Serialize against a fresh login replacing this evidence.
			err = withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
				if err := lockAuthConnection(ctx, c, binding.ConnectionID); err != nil {
					return err
				}
				if err := lockAuthUser(ctx, c, binding.UserID); err != nil {
					return err
				}
				b, e := c.LoginBinding.Get(ctx, binding.ID)
				if ent.IsNotFound(e) {
					return nil
				}
				if e != nil {
					return e
				}
				if !b.Evidence.ExpiresAt.IsZero() && !now.Before(b.Evidence.ExpiresAt) {
					return c.LoginBinding.UpdateOneID(b.ID).ClearEvidence().Exec(ctx)
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

type AuthRulesPreviewInput struct {
	Changes          *AuthConnectionChanges     `json:"changes,omitempty"`
	ExpectedRevision int64                      `json:"expected_revision"`
	Sources          map[string]json.RawMessage `json:"sources"`
}
type AuthRulesDiagnostics struct {
	Login  authrules.Result `json:"login"`
	Signup authrules.Result `json:"signup"`
	Link   authrules.Result `json:"link"`
}
type AuthRulesPreview struct {
	Revision   int64                          `json:"revision"`
	Simulated  bool                           `json:"simulated"`
	Attributes map[string]authrules.Attribute `json:"attributes"`
	Rules      AuthRulesDiagnostics           `json:"rules"`
}
type AuthAttributeCatalog struct {
	Revision  int64               `json:"revision"`
	Fields    []authrules.Field   `json:"fields"`
	Operators map[string][]string `json:"operators"`
}

func sourceTrustFingerprint(cfg *ent.AuthConnectionRevision) string {
	if configProtocol(cfg) == "oauth2" {
		in := oauthInput(cfg)
		sort.Strings(in.Scopes)
		data, _ := json.Marshal([]any{"oauth2", cfg.ConnectionID, in})
		digest := sha256.Sum256(data)
		return hex.EncodeToString(digest[:])
	}
	scopes := append([]string(nil), cfg.OidcScopes...)
	sort.Strings(scopes)
	data, _ := json.Marshal([]any{cfg.OidcIssuer, cfg.OidcClientID, cfg.OidcClientAuthMethod, scopes, cfg.OidcUserinfoEnabled})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func loginRulesChanged(a, b externalConnection) bool {
	return sourceTrustFingerprint(a.Config) != sourceTrustFingerprint(b.Config) ||
		authrules.RulesFingerprint(configProtocol(a.Config), a.Policy.LoginRules, a.Config.AttributeMappings) != authrules.RulesFingerprint(configProtocol(b.Config), b.Policy.LoginRules, b.Config.AttributeMappings) ||
		((a.Policy.LoginRules.Mode == "conditions" || b.Policy.LoginRules.Mode == "conditions") && a.Config.EvidenceMaxAgeSeconds != b.Config.EvidenceMaxAgeSeconds)
}

func authenticationSourceVersion(ctx context.Context, c *ent.Client, id string) (int64, int64, error) {
	if id == PasswordConnectionID {
		p, err := readPasswordPolicy(ctx, c)
		if err != nil {
			return 0, 0, err
		}
		if !p.canLogin() {
			return 0, 0, ErrReauthenticationRequired
		}
		return p.Connection.AuthGeneration, p.Policy.LoginPolicyRevision, nil
	}
	p, err := readExternalConnection(ctx, c, id, 0)
	if err != nil {
		return 0, 0, err
	}
	if p.Connection.ArchivedAt != nil || !p.Policy.Enabled || !p.Policy.LoginEnabled {
		return 0, 0, ErrReauthenticationRequired
	}
	return p.Connection.AuthGeneration, p.Policy.LoginPolicyRevision, nil
}
func validateTransactionAuthorization(ctx context.Context, c *ent.Client, row *ent.AuthTransaction, now time.Time) error {
	if row.AuthorizationConnectionID == "" || row.AuthorizationExpiresAt == nil || !now.Before(*row.AuthorizationExpiresAt) {
		return ErrReauthenticationRequired
	}
	generation, revision, err := authenticationSourceVersion(ctx, c, row.AuthorizationConnectionID)
	if err != nil || generation != row.AuthorizationConnectionGeneration || revision != row.AuthorizationPolicyRevision {
		return ErrReauthenticationRequired
	}
	return nil
}
func rulesForPurpose(p externalConnection, purpose authtransaction.Purpose) []authrules.RuleSet {
	switch purpose {
	case authtransaction.PurposeLink:
		return []authrules.RuleSet{p.Policy.LinkRules}
	case authtransaction.PurposeReauthenticate:
		return []authrules.RuleSet{p.Policy.LoginRules}
	case authtransaction.PurposeTest:
		return []authrules.RuleSet{p.Policy.LoginRules, p.Policy.SignupRules, p.Policy.LinkRules}
	default:
		return []authrules.RuleSet{p.Policy.LoginRules, p.Policy.SignupRules}
	}
}

// The snapshot used to obtain attributes must remain comparable with the
// operation's current policy. A display-only publication is harmless.
func transactionRulesCurrent(ctx context.Context, c *ent.Client, row *ent.AuthTransaction, p externalConnection) error {
	old, err := readExternalConnection(ctx, c, row.ConnectionID, row.ConnectionRevision)
	if err != nil {
		return ErrAuthConnectionConflict
	}
	if row.Purpose == authtransaction.PurposeTest {
		if old.Connection.ArchivedAt != nil || old.Connection.State == authconnection.StateDisabled || old.Connection.AuthGeneration != row.ConnectionGeneration || old.Connection.AggregateRevision != row.TestBaseRevision {
			return connectionError("state-conflict")
		}
		return nil
	}
	if p.Connection.ArchivedAt != nil || p.Connection.AuthGeneration != row.ConnectionGeneration || sourceTrustFingerprint(old.Config) != sourceTrustFingerprint(p.Config) {
		return ErrAuthConnectionConflict
	}
	a, b := rulesForPurpose(old, row.Purpose), rulesForPurpose(p, row.Purpose)
	for i := range a {
		if authrules.RulesFingerprint(configProtocol(old.Config), a[i], old.Config.AttributeMappings) != authrules.RulesFingerprint(configProtocol(p.Config), b[i], p.Config.AttributeMappings) {
			return ErrAuthConnectionConflict
		}
	}
	if old.Config.EvidenceMaxAgeSeconds != p.Config.EvidenceMaxAgeSeconds {
		return ErrAuthConnectionConflict
	}
	switch row.Purpose {
	case authtransaction.PurposeLink:
		if !p.Policy.Enabled || !p.Policy.LinkEnabled {
			return ErrAuthConnectionConflict
		}
	default:
		if !p.Policy.Enabled || !p.Policy.LoginEnabled {
			return ErrAuthConnectionConflict
		}
	}
	return nil
}

func ruleEvidence(p externalConnection, evidence authrules.Evidence, rule authrules.RuleSet, now time.Time, candidate bool) authrules.Result {
	if rule.Mode == "conditions" && (evidence.ObservedAt.IsZero() || evidence.ExpiresAt.IsZero()) {
		evidence.Attributes = nil
	}
	if candidate && rule.Mode == "conditions" && !evidence.ObservedAt.IsZero() {
		deadline := evidence.ObservedAt.Add(time.Duration(p.Config.EvidenceMaxAgeSeconds) * time.Second)
		if evidence.ExpiresAt.IsZero() || deadline.Before(evidence.ExpiresAt) {
			evidence.ExpiresAt = deadline
		}
	}
	if evidence.SourceFingerprint != sourceTrustFingerprint(p.Config) || evidence.Issuer != connectionAuthority(p) || evidence.Subject == "" || (!candidate && evidence.LoginPolicyRevision != p.Policy.LoginPolicyRevision) {
		evidence.Attributes = nil
	}
	return authrules.Evaluate(configProtocol(p.Config), rule, p.Config.AttributeMappings, evidence, now)
}
func requireRule(p externalConnection, evidence authrules.Evidence, rule authrules.RuleSet, now time.Time) error {
	result := ruleEvidence(p, evidence, rule, now, false)
	if result.Status == "pass" {
		return nil
	}
	if result.Retryable {
		return ErrExternalUnavailable
	}
	return ErrAuthenticationRuleDenied
}
func loginEvidence(p externalConnection, e authrules.Evidence) authrules.Evidence {
	e.Attributes = authrules.SelectAttributes(e.Attributes, authrules.RequiredAttributes(p.Config.AttributeMappings, p.Policy.LoginRules))
	if p.Policy.LoginRules.Mode == "unrestricted" {
		e.ExpiresAt = time.Time{}
	} else {
		e.ExpiresAt = e.ObservedAt.Add(time.Duration(p.Config.EvidenceMaxAgeSeconds) * time.Second)
	}
	return e
}
func diagnostics(p externalConnection, e authrules.Evidence, now time.Time) AuthRulesDiagnostics {
	return AuthRulesDiagnostics{Login: ruleEvidence(p, e, p.Policy.LoginRules, now, false), Signup: ruleEvidence(p, e, p.Policy.SignupRules, now, false), Link: ruleEvidence(p, e, p.Policy.LinkRules, now, false)}
}

func (s *ExternalAuthService) ConnectionAttributes(ctx context.Context, actorID int, id string, in AuthConnectionCandidateInput) (AuthAttributeCatalog, error) {
	p, err := s.candidateConnection(ctx, actorID, id, in)
	if err != nil {
		return AuthAttributeCatalog{}, err
	}
	fields, err := authrules.Catalog(configProtocol(p.Config), p.Config.AttributeMappings)
	return AuthAttributeCatalog{Revision: p.Config.Revision, Fields: fields, Operators: map[string][]string{
		"string": {"eq", "ne", "in"}, "boolean": {"eq"}, "integer": {"eq", "ne", "gt", "gte", "lt", "lte"}, "number": {"eq", "ne", "gt", "gte", "lt", "lte"}, "datetime": {"gt", "gte", "lt", "lte"}, "string_list": {"contains", "not_contains", "contains_any", "contains_all"}}}, err
}
func (s *ExternalAuthService) PreviewConnectionRules(ctx context.Context, actorID int, id string, in AuthRulesPreviewInput) (AuthRulesPreview, error) {
	p, err := s.candidateConnection(ctx, actorID, id, AuthConnectionCandidateInput{ExpectedRevision: in.ExpectedRevision, Changes: in.Changes})
	if err != nil {
		return AuthRulesPreview{}, err
	}
	for source := range in.Sources {
		if !authrules.ValidSource(configProtocol(p.Config), source) {
			return AuthRulesPreview{}, ErrInvalidInput
		}
	}
	now := s.now()
	attrs, err := authrules.Extract(configProtocol(p.Config), p.Config.AttributeMappings, in.Sources, nil, now)
	if err != nil {
		return AuthRulesPreview{}, ErrInvalidInput
	}
	e := authrules.Evidence{Issuer: connectionAuthority(p), Subject: "simulated", SourceFingerprint: sourceTrustFingerprint(p.Config), LoginPolicyRevision: p.Policy.LoginPolicyRevision, ObservedAt: now, ExpiresAt: now.Add(time.Duration(p.Config.EvidenceMaxAgeSeconds) * time.Second), Attributes: attrs}
	return AuthRulesPreview{Revision: p.Config.Revision, Simulated: true, Attributes: attrs, Rules: diagnostics(p, e, now)}, nil
}

func bindingRuleResult(ctx context.Context, c *ent.Client, b *ent.LoginBinding, p externalConnection, now time.Time, candidate bool) (authrules.Result, error) {
	unknown := authrules.Result{Status: "unknown"}
	if b.LastVerifiedAt == nil || b.VerifiedAuthGeneration != p.Connection.AuthGeneration || (!candidate && b.VerifiedLoginPolicyRevision != p.Policy.LoginPolicyRevision) {
		return unknown, nil
	}
	identity, err := c.ExternalIdentity.Get(ctx, b.ExternalIdentityID)
	if err != nil {
		return unknown, err
	}
	if p.Policy.LoginRules.Mode != "unrestricted" && (b.Evidence.Subject != identity.Subject || b.Evidence.Issuer != identity.Authority) {
		return unknown, nil
	}
	return ruleEvidence(p, b.Evidence, p.Policy.LoginRules, now, candidate), nil
}

func verifiedPasswordFallback(u *ent.User, p passwordPolicy) bool {
	return u.PasswordHash != "" && u.PasswordVerifiedAt != nil && u.PasswordVerifiedHash == hash.Full(u.PasswordHash) && p.canLogin()
}

func connectionFallbackAt(ctx context.Context, c *ent.Client, u *ent.User, excluding string, now time.Time) (bool, error) {
	password, err := readPasswordPolicy(ctx, c)
	if err != nil {
		return false, err
	}
	if excluding != PasswordConnectionID && verifiedPasswordFallback(u, password) {
		return true, nil
	}
	bindings, err := c.LoginBinding.Query().Where(loginbinding.UserIDEQ(u.ID)).All(ctx)
	if err != nil {
		return false, err
	}
	for _, b := range bindings {
		if b.ConnectionID == excluding {
			continue
		}
		p, err := readExternalConnection(ctx, c, b.ConnectionID, 0)
		if errors.Is(err, ErrAuthConnectionConflict) {
			continue
		}
		if err != nil {
			return false, err
		}
		if p.Connection.ArchivedAt != nil || !p.Policy.Enabled || !p.Policy.LoginEnabled {
			continue
		}
		result, err := bindingRuleResult(ctx, c, b, p, now, false)
		if err != nil {
			return false, err
		}
		if result.Status != "pass" {
			continue
		}
		fresh, err := hasFreshValidation(ctx, c, p.Connection.ID, connectionFingerprint(p.Config))
		if err != nil {
			return false, err
		}
		if p.Connection.Kind == authconnection.KindOidc && fresh {
			return true, nil
		}
	}
	return false, nil
}

func candidateImpact(ctx context.Context, c *ent.Client, p externalConnection, now time.Time, disabled bool) (AuthConnectionImpactResult, error) {
	out := emptyAuthImpact(p.Config.Revision, now)
	bindings, err := c.LoginBinding.Query().Where(loginbinding.ConnectionIDEQ(p.Connection.ID)).All(ctx)
	if err != nil {
		return out, err
	}
	for _, b := range bindings {
		u, err := c.User.Get(ctx, b.UserID)
		if err != nil {
			return out, err
		}
		if !u.Active {
			continue
		}
		out.AffectedUsers = append(out.AffectedUsers, u.ID)
		if u.Role == SystemRoleAdmin {
			out.AdministratorUsers = append(out.AdministratorUsers, u.ID)
		}
		result := authrules.Result{Status: "fail"}
		if !disabled {
			result, err = bindingRuleResult(ctx, c, b, p, now, true)
			if err != nil {
				return out, err
			}
		}
		switch result.Status {
		case "pass":
			out.PassUsers = append(out.PassUsers, u.ID)
		case "fail":
			out.FailUsers = append(out.FailUsers, u.ID)
		default:
			out.UnknownUsers = append(out.UnknownUsers, u.ID)
		}
		fresh := p.Config.FreshSupported
		if result.Status != "pass" || !fresh {
			fallback, err := connectionFallbackAt(ctx, c, u, p.Connection.ID, now)
			if err != nil {
				return out, err
			}
			if !fallback {
				out.BlockedUsers = append(out.BlockedUsers, u.ID)
				out.CanSave = false
			}
		}
	}
	for _, ids := range [][]int{out.AffectedUsers, out.BlockedUsers, out.PassUsers, out.FailUsers, out.UnknownUsers, out.AdministratorUsers} {
		sort.Ints(ids)
	}
	if p.Connection.CurrentRevision > 0 {
		current, readErr := readExternalConnection(ctx, c, p.Connection.ID, 0)
		if readErr != nil {
			return out, readErr
		}
		sessions, readErr := c.RefreshSession.Query().Where(refreshsession.ConnectionIDEQ(p.Connection.ID), refreshsession.RevokedAtIsNil(), refreshsession.ExpiresAtGT(now)).WithUser().WithBinding().All(ctx)
		if readErr != nil {
			return out, readErr
		}
		for _, session := range sessions {
			account, binding := session.Edges.User, session.Edges.Binding
			if account == nil || !account.Active || account.AuthVersion != session.AuthVersion || binding == nil || binding.UserID != account.ID || binding.ConnectionID != p.Connection.ID || session.ConnectionGeneration != current.Connection.AuthGeneration || session.LoginPolicyRevision != current.Policy.LoginPolicyRevision || current.Connection.ArchivedAt != nil || !current.Policy.Enabled || !current.Policy.LoginEnabled {
				continue
			}
			if current.Policy.LoginRules.Mode == "conditions" {
				identity, readErr := c.ExternalIdentity.Get(ctx, binding.ExternalIdentityID)
				if readErr != nil {
					return out, readErr
				}
				if identity.Subject != session.Evidence.Subject || identity.Authority != session.Evidence.Issuer || ruleEvidence(current, session.Evidence, current.Policy.LoginRules, now, false).Status != "pass" {
					continue
				}
			}
			out.AffectedSessions++
		}
	}
	if !out.CanSave {
		out.Remediation = "Have blocked users establish and verify a usable password or another login and maintenance method under the current configuration, then repeat impact and save."
	}
	return out, err
}

func emptyAuthImpact(revision int64, now time.Time) AuthConnectionImpactResult {
	return AuthConnectionImpactResult{Revision: revision, EvaluatedAt: now, AffectedUsers: []int{}, BlockedUsers: []int{}, PassUsers: []int{}, FailUsers: []int{}, UnknownUsers: []int{}, AdministratorUsers: []int{}, CanSave: true}
}
