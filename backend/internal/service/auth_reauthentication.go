package service

import (
	"context"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/loginbinding"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/reauthenticationproof"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

func validSensitiveAction(action string) bool {
	switch action {
	case "link", "password_set", "password_delete", "unlink":
		return true
	}
	return false
}

func (s *ExternalAuthService) issueReauthenticationProof(ctx context.Context, c *ent.Client, account *ent.User, method, connectionID, action, target string, authenticatedAt time.Time, evidences ...authrules.Evidence) (ReauthenticationResult, error) {
	if !validSensitiveAction(action) || target == "" {
		return ReauthenticationResult{}, ErrInvalidInput
	}
	now := timeutil.Normalize(s.now())
	expires := authenticatedAt.Add(reauthenticationTTL)
	if now.Add(reauthenticationTTL).Before(expires) {
		expires = now.Add(reauthenticationTTL)
	}
	if len(evidences) > 0 && !evidences[0].ExpiresAt.IsZero() && evidences[0].ExpiresAt.Before(expires) {
		expires = evidences[0].ExpiresAt
	}
	if !now.Before(expires) {
		return ReauthenticationResult{}, ErrReauthenticationRequired
	}
	raw, err := s.randomToken()
	if err != nil {
		return ReauthenticationResult{}, err
	}
	create := c.ReauthenticationProof.Create().SetTokenHash(hash.Full(raw)).SetUserID(account.ID).SetAuthVersion(account.AuthVersion).SetMethod(reauthenticationproof.Method(method)).SetAction(reauthenticationproof.Action(action)).SetTargetID(target).SetAuthenticatedAt(authenticatedAt).SetExpiresAt(expires)
	if connectionID != "" {
		create.SetConnectionID(connectionID)
		generation, revision, err := authenticationSourceVersion(ctx, c, connectionID)
		if err != nil {
			return ReauthenticationResult{}, err
		}
		create.SetConnectionGeneration(generation).SetLoginPolicyRevision(revision)
	}
	if err = create.Exec(ctx); err != nil {
		return ReauthenticationResult{}, err
	}
	return ReauthenticationResult{Proof: raw, ExpiresAt: expires}, nil
}

func (s *ExternalAuthService) consumeReauthenticationProof(ctx context.Context, c *ent.Client, userID int, raw, action, target string) (*ent.ReauthenticationProof, error) {
	if raw == "" || !validSensitiveAction(action) || target == "" {
		return nil, ErrReauthenticationRequired
	}
	proof, err := c.ReauthenticationProof.Query().Where(reauthenticationproof.TokenHashEQ(hash.Full(raw))).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrReauthenticationRequired
	}
	if err != nil {
		return nil, err
	}
	account, err := c.User.Get(ctx, userID)
	if err != nil {
		return nil, ErrReauthenticationRequired
	}
	if err = checkExternalActor(ctx, account); err != nil {
		return nil, err
	}
	now := timeutil.Normalize(s.now())
	if !account.Active || proof.UserID != userID || proof.AuthVersion != account.AuthVersion || proof.ConsumedAt != nil || proof.Action.String() != action || proof.TargetID != target || !now.Before(proof.ExpiresAt) || now.Sub(proof.AuthenticatedAt) > reauthenticationTTL {
		return nil, ErrReauthenticationRequired
	}
	generation, revision, err := authenticationSourceVersion(ctx, c, proof.ConnectionID)
	if err != nil || generation != proof.ConnectionGeneration || revision != proof.LoginPolicyRevision {
		return nil, ErrReauthenticationRequired
	}
	n, err := c.ReauthenticationProof.Update().Where(reauthenticationproof.IDEQ(proof.ID), reauthenticationproof.ConsumedAtIsNil()).SetConsumedAt(now).Save(ctx)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrReauthenticationRequired
	}
	return proof, nil
}

