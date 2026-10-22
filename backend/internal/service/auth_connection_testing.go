package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authhttp"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
)

type authTestSnapshot struct {
	Changes       AuthConnectionChanges `json:"configuration"`
	CallbackURL   string                `json:"callback_url"`
	CompletionURL string                `json:"completion_url"`
}
type authTestStartKey struct{}
type providerFlowKey struct{}

func externalSessionID(ctx context.Context) int {
	v, _ := ctx.Value(externalActorContextKey{}).(externalActorVersion)
	return v.sessionID
}
func WithExternalActorSession(ctx context.Context, userID int, authVersion int64, sessionID int) context.Context {
	return context.WithValue(ctx, externalActorContextKey{}, externalActorVersion{userID: userID, authVersion: authVersion, sessionID: sessionID})
}
func (s *ExternalAuthService) SetRequestGate(g *authhttp.Gate) {
	if g != nil {
		s.gate = g
	}
}

func (s *ExternalAuthService) checkTestSession(ctx context.Context, c *ent.Client, actorID int, authVersion int64, sessionID int) error {
	if sessionID < 1 {
		return ErrTokenInvalid
	}
	account, err := c.User.Get(ctx, actorID)
	if err != nil {
		return ErrTokenInvalid
	}
	if !account.Active || account.AuthVersion != authVersion {
		return ErrTokenInvalid
	}
	if account.Role != SystemRoleAdmin {
		return ErrForbidden
	}
	session, err := c.RefreshSession.Get(ctx, sessionID)
	if err != nil {
		return ErrTokenInvalid
	}
	if session.UserID != actorID || session.AuthVersion != authVersion || session.RevokedAt != nil || !s.now().Before(session.ExpiresAt) {
		return ErrTokenInvalid
	}
	return nil
}

