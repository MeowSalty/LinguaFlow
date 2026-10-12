package service

import (
	"context"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/accountidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionvalidation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/loginbinding"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

func (s *ExternalAuthService) Exchange(ctx context.Context, actorID *int, input ExternalExchangeInput) (ExternalExchangeResult, error) {
	if input.Ticket == "" || input.Verifier == "" {
		return ExternalExchangeResult{}, ErrExternalFlowInvalid
	}
	row, err := s.client.AuthTransaction.Query().Where(authtransaction.ExchangeTicketHashEQ(hash.Full(input.Ticket))).Only(ctx)
	if ent.IsNotFound(err) {
		return ExternalExchangeResult{}, ErrExternalFlowInvalid
	}
	if err != nil {
		return ExternalExchangeResult{}, err
	}
	now := timeutil.Normalize(s.now())
	if row.ExchangeTicketExpiresAt == nil || !now.Before(*row.ExchangeTicketExpiresAt) || !now.Before(row.ExpiresAt) || !verifierMatches(row.BrowserChallenge, input.Verifier) {
		return ExternalExchangeResult{}, ErrExternalFlowInvalid
	}
	if row.Status == authtransaction.StatusFailed {
		n, err := s.client.AuthTransaction.Update().Where(authtransaction.IDEQ(row.ID), authtransaction.StatusEQ(authtransaction.StatusFailed), authtransaction.ExchangeTicketHashEQ(hash.Full(input.Ticket))).ClearExchangeTicketHash().ClearExchangeTicketExpiresAt().Save(ctx)
		if err != nil {
			return ExternalExchangeResult{}, err
		}
		if n != 1 {
			return ExternalExchangeResult{}, ErrExternalFlowInvalid
		}
		if row.FailureCode == "identity_provider_unavailable" || row.FailureCode == "configuration_unavailable" {
			return ExternalExchangeResult{}, ErrExternalUnavailable
		}
		return ExternalExchangeResult{}, ErrExternalFlowInvalid
	}
	if row.Status != authtransaction.StatusCallbackVerified {
		return ExternalExchangeResult{}, ErrExternalFlowInvalid
	}
	var result ExternalExchangeResult
	err = withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
		if row.Purpose == authtransaction.PurposeLogin {
			if err := lockSetting(ctx, c, SettingRegistrationEnabled); err != nil {
				return err
			}
		}
		if err := lockAuthConnection(ctx, c, row.ConnectionID); err != nil {
			return err
		}
		if row.UserID != nil {
			if err := lockAuthUser(ctx, c, *row.UserID); err != nil {
				return err
			}
		}
		current, err := c.AuthTransaction.Get(ctx, row.ID)
		if err != nil {
			return err
		}
		now = timeutil.Normalize(s.now())
		if current.Status != authtransaction.StatusCallbackVerified || current.ExchangeTicketHash == nil || *current.ExchangeTicketHash != hash.Full(input.Ticket) || current.ExchangeTicketExpiresAt == nil || !now.Before(*current.ExchangeTicketExpiresAt) || !now.Before(current.ExpiresAt) {
			return ErrExternalFlowInvalid
		}
		p, err := readExternalConnection(ctx, c, current.ConnectionID, current.ConnectionRevision)
		if err != nil {
			return ErrAuthConnectionConflict
		}
		if p.Connection.AuthGeneration != current.ConnectionGeneration || p.Policy.LoginPolicyRevision != current.PolicyRevision {
			return ErrAuthConnectionConflict
		}
		if current.Purpose != authtransaction.PurposeTest {
			runtime, runtimeErr := readExternalConnection(ctx, c, current.ConnectionID, 0)
			if runtimeErr != nil || runtime.Connection.AuthGeneration != current.ConnectionGeneration || runtime.Policy.LoginPolicyRevision != current.PolicyRevision {
				return ErrAuthConnectionConflict
			}
			p.Connection, p.Policy = runtime.Connection, runtime.Policy
		}
		if current.UserID != nil {
			account, err := c.User.Get(ctx, *current.UserID)
			if err != nil {
				return ErrTokenInvalid
			}
			if err = checkExternalActor(ctx, account); err != nil {
				return err
			}
			if current.AuthVersion == nil || account.AuthVersion != *current.AuthVersion || !account.Active {
				return ErrTokenInvalid
			}
			if actorID == nil || *actorID != account.ID {
				return ErrTokenInvalid
			}
		}
		switch current.Purpose {
		case authtransaction.PurposeLogin:
			if p.Connection.ArchivedAt != nil || !p.Policy.Enabled || !p.Policy.LoginEnabled {
				return ErrAuthConnectionConflict
			}
			identity, identityErr := c.ExternalIdentity.Query().Where(externalidentity.ProtocolEQ(externalidentity.ProtocolOidc), externalidentity.AuthorityEQ(current.IdentityIssuer), externalidentity.SubjectEQ(current.IdentitySubject)).Only(ctx)
			if ent.IsNotFound(identityErr) {
				registration, readErr := readRegistrationPolicy(ctx, c)
				if readErr != nil {
					return readErr
				}
				if !registration || !p.Policy.SignupEnabled {
					return ErrRegistrationClosed
				}
				return s.awaitConfirmation(ctx, c, current, p, "signup_confirmation", &result)
			}
			if identityErr != nil {
				return identityErr
			}
			binding, bindingErr := c.LoginBinding.Query().Where(loginbinding.ExternalIdentityIDEQ(identity.ID), loginbinding.ConnectionIDEQ(current.ConnectionID)).Only(ctx)
			if ent.IsNotFound(bindingErr) {
				return ErrAuthConnectionConflict
			}
			if bindingErr != nil {
				return bindingErr
			}
			if err = lockAuthUser(ctx, c, binding.UserID); err != nil {
				return err
			}
			binding, bindingErr = c.LoginBinding.Query().Where(loginbinding.IDEQ(binding.ID), loginbinding.UserIDEQ(binding.UserID), loginbinding.ConnectionIDEQ(current.ConnectionID)).Only(ctx)
			if bindingErr != nil {
				return ErrInvalidCredentials
			}
			account, err := c.User.Get(ctx, binding.UserID)
			if err != nil || !account.Active {
				return ErrInvalidCredentials
			}
			session, err := s.auth.createSessionForConnection(ctx, c, account, p.Connection, p.Policy.LoginPolicyRevision, binding, nil)
			if err != nil {
				return err
			}
			if err = c.LoginBinding.UpdateOneID(binding.ID).SetLastUsedAt(now).SetLastVerifiedAt(now).SetVerifiedAuthGeneration(p.Connection.AuthGeneration).SetVerifiedLoginPolicyRevision(p.Policy.LoginPolicyRevision).Exec(ctx); err != nil {
				return err
			}
			if err = s.finishExchange(ctx, c, current.ID, input.Ticket); err != nil {
				return err
			}
			result = ExternalExchangeResult{Kind: "session", Session: session}
			return nil
		case authtransaction.PurposeLink:
			if !p.Policy.Enabled || !p.Policy.LinkEnabled || p.Connection.ArchivedAt != nil {
				return ErrAuthConnectionConflict
			}
			return s.awaitConfirmation(ctx, c, current, p, "link_confirmation", &result)
		case authtransaction.PurposeReauthenticate:
			if current.BindingID == nil || current.UserID == nil || current.IdentityAuthTime == nil || p.Connection.ArchivedAt != nil || !p.Policy.Enabled || !p.Policy.LoginEnabled {
				return ErrReauthenticationRequired
			}
			binding, err := c.LoginBinding.Query().Where(loginbinding.IDEQ(*current.BindingID), loginbinding.UserIDEQ(*current.UserID), loginbinding.ConnectionIDEQ(current.ConnectionID)).WithExternalIdentity().Only(ctx)
			if err != nil {
				return ErrReauthenticationRequired
			}
			if binding.Edges.ExternalIdentity == nil || binding.Edges.ExternalIdentity.Authority != current.IdentityIssuer || binding.Edges.ExternalIdentity.Subject != current.IdentitySubject {
				return ErrReauthenticationRequired
			}
			account, err := c.User.Get(ctx, *current.UserID)
			if err != nil {
				return err
			}
			authenticatedAt := now
			if current.IdentityAuthTime != nil {
				authenticatedAt = *current.IdentityAuthTime
			}
			proof, err := s.issueReauthenticationProof(ctx, c, account, "oidc", current.ConnectionID, current.AuthorizedAction, current.AuthorizedTargetID, authenticatedAt)
			if err != nil {
				return err
			}
			if err = c.LoginBinding.UpdateOneID(binding.ID).SetLastUsedAt(now).SetLastVerifiedAt(authenticatedAt).SetVerifiedAuthGeneration(p.Connection.AuthGeneration).SetVerifiedLoginPolicyRevision(p.Policy.LoginPolicyRevision).Exec(ctx); err != nil {
				return err
			}
			if err = s.finishExchange(ctx, c, current.ID, input.Ticket); err != nil {
				return err
			}
			result = ExternalExchangeResult{Kind: "reauthentication_proof", Proof: &proof}
			return nil
		case authtransaction.PurposeTest:
			if current.UserID == nil || current.IdentityAuthTime == nil || now.Sub(*current.IdentityAuthTime) >= reauthenticationTTL {
				return ErrReauthenticationRequired
			}
			account, err := c.User.Get(ctx, *current.UserID)
			if err != nil || account.Role != SystemRoleAdmin {
				return ErrForbidden
			}
			fingerprint := connectionFingerprint(p.Config)
			validation, err := c.AuthConnectionValidation.Query().Where(authconnectionvalidation.ConnectionIDEQ(current.ConnectionID), authconnectionvalidation.FingerprintEQ(fingerprint)).Only(ctx)
			if ent.IsNotFound(err) {
				err = c.AuthConnectionValidation.Create().SetConnectionID(current.ConnectionID).SetFingerprint(fingerprint).SetRevision(current.ConnectionRevision).SetFreshSupported(true).SetTestedAt(now).SetActorUserID(account.ID).Exec(ctx)
			} else if err == nil {
				err = c.AuthConnectionValidation.UpdateOneID(validation.ID).SetFreshSupported(true).SetTestedAt(now).SetActorUserID(account.ID).Exec(ctx)
			}
			if err != nil {
				return err
			}
			if err = s.finishExchange(ctx, c, current.ID, input.Ticket); err != nil {
				return err
			}
			result = ExternalExchangeResult{Kind: "test_result", Test: &ExternalTestResult{ConnectionID: current.ConnectionID, Revision: current.ConnectionRevision, FreshSupported: true}}
			return nil
		default:
			return ErrExternalFlowInvalid
		}
	})
	if err != nil {
		return ExternalExchangeResult{}, err
	}
	if result.Session != nil {
		result.Session.User.Unwrap()
	}
	return result, nil
}