func (s *ExternalAuthService) Reauthenticate(ctx context.Context, userID int, input ReauthenticationInput) (*ReauthenticationResult, *ExternalStartResult, error) {
	if !validSensitiveAction(input.Action) || input.TargetID == "" {
		return nil, nil, ErrInvalidInput
	}
	if input.Method == "password" {
		observed, err := s.client.User.Get(ctx, userID)
		if err != nil {
			return nil, nil, ErrTokenInvalid
		}
		passwordHash := observed.PasswordHash
		if passwordHash == "" {
			passwordHash = dummyPasswordHash
		}
		matches := comparePassword(passwordHash, input.Password) == nil
		var result ReauthenticationResult
		err = withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
			if err := lockPasswordConnection(ctx, c); err != nil {
				return err
			}
			if err := lockAuthUser(ctx, c, userID); err != nil {
				return err
			}
			account, err := c.User.Get(ctx, userID)
			if err != nil {
				return err
			}
			if err = checkExternalActor(ctx, account); err != nil {
				return err
			}
			p, err := readPasswordPolicy(ctx, c)
			if err != nil {
				return err
			}
			if !matches || observed.PasswordHash == "" || account.PasswordHash != observed.PasswordHash || account.AuthVersion != observed.AuthVersion || !account.Active || !p.canLogin() {
				return ErrInvalidCredentials
			}
			if err := c.User.UpdateOneID(account.ID).SetPasswordVerifiedHash(hash.Full(account.PasswordHash)).SetPasswordVerifiedAt(s.now()).Exec(ctx); err != nil {
				return err
			}
			result, err = s.issueReauthenticationProof(ctx, c, account, "password", PasswordConnectionID, input.Action, input.TargetID, timeutil.Normalize(s.now()))
			return err
		})
		if err != nil {
			return nil, nil, err
		}
		return &result, nil, nil
	}
	if input.Method != "oidc" || input.BindingID == nil || input.BrowserChallenge == "" || input.CompletionURL == "" {
		return nil, nil, ErrInvalidInput
	}
	binding, err := s.client.LoginBinding.Query().Where(loginbinding.IDEQ(*input.BindingID), loginbinding.UserIDEQ(userID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil, ErrReauthenticationRequired
	}
	if err != nil {
		return nil, nil, err
	}
	p, err := readExternalConnection(ctx, s.client, binding.ConnectionID, 0)
	if err != nil {
		return nil, nil, err
	}
	if p.Connection.ArchivedAt != nil || !p.Policy.Enabled || !p.Policy.LoginEnabled {
		return nil, nil, ErrAuthConnectionConflict
	}
	fresh, e := hasFreshValidation(ctx, s.client, p.Connection.ID, connectionFingerprint(p.Config))
	if e != nil {
		return nil, nil, e
	}
	if p.Connection.Kind != authconnection.KindOidc || !fresh {
		return nil, nil, ErrReauthenticationRequired
	}
	account, err := s.client.User.Get(ctx, userID)
	if err != nil || !account.Active {
		return nil, nil, ErrTokenInvalid
	}
	if err = checkExternalActor(ctx, account); err != nil {
		return nil, nil, err
	}
	expiry := timeutil.Normalize(s.now()).Add(reauthenticationTTL)
	start, err := s.startExternal(ctx, p, authtransaction.PurposeReauthenticate, &userID, input.BindingID, ExternalBrowserInput{input.BrowserChallenge, input.CompletionURL, input.ReturnTo}, input.Action, input.TargetID, expiry)
	if err != nil {
		return nil, nil, err
	}
	return nil, &start, nil
}

func (s *ExternalAuthService) StartLink(ctx context.Context, userID int, connectionID string, input ExternalLinkStartInput) (ExternalStartResult, error) {
	p, err := readExternalConnection(ctx, s.client, connectionID, 0)
	if err != nil {
		return ExternalStartResult{}, err
	}
	if p.Connection.ArchivedAt != nil || !p.Policy.Enabled || !p.Policy.LinkEnabled {
		return ExternalStartResult{}, ErrAuthConnectionConflict
	}
	var proof *ent.ReauthenticationProof
	err = withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
		if err := lockAuthConnection(ctx, c, connectionID); err != nil {
			return err
		}
		if err := lockAuthUser(ctx, c, userID); err != nil {
			return err
		}
		var consumeErr error
		proof, consumeErr = s.consumeReauthenticationProof(ctx, c, userID, input.ReauthenticationProof, "link", connectionID)
		return consumeErr
	})
	if err != nil {
		return ExternalStartResult{}, err
	}
	return s.startExternal(WithExternalActor(ctx, userID, proof.AuthVersion), p, authtransaction.PurposeLink, &userID, nil, input.ExternalBrowserInput, "link", connectionID, proof.ExpiresAt, proof)
}

func (s *ExternalAuthService) StartTest(ctx context.Context, actorID int, connectionID string, expected int64, input ExternalBrowserInput) (ExternalStartResult, error) {
	p, err := s.candidateConnection(ctx, actorID, connectionID, expected)
	if err != nil {
		return ExternalStartResult{}, err
	}
	account, err := s.client.User.Get(ctx, actorID)
	if err != nil || account.Role != SystemRoleAdmin || !account.Active {
		return ExternalStartResult{}, ErrForbidden
	}
	if err = checkExternalActor(ctx, account); err != nil {
		return ExternalStartResult{}, err
	}
	return s.startExternal(ctx, p, authtransaction.PurposeTest, &actorID, nil, input, "", "", time.Time{})
}
