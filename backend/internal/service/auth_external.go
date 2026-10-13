package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
	"github.com/google/uuid"
)

type sealedAuthField struct {
	Version            int `json:"v"`
	KeyID, Nonce, Data string
}

func authTransactionAAD(id, field string) []byte {
	b, _ := json.Marshal([]any{"linguaflow.authentication.transaction", 1, id, field})
	return b
}
func (s *ExternalAuthService) sealTransactionField(id, field, value string) (string, error) {
	if s.keys == nil {
		return "", ErrSettingsUnavailable
	}
	v, err := s.keys.Seal([]byte(value), authTransactionAAD(id, field))
	if err != nil {
		return "", ErrSettingsUnavailable
	}
	b, err := json.Marshal(sealedAuthField{v.Version, v.KeyID, base64.RawStdEncoding.EncodeToString(v.Nonce), base64.RawStdEncoding.EncodeToString(v.Data)})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (s *ExternalAuthService) openTransactionField(id, field, value string) (string, error) {
	if s.keys == nil {
		return "", ErrSettingsUnavailable
	}
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", ErrExternalFlowInvalid
	}
	var v sealedAuthField
	if json.Unmarshal(b, &v) != nil {
		return "", ErrExternalFlowInvalid
	}
	nonce, err := base64.RawStdEncoding.DecodeString(v.Nonce)
	if err != nil {
		return "", ErrExternalFlowInvalid
	}
	data, err := base64.RawStdEncoding.DecodeString(v.Data)
	if err != nil {
		return "", ErrExternalFlowInvalid
	}
	plain, err := s.keys.Open(credential.Ciphertext{Version: v.Version, KeyID: v.KeyID, Nonce: nonce, Data: data}, authTransactionAAD(id, field))
	if err != nil {
		return "", ErrExternalFlowInvalid
	}
	return string(plain), nil
}
func (s *ExternalAuthService) randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(s.rand, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func validBrowserChallenge(v string) bool {
	b, err := base64.RawURLEncoding.DecodeString(v)
	return err == nil && len(b) == sha256.Size && base64.RawURLEncoding.EncodeToString(b) == v
}
func verifierMatches(challenge, verifier string) bool {
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(challenge), []byte(got)) == 1
}
func validReturnTo(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && !u.IsAbs() && u.Host == "" && strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(u.Path, "//") && !strings.ContainsAny(u.Path, "\\\r\n") && !strings.Contains(raw, "\\")
}
func (s *ExternalAuthService) validCompletionURL(raw string) bool {
	for _, allowed := range s.cfg.CompletionURLs {
		if raw == allowed {
			return true
		}
	}
	return false
}
func (s *ExternalAuthService) callbackURL(connectionID string) (string, error) {
	base, err := url.Parse(s.cfg.PublicBaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", ErrSettingsUnavailable
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/auth/external/" + url.PathEscape(connectionID) + "/callback"
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), nil
}

func (s *ExternalAuthService) StartLogin(ctx context.Context, connectionID string, input ExternalBrowserInput) (ExternalStartResult, error) {
	p, err := readExternalConnection(ctx, s.client, connectionID, 0)
	if err != nil {
		return ExternalStartResult{}, err
	}
	if p.Connection.ArchivedAt != nil || !p.Policy.Enabled || !p.Policy.LoginEnabled {
		return ExternalStartResult{}, ErrAuthConnectionConflict
	}
	return s.startExternal(ctx, p, authtransaction.PurposeLogin, nil, nil, input, "", "", time.Time{})
}