func (s *ExternalAuthService) finishExchange(ctx context.Context, c *ent.Client, id, ticket string) error {
	now := timeutil.Normalize(s.now())
	n, err := c.AuthTransaction.Update().Where(authtransaction.IDEQ(id), authtransaction.StatusEQ(authtransaction.StatusCallbackVerified), authtransaction.ExchangeTicketHashEQ(hash.Full(ticket))).SetStatus(authtransaction.StatusCompleted).ClearExchangeTicketHash().ClearExchangeTicketExpiresAt().SetFinishedAt(now).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrExternalFlowInvalid
	}
	return nil
}

func (s *ExternalAuthService) awaitConfirmation(ctx context.Context, c *ent.Client, row *ent.AuthTransaction, p externalConnection, kind string, result *ExternalExchangeResult) error {
	raw, err := s.randomToken()
	if err != nil {
		return err
	}
	now := timeutil.Normalize(s.now())
	deadline := now.Add(externalConfirmationTTL)
	if row.ExpiresAt.Before(deadline) {
		deadline = row.ExpiresAt
	}
	if row.AuthorizationExpiresAt != nil && row.AuthorizationExpiresAt.Before(deadline) {
		deadline = *row.AuthorizationExpiresAt
	}
	if !now.Before(deadline) {
		return ErrExternalFlowInvalid
	}
	n, err := c.AuthTransaction.Update().Where(authtransaction.IDEQ(row.ID), authtransaction.StatusEQ(authtransaction.StatusCallbackVerified)).SetStatus(authtransaction.StatusAwaitingConfirmation).ClearExchangeTicketHash().ClearExchangeTicketExpiresAt().SetConfirmationTokenHash(hash.Full(raw)).SetConfirmationExpiresAt(deadline).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrExternalFlowInvalid
	}
	display := row.IdentityDisplayName
	if display == "" {
		display = row.IdentityUsername
	}
	if display == "" {
		display = row.IdentitySubject
	}
	result.Kind = kind
	result.Confirmation = &ExternalConfirmation{TransactionID: row.ID, ConfirmationToken: raw, ExpiresAt: deadline, Profile: ExternalProfile{ConnectionID: row.ConnectionID, ConnectionName: p.Config.DisplayName, ExternalDisplay: display}}
	return nil
}