func fingerprintValue(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func (s *ExternalAuthService) deploymentFingerprint() string {
	return fingerprintValue([]any{s.cfg, s.corsOrigins})
}
func testProtocolFingerprint(p externalConnection, id string) string {
	material := ""
	if p.SecretOverride != nil {
		material = id
	}
	return fingerprintValue([]any{connectionFingerprint(p.Config), material})
}
func testDiagnosticFingerprint(p externalConnection) string {
	return fingerprintValue([]any{append([]authrules.Field{}, p.Config.AttributeMappings...), p.Config.EvidenceMaxAgeSeconds, policyInput(p.Policy)})
}
func captureTestSnapshot(p externalConnection, callback, completion string) authTestSnapshot {
	v := connectionSnapshot(p)
	in := AuthConnectionChanges{DisplayName: &v.DisplayName, Description: &v.Description, DisplayOrder: &v.DisplayOrder, OIDC: v.OIDC, OAuth2: v.OAuth2, Policy: &v.Policy, AttributeMappings: &v.AttributeMappings, EvidenceMaxAgeSeconds: &v.EvidenceMaxAgeSeconds}
	if p.SecretOverride != nil {
		if *p.SecretOverride == "" {
			in.ClientSecret = json.RawMessage("null")
		} else {
			in.ClientSecret, _ = json.Marshal(*p.SecretOverride)
		}
	}
	return authTestSnapshot{Changes: in, CallbackURL: callback, CompletionURL: completion}
}

func (s *ExternalAuthService) StartTest(ctx context.Context, actorID int, id string, in AuthConnectionTestInput) (ExternalStartResult, error) {
	p, err := s.candidateConnection(ctx, actorID, id, in.AuthConnectionCandidateInput)
	if err != nil {
		return ExternalStartResult{}, err
	}
	if p.Connection.ArchivedAt != nil || p.Connection.State == authconnection.StateDisabled {
		return ExternalStartResult{}, connectionError("state-conflict")
	}
	if err = s.requireConnectionReady(p); err != nil {
		return ExternalStartResult{}, err
	}
	actor, ok := ctx.Value(externalActorContextKey{}).(externalActorVersion)
	if !ok || actor.userID != actorID {
		return ExternalStartResult{}, ErrTokenInvalid
	}
	if err = s.checkTestSession(ctx, s.client, actorID, actor.authVersion, actor.sessionID); err != nil {
		return ExternalStartResult{}, err
	}
	callback, err := s.callbackURL(id)
	if err != nil {
		return ExternalStartResult{}, err
	}
	ctx = context.WithValue(ctx, authTestStartKey{}, captureTestSnapshot(p, callback, in.CompletionURL))
	return s.startExternal(ctx, p, authtransaction.PurposeTest, &actorID, nil, in.ExternalBrowserInput, "", "", time.Time{})
}

func (s *ExternalAuthService) readTestConnection(ctx context.Context, c *ent.Client, row *ent.AuthTransaction) (externalConnection, error) {
	p, err := readExternalConnection(ctx, c, row.ConnectionID, row.ConnectionRevision)
	if err != nil {
		return p, err
	}
	if row.Purpose != authtransaction.PurposeTest {
		return p, nil
	}
	plain, err := s.openTransactionField(row.ID, "test-snapshot", row.TestSnapshot)
	if err != nil {
		return p, ErrExternalFlowInvalid
	}
	var snapshot authTestSnapshot
	if err = json.Unmarshal([]byte(plain), &snapshot); err != nil {
		return p, ErrExternalFlowInvalid
	}
	p, err = s.applyConnectionChanges(ctx, c, p, &snapshot.Changes)
	if err != nil {
		return p, err
	}
	if testProtocolFingerprint(p, row.ID) != row.TestProtocolFingerprint || testDiagnosticFingerprint(p) != row.TestDiagnosticFingerprint {
		return p, connectionError("test-mismatch")
	}
	callback, err := s.callbackURL(row.ConnectionID)
	if err != nil || callback != snapshot.CallbackURL || snapshot.CompletionURL != row.CompletionURL || row.TestDeploymentFingerprint != s.deploymentFingerprint() {
		return p, connectionError("test-mismatch")
	}
	return p, nil
}

func (s *ExternalAuthService) validateTestLifecycle(ctx context.Context, c *ent.Client, row *ent.AuthTransaction) error {
	if !s.now().Before(row.ExpiresAt) {
		return connectionError("test-expired")
	}
	if row.Status == authtransaction.StatusCancelled || row.Status == authtransaction.StatusExpired || row.Status == authtransaction.StatusFailed {
		return connectionError("test-mismatch")
	}
	p, err := c.AuthConnection.Get(ctx, row.ConnectionID)
	if err != nil {
		return err
	}
	if p.ArchivedAt != nil || p.State == authconnection.StateDisabled || p.AuthGeneration != row.ConnectionGeneration {
		return connectionError("state-conflict")
	}
	expected := row.TestBaseRevision
	if row.TestAppliedRevision != nil {
		expected = *row.TestAppliedRevision
	}
	if p.AggregateRevision != expected {
		return connectionError("revision-conflict")
	}
	if row.TestDeploymentFingerprint != s.deploymentFingerprint() {
		return connectionError("test-mismatch")
	}
	if row.UserID == nil || row.AuthVersion == nil || row.ActorSessionID == nil {
		return ErrTokenInvalid
	}
	return s.checkTestSession(ctx, c, *row.UserID, *row.AuthVersion, *row.ActorSessionID)
}

func (s *ExternalAuthService) verifyConnectionTest(ctx context.Context, c *ent.Client, actorID int, p externalConnection, expected int64, id, verifier string, allowApplied bool) (*ent.AuthTransaction, error) {
	if id == "" {
		return nil, connectionError("test-required")
	}
	row, err := c.AuthTransaction.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, connectionError("test-expired")
	}
	if err != nil {
		return nil, err
	}
	if row.Purpose != authtransaction.PurposeTest || row.ConnectionID != p.Connection.ID || row.UserID == nil || *row.UserID != actorID || row.ActorSessionID == nil || *row.ActorSessionID != externalSessionID(ctx) || !verifierMatches(row.BrowserChallenge, verifier) {
		return nil, connectionError("test-mismatch")
	}
	if err = s.validateTestLifecycle(ctx, c, row); err != nil {
		return nil, err
	}
	if row.Status != authtransaction.StatusCompleted || row.TestValidatedAt == nil {
		return nil, connectionError("test-required")
	}
	if row.TestAppliedRevision != nil && *row.TestAppliedRevision != expected {
		return nil, connectionError("test-mismatch")
	}
	if row.TestAppliedRevision != nil && !allowApplied {
		return nil, connectionError("test-mismatch")
	}
	tested, err := s.readTestConnection(ctx, c, row)
	if err != nil {
		return nil, err
	}
	a, b := *tested.Config, *p.Config
	a.SecretVersion = nil
	b.SecretVersion = nil
	if connectionFingerprint(&a) != connectionFingerprint(&b) || testDiagnosticFingerprint(tested) != testDiagnosticFingerprint(p) {
		return nil, connectionError("test-mismatch")
	}
	if tested.SecretOverride == nil && p.SecretOverride == nil && connectionSecretVersion(tested.Config) != connectionSecretVersion(p.Config) {
		return nil, connectionError("test-mismatch")
	}
	secret := func(x externalConnection) (string, error) {
		if x.SecretOverride != nil {
			return *x.SecretOverride, nil
		}
		return s.connectionClientSecret(ctx, c, x.Config)
	}
	x, err := secret(tested)
	if err != nil {
		return nil, err
	}
	y, err := secret(p)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(x), []byte(y)) != 1 {
		return nil, connectionError("test-mismatch")
	}
	return row, nil
}

