package service

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authchannelpolicy"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/google/uuid"
)

func (s *ExternalAuthService) CreateConnection(ctx context.Context, actorID int, in CreateAuthConnectionInput) (AuthConnectionRecord, error) {
	if !supportedExternalKind(authconnection.Kind(in.Kind)) {
		return AuthConnectionRecord{}, ErrInvalidInput
	}
	var result AuthConnectionRecord
	err := withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
		if err := lockExternalConfiguration(ctx, c, actorID); err != nil {
			return err
		}
		id := uuid.NewString()
		q := c.AuthConnection.Create().SetID(id).SetKind(authconnection.Kind(in.Kind)).SetState(authconnection.StateConfiguring).SetCurrentRevision(1)
		if in.Kind == "generic_oauth2" {
			q.SetIdentityAuthorityID(uuid.NewString())
		}
		row, err := q.Save(ctx)
		if err != nil {
			return err
		}
		cfg := &ent.AuthConnectionRevision{ConnectionID: id, Revision: 1, ConfigVersion: 1, EvidenceMaxAgeSeconds: 3600, DisplayName: in.DisplayName, Description: in.Description, DisplayOrder: in.DisplayOrder}
		if in.Kind == "oidc" {
			v, _ := normalizeOIDCInput(OIDCConnectionInput{})
			cfg.OidcClientAuthMethod = authconnectionrevision.OidcClientAuthMethod(v.ClientAuthMethod)
			cfg.OidcScopes = v.Scopes
			cfg.OidcUserinfoEnabled = *v.UserinfoEnabled
		} else {
			v, err := s.normalizeOAuthInput(ctx, OAuth2ConnectionInput{})
			if err != nil {
				return err
			}
			setOAuthInput(cfg, v)
		}
		policy := defaultExternalPolicy()
		p := externalConnection{Connection: row, Config: cfg, Policy: &ent.AuthChannelPolicy{Revision: 1, LoginPolicyRevision: 1, SignupPolicyRevision: 1, LinkPolicyRevision: 1, LoginRules: policy.LoginRules, SignupRules: policy.SignupRules, LinkRules: policy.LinkRules, SignupEmailRequirement: authchannelpolicy.SignupEmailRequirementOptional, SignupPasswordRequirement: authchannelpolicy.SignupPasswordRequirementNone}}
		changes := AuthConnectionChanges{OIDC: in.OIDC, OAuth2: in.OAuth2}
		if in.ClientSecret != "" {
			changes.ClientSecret, _ = json.Marshal(in.ClientSecret)
		}
		p, err = s.applyConnectionChanges(ctx, c, p, &changes)
		if err != nil {
			return err
		}
		if err = s.persistConnectionSnapshot(ctx, c, p, 1, actorID); err != nil {
			return err
		}
		if err = recordAuditEvent(ctx, c, AuditEvent{ActorUserID: actorID, Action: "admin.auth_connection.created", ResourceType: "auth_connection", Metadata: map[string]any{"connection_id": id, "revision": 1}}); err != nil {
			return err
		}
		result, err = s.connectionRecord(ctx, c, row)
		return err
	})
	return result, err
}

func (s *ExternalAuthService) mutateConnection(ctx context.Context, actorID int, id string, expected int64, mutate func(*ent.Client, *ent.AuthConnection, externalConnection) error) (AuthConnectionRecord, error) {
	var result AuthConnectionRecord
	if expected < 1 {
		return result, ErrInvalidInput
	}
	err := withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
		if err := lockExternalConfiguration(ctx, c, actorID); err != nil {
			return err
		}
		p, err := readExternalConnection(ctx, c, id, 0)
		if err != nil {
			return err
		}
		row := p.Connection
		if !supportedExternalKind(row.Kind) {
			return ErrInvalidInput
		}
		if row.AggregateRevision != expected || row.AggregateRevision >= maxAuthVersion {
			return connectionError("revision-conflict")
		}
		if err = mutate(c, row, p); err != nil {
			return err
		}
		row, err = c.AuthConnection.Get(ctx, id)
		if err != nil {
			return err
		}
		result, err = s.connectionRecord(ctx, c, row)
		return err
	})
	return result, err
}

