package service

import (
	"context"
	"strconv"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/loginbinding"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/reauthenticationproof"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

type LoginMethodRecord struct {
	BindingID         int    `json:"binding_id"`
	ConnectionID      string `json:"connection_id"`
	DisplayName       string `json:"display_name"`
	Archived          bool   `json:"archived"`
	CanLogin          bool   `json:"can_login"`
	CanReauthenticate bool   `json:"can_reauthenticate"`
	CanUnlink         bool   `json:"can_unlink"`
	Verified          bool   `json:"verified"`
}
type AvailableLoginConnection struct {
	ConnectionID string  `json:"connection_id"`
	DisplayName  string  `json:"display_name"`
	CanLink      bool    `json:"can_link"`
	LogoURL      *string `json:"logo_url,omitempty"`
}
type PasswordLoginMethod struct {
	IsSet     bool `json:"is_set"`
	CanLogin  bool `json:"can_login"`
	CanSet    bool `json:"can_set"`
	CanDelete bool `json:"can_delete"`
}
type EmailLoginMethod struct {
	Address    *string `json:"address"`
	Verified   bool    `json:"verified"`
	CanRecover bool    `json:"can_recover"`
}
type LoginMethodsResult struct {
	Password             PasswordLoginMethod        `json:"password"`
	Email                EmailLoginMethod           `json:"email"`
	Bindings             []LoginMethodRecord        `json:"bindings"`
	AvailableConnections []AvailableLoginConnection `json:"available_connections"`
}

func (s *ExternalAuthService) LoginMethods(ctx context.Context, userID int) (LoginMethodsResult, error) {
	out := LoginMethodsResult{Bindings: []LoginMethodRecord{}, AvailableConnections: []AvailableLoginConnection{}}
	account, err := s.client.User.Get(ctx, userID)
	if err != nil {
		return out, err
	}
	if err = checkExternalActor(ctx, account); err != nil {
		return out, err
	}
	passwordPolicy, err := readPasswordPolicy(ctx, s.client)
	if err != nil {
		return out, err
	}
	out.Password.IsSet = account.PasswordHash != ""
	out.Password.CanLogin = out.Password.IsSet && passwordPolicy.canLogin()
	out.Password.CanSet = !out.Password.IsSet && passwordPolicy.canLogin()
	if account.Email != "" {
		value := account.Email
		out.Email.Address = &value
	}
	out.Email.Verified = account.EmailVerifiedAt != nil
	out.Email.CanRecover = false
	bindings, err := s.client.LoginBinding.Query().Where(loginbinding.UserIDEQ(userID)).All(ctx)
	if err != nil {
		return out, err
	}
	bound := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		bound[binding.ConnectionID] = true
		p, readErr := readExternalConnection(ctx, s.client, binding.ConnectionID, 0)
		if readErr != nil {
			if p.Connection == nil {
				continue
			}
		}
		record := LoginMethodRecord{BindingID: binding.ID, ConnectionID: binding.ConnectionID}
		if p.Connection != nil {
			record.Archived = p.Connection.ArchivedAt != nil
			record.DisplayName = p.Config.DisplayName
			record.CanLogin = !record.Archived && p.Policy.Enabled && p.Policy.LoginEnabled
			record.Verified = binding.LastVerifiedAt != nil && binding.VerifiedAuthGeneration == p.Connection.AuthGeneration && binding.VerifiedLoginPolicyRevision == p.Policy.LoginPolicyRevision
			if record.CanLogin {
				fresh, _ := hasFreshValidation(ctx, s.client, binding.ConnectionID, connectionFingerprint(p.Config))
				record.CanReauthenticate = fresh
			}
		}
		fallback, fallbackErr := connectionFallbackAvailable(ctx, s.client, account, binding.ConnectionID)
		if fallbackErr == nil {
			record.CanUnlink = fallback
		}
		out.Bindings = append(out.Bindings, record)
	}
	out.Password.CanDelete = false
	if out.Password.IsSet {
		out.Password.CanDelete, _ = connectionFallbackAvailable(ctx, s.client, account, PasswordConnectionID)
	}
	connections, err := s.client.AuthConnection.Query().Where(authconnection.KindEQ(authconnection.KindOidc), authconnection.ArchivedAtIsNil(), authconnection.PublishedRevisionNotNil()).All(ctx)
	if err != nil {
		return out, err
	}
	for _, connection := range connections {
		if bound[connection.ID] {
			continue
		}
		p, err := readExternalConnection(ctx, s.client, connection.ID, 0)
		if err != nil {
			return out, err
		}
		if !p.Policy.Enabled || !p.Policy.LinkEnabled {
			continue
		}
		var logo *string
		if len(p.Config.LogoData) > 0 {
			v := "/api/v1/auth/connections/" + connection.ID + "/logo"
			logo = &v
		}
		out.AvailableConnections = append(out.AvailableConnections, AvailableLoginConnection{ConnectionID: connection.ID, DisplayName: p.Config.DisplayName, CanLink: true, LogoURL: logo})
	}
	return out, nil
}

