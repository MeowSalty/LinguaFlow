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

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
)

const (
	SystemRoleUser  = "user"
	SystemRoleAdmin = "admin"
)

var (
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrTokenInvalid        = errors.New("token invalid")
	ErrTokenExpired        = errors.New("token expired")
	ErrRefreshTokenRevoked = errors.New("refresh token revoked")
	ErrUserExists          = errors.New("user already exists")
	ErrUserInactive        = errors.New("user inactive")
	ErrInvalidInput        = errors.New("invalid input")
)

type AuthConfig struct {
	Secret          []byte
	Issuer          string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

func AuthConfigFromServer(cfg config.ServerConfig) AuthConfig {
	return AuthConfig{
		Secret:          []byte(cfg.JWTSecret),
		Issuer:          cfg.JWTIssuer,
		AccessTokenTTL:  cfg.JWTExpiry,
		RefreshTokenTTL: cfg.RefreshExpiry,
	}
}

type AuthService struct {
	client *ent.Client
	cfg    AuthConfig
	now    func() time.Time
	rand   io.Reader
}

type AccessTokenClaims struct {
	UserID   int    `json:"uid"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

type RegisterInput struct {
	Username    string
	Password    string
	Email       string
	DisplayName string
}

type LoginInput struct {
	Username string
	Password string
}

type Session struct {
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
	User             *ent.User
}

func NewAuthService(client *ent.Client, cfg AuthConfig) *AuthService {
	return &AuthService{
		client: client,
		cfg:    cfg,
		now:    time.Now,
		rand:   crand.Reader,
	}
}

func (s *AuthService) Register(ctx context.Context, input RegisterInput) (*Session, error) {
	username := normalizeIdentity(input.Username)
	email := normalizeIdentity(input.Email)
	if username == "" || email == "" || len(input.Password) < 8 {
		return nil, ErrInvalidInput
	}
	if !strings.Contains(email, "@") {
		return nil, ErrInvalidInput
	}
	passwordHash, err := hashPassword(input.Password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	createdUser, err := s.client.User.Create().
		SetUsername(username).
		SetPasswordHash(passwordHash).
		SetEmail(email).
		SetDisplayName(strings.TrimSpace(input.DisplayName)).
		SetRole(SystemRoleUser).
		SetActive(true).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrUserExists
		}
		return nil, err
	}
	return s.issueSession(ctx, createdUser, nil)
}

func (s *AuthService) Login(ctx context.Context, input LoginInput) (*Session, error) {
	account, err := s.client.User.Query().Where(user.UsernameEQ(normalizeIdentity(input.Username))).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if !account.Active {
		return nil, ErrUserInactive
	}
	if err := comparePassword(account.PasswordHash, input.Password); err != nil {
		return nil, ErrInvalidCredentials
	}
	return s.issueSession(ctx, account, nil)
}

func (s *AuthService) Refresh(ctx context.Context, rawRefreshToken string) (*Session, error) {
	hashed := hash.Full(strings.TrimSpace(rawRefreshToken))
	tokenRecord, err := s.client.RefreshToken.Query().
		Where(refreshtoken.TokenHashEQ(hashed)).
		WithUser().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrTokenInvalid
		}
		return nil, err
	}
	now := s.now()
	if tokenRecord.RevokedAt != nil {
		return nil, ErrRefreshTokenRevoked
	}
	if now.After(tokenRecord.ExpiresAt) {
		return nil, ErrTokenExpired
	}
	if tokenRecord.Edges.User == nil {
		return nil, ErrTokenInvalid
	}
	return s.issueSession(ctx, tokenRecord.Edges.User, tokenRecord)
}

func (s *AuthService) Logout(ctx context.Context, rawRefreshToken string) error {
	hashed := hash.Full(strings.TrimSpace(rawRefreshToken))
	storedToken, err := s.client.RefreshToken.Query().Where(refreshtoken.TokenHashEQ(hashed)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrTokenInvalid
		}
		return err
	}
	if storedToken.RevokedAt != nil {
		return nil
	}
	return s.client.RefreshToken.UpdateOneID(storedToken.ID).SetRevokedAt(s.now()).Exec(ctx)
}

func (s *AuthService) ParseAccessToken(rawToken string) (*AccessTokenClaims, error) {
	claims := &AccessTokenClaims{}
	token, err := jwt.ParseWithClaims(strings.TrimSpace(rawToken), claims, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %s", token.Method.Alg())
		}
		return s.cfg.Secret, nil
	}, jwt.WithIssuer(s.cfg.Issuer))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrTokenInvalid
	}
	if !token.Valid {
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
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil, ErrTokenInvalid
		}
		return nil, nil, err
	}
	if !account.Active {
		return nil, nil, ErrUserInactive
	}
	return account, claims, nil
}

func (s *AuthService) ChangePassword(ctx context.Context, userID int, currentPassword, newPassword string) error {
	if len(newPassword) < 8 {
		return ErrInvalidInput
	}
	account, err := s.client.User.Get(ctx, userID)
	if err != nil {
		return err
	}
	if err := comparePassword(account.PasswordHash, currentPassword); err != nil {
		return ErrInvalidCredentials
	}
	passwordHash, err := hashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.client.User.UpdateOneID(userID).SetPasswordHash(passwordHash).Exec(ctx)
}

func (s *AuthService) issueSession(ctx context.Context, account *ent.User, revokeToken *ent.RefreshToken) (_ *Session, err error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	now := s.now()
	if revokeToken != nil && revokeToken.RevokedAt == nil {
		if err = tx.RefreshToken.UpdateOneID(revokeToken.ID).SetRevokedAt(now).Exec(ctx); err != nil {
			return nil, err
		}
	}
	refreshRaw, err := s.generateOpaqueToken()
	if err != nil {
		return nil, err
	}
	refreshExpiry := now.Add(s.cfg.RefreshTokenTTL)
	if _, err = tx.RefreshToken.Create().
		SetTokenHash(hash.Full(refreshRaw)).
		SetExpiresAt(refreshExpiry).
		SetUserID(account.ID).
		Save(ctx); err != nil {
		return nil, err
	}
	accessExpiry := now.Add(s.cfg.AccessTokenTTL)
	accessToken, err := s.signAccessToken(account, accessExpiry)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &Session{
		AccessToken:      accessToken,
		RefreshToken:     refreshRaw,
		AccessExpiresAt:  accessExpiry,
		RefreshExpiresAt: refreshExpiry,
		User:             account,
	}, nil
}

func (s *AuthService) signAccessToken(account *ent.User, expiresAt time.Time) (string, error) {
	now := s.now()
	claims := AccessTokenClaims{
		UserID:   account.ID,
		Username: account.Username,
		Role:     account.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.cfg.Issuer,
			Subject:   fmt.Sprintf("user:%d", account.ID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.cfg.Secret)
}

func (s *AuthService) generateOpaqueToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(s.rand, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func normalizeIdentity(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func hashPassword(raw string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashed), nil
}

func comparePassword(hashed, raw string) error {
	return bcrypt.CompareHashAndPassword([]byte(hashed), []byte(raw))
}