func (s *ExternalAuthService) persistConnectionSnapshot(ctx context.Context, c *ent.Client, p externalConnection, next int64, actorID int) error {
	if p.SecretOverride != nil {
		if *p.SecretOverride == "" {
			p.Config.SecretVersion = nil
		} else {
			if err := s.saveConnectionSecret(ctx, c, p.Connection.ID, next, *p.SecretOverride, actorID); err != nil {
				return err
			}
			p.Config.SecretVersion = &next
		}
	}
	return saveExternalSnapshot(ctx, c, p.Connection.ID, next, p.Policy.LoginPolicyRevision, p.Config, policyInput(p.Policy), p.Policy.SignupPolicyRevision, p.Policy.LinkPolicyRevision)
}

func sameConnectionContent(a, b externalConnection) bool {
	x, y := connectionSnapshot(a), connectionSnapshot(b)
	x.LogoURL = nil
	y.LogoURL = nil
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return bytes.Equal(xb, yb) && bytes.Equal(a.Config.LogoData, b.Config.LogoData) && a.Config.LogoMediaType == b.Config.LogoMediaType && b.SecretOverride == nil && a.Config.FreshSupported == b.Config.FreshSupported
}

func (s *ExternalAuthService) UpdateConnection(ctx context.Context, actorID int, id string, in UpdateAuthConnectionInput) (AuthConnectionRecord, error) {
	return s.mutateConnection(ctx, actorID, id, in.ExpectedRevision, func(c *ent.Client, row *ent.AuthConnection, current externalConnection) error {
		p, err := s.applyConnectionChanges(ctx, c, current, &in.AuthConnectionChanges)
		if err != nil {
			return err
		}
		protocolChanged := connectionFingerprint(current.Config) != connectionFingerprint(p.Config) || p.SecretOverride != nil
		if protocolChanged {
			p.Config.FreshSupported = false
			p.Config.TestedAt = nil
			p.Config.AppliedTestID = ""
		}
		var proof *ent.AuthTransaction
		if row.State == authconnection.StateEnabled {
			if err = s.requireConnectionReady(p); err != nil {
				return err
			}
			if protocolChanged || in.TestID != "" {
				proof, err = s.verifyConnectionTest(ctx, c, actorID, p, in.ExpectedRevision, in.TestID, in.Verifier, false)
				if err != nil {
					return err
				}
				applyTestCapability(p.Config, proof, s.now())
			}
		} else if in.TestID != "" {
			proof, err = s.verifyConnectionTest(ctx, c, actorID, p, in.ExpectedRevision, in.TestID, in.Verifier, false)
			if err != nil {
				return err
			}
			applyTestCapability(p.Config, proof, s.now())
		}
		if sameConnectionContent(current, p) {
			return nil
		}
		if err := ensureExternalSignupReady(ctx, c, p); err != nil {
			return err
		}
		if purposePolicyChanged(current, p, "signup") {
			if p.Policy.SignupPolicyRevision >= maxAuthVersion {
				return connectionError("revision-conflict")
			}
			p.Policy.SignupPolicyRevision++
		}
		if purposePolicyChanged(current, p, "link") {
			if p.Policy.LinkPolicyRevision >= maxAuthVersion {
				return connectionError("revision-conflict")
			}
			p.Policy.LinkPolicyRevision++
		}
		generation := row.AuthGeneration
		invalidates := loginRulesChanged(current, p)
		turnsOff := current.Policy.Enabled && current.Policy.LoginEnabled && !p.Policy.LoginEnabled
		freshChanged := current.Config.FreshSupported != p.Config.FreshSupported
		if row.State == authconnection.StateEnabled && (invalidates || turnsOff || freshChanged) {
			if err = s.ensureConnectionImpact(ctx, c, p, !p.Policy.LoginEnabled); err != nil {
				return err
			}
			if generation >= maxAuthVersion || p.Policy.LoginPolicyRevision >= maxAuthVersion {
				return connectionError("revision-conflict")
			}
			generation++
			if invalidates {
				p.Policy.LoginPolicyRevision++
			}
			if err = revokeConnectionSessions(ctx, c, id, s.now()); err != nil {
				return err
			}
		}
		next := row.AggregateRevision + 1
		if err = s.persistConnectionSnapshot(ctx, c, p, next, actorID); err != nil {
			return err
		}
		if err = c.AuthConnection.UpdateOneID(id).SetCurrentRevision(next).SetAggregateRevision(next).SetAuthGeneration(generation).Exec(ctx); err != nil {
			return err
		}
		if proof != nil {
			if err = markConnectionTestApplied(ctx, c, proof, next); err != nil {
				return err
			}
		}
		return recordAuditEvent(ctx, c, AuditEvent{ActorUserID: actorID, Action: "admin.auth_connection.saved", ResourceType: "auth_connection", Metadata: map[string]any{"connection_id": id, "revision": next, "auth_generation": generation}})
	})
}

