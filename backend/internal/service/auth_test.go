package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
)

func newAccountTestAuth(t *testing.T) (*AuthService, *ent.Client) {
	t.Helper()
	client := testClient(t)
	client.SystemSetting.Create().SetKey(SettingRegistrationEnabled).SetValue("true").SaveX(context.Background())
	return NewAuthService(client, AuthConfig{
		Secret: []byte("account-service-test-secret"), Issuer: "account-service-test",
		AccessTokenTTL: time.Hour, RefreshTokenTTL: 24 * time.Hour,
	}, NewSettingsService(client)), client
}

func passwordContractCases() []struct {
	name     string
	password string
	valid    bool
} {
	return []struct {
		name     string
		password string
		valid    bool
	}{
		{"empty", "", false},
		{"7 ASCII code points", "1234567", false},
		{"8 ASCII code points", "12345678", true},
		{"7 Chinese code points", strings.Repeat("汉", 7), false},
		{"8 Chinese code points", strings.Repeat("汉", 8), true},
		{"7 emoji code points", strings.Repeat("🙂", 7), false},
		{"8 emoji code points", strings.Repeat("🙂", 8), true},
		{"72 ASCII bytes", strings.Repeat("a", 72), true},
		{"73 ASCII bytes", strings.Repeat("a", 73), false},
		{"72 Chinese bytes", strings.Repeat("汉", 24), true},
		{"73 mixed bytes", strings.Repeat("汉", 24) + "a", false},
		{"72 emoji bytes", strings.Repeat("🙂", 18), true},
		{"73 emoji and ASCII bytes", strings.Repeat("🙂", 18) + "a", false},
		{"leading and trailing spaces", " 123456 ", true},
		{"spaces remain password characters", "        ", true},
		{"combining marks are code points", strings.Repeat("e\u0301", 4), true},
	}
}

