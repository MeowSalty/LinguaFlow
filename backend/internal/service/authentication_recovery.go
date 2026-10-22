package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/accountidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authenticationrecovery"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/instanceinitialization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

const (
	deploymentRecoveryTokenLifetime = 5 * time.Minute
	deploymentRecoveryTokenSize     = 32
)

var (
	ErrAuthenticationRecoveryInvalid   = errors.New("authentication recovery token is invalid, expired, revoked, or already used")
	ErrAuthenticationRecoveryConflict  = errors.New("authentication recovery authorization or policy changed")
	ErrDeploymentRecoveryUninitialized = errors.New("deployment recovery is only available on an initialized serve instance")
	ErrMissingAdministrator            = errors.New("deployment recovery target must be an active administrator")
)

type DeploymentRecoveryService struct {
	client *ent.Client
	now    func() time.Time
}

type DeploymentRecoveryIssueInput struct {
	Username string
}

type DeploymentRecoveryRestoreInput struct {
	Token    []byte
	Password string
}

type DeploymentRecoveryIssued struct {
	Token     []byte
	UserID    int
	ExpiresAt time.Time
}

func NewDeploymentRecoveryService(client *ent.Client) *DeploymentRecoveryService {
	return &DeploymentRecoveryService{client: client, now: timeutil.NowUTC}
}

func (s *DeploymentRecoveryService) Issue(ctx context.Context, input DeploymentRecoveryIssueInput) (*DeploymentRecoveryIssued, error) {
	username, err := accountidentity.NormalizeUsername(input.Username)
	if err != nil {
		return nil, ErrInvalidInput
	}
	var result *DeploymentRecoveryIssued
	err = withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
		if err := lockAuthenticationConfiguration(ctx, c); err != nil {
			return err
		}
		if err := requireServeDeploymentRecoveryPermissions(ctx, c); err != nil {
			return err
		}
		target, err := c.User.Query().Where(user.UsernameEQ(username), user.ActiveEQ(true), user.RoleEQ(SystemRoleAdmin)).Only(ctx)
		if err != nil {
			return ErrMissingAdministrator
		}
		if err := lockAuthUser(ctx, c, target.ID); err != nil {
			return ErrMissingAdministrator
		}
		target, err = c.User.Get(ctx, target.ID)
		if err != nil || !target.Active || target.Role != SystemRoleAdmin {
			return ErrMissingAdministrator
		}
		policy, err := readPasswordPolicy(ctx, c)
		if err != nil {
			return err
		}
		now := timeutil.Normalize(s.now())
		exists, err := c.AuthenticationRecovery.Query().Where(
			authenticationrecovery.UserIDEQ(target.ID),
			authenticationrecovery.ConsumedAtIsNil(),
			authenticationrecovery.RevokedAtIsNil(),
			authenticationrecovery.ExpiresAtGT(now),
		).Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			return ErrAuthenticationRecoveryConflict
		}
		token := make([]byte, deploymentRecoveryTokenSize)
		if _, err := rand.Read(token); err != nil {
			return err
		}
		expires := timeutil.Normalize(now.Add(deploymentRecoveryTokenLifetime))
		if err := c.AuthenticationRecovery.Create().
			SetTokenHash(hash.Full(deploymentRecoveryTokenString(token))).
			SetUserID(target.ID).
			SetAuthVersion(target.AuthVersion).
			SetLocalPolicyRevision(policy.Local.Revision).
			SetAction(authenticationrecovery.ActionRestoreLocalLogin).
			SetExpiresAt(expires).
			Exec(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return ErrAuthenticationRecoveryConflict
			}
			return err
		}
		if err := recordAuditEvent(ctx, c, AuditEvent{
			Action:       "admin.authentication_recovery.issued",
			ResourceType: "local_account",
			ResourceID:   1,
			Metadata: map[string]any{
				"target_user_id":        target.ID,
				"local_policy_revision": policy.Local.Revision,
				"auth_version":          target.AuthVersion,
			},
		}); err != nil {
			return err
		}
		result = &DeploymentRecoveryIssued{Token: token, UserID: target.ID, ExpiresAt: expires}
		return nil
	})
	return result, err
}