func (s *ExternalAuthService) startExternal(ctx context.Context, p externalConnection, purpose authtransaction.Purpose, userID *int, bindingID *int, input ExternalBrowserInput, action, target string, authorizationExpiry time.Time, authorizations ...*ent.ReauthenticationProof) (ExternalStartResult, error) {
	if !validBrowserChallenge(input.BrowserChallenge) || !s.validCompletionURL(input.CompletionURL) || !validReturnTo(input.ReturnTo) {
		return ExternalStartResult{}, ErrInvalidInput
	}
	now := timeutil.Normalize(s.now())
	expires := now.Add(externalTransactionTTL)
	if !authorizationExpiry.IsZero() && authorizationExpiry.Before(expires) {
		expires = authorizationExpiry
	}
	if !now.Before(expires) {
		return ExternalStartResult{}, ErrReauthenticationRequired
	}
	id := uuid.NewString()
	state, err := s.randomToken()
	if err != nil {
		return ExternalStartResult{}, err
	}
	nonce, err := s.randomToken()
	if err != nil {
		return ExternalStartResult{}, err
	}
	pkce, err := s.randomToken()
	if err != nil {
		return ExternalStartResult{}, err
	}
	sealedNonce, err := s.sealTransactionField(id, "nonce", nonce)
	if err != nil {
		return ExternalStartResult{}, err
	}
	sealedPKCE, err := s.sealTransactionField(id, "pkce", pkce)
	if err != nil {
		return ExternalStartResult{}, err
	}
	redirect, err := s.callbackURL(p.Connection.ID)
	if err != nil {
		return ExternalStartResult{}, err
	}
	fresh := p.Connection.Kind == authconnection.KindOidc && (purpose == authtransaction.PurposeReauthenticate || purpose == authtransaction.PurposeTest)
	authz, err := s.buildAuthorization(ctx, p, authoidc.AuthorizationRequest{RedirectURL: redirect, State: state, Nonce: nonce, CodeVerifier: pkce, Fresh: fresh, MaxAge: 0})
	if err != nil {
		return ExternalStartResult{}, ErrExternalUnavailable
	}
	create := s.client.AuthTransaction.Create().SetID(id).SetPurpose(purpose).SetConnectionID(p.Connection.ID).SetConnectionRevision(p.Config.Revision).SetConnectionGeneration(p.Connection.AuthGeneration).SetPolicyRevision(p.Policy.LoginPolicyRevision).SetStateHash(hash.Full(state)).SetNonce(sealedNonce).SetPkceVerifier(sealedPKCE).SetBrowserChallenge(input.BrowserChallenge).SetCompletionURL(input.CompletionURL).SetReturnTo(input.ReturnTo).SetExpiresAt(expires)
	if userID != nil {
		account, err := s.client.User.Get(ctx, *userID)
		if err != nil || !account.Active {
			return ExternalStartResult{}, ErrTokenInvalid
		}
		if err = checkExternalActor(ctx, account); err != nil {
			return ExternalStartResult{}, err
		}
		create.SetUserID(*userID).SetAuthVersion(account.AuthVersion)
	}
	if bindingID != nil {
		create.SetBindingID(*bindingID)
	}
	if len(authorizations) > 0 && authorizations[0] != nil {
		proof := authorizations[0]
		create.SetAuthorizationConnectionID(proof.ConnectionID).SetAuthorizationConnectionGeneration(proof.ConnectionGeneration).SetAuthorizationPolicyRevision(proof.LoginPolicyRevision)
	}
	if action != "" {
		create.SetAuthorizedAction(action).SetAuthorizedTargetID(target).SetAuthorizationExpiresAt(authorizationExpiry)
	}
	if err := create.Exec(ctx); err != nil {
		return ExternalStartResult{}, err
	}
	return ExternalStartResult{TransactionID: id, AuthorizationURL: authz.URL, ExpiresAt: expires}, nil
}

