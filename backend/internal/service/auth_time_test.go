package service

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
)

func TestSessionExpiryMatchesCredentials(t *testing.T) {
	client := testClient(t)
	account := createTestUser(t, client, "expiry-user")
	assertSessionExpiryMatchesCredentials(t, client, account)
}

func assertSessionExpiryMatchesCredentials(t *testing.T, client *ent.Client, account *ent.User) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 17, 45, 32, 123456789, time.FixedZone("east", 8*60*60))
	cfg := AuthConfig{Secret: []byte("expiry-test-key"), Issuer: "expiry-test", AccessTokenTTL: 17*time.Minute + 234*time.Nanosecond, RefreshTokenTTL: 30*24*time.Hour + 345*time.Nanosecond}
	svc := NewAuthService(client, cfg, nil)
	svc.now = func() time.Time { return now }
	session, err := svc.issueSession(ctx, account, nil)
	if err != nil {
		t.Fatal(err)
	}
	claims := &AccessTokenClaims{}
	_, err = jwt.ParseWithClaims(session.AccessToken, claims, func(*jwt.Token) (any, error) { return cfg.Secret, nil }, jwt.WithIssuer(cfg.Issuer), jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	if !session.AccessExpiresAt.Equal(claims.ExpiresAt.Time) || session.AccessExpiresAt.Location() != time.UTC {
		t.Fatalf("API expiry=%s, encoded expiry=%s; want same instant in UTC", session.AccessExpiresAt, claims.ExpiresAt.Time)
	}
	stored, err := client.RefreshToken.Query().Where(refreshtoken.TokenHashEQ(hash.Full(session.RefreshToken))).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !session.RefreshExpiresAt.Equal(stored.ExpiresAt) || session.RefreshExpiresAt.Location() != time.UTC {
		t.Fatalf("API refresh expiry=%s, persisted=%s; want exact stored UTC instant", session.RefreshExpiresAt, stored.ExpiresAt)
	}
	if session.RefreshExpiresAt.Sub(now.Add(cfg.RefreshTokenTTL)) > 0 || now.Add(cfg.RefreshTokenTTL).Sub(session.RefreshExpiresAt) >= time.Microsecond {
		t.Fatalf("refresh TTL changed beyond database precision: %s", session.RefreshExpiresAt)
	}
	if err := svc.Logout(ctx, session.RefreshToken); err != nil {
		t.Fatal(err)
	}
	stored, err = client.RefreshToken.Get(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RevokedAt == nil || stored.RevokedAt.Location() != time.UTC || now.Sub(*stored.RevokedAt) < 0 || now.Sub(*stored.RevokedAt) >= time.Microsecond {
		t.Fatalf("unexpected revocation time: %v", stored.RevokedAt)
	}
}