func TestRegisterPasswordContract(t *testing.T) {
	ctx := context.Background()
	svc, client := newAccountTestAuth(t)
	for i, tt := range passwordContractCases() {
		t.Run(tt.name, func(t *testing.T) {
			beforeUsers, err := client.User.Query().Count(ctx)
			if err != nil {
				t.Fatal(err)
			}
			beforeTokens, err := client.RefreshToken.Query().Count(ctx)
			if err != nil {
				t.Fatal(err)
			}
			username := fmt.Sprintf("registration-%d", i)
			session, err := svc.Register(ctx, RegisterInput{
				Username: username, Password: tt.password, Email: username + "@example.com",
			})
			if !tt.valid {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("error = %v, want ErrInvalidInput", err)
				}
				afterUsers, err := client.User.Query().Count(ctx)
				if err != nil {
					t.Fatal(err)
				}
				afterTokens, err := client.RefreshToken.Query().Count(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if afterUsers != beforeUsers || afterTokens != beforeTokens {
					t.Fatal("invalid registration persisted an account or session")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if session.User.Username != username {
				t.Fatalf("registered username = %q, want %q", session.User.Username, username)
			}
			if _, err := svc.Login(ctx, LoginInput{Username: username, Password: tt.password}); err != nil {
				t.Fatalf("registered password cannot log in: %v", err)
			}
			if trimmed := strings.TrimSpace(tt.password); trimmed != tt.password {
				if _, err := svc.Login(ctx, LoginInput{Username: username, Password: trimmed}); !errors.Is(err, ErrInvalidCredentials) {
					t.Fatalf("trimmed password error = %v, want ErrInvalidCredentials", err)
				}
			}
		})
	}
}

func TestChangePasswordContract(t *testing.T) {
	ctx := context.Background()
	svc, client := newAccountTestAuth(t)
	const originalPassword = " old password "
	for i, tt := range passwordContractCases() {
		t.Run(tt.name, func(t *testing.T) {
			original := createPasswordTestUser(t, client, fmt.Sprintf("password-%d", i), originalPassword)
			err := svc.ChangePassword(ctx, original.ID, originalPassword, tt.password)
			stored, readErr := client.User.Get(ctx, original.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !tt.valid {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("error = %v, want ErrInvalidInput", err)
				}
				if stored.PasswordHash != original.PasswordHash || !stored.UpdatedAt.Equal(original.UpdatedAt) {
					t.Fatal("invalid password changed stored credentials")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := comparePassword(stored.PasswordHash, tt.password); err != nil {
				t.Fatalf("new password was changed before hashing: %v", err)
			}
			if comparePassword(stored.PasswordHash, originalPassword) == nil {
				t.Fatal("old password still matches after change")
			}
			if trimmed := strings.TrimSpace(tt.password); trimmed != tt.password && comparePassword(stored.PasswordHash, trimmed) == nil {
				t.Fatal("password was trimmed")
			}
			if stored.Username != original.Username || stored.Email != original.Email || stored.DisplayName != original.DisplayName || stored.Role != original.Role || stored.Active != original.Active {
				t.Fatal("password change modified account profile")
			}
		})
	}
}

func TestChangePasswordCurrentPasswordErrors(t *testing.T) {
	ctx := context.Background()
	svc, client := newAccountTestAuth(t)
	original := createPasswordTestUser(t, client, "current-password", " current password ")
	tests := []struct {
		name     string
		password string
		wantErr  error
	}{
		{"empty", "", ErrInvalidInput},
		{"incorrect", "wrong password", ErrCurrentPasswordMismatch},
		{"untrimmed comparison", "current password", ErrCurrentPasswordMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.ChangePassword(ctx, original.ID, tt.password, "new password")
			if !errors.Is(err, tt.wantErr) || errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("error = %v, want %v without login failure semantics", err, tt.wantErr)
			}
			stored, err := client.User.Get(ctx, original.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.PasswordHash != original.PasswordHash || !stored.UpdatedAt.Equal(original.UpdatedAt) {
				t.Fatal("current password failure changed credentials")
			}
		})
	}
}

func TestChangePasswordAcceptsLegacyPasswordsAndPreservesSessions(t *testing.T) {
	ctx := context.Background()
	svc, client := newAccountTestAuth(t)
	for i, oldPassword := range []string{"short", "旧密码", " old "} {
		t.Run(fmt.Sprintf("legacy-%d", i), func(t *testing.T) {
			account := createPasswordTestUser(t, client, fmt.Sprintf("legacy-%d", i), oldPassword)
			session, err := svc.Login(ctx, LoginInput{Username: account.Username, Password: oldPassword})
			if err != nil {
				t.Fatalf("legacy password cannot log in: %v", err)
			}
			newPassword := " new password 🙂 "
			if err := NewUserService(client, svc).ChangeMyPassword(ctx, account.ID, oldPassword, newPassword); err != nil {
				t.Fatalf("legacy password cannot be used for change: %v", err)
			}
			if _, err := svc.Login(ctx, LoginInput{Username: account.Username, Password: oldPassword}); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("old password login error = %v, want ErrInvalidCredentials", err)
			}
			if _, err := svc.Login(ctx, LoginInput{Username: account.Username, Password: newPassword}); err != nil {
				t.Fatalf("new password cannot log in: %v", err)
			}
			if _, _, err := svc.ResolveUserFromAccessToken(ctx, session.AccessToken); err != nil {
				t.Fatalf("password change invalidated existing access token: %v", err)
			}
			if _, err := svc.Refresh(ctx, session.RefreshToken); err != nil {
				t.Fatalf("password change invalidated existing refresh token: %v", err)
			}
		})
	}
}

func TestLogoutOwnershipAndSessionIsolation(t *testing.T) {
	ctx := context.Background()
	svc, client := newAccountTestAuth(t)
	owner := createTestUser(t, client, "logout-owner")
	other := createTestUser(t, client, "logout-other")
	issue := func(account *ent.User) *Session {
		t.Helper()
		session, err := svc.issueSession(ctx, account, nil)
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	target := issue(owner)
	sibling := issue(owner)
	otherSession := issue(other)

	if err := svc.Logout(ctx, other.ID, target.RefreshToken); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign token error = %v, want ErrForbidden", err)
	}
	if stored := accountTestRefreshToken(t, client, target.RefreshToken); stored.RevokedAt != nil {
		t.Fatal("foreign logout revoked owner's token")
	}
	if err := svc.Logout(ctx, owner.ID, target.RefreshToken); err != nil {
		t.Fatal(err)
	}
	revoked := accountTestRefreshToken(t, client, target.RefreshToken)
	if revoked.RevokedAt == nil {
		t.Fatal("logout did not revoke token")
	}
	later := revoked.RevokedAt.Add(time.Minute)
	svc.now = func() time.Time { return later }
	if err := svc.Logout(ctx, owner.ID, target.RefreshToken); err != nil {
		t.Fatalf("repeated logout failed: %v", err)
	}
	if err := svc.Logout(ctx, other.ID, target.RefreshToken); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked foreign token error = %v, want ErrForbidden", err)
	}
	after := accountTestRefreshToken(t, client, target.RefreshToken)
	if after.RevokedAt == nil || !after.RevokedAt.Equal(*revoked.RevokedAt) || !after.UpdatedAt.Equal(revoked.UpdatedAt) {
		t.Fatal("repeated or foreign logout modified already revoked token")
	}
	if _, err := svc.Refresh(ctx, target.RefreshToken); !errors.Is(err, ErrRefreshTokenRevoked) {
		t.Fatalf("logged out token refresh error = %v, want ErrRefreshTokenRevoked", err)
	}
	if _, _, err := svc.ResolveUserFromAccessToken(ctx, target.AccessToken); err != nil {
		t.Fatalf("logout invalidated access token before expiration: %v", err)
	}
	for name, raw := range map[string]string{"same user other session": sibling.RefreshToken, "other user session": otherSession.RefreshToken} {
		if _, err := svc.Refresh(ctx, raw); err != nil {
			t.Fatalf("logout affected %s: %v", name, err)
		}
	}
}

func TestLogoutInvalidAndExpiredTokens(t *testing.T) {
	ctx := context.Background()
	svc, client := newAccountTestAuth(t)
	owner := createTestUser(t, client, "expired-owner")
	other := createTestUser(t, client, "expired-other")
	for _, raw := range []string{"", " \t", "unknown-token"} {
		if err := svc.Logout(ctx, owner.ID, raw); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("unknown token error = %v, want ErrTokenInvalid", err)
		}
	}
	const raw = "expired-but-owned-refresh-token"
	_, err := client.RefreshToken.Create().SetTokenHash(hash.Full(raw)).
		SetExpiresAt(time.Now().UTC().Add(-time.Hour)).SetUserID(owner.ID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, other.ID, raw); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired foreign token error = %v, want ErrForbidden", err)
	}
	if stored := accountTestRefreshToken(t, client, raw); stored.RevokedAt != nil {
		t.Fatal("foreign logout revoked expired token")
	}
	if err := svc.Logout(ctx, owner.ID, raw); err != nil {
		t.Fatalf("owner cannot revoke expired token: %v", err)
	}
	if stored := accountTestRefreshToken(t, client, raw); stored.RevokedAt == nil {
		t.Fatal("expired token was not revoked")
	}
}

func createPasswordTestUser(t *testing.T, client *ent.Client, username, password string) *ent.User {
	t.Helper()
	// Fixtures include passwords that predate the current registration rules.
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	account, err := client.User.Create().SetUsername(username).
		SetEmail(username + "@example.com").SetDisplayName("Original profile").
		SetPasswordHash(string(passwordHash)).Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func accountTestRefreshToken(t *testing.T, client *ent.Client, raw string) *ent.RefreshToken {
	t.Helper()
	stored, err := client.RefreshToken.Query().Where(refreshtoken.TokenHashEQ(hash.Full(raw))).Only(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return stored
}