func (s *ExternalAuthService) ensureConnectionImpact(ctx context.Context, c *ent.Client, p externalConnection, disabled bool) error {
	impact, err := candidateImpact(ctx, c, p, s.now(), disabled)
	if err != nil {
		return err
	}
	if !impact.CanSave {
		return &AuthConnectionError{Code: "fallback-required", Impact: &impact}
	}
	return nil
}

func (s *ExternalAuthService) ConfigureConnection(ctx context.Context, actorID int, id string, in AuthConnectionActionInput) (AuthConnectionRecord, error) {
	return s.changeConnectionState(ctx, actorID, id, in, authconnection.StateConfiguring, nil)
}
func (s *ExternalAuthService) EnableConnection(ctx context.Context, actorID int, id string, in AuthConnectionActionInput) (AuthConnectionRecord, error) {
	return s.changeConnectionState(ctx, actorID, id, in, authconnection.StateEnabled, nil)
}
func (s *ExternalAuthService) DisableConnection(ctx context.Context, actorID int, id string, in AuthConnectionActionInput) (AuthConnectionRecord, error) {
	return s.changeConnectionState(ctx, actorID, id, in, authconnection.StateDisabled, nil)
}
func (s *ExternalAuthService) ArchiveConnection(ctx context.Context, actorID int, id string, in AuthConnectionActionInput) (AuthConnectionRecord, error) {
	v := true
	return s.changeConnectionState(ctx, actorID, id, in, authconnection.StateDisabled, &v)
}
func (s *ExternalAuthService) RestoreConnection(ctx context.Context, actorID int, id string, in AuthConnectionActionInput) (AuthConnectionRecord, error) {
	v := false
	return s.changeConnectionState(ctx, actorID, id, in, authconnection.StateDisabled, &v)
}