func applyTestCapability(cfg *ent.AuthConnectionRevision, row *ent.AuthTransaction, now time.Time) {
	if row.TestValidatedAt != nil {
		now = *row.TestValidatedAt
	}
	cfg.FreshSupported = row.IdentityProtocol == authtransaction.IdentityProtocolOidc && row.IdentityAuthTime != nil && !row.IdentityAuthTime.After(now.Add(2*time.Second)) && now.Sub(*row.IdentityAuthTime) < reauthenticationTTL
	cfg.TestedAt = row.TestValidatedAt
	cfg.AppliedTestID = row.ID
}
func markConnectionTestApplied(ctx context.Context, c *ent.Client, row *ent.AuthTransaction, revision int64) error {
	return c.AuthTransaction.UpdateOneID(row.ID).SetTestAppliedRevision(revision).Exec(ctx)
}

func (s *ExternalAuthService) enterProvider(ctx context.Context, p externalConnection) (context.Context, func(), error) {
	id, _ := ctx.Value(providerFlowKey{}).(string)
	check := func(ctx context.Context) error {
		if id == "" {
			return ErrExternalFlowInvalid
		}
		row, err := s.client.AuthTransaction.Get(ctx, id)
		if err != nil {
			return err
		}
		if row.Status != authtransaction.StatusCreated && row.Status != authtransaction.StatusCallbackProcessing {
			return ErrExternalFlowInvalid
		}
		if !s.now().Before(row.ExpiresAt) {
			return ErrExternalFlowInvalid
		}
		if row.Purpose == authtransaction.PurposeTest {
			return s.validateTestLifecycle(ctx, s.client, row)
		}
		current, err := readExternalConnection(ctx, s.client, row.ConnectionID, 0)
		if err != nil {
			return err
		}
		return transactionRulesCurrent(ctx, s.client, row, current)
	}
	ctx, release, err := s.gate.Enter(ctx, p.Connection.ID, check)
	if err != nil {
		if errors.Is(err, authhttp.ErrGateBlocked) {
			err = connectionError("state-conflict")
		}
		return ctx, func() {}, err
	}
	return authhttp.WithGate(ctx, s.gate, p.Connection.ID, check), release, nil
}