func (s *ExternalAuthService) Complete(ctx context.Context, actorID *int, input ExternalCompleteInput) (ExternalCompleteResult, error) {
	if input.ConfirmationToken == "" || input.Verifier == "" {
		return ExternalCompleteResult{}, ErrExternalFlowInvalid
	}
	row, err := s.client.AuthTransaction.Query().Where(authtransaction.ConfirmationTokenHashEQ(hash.Full(input.ConfirmationToken))).Only(ctx)
	if ent.IsNotFound(err) {
		return ExternalCompleteResult{}, ErrExternalFlowInvalid
	}
	if err != nil {
		return ExternalCompleteResult{}, err
	}
	now := timeutil.Normalize(s.now())
	if row.Status != authtransaction.StatusAwaitingConfirmation || row.ConfirmationExpiresAt == nil || !now.Before(*row.ConfirmationExpiresAt) || !now.Before(row.ExpiresAt) || !verifierMatches(row.BrowserChallenge, input.Verifier) {
		return ExternalCompleteResult{}, ErrExternalFlowInvalid
	}
	var result ExternalCompleteResult
	err = withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
		if row.Purpose == authtransaction.PurposeLogin {
			if err := lockSetting(ctx, c, SettingRegistrationEnabled); err != nil {
				return err
			}
		}
		if err := lockAuthConnection(ctx, c, row.ConnectionID); err != nil {
			return err
		}
		if row.UserID != nil {
			if err := lockAuthUser(ctx, c, *row.UserID); err != nil {
				return err
			}
		}
		current, err := c.AuthTransaction.Get(ctx, row.ID)
		if err != nil {
			return err
		}
		if current.Status != authtransaction.StatusAwaitingConfirmation || current.ConfirmationTokenHash == nil || *current.ConfirmationTokenHash != hash.Full(input.ConfirmationToken) {
			return ErrExternalFlowInvalid
		}
		now = timeutil.Normalize(s.now())
		if current.ConfirmationExpiresAt == nil || !now.Before(*current.ConfirmationExpiresAt) || !now.Before(current.ExpiresAt) || current.AuthorizationExpiresAt != nil && !now.Before(*current.AuthorizationExpiresAt) {
			return ErrExternalFlowInvalid
		}
		p, err := readExternalConnection(ctx, c, current.ConnectionID, 0)
		if err != nil {
			return err
		}
		if p.Connection.ArchivedAt != nil || p.Connection.AuthGeneration != current.ConnectionGeneration || p.Policy.LoginPolicyRevision != current.PolicyRevision {
			return ErrAuthConnectionConflict
		}
		switch current.Purpose {
		case authtransaction.PurposeLogin:
			if actorID != nil {
				return ErrAuthConnectionConflict
			}
			registration, err := readRegistrationPolicy(ctx, c)
			if err != nil {
				return err
			}
			if !registration || !p.Policy.Enabled || !p.Policy.LoginEnabled || !p.Policy.SignupEnabled {
				return ErrRegistrationClosed
			}
			username, err := accountidentity.NormalizeUsername(input.Username)
			if err != nil {
				return ErrInvalidInput
			}
			if err = accountidentity.ValidateAllocation(username, accountidentity.Public); err != nil {
				return ErrInvalidInput
			}
			exists, err := c.ExternalIdentity.Query().Where(externalidentity.ProtocolEQ(externalidentity.ProtocolOidc), externalidentity.AuthorityEQ(current.IdentityIssuer), externalidentity.SubjectEQ(current.IdentitySubject)).Exist(ctx)
			if err != nil {
				return err
			}
			if exists {
				return ErrAuthConnectionConflict
			}
			account, err := c.User.Create().SetUsername(username).SetDisplayName(strings.TrimSpace(input.DisplayName)).SetRole(SystemRoleUser).SetActive(true).Save(ctx)
			if ent.IsConstraintError(err) {
				return ErrUserExists
			}
			if err != nil {
				return err
			}
			identity, err := c.ExternalIdentity.Create().SetUserID(account.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority(current.IdentityIssuer).SetSubject(current.IdentitySubject).Save(ctx)
			if ent.IsConstraintError(err) {
				return ErrAuthConnectionConflict
			}
			if err != nil {
				return err
			}
			binding, err := c.LoginBinding.Create().SetUserID(account.ID).SetConnectionID(current.ConnectionID).SetExternalIdentityID(identity.ID).SetLastUsedAt(now).Save(ctx)
			if err != nil {
				return err
			}
			session, err := s.auth.createSessionForConnection(ctx, c, account, p.Connection, p.Policy.LoginPolicyRevision, binding, nil)
			if err != nil {
				return err
			}
			if err = s.finishConfirmation(ctx, c, current.ID, input.ConfirmationToken); err != nil {
				return err
			}
			if err = recordAuditEvent(ctx, c, AuditEvent{ActorUserID: account.ID, Action: "account.authentication.registered", ResourceType: "user", ResourceID: account.ID, VisibilityScope: "personal", Metadata: map[string]any{"connection_id": current.ConnectionID}}); err != nil {
				return err
			}
			result = ExternalCompleteResult{Kind: "session", Session: session}
			return nil
		case authtransaction.PurposeLink:
			if actorID == nil || current.UserID == nil || *actorID != *current.UserID {
				return ErrTokenInvalid
			}
			account, err := c.User.Get(ctx, *actorID)
			if err != nil {
				return err
			}
			if err = checkExternalActor(ctx, account); err != nil {
				return err
			}
			if current.AuthVersion == nil || account.AuthVersion != *current.AuthVersion || !account.Active || !p.Policy.Enabled || !p.Policy.LinkEnabled {
				return ErrAuthConnectionConflict
			}
			identity, err := c.ExternalIdentity.Query().Where(externalidentity.ProtocolEQ(externalidentity.ProtocolOidc), externalidentity.AuthorityEQ(current.IdentityIssuer), externalidentity.SubjectEQ(current.IdentitySubject)).Only(ctx)
			if ent.IsNotFound(err) {
				identity, err = c.ExternalIdentity.Create().SetUserID(account.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority(current.IdentityIssuer).SetSubject(current.IdentitySubject).Save(ctx)
				if ent.IsConstraintError(err) {
					return ErrAuthConnectionConflict
				}
			} else if err == nil && identity.UserID != account.ID {
				return ErrAuthConnectionConflict
			}
			if err != nil {
				return err
			}
			binding, err := c.LoginBinding.Create().SetUserID(account.ID).SetConnectionID(current.ConnectionID).SetExternalIdentityID(identity.ID).Save(ctx)
			if ent.IsConstraintError(err) {
				return ErrAuthConnectionConflict
			}
			if err != nil {
				return err
			}
			if err = s.finishConfirmation(ctx, c, current.ID, input.ConfirmationToken); err != nil {
				return err
			}
			if err = recordAuditEvent(ctx, c, AuditEvent{ActorUserID: account.ID, Action: "account.login_binding.linked", ResourceType: "login_binding", ResourceID: binding.ID, VisibilityScope: "personal", Metadata: map[string]any{"connection_id": current.ConnectionID}}); err != nil {
				return err
			}
			result = ExternalCompleteResult{Kind: "linked", BindingID: binding.ID}
			return nil
		default:
			return ErrExternalFlowInvalid
		}
	})
	if err != nil {
		return ExternalCompleteResult{}, err
	}
	if result.Session != nil {
		result.Session.User.Unwrap()
	}
	return result, nil
}