func (s *ExternalAuthService) changeConnectionState(ctx context.Context, actorID int, id string, in AuthConnectionActionInput, target authconnection.State, archive *bool) (AuthConnectionRecord, error) {
	// Authorization precedes cancellation so an unauthenticated caller cannot
	// disturb another administrator's provider request.
	account, err := s.client.User.Get(ctx, actorID)
	if err != nil {
		return AuthConnectionRecord{}, err
	}
	if err = checkExternalActor(ctx, account); err != nil {
		return AuthConnectionRecord{}, err
	}
	if err = checkConnectionAdmin(ctx, s.client, actorID); err != nil {
		return AuthConnectionRecord{}, err
	}
	observed, err := s.client.AuthConnection.Get(ctx, id)
	if ent.IsNotFound(err) {
		return AuthConnectionRecord{}, ErrAuthConnectionNotFound
	}
	if err != nil {
		return AuthConnectionRecord{}, err
	}
	if observed.AggregateRevision != in.ExpectedRevision {
		return AuthConnectionRecord{}, connectionError("revision-conflict")
	}
	if target != authconnection.StateEnabled && (observed.State != target || (archive != nil && *archive != (observed.ArchivedAt != nil))) {
		timeout := s.cfg.RequestTimeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		blockCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		unblock, err := s.gate.Block(blockCtx, id)
		if err != nil {
			return AuthConnectionRecord{}, err
		}
		defer unblock()
	}
	return s.mutateConnection(ctx, actorID, id, in.ExpectedRevision, func(c *ent.Client, row *ent.AuthConnection, p externalConnection) error {
		if archive == nil && row.ArchivedAt != nil {
			return connectionError("state-conflict")
		}
		if row.State == target && (archive == nil || *archive == (row.ArchivedAt != nil)) {
			return nil
		}
		if archive != nil && !*archive && row.ArchivedAt == nil {
			return connectionError("state-conflict")
		}
		var proof *ent.AuthTransaction
		if target == authconnection.StateEnabled {
			if row.State != authconnection.StateConfiguring {
				return connectionError("state-conflict")
			}
			if err := s.requireConnectionReady(p); err != nil {
				return err
			}
			var err error
			proof, err = s.verifyConnectionTest(ctx, c, actorID, p, in.ExpectedRevision, in.TestID, in.Verifier, true)
			if err != nil {
				return err
			}
			applyTestCapability(p.Config, proof, s.now())
			p.Policy.Enabled = true
			if err := ensureExternalSignupReady(ctx, c, p); err != nil {
				return err
			}
			if err = s.ensureConnectionImpact(ctx, c, p, !p.Policy.LoginEnabled); err != nil {
				return err
			}
		} else if row.State == authconnection.StateEnabled {
			if err := s.ensureConnectionImpact(ctx, c, p, true); err != nil {
				return err
			}
		}
		if err := ensureIdentityContract(ctx, c, p); err != nil {
			return err
		}
		next := row.AggregateRevision + 1
		update := c.AuthConnection.UpdateOneID(id).SetState(target).SetAggregateRevision(next)
		if target == authconnection.StateEnabled {
			if err := s.persistConnectionSnapshot(ctx, c, p, next, actorID); err != nil {
				return err
			}
			update.SetCurrentRevision(next)
			if row.IdentityContract == "" {
				update.SetIdentityContract(identityContract(p))
			}
			if err := markConnectionTestApplied(ctx, c, proof, next); err != nil {
				return err
			}
		} else {
			if row.AuthGeneration >= maxAuthVersion {
				return connectionError("revision-conflict")
			}
			update.AddAuthGeneration(1)
			if err := revokeConnectionSessions(ctx, c, id, s.now()); err != nil {
				return err
			}
			if _, err := c.AuthTransaction.Update().Where(authtransaction.ConnectionIDEQ(id), authtransaction.PurposeEQ(authtransaction.PurposeTest)).SetStatus(authtransaction.StatusCancelled).SetFinishedAt(s.now()).ClearTestValidatedAt().ClearExchangeTicketHash().ClearConfirmationTokenHash().Save(ctx); err != nil {
				return err
			}
		}
		if archive != nil {
			if *archive {
				update.SetArchivedAt(s.now())
			} else {
				update.ClearArchivedAt()
			}
		}
		if err := update.Exec(ctx); err != nil {
			return err
		}
		action := "admin.auth_connection." + string(target)
		if archive != nil {
			if *archive {
				action = "admin.auth_connection.archived"
			} else {
				action = "admin.auth_connection.restored"
			}
		}
		return recordAuditEvent(ctx, c, AuditEvent{ActorUserID: actorID, Action: action, ResourceType: "auth_connection", Metadata: map[string]any{"connection_id": id, "revision": next}})
	})
}
