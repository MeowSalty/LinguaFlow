package service

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/accountidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/loginbinding"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	SystemRoleUser  = "user"
	SystemRoleAdmin = "admin"
)

var (
	ErrInvalidCredentials      = errors.New("invalid credentials")
	ErrTokenInvalid            = errors.New("token invalid")
	ErrTokenExpired            = errors.New("token expired")
	ErrRefreshTokenRevoked     = errors.New("refresh token revoked")
	ErrUserExists              = errors.New("user already exists")
	ErrUserInactive            = errors.New("user inactive")
	ErrInvalidInput            = errors.New("invalid input")
	ErrCurrentPasswordMismatch = errors.New("current password mismatch")
	ErrAccountConflict         = errors.New("account security state changed")
)

// A fixed valid bcrypt value keeps unknown users, disabled users and accounts
// without a password on the same expensive verification path.
const dummyPasswordHash = "$2a$10$7EqJtq98hPqEX7fNZaFWoO5z8O4uKq7H6N4jGkCq5HqFQHGXQrX8u"

type AuthConfig struct {
	Secret          []byte
	Issuer          string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

func AuthConfigFromServer(cfg config.ServerConfig) AuthConfig {
	return AuthConfig{Secret: []byte(cfg.JWTSecret), Issuer: cfg.JWTIssuer, AccessTokenTTL: cfg.JWTExpiry, RefreshTokenTTL: cfg.RefreshExpiry}
}

type AuthService struct {
	client   *ent.Client
	cfg      AuthConfig
	now      func() time.Time
	rand     io.Reader
	settings RegistrationPolicy
}

func NewAuthService(client *ent.Client, cfg AuthConfig, settings RegistrationPolicy) *AuthService {
	return &AuthService{client: client, cfg: cfg, now: timeutil.NowUTC, rand: crand.Reader, settings: settings}
}

type AccessTokenClaims struct {
	UserID      int    `json:"uid"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	AuthVersion int64  `json:"auth_version"`
	jwt.RegisteredClaims
}
type RegisterInput struct{ Username, Password, Email, DisplayName string }
type LoginInput struct{ Identifier, Password string }
type Session struct {
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
	User             *ent.User
}

func (s *AuthService) Register(ctx context.Context, input RegisterInput) (*Session, error) {
	if s.settings == nil {
		return nil, ErrSettingsUnavailable
	}
	enabled, err := s.settings.RegistrationEnabled(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
	}
	if !enabled {
		return nil, ErrRegistrationClosed
	}
	username, err := accountidentity.NormalizeUsername(input.Username)
	if err != nil {
		return nil, invalidAccountField("username", err)
	}
	if err := accountidentity.ValidateAllocation(username, accountidentity.Public); err != nil {
		return nil, invalidAccountField("username", err)
	}
	email, err := accountidentity.NormalizeEmail(input.Email)
	if err != nil {
		return nil, invalidAccountField("email", err)
	}
	if err := validateNewPassword(input.Password); err != nil {
		return nil, err
	}
	passwordHash, err := hashPassword(input.Password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	var result *Session
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		result = nil
		if err := lockSetting(ctx, tx, SettingRegistrationEnabled); err != nil {
			return err
		}
		if err := lockPasswordConnection(ctx, tx); err != nil {
			return err
		}
		registration, err := readRegistrationPolicy(ctx, tx)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
		}
		p, err := readPasswordPolicy(ctx, tx)
		if err != nil {
			return err
		}
		if !registration || !p.canLogin() || !p.Policy.SignupEnabled {
			return ErrRegistrationClosed
		}
		create := tx.User.Create().SetUsername(username).SetPasswordHash(passwordHash).SetDisplayName(strings.TrimSpace(input.DisplayName)).SetRole(SystemRoleUser).SetActive(true)
		if email != "" {
			create.SetEmail(email)
		}
		account, err := create.Save(ctx)
		if ent.IsConstraintError(err) {
			return ErrUserExists
		}
		if err != nil {
			return err
		}
		result, err = s.createSession(ctx, tx, account, p, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	result.User.Unwrap()
	return result, nil
}

// Verification observes one credential version. Final issue compares it under lock.
func (s *AuthService) Login(ctx context.Context, input LoginInput) (*Session, error) {
	if strings.Contains(input.Identifier, "@") {
		return nil, ErrInvalidCredentials
	}
	identifier, err := accountidentity.NormalizeUsername(input.Identifier)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	account, err := s.client.User.Query().Where(user.UsernameEQ(identifier)).Only(ctx)
	if ent.IsNotFound(err) {
		_ = comparePassword(dummyPasswordHash, input.Password)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	passwordHash := account.PasswordHash
	if passwordHash == "" {
		passwordHash = dummyPasswordHash
	}
	passwordMatches := comparePassword(passwordHash, input.Password) == nil
	if !account.Active || account.PasswordHash == "" || !passwordMatches {
		return nil, ErrInvalidCredentials
	}
	return s.issueSession(ctx, account)
}

func (s *AuthService) issueSession(ctx context.Context, observed *ent.User) (*Session, error) {
	var result *Session
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		result = nil
		if err := lockPasswordConnection(ctx, tx); err != nil {
			return err
		}
		if err := lockAuthUser(ctx, tx, observed.ID); err != nil {
			return err
		}
		p, err := readPasswordPolicy(ctx, tx)
		if err != nil {
			return err
		}
		account, err := tx.User.Get(ctx, observed.ID)
		if err != nil {
			return err
		}
		if !p.canLogin() || !account.Active || account.PasswordHash == "" || account.AuthVersion < 1 || account.AuthVersion != observed.AuthVersion || account.PasswordHash != observed.PasswordHash {
			return ErrInvalidCredentials
		}
		result, err = s.createSession(ctx, tx, account, p, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	result.User.Unwrap()
	return result, nil
}

func (s *AuthService) Refresh(ctx context.Context, rawRefreshToken string) (*Session, error) {
	stored, err := s.client.RefreshToken.Query().Where(refreshtoken.TokenHashEQ(hash.Full(strings.TrimSpace(rawRefreshToken)))).WithSession().Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	if stored.Edges.Session == nil {
		return nil, ErrTokenInvalid
	}
	var result *Session
	var rejection error
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		result, rejection = nil, nil
		if err := lockAuthConnection(ctx, tx, stored.Edges.Session.ConnectionID); err != nil {
			return err
		}
		if err := lockAuthUser(ctx, tx, stored.Edges.Session.UserID); err != nil {
			return err
		}
		if err := lockRefreshSession(ctx, tx, stored.SessionID); err != nil {
			return err
		}
		chain, err := tx.RefreshSession.Get(ctx, stored.SessionID)
		if err != nil {
			return err
		}
		token, err := tx.RefreshToken.Query().Where(refreshtoken.IDEQ(stored.ID)).WithUser().Only(ctx)
		if err != nil {
			return err
		}
		if token.Edges.User == nil || token.Edges.User.ID != chain.UserID {
			return ErrTokenInvalid
		}
		now := timeutil.Normalize(s.now())
		if chain.RevokedAt != nil || token.RevokedAt != nil {
			return ErrRefreshTokenRevoked
		}
		// Rejection must commit revocation, not roll it back with a returned error.
		reject := func(reason error) error {
			if err := revokeRefreshSession(ctx, tx, chain.ID, now); err != nil {
				return err
			}
			rejection = reason
			return nil
		}
		if token.ConsumedAt != nil {
			return reject(ErrRefreshTokenRevoked)
		}
		if !now.Before(chain.ExpiresAt) || !now.Before(token.ExpiresAt) {
			return reject(ErrTokenExpired)
		}
		account := token.Edges.User
		validConnection := false
		var connection *ent.AuthConnection
		var policyRevision int64
		var binding *ent.LoginBinding
		if chain.ConnectionID == PasswordConnectionID {
			p, policyErr := readPasswordPolicy(ctx, tx)
			if policyErr != nil {
				return policyErr
			}
			connection, policyRevision = p.Connection, p.Policy.LoginPolicyRevision
			validConnection = p.canLogin() && account.PasswordHash != "" && chain.BindingID == nil
		} else {
			p, policyErr := readExternalConnection(ctx, tx, chain.ConnectionID, 0)
			if policyErr == nil && chain.BindingID != nil {
				binding, policyErr = tx.LoginBinding.Query().Where(loginbinding.IDEQ(*chain.BindingID), loginbinding.UserIDEQ(account.ID), loginbinding.ConnectionIDEQ(chain.ConnectionID)).Only(ctx)
			}
			if policyErr != nil && !ent.IsNotFound(policyErr) {
				return policyErr
			}
			if policyErr == nil {
				connection, policyRevision = p.Connection, p.Policy.LoginPolicyRevision
				validConnection = p.Connection.ArchivedAt == nil && p.Policy.Enabled && p.Policy.LoginEnabled && binding != nil
				if validConnection && p.Policy.LoginRules.Mode == "conditions" {
					identity, identityErr := tx.ExternalIdentity.Get(ctx, binding.ExternalIdentityID)
					if identityErr != nil {
						return identityErr
					}
					if chain.Evidence.Subject != identity.Subject || chain.Evidence.Issuer != identity.Authority || requireRule(p, chain.Evidence, p.Policy.LoginRules, now) != nil {
						return reject(ErrRefreshTokenRevoked)
					}
				}
			}
		}
		if !account.Active || account.AuthVersion != chain.AuthVersion || !validConnection || connection == nil || chain.ConnectionGeneration != connection.AuthGeneration || chain.LoginPolicyRevision != policyRevision || token.Generation != chain.Generation {
			return reject(ErrRefreshTokenRevoked)
		}
		if chain.Generation >= maxAuthVersion {
			return reject(ErrRefreshTokenRevoked)
		}
		n, err := tx.RefreshToken.Update().Where(refreshtoken.IDEQ(token.ID), refreshtoken.ConsumedAtIsNil(), refreshtoken.RevokedAtIsNil()).SetConsumedAt(now).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return reject(ErrRefreshTokenRevoked)
		}
		chain, err = tx.RefreshSession.UpdateOneID(chain.ID).AddGeneration(1).Save(ctx)
		if err != nil {
			return err
		}
		result, err = s.createSessionForConnection(ctx, tx, account, connection, policyRevision, binding, chain)
		return err
	})
	if err != nil {
		return nil, err
	}
	if rejection != nil {
		return nil, rejection
	}
	result.User.Unwrap()
	return result, nil
}

func (s *AuthService) Logout(ctx context.Context, actorUserID int, rawRefreshToken string) error {
	stored, err := s.client.RefreshToken.Query().Where(refreshtoken.TokenHashEQ(hash.Full(strings.TrimSpace(rawRefreshToken)))).WithSession().Only(ctx)
	if ent.IsNotFound(err) {
		return ErrTokenInvalid
	}
	if err != nil {
		return err
	}
	if stored.Edges.Session == nil {
		return ErrTokenInvalid
	}
	if stored.Edges.Session.UserID != actorUserID {
		return ErrForbidden
	}
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if err := lockAuthUser(ctx, tx, actorUserID); err != nil {
			return err
		}
		if err := lockRefreshSession(ctx, tx, stored.SessionID); err != nil {
			return err
		}
		return revokeRefreshSession(ctx, tx, stored.SessionID, timeutil.Normalize(s.now()))
	})
}

func (s *AuthService) ParseAccessToken(rawToken string) (*AccessTokenClaims, error) {
	claims := &AccessTokenClaims{}
	token, err := jwt.ParseWithClaims(strings.TrimSpace(rawToken), claims, func(*jwt.Token) (any, error) { return s.cfg.Secret, nil }, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer(s.cfg.Issuer), jwt.WithExpirationRequired(), jwt.WithTimeFunc(s.now))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrTokenInvalid
	}
	if !token.Valid || claims.UserID < 1 || claims.AuthVersion < 1 || claims.AuthVersion > maxAuthVersion || claims.Subject != fmt.Sprintf("user:%d", claims.UserID) {
		return nil, ErrTokenInvalid
	}
	return claims, nil
}

func (s *AuthService) ResolveUserFromAccessToken(ctx context.Context, rawToken string) (*ent.User, *AccessTokenClaims, error) {
	claims, err := s.ParseAccessToken(rawToken)
	if err != nil {
		return nil, nil, err
	}
	account, err := s.client.User.Get(ctx, claims.UserID)
	if ent.IsNotFound(err) {
		return nil, nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, nil, err
	}
	if !account.Active {
		return nil, nil, ErrUserInactive
	}
	if account.AuthVersion != claims.AuthVersion {
		return nil, nil, ErrTokenInvalid
	}
	return account, claims, nil
}

// Established streams always re-read state; refreshing a token does not extend them.
func (s *AuthService) CheckAccessSession(ctx context.Context, userID int, version int64, expiresAt time.Time) error {
	if version < 1 || version > maxAuthVersion || userID < 1 {
		return ErrTokenInvalid
	}
	if !s.now().Before(expiresAt) {
		return ErrTokenExpired
	}
	account, err := s.client.User.Get(ctx, userID)
	if ent.IsNotFound(err) {
		return ErrTokenInvalid
	}
	if err != nil {
		return err
	}
	if !account.Active {
		return ErrUserInactive
	}
	if account.AuthVersion != version {
		return ErrTokenInvalid
	}
	if !s.now().Before(expiresAt) {
		return ErrTokenExpired
	}
	return nil
}

func (s *AuthService) ChangePassword(ctx context.Context, userID int, currentPassword, newPassword string) error {
	if currentPassword == "" {
		return ErrInvalidInput
	}
	if err := validateNewPassword(newPassword); err != nil {
		return err
	}
	observed, err := s.client.User.Get(ctx, userID)
	if err != nil {
		return err
	}
	if observed.PasswordHash == "" || comparePassword(observed.PasswordHash, currentPassword) != nil {
		return ErrCurrentPasswordMismatch
	}
	verifiedAt := s.now()
	passwordHash, err := hashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if err := lockPasswordConnection(ctx, tx); err != nil {
			return err
		}
		if err := lockAuthUser(ctx, tx, userID); err != nil {
			return err
		}
		p, err := readPasswordPolicy(ctx, tx)
		if err != nil {
			return err
		}
		account, err := tx.User.Get(ctx, userID)
		if err != nil {
			return err
		}
		if !account.Active || !p.canLogin() || account.AuthVersion != observed.AuthVersion || account.PasswordHash != observed.PasswordHash || !s.now().Before(verifiedAt.Add(5*time.Minute)) {
			return ErrAccountConflict
		}
		if err := revokeUserSessions(ctx, tx, userID, timeutil.Normalize(s.now())); err != nil {
			return err
		}
		if err := tx.User.UpdateOneID(userID).SetPasswordHash(passwordHash).Exec(ctx); err != nil {
			return err
		}
		return recordAuditEvent(ctx, tx, AuditEvent{ActorUserID: userID, Action: "account.password.changed", ResourceType: "user", ResourceID: userID, VisibilityScope: "personal", Metadata: map[string]any{"auth_version": account.AuthVersion + 1, "connection_id": PasswordConnectionID}})
	})
}

// Caller owns connection/user locks and has verified credentials and policy.
func (s *AuthService) createSession(ctx context.Context, tx *ent.Client, account *ent.User, p passwordPolicy, chain *ent.RefreshSession) (*Session, error) {
	if chain == nil {
		if err := tx.User.UpdateOneID(account.ID).SetPasswordVerifiedHash(hash.Full(account.PasswordHash)).SetPasswordVerifiedAt(s.now()).Exec(ctx); err != nil {
			return nil, err
		}
	}
	return s.createSessionForConnection(ctx, tx, account, p.Connection, p.Policy.LoginPolicyRevision, nil, chain)
}

func (s *AuthService) createSessionForConnection(ctx context.Context, tx *ent.Client, account *ent.User, connection *ent.AuthConnection, policyRevision int64, binding *ent.LoginBinding, chain *ent.RefreshSession, evidences ...authrules.Evidence) (*Session, error) {
	now := timeutil.Normalize(s.now())
	if s.cfg.AccessTokenTTL <= 0 || s.cfg.RefreshTokenTTL <= 0 || len(s.cfg.Secret) == 0 {
		return nil, fmt.Errorf("invalid session configuration")
	}
	if chain == nil {
		create := tx.RefreshSession.Create().SetUserID(account.ID).SetAuthVersion(account.AuthVersion).SetConnectionID(connection.ID).SetConnectionGeneration(connection.AuthGeneration).SetLoginPolicyRevision(policyRevision).SetExpiresAt(now.Add(s.cfg.RefreshTokenTTL))
		if binding != nil {
			create.SetBindingID(binding.ID)
			evidence := authrules.Evidence{}
			if len(evidences) > 0 {
				evidence = evidences[0]
			}
			create.SetEvidence(evidence)
			if err := tx.LoginBinding.UpdateOneID(binding.ID).SetLastUsedAt(now).SetLastVerifiedAt(now).SetVerifiedAuthGeneration(connection.AuthGeneration).SetVerifiedLoginPolicyRevision(policyRevision).SetEvidence(evidence).Exec(ctx); err != nil {
				return nil, err
			}
		}
		created, err := create.Save(ctx)
		if err != nil {
			return nil, err
		}
		// Read the native persisted absolute deadline, including PostgreSQL precision.
		chain, err = tx.RefreshSession.Get(ctx, created.ID)
		if err != nil {
			return nil, err
		}
	}
	raw, err := s.generateOpaqueToken()
	if err != nil {
		return nil, err
	}
	if err := tx.RefreshToken.Create().SetTokenHash(hash.Full(raw)).SetSessionID(chain.ID).SetGeneration(chain.Generation).SetExpiresAt(chain.ExpiresAt).SetUserID(account.ID).Exec(ctx); err != nil {
		return nil, err
	}
	accessExpiry := timeutil.Normalize(jwt.NewNumericDate(now.Add(s.cfg.AccessTokenTTL)).Time)
	access, err := s.signAccessToken(account, accessExpiry)
	if err != nil {
		return nil, err
	}
	return &Session{AccessToken: access, RefreshToken: raw, AccessExpiresAt: accessExpiry, RefreshExpiresAt: timeutil.Normalize(chain.ExpiresAt), User: account}, nil
}

func lockAuthConnection(ctx context.Context, client *ent.Client, id string) error {
	if id == PasswordConnectionID {
		return lockPasswordConnection(ctx, client)
	}
	result, err := client.ExecContext(ctx, "UPDATE auth_connections SET auth_generation = auth_generation WHERE id = '"+strings.ReplaceAll(id, "'", "''")+"'")
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrSettingsUnavailable
	}
	return nil
}
func (s *AuthService) signAccessToken(account *ent.User, expiresAt time.Time) (string, error) {
	claims := AccessTokenClaims{UserID: account.ID, Username: account.Username, Role: account.Role, AuthVersion: account.AuthVersion, RegisteredClaims: jwt.RegisteredClaims{Issuer: s.cfg.Issuer, Subject: fmt.Sprintf("user:%d", account.ID), IssuedAt: jwt.NewNumericDate(timeutil.Normalize(s.now())), ExpiresAt: jwt.NewNumericDate(expiresAt)}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.cfg.Secret)
}
func (s *AuthService) generateOpaqueToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(s.rand, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Organization slugs retain their existing contract; account names do not use this helper.
func normalizeIdentity(v string) string { return strings.ToLower(strings.TrimSpace(v)) }
func validateNewPassword(raw string) error {
	if utf8.RuneCountInString(raw) < 8 || len(raw) > 72 {
		return ErrInvalidInput
	}
	return nil
}
func hashPassword(raw string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
	return string(hashed), err
}
func comparePassword(hashed, raw string) error {
	return bcrypt.CompareHashAndPassword([]byte(hashed), []byte(raw))
}