func (s *ExternalAuthService) Unlink(ctx context.Context, userID, bindingID int, rawProof string) error {
	return withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
		binding, err := c.LoginBinding.Query().Where(loginbinding.IDEQ(bindingID), loginbinding.UserIDEQ(userID)).Only(ctx)
		if ent.IsNotFound(err) {
			return ErrAuthConnectionNotFound
		}
		if err != nil {
			return err
		}
		if err = lockAuthConnection(ctx, c, binding.ConnectionID); err != nil {
			return err
		}
		if err = lockAuthUser(ctx, c, userID); err != nil {
			return err
		}
		account, err := c.User.Get(ctx, userID)
		if err != nil {
			return err
		}
		if err = checkExternalActor(ctx, account); err != nil {
			return err
		}
		proof, err := s.consumeReauthenticationProof(ctx, c, userID, rawProof, "unlink", strconv.Itoa(bindingID))
		if err != nil {
			return err
		}
		fallback, err := connectionFallbackAvailable(ctx, c, account, binding.ConnectionID)
		if err != nil {
			return err
		}
		if !fallback {
			return ErrLoginMethodConflict
		}
		if proof.ConnectionID == binding.ConnectionID {
			return ErrReauthenticationRequired
		}
		identity, err := c.ExternalIdentity.Get(ctx, binding.ExternalIdentityID)
		if err != nil {
			return err
		}
		now := timeutil.Normalize(s.now())
		if err = revokeUserSessions(ctx, c, userID, now); err != nil {
			return err
		}
		if _, err = c.ExecContext(ctx, "UPDATE refresh_sessions SET binding_id = NULL WHERE binding_id = "+strconv.Itoa(bindingID)); err != nil {
			return err
		}
		if err = c.LoginBinding.DeleteOneID(bindingID).Exec(ctx); err != nil {
			return err
		}
		// Invalidate already verified anonymous tickets too: otherwise an old
		// callback could reclaim a subject after its last binding is removed.
		if _, err = c.AuthTransaction.Update().Where(
			authtransaction.StatusIn(authtransaction.StatusCreated, authtransaction.StatusCallbackProcessing, authtransaction.StatusCallbackVerified, authtransaction.StatusAwaitingConfirmation),
			authtransaction.Or(authtransaction.UserIDEQ(userID), authtransaction.And(
				authtransaction.PurposeNEQ(authtransaction.PurposeTest),
				authtransaction.IdentityIssuerEQ(identity.Authority), authtransaction.IdentitySubjectEQ(identity.Subject),
			)),
		).SetStatus(authtransaction.StatusCancelled).SetFinishedAt(now).ClearExchangeTicketHash().ClearExchangeTicketExpiresAt().ClearConfirmationTokenHash().ClearConfirmationExpiresAt().Save(ctx); err != nil {
			return err
		}
		// Other connections may still bind the same issuer/subject. The user
		// lock serializes binding changes, and the FK protects concurrent owners.
		if _, err = c.ExternalIdentity.Delete().Where(externalidentity.IDEQ(identity.ID), externalidentity.Not(externalidentity.HasBindings())).Exec(ctx); err != nil {
			return err
		}
		return recordAuditEvent(ctx, c, AuditEvent{ActorUserID: userID, Action: "account.login_binding.unlinked", ResourceType: "login_binding", ResourceID: bindingID, VisibilityScope: "personal", Metadata: map[string]any{"connection_id": binding.ConnectionID}})
	})
}