func (s *ExternalAuthService) finishConfirmation(ctx context.Context, c *ent.Client, id, token string) error {
	now := timeutil.Normalize(s.now())
	n, err := c.AuthTransaction.Update().Where(authtransaction.IDEQ(id), authtransaction.StatusEQ(authtransaction.StatusAwaitingConfirmation), authtransaction.ConfirmationTokenHashEQ(hash.Full(token))).SetStatus(authtransaction.StatusCompleted).ClearConfirmationTokenHash().ClearConfirmationExpiresAt().SetFinishedAt(now).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrExternalFlowInvalid
	}
	return nil
}

func (s *ExternalAuthService) Cancel(ctx context.Context, actorID *int, input ExternalCancelInput) error {
	if input.TransactionID == "" || input.Verifier == "" {
		return ErrExternalFlowInvalid
	}
	return withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
		row, err := c.AuthTransaction.Get(ctx, input.TransactionID)
		if ent.IsNotFound(err) {
			return ErrExternalFlowInvalid
		}
		if err != nil {
			return err
		}
		if row.UserID != nil {
			if actorID == nil || *actorID != *row.UserID {
				return ErrTokenInvalid
			}
			if err = lockAuthUser(ctx, c, *actorID); err != nil {
				return err
			}
			account, err := c.User.Get(ctx, *actorID)
			if err != nil {
				return err
			}
			if err = checkExternalActor(ctx, account); err != nil {
				return err
			}
			if row.AuthVersion == nil || account.AuthVersion != *row.AuthVersion {
				return ErrTokenInvalid
			}
		} else if actorID != nil {
			return ErrExternalFlowInvalid
		}
		if !verifierMatches(row.BrowserChallenge, input.Verifier) {
			return ErrExternalFlowInvalid
		}
		now := timeutil.Normalize(s.now())
		n, err := c.AuthTransaction.Update().Where(authtransaction.IDEQ(row.ID), authtransaction.ExpiresAtGT(now), authtransaction.StatusIn(authtransaction.StatusCreated, authtransaction.StatusCallbackProcessing, authtransaction.StatusCallbackVerified, authtransaction.StatusAwaitingConfirmation)).SetStatus(authtransaction.StatusCancelled).ClearExchangeTicketHash().ClearExchangeTicketExpiresAt().ClearConfirmationTokenHash().ClearConfirmationExpiresAt().SetFinishedAt(now).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrExternalFlowInvalid
		}
		return nil
	})
}