func (s *DeploymentRecoveryService) RestoreLocalLogin(ctx context.Context, input DeploymentRecoveryRestoreInput) error {
	if len(input.Token) != deploymentRecoveryTokenSize {
		return ErrAuthenticationRecoveryInvalid
	}
	if err := validateNewPassword(input.Password); err != nil {
		return err
	}
	passwordHash, err := hashPassword(input.Password)
	if err != nil {
		return err
	}
	return withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
		if err := lockAuthenticationConfiguration(ctx, c); err != nil {
			return err
		}
		if err := requireServeDeploymentRecoveryPermissions(ctx, c); err != nil {
			return err
		}
		row, err := c.AuthenticationRecovery.Query().Where(
			authenticationrecovery.TokenHashEQ(hash.Full(deploymentRecoveryTokenString(input.Token))),
		).Only(ctx)
		if err != nil {
			return ErrAuthenticationRecoveryInvalid
		}
		if row.Action != authenticationrecovery.ActionRestoreLocalLogin {
			return ErrAuthenticationRecoveryInvalid
		}
		now := timeutil.Normalize(s.now())
		if row.ConsumedAt != nil || row.RevokedAt != nil || !now.Before(timeutil.Normalize(row.ExpiresAt)) {
			return ErrAuthenticationRecoveryInvalid
		}
		if err := lockAuthUser(ctx, c, row.UserID); err != nil {
			return ErrAuthenticationRecoveryInvalid
		}
		target, err := c.User.Get(ctx, row.UserID)
		if err != nil || !target.Active || target.Role != SystemRoleAdmin {
			return ErrMissingAdministrator
		}
		if target.AuthVersion != row.AuthVersion {
			return ErrAuthenticationRecoveryConflict
		}
		p, err := readPasswordPolicy(ctx, c)
		if err != nil {
			return err
		}
		if p.Local.Revision != row.LocalPolicyRevision {
			return ErrAuthenticationRecoveryConflict
		}
		nextPolicy := localAccountInput(p.Local)
		nextPolicy.PasswordLoginEnabled = true
		nextPolicy.UsernameLoginEnabled = true
		nextPolicy.Signup.Enabled = false
		if err := writeLocalAccountPolicy(ctx, c, p, nextPolicy, now); err != nil {
			return err
		}
		if _, err := c.User.UpdateOneID(target.ID).SetPasswordHash(passwordHash).ClearPasswordVerifiedHash().ClearPasswordVerifiedAt().Save(ctx); err != nil {
			return err
		}
		if err := revokeUserSessions(ctx, c, target.ID, now); err != nil {
			return err
		}
		updated, err := c.AuthenticationRecovery.Update().
			Where(
				authenticationrecovery.IDEQ(row.ID),
				authenticationrecovery.TokenHashEQ(hash.Full(deploymentRecoveryTokenString(input.Token))),
				authenticationrecovery.ConsumedAtIsNil(),
				authenticationrecovery.RevokedAtIsNil(),
				authenticationrecovery.ExpiresAtGT(now),
			).
			SetConsumedAt(now).
			Save(ctx)
		if err != nil {
			return err
		}
		if updated != 1 {
			return ErrAuthenticationRecoveryInvalid
		}
		return recordAuditEvent(ctx, c, AuditEvent{
			Action:       "admin.authentication_recovery.restored",
			ResourceType: "local_account",
			ResourceID:   1,
			Metadata: map[string]any{
				"target_user_id":        target.ID,
				"local_policy_revision": p.Local.Revision,
				"auth_version":          target.AuthVersion,
			},
		})
	})
}

func (s *DeploymentRecoveryService) Revoke(ctx context.Context, token []byte) error {
	if len(token) != deploymentRecoveryTokenSize {
		return ErrAuthenticationRecoveryInvalid
	}
	return withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
		if err := lockAuthenticationConfiguration(ctx, c); err != nil {
			return err
		}
		if err := requireServeDeploymentRecoveryPermissions(ctx, c); err != nil {
			return err
		}
		row, err := c.AuthenticationRecovery.Query().Where(
			authenticationrecovery.TokenHashEQ(hash.Full(deploymentRecoveryTokenString(token))),
			authenticationrecovery.ConsumedAtIsNil(),
			authenticationrecovery.RevokedAtIsNil(),
		).Only(ctx)
		if err != nil {
			return ErrAuthenticationRecoveryInvalid
		}
		if row.Action != authenticationrecovery.ActionRestoreLocalLogin {
			return ErrAuthenticationRecoveryInvalid
		}
		now := timeutil.Normalize(s.now())
		if row.ConsumedAt != nil || row.RevokedAt != nil || !now.Before(timeutil.Normalize(row.ExpiresAt)) {
			return ErrAuthenticationRecoveryInvalid
		}
		if err := lockAuthUser(ctx, c, row.UserID); err != nil {
			return ErrAuthenticationRecoveryInvalid
		}
		target, err := c.User.Get(ctx, row.UserID)
		if err != nil || !target.Active || target.Role != SystemRoleAdmin {
			return ErrMissingAdministrator
		}
		if target.AuthVersion != row.AuthVersion {
			return ErrAuthenticationRecoveryConflict
		}
		policy, err := readPasswordPolicy(ctx, c)
		if err != nil {
			return err
		}
		if policy.Local.Revision != row.LocalPolicyRevision {
			return ErrAuthenticationRecoveryConflict
		}
		updated, err := c.AuthenticationRecovery.Update().
			Where(
				authenticationrecovery.IDEQ(row.ID),
				authenticationrecovery.TokenHashEQ(hash.Full(deploymentRecoveryTokenString(token))),
				authenticationrecovery.ConsumedAtIsNil(),
				authenticationrecovery.RevokedAtIsNil(),
			).
			SetRevokedAt(now).
			Save(ctx)
		if err != nil {
			return err
		}
		if updated != 1 {
			return ErrAuthenticationRecoveryInvalid
		}
		return recordAuditEvent(ctx, c, AuditEvent{
			Action:       "admin.authentication_recovery.revoked",
			ResourceType: "local_account",
			ResourceID:   1,
			Metadata: map[string]any{
				"target_user_id":        target.ID,
				"local_policy_revision": policy.Local.Revision,
				"auth_version":          target.AuthVersion,
			},
		})
	})
}

func requireServeDeploymentRecoveryPermissions(ctx context.Context, client *ent.Client) error {
	rows, err := client.InstanceInitialization.Query().Where(
		instanceinitialization.IDEQ(1),
		instanceinitialization.ModeEQ(instanceinitialization.ModeServe),
	).All(ctx)
	if err != nil || len(rows) != 1 {
		return ErrDeploymentRecoveryUninitialized
	}
	return nil
}

func deploymentRecoveryTokenString(token []byte) string {
	return base64.RawURLEncoding.EncodeToString(token)
}