func (s *ExternalAuthService) SetInitialPassword(ctx context.Context, userID int, newPassword, rawProof string) error {
	if err := validateNewPassword(newPassword); err != nil {
		return err
	}
	passwordHash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	return withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
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
		if account.PasswordHash != "" || !p.canLogin() {
			return ErrLoginMethodConflict
		}
		if _, err = s.consumeReauthenticationProof(ctx, c, userID, rawProof, "password_set", PasswordConnectionID); err != nil {
			return err
		}
		if err = revokeUserSessions(ctx, c, userID, timeutil.Normalize(s.now())); err != nil {
			return err
		}
		if err = c.User.UpdateOneID(userID).SetPasswordHash(passwordHash).Exec(ctx); err != nil {
			return err
		}
		return recordAuditEvent(ctx, c, AuditEvent{ActorUserID: userID, Action: "account.password.set", ResourceType: "user", ResourceID: userID, VisibilityScope: "personal"})
	})
}

func (s *ExternalAuthService) DeletePassword(ctx context.Context, userID int, rawProof string) error {
	return withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
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
		if account.PasswordHash == "" {
			return ErrLoginMethodConflict
		}
		proof, err := s.consumeReauthenticationProof(ctx, c, userID, rawProof, "password_delete", PasswordConnectionID)
		if err != nil {
			return err
		}
		fallback, err := connectionFallbackAvailable(ctx, c, account, PasswordConnectionID)
		if err != nil {
			return err
		}
		if !fallback {
			return ErrLoginMethodConflict
		}
		if proof.Method == reauthenticationproof.MethodPassword || proof.ConnectionID == PasswordConnectionID {
			return ErrReauthenticationRequired
		}
		if account.Role == SystemRoleAdmin {
			remaining, err := c.User.Query().Where(user.IDNEQ(userID), user.ActiveEQ(true), user.RoleEQ(SystemRoleAdmin), user.PasswordHashNotNil(), user.PasswordHashNEQ("")).Count(ctx)
			if err != nil {
				return err
			}
			if remaining < 1 {
				return ErrLoginMethodConflict
			}
		}
		now := timeutil.Normalize(s.now())
		if err = revokeUserSessions(ctx, c, userID, now); err != nil {
			return err
		}
		if err = c.User.UpdateOneID(userID).ClearPasswordHash().Exec(ctx); err != nil {
			return err
		}
		return recordAuditEvent(ctx, c, AuditEvent{ActorUserID: userID, Action: "account.password.deleted", ResourceType: "user", ResourceID: userID, VisibilityScope: "personal"})
	})
}

// CleanupExpired removes short-lived proofs and provider profile data. Terminal
// rows remain until the original deadline so retries stay unambiguously invalid.
func (s *ExternalAuthService) CleanupExpired(ctx context.Context) error {
	now := timeutil.Normalize(s.now())
	if _, err := s.client.AuthTransaction.Delete().Where(authtransaction.ExpiresAtLTE(now)).Exec(ctx); err != nil {
		return err
	}
	_, err := s.client.ReauthenticationProof.Delete().Where(reauthenticationproof.ExpiresAtLTE(now)).Exec(ctx)
	return err
}