func (s *ExternalAuthService) HandleCallback(ctx context.Context, connectionID, state, code, providerError string, responseIssuer ...string) (ExternalCallbackResult, error) {
	if state == "" {
		return ExternalCallbackResult{}, ErrExternalFlowInvalid
	}
	row, err := s.client.AuthTransaction.Query().Where(authtransaction.StateHashEQ(hash.Full(state))).Only(ctx)
	if ent.IsNotFound(err) || err == nil && row.ConnectionID != connectionID {
		return ExternalCallbackResult{}, ErrExternalFlowInvalid
	}
	if err != nil {
		return ExternalCallbackResult{}, err
	}
	now := timeutil.Normalize(s.now())
	if !now.Before(row.ExpiresAt) {
		_, _ = s.client.AuthTransaction.Update().Where(authtransaction.IDEQ(row.ID), authtransaction.StatusEQ(authtransaction.StatusCreated)).SetStatus(authtransaction.StatusExpired).SetFinishedAt(now).Save(ctx)
		return ExternalCallbackResult{}, ErrExternalFlowInvalid
	}
	n, err := s.client.AuthTransaction.Update().Where(authtransaction.IDEQ(row.ID), authtransaction.StatusEQ(authtransaction.StatusCreated)).SetStatus(authtransaction.StatusCallbackProcessing).Save(ctx)
	if err != nil {
		return ExternalCallbackResult{}, err
	}
	if n != 1 {
		return ExternalCallbackResult{}, ErrExternalFlowInvalid
	}
	issueFailure := func(code string) (ExternalCallbackResult, error) {
		now = timeutil.Normalize(s.now())
		if !now.Before(row.ExpiresAt) {
			_, e := s.client.AuthTransaction.Update().Where(authtransaction.IDEQ(row.ID), authtransaction.StatusEQ(authtransaction.StatusCallbackProcessing)).SetStatus(authtransaction.StatusExpired).SetFinishedAt(now).Save(ctx)
			if e != nil {
				return ExternalCallbackResult{}, e
			}
			return ExternalCallbackResult{}, ErrExternalFlowInvalid
		}
		ticket, e := s.randomToken()
		if e != nil {
			return ExternalCallbackResult{}, e
		}
		deadline := now.Add(externalTicketTTL)
		if row.ExpiresAt.Before(deadline) {
			deadline = row.ExpiresAt
		}
		n, e := s.client.AuthTransaction.Update().Where(authtransaction.IDEQ(row.ID), authtransaction.StatusEQ(authtransaction.StatusCallbackProcessing)).SetStatus(authtransaction.StatusFailed).SetFailureCode(code).SetExchangeTicketHash(hash.Full(ticket)).SetExchangeTicketExpiresAt(deadline).SetFinishedAt(now).Save(ctx)
		if e != nil {
			return ExternalCallbackResult{}, e
		}
		if n != 1 {
			return ExternalCallbackResult{}, ErrExternalFlowInvalid
		}
		return ExternalCallbackResult{CompletionURL: row.CompletionURL, Ticket: ticket, TransactionID: row.ID, ReturnTo: row.ReturnTo, Failed: true}, nil
	}
	if providerError != "" || code == "" {
		return issueFailure("provider_rejected")
	}
	nonce, err := s.openTransactionField(row.ID, "nonce", row.Nonce)
	if err != nil {
		return issueFailure("invalid_transaction")
	}
	pkce, err := s.openTransactionField(row.ID, "pkce", row.PkceVerifier)
	if err != nil {
		return issueFailure("invalid_transaction")
	}
	p, err := readExternalConnection(ctx, s.client, row.ConnectionID, row.ConnectionRevision)
	if err != nil {
		return issueFailure("configuration_changed")
	}
	if row.Purpose != authtransaction.PurposeTest {
		runtime, e := readExternalConnection(ctx, s.client, row.ConnectionID, 0)
		if e != nil || transactionRulesCurrent(ctx, s.client, row, runtime) != nil {
			return issueFailure("configuration_changed")
		}
	} else if transactionRulesCurrent(ctx, s.client, row, p) != nil {
		return issueFailure("configuration_changed")
	}
	if len(responseIssuer) > 0 && responseIssuer[0] != "" && (p.Connection.Kind != authconnection.KindOidc || responseIssuer[0] != p.Config.OidcIssuer) {
		return issueFailure("issuer_mismatch")
	}
	redirect, err := s.callbackURL(row.ConnectionID)
	if err != nil {
		return issueFailure("configuration_unavailable")
	}
	identity, err := s.verifyExternal(ctx, p, authoidc.ExchangeRequest{RedirectURL: redirect, Code: code, CodeVerifier: pkce, ExpectedNonce: nonce, RequireFresh: row.Purpose == authtransaction.PurposeReauthenticate, MaxAge: reauthenticationTTL, Now: now})
	if err != nil {
		if errors.Is(err, authoidc.ErrUnavailable) {
			return issueFailure("identity_provider_unavailable")
		}
		return issueFailure("provider_verification_failed")
	}
	now = timeutil.Normalize(s.now())
	if !now.Before(row.ExpiresAt) {
		return issueFailure("expired_transaction")
	}
	if err := validateIdentityResult(p, identity); err != nil {
		return issueFailure("provider_verification_failed")
	}
	attrs, err := authrules.Extract(configProtocol(p.Config), p.Config.AttributeMappings, identity.Sources, identity.SourceErrors, now)
	if err != nil {
		return issueFailure("provider_verification_failed")
	}
	if row.Purpose != authtransaction.PurposeTest {
		attrs = authrules.SelectAttributes(attrs, authrules.RequiredAttributes(p.Config.AttributeMappings, rulesForPurpose(p, row.Purpose)...))
	}
	evidence := authrules.Evidence{Issuer: identity.Authority, Subject: identity.Subject, SourceFingerprint: sourceTrustFingerprint(p.Config), LoginPolicyRevision: p.Policy.LoginPolicyRevision, ObservedAt: now, ExpiresAt: now.Add(time.Duration(p.Config.EvidenceMaxAgeSeconds) * time.Second), Attributes: attrs}
	if row.Purpose != authtransaction.PurposeTest {
		runtime, e := readExternalConnection(ctx, s.client, row.ConnectionID, 0)
		if e != nil || transactionRulesCurrent(ctx, s.client, row, runtime) != nil {
			return issueFailure("configuration_changed")
		}
	} else if transactionRulesCurrent(ctx, s.client, row, p) != nil {
		return issueFailure("configuration_changed")
	}
	ticket, err := s.randomToken()
	if err != nil {
		return ExternalCallbackResult{}, err
	}
	deadline := now.Add(externalTicketTTL)
	if row.ExpiresAt.Before(deadline) {
		deadline = row.ExpiresAt
	}
	update := s.client.AuthTransaction.Update().Where(authtransaction.IDEQ(row.ID), authtransaction.StatusEQ(authtransaction.StatusCallbackProcessing)).SetStatus(authtransaction.StatusCallbackVerified).SetExchangeTicketHash(hash.Full(ticket)).SetExchangeTicketExpiresAt(deadline).SetIdentityProtocol(authtransaction.IdentityProtocol(identity.Protocol)).SetIdentityIssuer(identity.Authority).SetIdentitySubject(identity.Subject).SetIdentityDisplayName(identity.DisplayName).SetIdentityUsername(identity.PreferredUsername).SetIdentityEmail(identity.Email).SetEvidence(evidence)
	if !identity.AuthTime.IsZero() {
		update.SetIdentityAuthTime(identity.AuthTime)
	}
	n, err = update.Save(ctx)
	if err != nil {
		return ExternalCallbackResult{}, err
	}
	if n != 1 {
		return ExternalCallbackResult{}, ErrExternalFlowInvalid
	}
	return ExternalCallbackResult{CompletionURL: row.CompletionURL, Ticket: ticket, TransactionID: row.ID, ReturnTo: row.ReturnTo}, nil
}

func mapExternalProviderError(err error) error {
	if errors.Is(err, authoidc.ErrUnavailable) {
		return ErrExternalUnavailable
	}
	return ErrExternalFlowInvalid
}
